package session

import (
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// ManifestSchemaVersion identifies the versioned, machine-readable coverage
// contract emitted for a review (or scan) run. Consumers should gate on this
// value; unknown future versions must be ignored rather than misread.
const ManifestSchemaVersion = "ocr.run-manifest/v1"

// FailureClass is the typed classification attached to every failed coverage
// item. The set is fixed so downstream consumers can switch on it reliably.
// FailureUnknown is the mandatory catch-all: any error that cannot be mapped
// to a more specific class, and any item swept in by Finalize, uses it.
type FailureClass string

const (
	FailureProvider      FailureClass = "provider"
	FailureTimeout       FailureClass = "timeout"
	FailureCancelled     FailureClass = "cancelled"
	FailureConfiguration FailureClass = "configuration"
	FailureInput         FailureClass = "input"
	FailureBudget        FailureClass = "budget"
	FailurePanic         FailureClass = "panic"
	FailureUnknown       FailureClass = "unknown"
)

// valid reports whether c is one of the fixed failure classes.
func (c FailureClass) valid() bool {
	switch c {
	case FailureProvider, FailureTimeout, FailureCancelled, FailureConfiguration,
		FailureInput, FailureBudget, FailurePanic, FailureUnknown:
		return true
	default:
		return false
	}
}

// TerminalState is the single, coverage-derived outcome of a run. It is the
// authoritative replacement for the warning-derived "completed_with_errors"
// status: it is computed only from the coverage sets, never from comment count
// or warnings.
type TerminalState string

const (
	StateComplete TerminalState = "complete"
	StatePartial  TerminalState = "partial"
	StateFailed   TerminalState = "failed"
	StateSkipped  TerminalState = "skipped"
)

// CoverageItem is one file's entry in a coverage set. ItemID is the stable
// identity (the normalized diff fingerprint) used to sort and cross-reference
// entries. Classification and Reason are only populated for failed items;
// Reason, when set, must already be a redacted summary — the builder never
// inspects or sanitizes it.
type CoverageItem struct {
	ItemID         string       `json:"item_id"`
	Path           string       `json:"path"`
	OldPath        string       `json:"old_path,omitempty"`
	Fingerprint    string       `json:"fingerprint,omitempty"`
	Classification FailureClass `json:"classification,omitempty"`
	Reason         string       `json:"reason,omitempty"`
}

// Coverage holds the five disjoint file sets. selected is the denominator and
// equals the disjoint union of completed, reused, failed and waived. Arrays are
// sorted by ItemID and are always non-nil so JSON renders "[]" rather than null.
type Coverage struct {
	Selected  []CoverageItem `json:"selected"`
	Completed []CoverageItem `json:"completed"`
	Reused    []CoverageItem `json:"reused"`
	Failed    []CoverageItem `json:"failed"`
	Waived    []CoverageItem `json:"waived"`
}

// ManifestRepository is the redacted repository identity for a run.
type ManifestRepository struct {
	IdentitySHA256 string `json:"identity_sha256,omitempty"`
}

// ManifestInput is the frozen, resolved input identity of a run. Resolved
// values are actual commit SHAs captured before execution, not the mutable refs
// the user typed; resume inherits these from the parent manifest.
type ManifestInput struct {
	RequestedFrom        string `json:"requested_from,omitempty"`
	RequestedHead        string `json:"requested_head,omitempty"`
	ResolvedBase         string `json:"resolved_base,omitempty"`
	ResolvedHead         string `json:"resolved_head,omitempty"`
	ExactRange           string `json:"exact_range,omitempty"`
	SourceArtifactSHA256 string `json:"source_artifact_sha256,omitempty"`
}

// ManifestExecution records how the run was executed. Only non-secret values
// and hashes are stored here — never tokens, endpoints or raw config.
type ManifestExecution struct {
	OCRVersion            string `json:"ocr_version,omitempty"`
	Provider              string `json:"provider,omitempty"`
	Model                 string `json:"model,omitempty"`
	ConfiguredConcurrency int    `json:"configured_concurrency,omitempty"`
	RuleConfigSHA256      string `json:"rule_config_sha256,omitempty"`
	RuntimeConfigSHA256   string `json:"runtime_config_sha256,omitempty"`
}

// RunManifest is the immutable, versioned coverage snapshot of a single run.
// It is produced once, at Finalize, and is the same object serialized to both
// the CLI JSON and the persisted session, so the two outlets can never compute
// coverage differently.
type RunManifest struct {
	SchemaVersion string             `json:"schema_version"`
	RunID         string             `json:"run_id"`
	ParentRunID   string             `json:"parent_run_id,omitempty"`
	Operation     string             `json:"operation"`
	TerminalState TerminalState      `json:"terminal_state"`
	Repository    ManifestRepository `json:"repository"`
	Input         ManifestInput      `json:"input"`
	Execution     ManifestExecution  `json:"execution"`
	Coverage      Coverage           `json:"coverage"`
	ElapsedMS     int64              `json:"elapsed_ms"`
}

// itemState is the internal per-item lifecycle. Every registered item starts as
// selected and moves to exactly one terminal state.
type itemState string

const (
	stateSelected  itemState = "selected"
	stateCompleted itemState = "completed"
	stateReused    itemState = "reused"
	stateFailed    itemState = "failed"
	stateWaived    itemState = "waived"
)

type builderItem struct {
	item  CoverageItem
	state itemState
}

// ManifestBuilder accumulates coverage for one run and freezes it into a
// RunManifest. It is safe for concurrent use: every registration, transition
// and freeze is serialized by a single mutex, a written terminal state is never
// overwritten by a second transition, and once frozen the builder rejects all
// further mutation. This lets subtasks at any concurrency update the same
// builder without racing on coverage.
type ManifestBuilder struct {
	mu    sync.Mutex
	items map[string]*builderItem

	runID       string
	parentRunID string
	operation   string
	repository  ManifestRepository
	input       ManifestInput
	execution   ManifestExecution

	runLevelFailure bool

	frozen bool
	result *RunManifest
}

// NewManifestBuilder creates a builder for a run identified by runID (the
// canonical session ID) and operation ("review" or "scan").
func NewManifestBuilder(runID, operation string) *ManifestBuilder {
	return &ManifestBuilder{
		runID:     runID,
		operation: operation,
		items:     make(map[string]*builderItem),
	}
}

// SetParentRunID links this run to the session it resumed from.
func (b *ManifestBuilder) SetParentRunID(parent string) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.frozen {
		b.parentRunID = parent
	}
}

// SetRepository sets the redacted repository identity.
func (b *ManifestBuilder) SetRepository(repo ManifestRepository) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.frozen {
		b.repository = repo
	}
}

// SetInput sets the frozen, resolved input identity.
func (b *ManifestBuilder) SetInput(in ManifestInput) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.frozen {
		b.input = in
	}
}

// SetExecution sets the non-secret execution provenance.
func (b *ManifestBuilder) SetExecution(ex ManifestExecution) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.frozen {
		b.execution = ex
	}
}

// RegisterSelected records an item in the selected set (the coverage
// denominator). It must be called once per item after filtering and before
// dispatch. Re-registering an already-known item_id is ignored so the first
// registration wins; registration after freeze is a no-op.
func (b *ManifestBuilder) RegisterSelected(item CoverageItem) {
	if b == nil || item.ItemID == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.frozen {
		return
	}
	if _, exists := b.items[item.ItemID]; exists {
		return
	}
	sel := CoverageItem{
		ItemID:      item.ItemID,
		Path:        item.Path,
		OldPath:     item.OldPath,
		Fingerprint: item.Fingerprint,
	}
	b.items[item.ItemID] = &builderItem{item: sel, state: stateSelected}
}

// transition moves a selected item to a terminal state. The first terminal
// state wins: a later call for the same item is ignored, so a completed item is
// never demoted to failed by a late error path. Transitions for unknown items
// or after freeze are no-ops.
func (b *ManifestBuilder) transition(itemID string, to itemState, class FailureClass, reason string) {
	if b == nil || itemID == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.frozen {
		return
	}
	bi, ok := b.items[itemID]
	if !ok || bi.state != stateSelected {
		return
	}
	bi.state = to
	if to == stateFailed {
		if !class.valid() {
			class = FailureUnknown
		}
		bi.item.Classification = class
		bi.item.Reason = sanitizeReason(reason)
	}
	if to == stateWaived {
		bi.item.Reason = sanitizeReason(reason)
	}
}

// maxReasonLen bounds the redacted failure/waive summary stored in the manifest,
// counted in runes so multibyte text is never cut mid-character.
const maxReasonLen = 500

var (
	// urlUserinfoRe matches credentials embedded in a URL (scheme://user:pass@host).
	urlUserinfoRe = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.\-]*://)[^/\s:@]+(?::[^/\s@]+)?@`)
	// bearerRe matches "Bearer <token>" / "Basic <token>" auth values.
	bearerRe = regexp.MustCompile(`(?i)\b(?:bearer|basic)\s+[A-Za-z0-9._~+/=\-]+`)
	// secretAssignmentRe matches `key: value` / `key=value` where the key names a
	// credential-like field, so the value can be redacted while the key is kept.
	secretAssignmentRe = regexp.MustCompile(`(?i)\b(authorization|api[_-]?key|access[_-]?token|refresh[_-]?token|client[_-]?secret|secret|password|passwd|token)\b(\s*[:=]\s*)"?[^\s"']+`)
)

// sanitizeReason is the single, best-effort redaction+truncation floor applied
// to every reason the builder stores, so no caller path can write an unredacted
// summary into the manifest (which is serialized to both CLI JSON and the
// persisted session). It is a floor, not a substitute for caller-side
// redaction: callers should still pass an already-summarized reason and omit
// anything they cannot confirm is safe. It strips a few high-confidence secret
// shapes, collapses to a single line, and caps length.
func sanitizeReason(s string) string {
	if s == "" {
		return ""
	}
	// Order matters: strip "Bearer <tok>" before the assignment rule, so a token
	// following "Authorization:" is removed rather than left behind.
	s = urlUserinfoRe.ReplaceAllString(s, "${1}[REDACTED]@")
	s = bearerRe.ReplaceAllString(s, "[REDACTED]")
	s = secretAssignmentRe.ReplaceAllString(s, "${1}${2}[REDACTED]")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	if utf8.RuneCountInString(s) > maxReasonLen {
		s = string([]rune(s)[:maxReasonLen]) + "…"
	}
	return s
}

// MarkCompleted records that the item's subtask completed. Whether it produced
// comments does not affect completion.
func (b *ManifestBuilder) MarkCompleted(itemID string) {
	b.transition(itemID, stateCompleted, "", "")
}

// MarkReused records that the item was reused from a parent session checkpoint.
func (b *ManifestBuilder) MarkReused(itemID string) {
	b.transition(itemID, stateReused, "", "")
}

// MarkFailed records that the item failed. An invalid or empty class is stored
// as FailureUnknown. reason must already be a redacted summary.
func (b *ManifestBuilder) MarkFailed(itemID string, class FailureClass, reason string) {
	b.transition(itemID, stateFailed, class, reason)
}

// MarkWaived records that the user explicitly waived this selected item, which
// must still be in the pre-terminal (selected) state — waiving an item that has
// already reached a terminal state is a no-op, preserving the first-terminal-
// wins rule. In the resume flow an item that failed in the parent session is
// re-registered as selected in this child run and waived here; the parent
// manifest is never modified. reason is required by the contract but not
// enforced at this layer.
func (b *ManifestBuilder) MarkWaived(itemID, reason string) {
	b.transition(itemID, stateWaived, "", reason)
}

// SetRunLevelFailure marks a failure that happened before or independently of
// per-item selection (e.g. diff resolution failed). It forces the terminal
// state to failed regardless of coverage.
func (b *ManifestBuilder) SetRunLevelFailure() {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.frozen {
		b.runLevelFailure = true
	}
}

// Frozen reports whether Finalize has already run.
func (b *ManifestBuilder) Frozen() bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.frozen
}

// Finalize sweeps any item that never received a terminal state into failed
// with FailureUnknown, computes the terminal state from the coverage sets,
// freezes the builder and returns the immutable manifest. It is idempotent:
// later calls return the same frozen manifest and ignore elapsed.
func (b *ManifestBuilder) Finalize(elapsed time.Duration) RunManifest {
	if b == nil {
		// Consistent with the nil-receiver guards on every other method: a nil
		// builder yields a well-formed, empty manifest (non-nil coverage arrays)
		// rather than panicking on b.mu.Lock().
		return RunManifest{
			SchemaVersion: ManifestSchemaVersion,
			TerminalState: StateSkipped,
			Coverage: Coverage{
				Selected:  []CoverageItem{},
				Completed: []CoverageItem{},
				Reused:    []CoverageItem{},
				Failed:    []CoverageItem{},
				Waived:    []CoverageItem{},
			},
		}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.frozen && b.result != nil {
		return *b.result
	}

	// Backstop: no selected item may be left without an outcome. This covers
	// goroutines that exited early, cancellation before dispatch, or any path
	// the caller forgot. It only runs when the process can still execute
	// finalize; a hard kill falls back to the per-item checkpoints.
	for _, bi := range b.items {
		if bi.state == stateSelected {
			bi.state = stateFailed
			bi.item.Classification = FailureUnknown
			if bi.item.Reason == "" {
				bi.item.Reason = "no terminal outcome recorded"
			}
		}
	}

	cov := b.buildCoverageLocked()
	m := RunManifest{
		SchemaVersion: ManifestSchemaVersion,
		RunID:         b.runID,
		ParentRunID:   b.parentRunID,
		Operation:     b.operation,
		TerminalState: b.computeTerminalLocked(cov),
		Repository:    b.repository,
		Input:         b.input,
		Execution:     b.execution,
		Coverage:      cov,
		ElapsedMS:     elapsed.Milliseconds(),
	}
	b.frozen = true
	b.result = &m
	return m
}

// buildCoverageLocked assembles the five sorted, non-nil coverage sets from the
// current item map. Caller must hold b.mu.
func (b *ManifestBuilder) buildCoverageLocked() Coverage {
	cov := Coverage{
		Selected:  []CoverageItem{},
		Completed: []CoverageItem{},
		Reused:    []CoverageItem{},
		Failed:    []CoverageItem{},
		Waived:    []CoverageItem{},
	}
	for _, bi := range b.items {
		sel := CoverageItem{
			ItemID:      bi.item.ItemID,
			Path:        bi.item.Path,
			OldPath:     bi.item.OldPath,
			Fingerprint: bi.item.Fingerprint,
		}
		cov.Selected = append(cov.Selected, sel)
		switch bi.state {
		case stateCompleted:
			cov.Completed = append(cov.Completed, sel)
		case stateReused:
			cov.Reused = append(cov.Reused, sel)
		case stateFailed:
			cov.Failed = append(cov.Failed, bi.item)
		case stateWaived:
			cov.Waived = append(cov.Waived, bi.item)
		}
	}
	sortItems(cov.Selected)
	sortItems(cov.Completed)
	sortItems(cov.Reused)
	sortItems(cov.Failed)
	sortItems(cov.Waived)
	return cov
}

// computeTerminalLocked derives the terminal state purely from coverage.
// Caller must hold b.mu. After the Finalize sweep every item is terminal, so
// "no failed" means all completed/reused/waived.
func (b *ManifestBuilder) computeTerminalLocked(cov Coverage) TerminalState {
	if b.runLevelFailure {
		return StateFailed
	}
	selected := len(cov.Selected)
	if selected == 0 {
		return StateSkipped
	}
	failed := len(cov.Failed)
	switch {
	case failed == 0:
		return StateComplete
	case failed == selected:
		return StateFailed
	default:
		return StatePartial
	}
}

func sortItems(items []CoverageItem) {
	sort.Slice(items, func(i, j int) bool {
		return items[i].ItemID < items[j].ItemID
	})
}
