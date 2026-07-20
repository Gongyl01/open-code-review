package session

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

func sel(id string) CoverageItem {
	return CoverageItem{ItemID: id, Path: id + ".go", Fingerprint: "fp-" + id}
}

// register a set of selected items on a fresh builder.
func newBuilderWith(ids ...string) *ManifestBuilder {
	b := NewManifestBuilder("run-1", "review")
	for _, id := range ids {
		b.RegisterSelected(sel(id))
	}
	return b
}

func TestTerminalComplete(t *testing.T) {
	b := newBuilderWith("a", "b")
	b.MarkCompleted("a")
	b.MarkCompleted("b")
	m := b.Finalize(0)
	if m.TerminalState != StateComplete {
		t.Fatalf("terminal = %q, want complete", m.TerminalState)
	}
	if len(m.Coverage.Completed) != 2 || len(m.Coverage.Failed) != 0 {
		t.Fatalf("coverage = %+v", m.Coverage)
	}
	if len(m.Coverage.Selected) != 2 {
		t.Fatalf("selected = %d, want 2", len(m.Coverage.Selected))
	}
}

// Zero findings is a coverage concept: all selected complete regardless of
// comments, so the terminal state is still complete.
func TestTerminalCompleteZeroFindings(t *testing.T) {
	b := newBuilderWith("a")
	b.MarkCompleted("a")
	if got := b.Finalize(0).TerminalState; got != StateComplete {
		t.Fatalf("terminal = %q, want complete", got)
	}
}

func TestTerminalPartial(t *testing.T) {
	b := newBuilderWith("a", "b", "c")
	b.MarkCompleted("a")
	b.MarkReused("b")
	b.MarkFailed("c", FailureProvider, "provider concurrency")
	m := b.Finalize(0)
	if m.TerminalState != StatePartial {
		t.Fatalf("terminal = %q, want partial", m.TerminalState)
	}
	if len(m.Coverage.Failed) != 1 || m.Coverage.Failed[0].Classification != FailureProvider {
		t.Fatalf("failed coverage = %+v", m.Coverage.Failed)
	}
}

func TestTerminalFailedAll(t *testing.T) {
	b := newBuilderWith("a", "b")
	b.MarkFailed("a", FailureProvider, "x")
	b.MarkFailed("b", FailureTimeout, "y")
	if got := b.Finalize(0).TerminalState; got != StateFailed {
		t.Fatalf("terminal = %q, want failed", got)
	}
}

func TestTerminalSkipped(t *testing.T) {
	b := NewManifestBuilder("run-1", "review")
	if got := b.Finalize(0).TerminalState; got != StateSkipped {
		t.Fatalf("terminal = %q, want skipped", got)
	}
}

func TestRunLevelFailureForcesFailed(t *testing.T) {
	// No selected items, but a run-level error occurred -> failed, not skipped.
	b := NewManifestBuilder("run-1", "review")
	b.SetRunLevelFailure()
	if got := b.Finalize(0).TerminalState; got != StateFailed {
		t.Fatalf("terminal = %q, want failed", got)
	}
	// Even a fully-completed set is failed once a run-level error is flagged.
	b2 := newBuilderWith("a")
	b2.MarkCompleted("a")
	b2.SetRunLevelFailure()
	if got := b2.Finalize(0).TerminalState; got != StateFailed {
		t.Fatalf("terminal = %q, want failed", got)
	}
}

// Waived items count as resolved (non-failed): a run with completed + waived
// and no failed is complete.
func TestWaivedResolvesToComplete(t *testing.T) {
	b := newBuilderWith("a", "b")
	b.MarkReused("a")
	b.MarkWaived("b", "user waived on resume")
	m := b.Finalize(0)
	if m.TerminalState != StateComplete {
		t.Fatalf("terminal = %q, want complete", m.TerminalState)
	}
	if len(m.Coverage.Waived) != 1 || m.Coverage.Waived[0].Reason == "" {
		t.Fatalf("waived coverage = %+v", m.Coverage.Waived)
	}
}

// A failed + waived + completed mix still has an unresolved failure -> partial.
func TestFailedPlusWaivedIsPartial(t *testing.T) {
	b := newBuilderWith("a", "b", "c")
	b.MarkCompleted("a")
	b.MarkWaived("b", "waived")
	b.MarkFailed("c", FailureProvider, "boom")
	if got := b.Finalize(0).TerminalState; got != StatePartial {
		t.Fatalf("terminal = %q, want partial", got)
	}
}

// Finalize must sweep any item left without a terminal outcome into
// failed/unknown, so no selected item is ever silently dropped.
func TestFinalizeSweepsUndecidedToUnknown(t *testing.T) {
	b := newBuilderWith("a", "b")
	b.MarkCompleted("a")
	// "b" never marked — simulates a goroutine that exited early.
	m := b.Finalize(0)
	if m.TerminalState != StatePartial {
		t.Fatalf("terminal = %q, want partial", m.TerminalState)
	}
	if len(m.Coverage.Failed) != 1 {
		t.Fatalf("failed = %d, want 1", len(m.Coverage.Failed))
	}
	if got := m.Coverage.Failed[0]; got.Classification != FailureUnknown || got.Reason == "" {
		t.Fatalf("swept item = %+v, want unknown with reason", got)
	}
}

// The first terminal state wins; a late error must not demote a completed item.
func TestStateNotOverwritten(t *testing.T) {
	b := newBuilderWith("a")
	b.MarkCompleted("a")
	b.MarkFailed("a", FailureProvider, "late error")
	m := b.Finalize(0)
	if len(m.Coverage.Completed) != 1 || len(m.Coverage.Failed) != 0 {
		t.Fatalf("coverage = %+v, want a still completed", m.Coverage)
	}
}

// An invalid/empty failure class is normalized to unknown.
func TestInvalidFailureClassBecomesUnknown(t *testing.T) {
	b := newBuilderWith("a")
	b.MarkFailed("a", FailureClass("bogus"), "r")
	m := b.Finalize(0)
	if m.Coverage.Failed[0].Classification != FailureUnknown {
		t.Fatalf("class = %q, want unknown", m.Coverage.Failed[0].Classification)
	}
}

// Marking an item that was never selected is a no-op (not counted).
func TestMarkUnknownItemIgnored(t *testing.T) {
	b := newBuilderWith("a")
	b.MarkCompleted("a")
	b.MarkCompleted("ghost")
	m := b.Finalize(0)
	if len(m.Coverage.Selected) != 1 {
		t.Fatalf("selected = %d, want 1", len(m.Coverage.Selected))
	}
}

// Duplicate registration keeps the first entry.
func TestDuplicateRegistrationIgnored(t *testing.T) {
	b := NewManifestBuilder("run-1", "review")
	b.RegisterSelected(CoverageItem{ItemID: "a", Path: "first.go"})
	b.RegisterSelected(CoverageItem{ItemID: "a", Path: "second.go"})
	m := b.Finalize(0)
	if len(m.Coverage.Selected) != 1 || m.Coverage.Selected[0].Path != "first.go" {
		t.Fatalf("selected = %+v, want single first.go", m.Coverage.Selected)
	}
}

// After Finalize the builder is frozen: further mutation is ignored and
// Finalize is idempotent.
func TestFrozenAfterFinalize(t *testing.T) {
	b := newBuilderWith("a", "b")
	b.MarkCompleted("a")
	b.MarkCompleted("b")
	first := b.Finalize(5 * time.Second)
	if !b.Frozen() {
		t.Fatal("builder should be frozen")
	}
	b.RegisterSelected(sel("c"))
	b.MarkFailed("a", FailureProvider, "x")
	second := b.Finalize(99 * time.Second)
	if first.TerminalState != second.TerminalState || len(second.Coverage.Selected) != 2 {
		t.Fatalf("frozen manifest changed: %+v vs %+v", first, second)
	}
	if first.ElapsedMS != second.ElapsedMS {
		t.Fatalf("elapsed changed on re-finalize: %d vs %d", first.ElapsedMS, second.ElapsedMS)
	}
}

// Identity/execution setters populate the manifest and stop mattering after freeze.
func TestIdentityAndExecutionFields(t *testing.T) {
	b := newBuilderWith("a")
	b.SetParentRunID("run-parent")
	b.SetRepository(ManifestRepository{IdentitySHA256: "sha256:repo"})
	b.SetInput(ManifestInput{ResolvedBase: "8f6c", ResolvedHead: "c2d1", ExactRange: "8f6c..c2d1"})
	b.SetExecution(ManifestExecution{Provider: "anthropic", Model: "claude", ConfiguredConcurrency: 16})
	b.MarkCompleted("a")
	m := b.Finalize(0)
	if m.ParentRunID != "run-parent" || m.Repository.IdentitySHA256 != "sha256:repo" {
		t.Fatalf("identity not set: %+v", m)
	}
	if m.Input.ExactRange != "8f6c..c2d1" || m.Execution.ConfiguredConcurrency != 16 {
		t.Fatalf("input/execution not set: %+v", m)
	}
	if m.SchemaVersion != ManifestSchemaVersion || m.RunID != "run-1" || m.Operation != "review" {
		t.Fatalf("header wrong: %+v", m)
	}
}

// Coverage arrays are sorted by item_id and marshal as [] (never null).
func TestCoverageSortedAndNonNilJSON(t *testing.T) {
	b := newBuilderWith("c", "a", "b")
	b.MarkCompleted("c")
	b.MarkCompleted("a")
	b.MarkCompleted("b")
	m := b.Finalize(0)
	ids := []string{m.Coverage.Completed[0].ItemID, m.Coverage.Completed[1].ItemID, m.Coverage.Completed[2].ItemID}
	if ids[0] != "a" || ids[1] != "b" || ids[2] != "c" {
		t.Fatalf("not sorted: %v", ids)
	}
	data, err := json.Marshal(m.Coverage)
	if err != nil {
		t.Fatal(err)
	}
	// reused/failed/waived are empty and must render as [].
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"selected", "completed", "reused", "failed", "waived"} {
		if string(raw[k]) == "null" || raw[k] == nil {
			t.Fatalf("%s serialized as null, want []", k)
		}
	}
}

// Finalize on a nil receiver must not panic and must return a well-formed,
// empty manifest with non-nil coverage arrays (mirrors the other nil guards).
func TestFinalizeNilReceiver(t *testing.T) {
	var b *ManifestBuilder
	m := b.Finalize(0)
	if m.TerminalState != StateSkipped || m.SchemaVersion != ManifestSchemaVersion {
		t.Fatalf("nil finalize = %+v", m)
	}
	data, err := json.Marshal(m.Coverage)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"selected", "completed", "reused", "failed", "waived"} {
		if string(raw[k]) == "null" || raw[k] == nil {
			t.Fatalf("%s serialized as null on nil finalize", k)
		}
	}
}

// Within a single run, waiving an item that has already failed is a no-op: the
// first terminal state wins. Waiving happens on resume (a new run) where the
// item is re-registered as selected, not here.
func TestWaiveAfterFailedIsNoOp(t *testing.T) {
	b := newBuilderWith("a")
	b.MarkFailed("a", FailureProvider, "boom")
	b.MarkWaived("a", "too late")
	m := b.Finalize(0)
	if len(m.Coverage.Failed) != 1 || len(m.Coverage.Waived) != 0 {
		t.Fatalf("coverage = %+v, want a still failed", m.Coverage)
	}
	if m.TerminalState != StateFailed {
		t.Fatalf("terminal = %q, want failed", m.TerminalState)
	}
}

// sanitizeReason must strip high-confidence secret shapes and never let the
// secret substring survive into the stored reason.
func TestSanitizeReasonStripsSecrets(t *testing.T) {
	cases := []struct {
		name   string
		in     string
		secret string
	}{
		{"bearer", "provider error: Authorization: Bearer sk-abc123XYZ rejected", "sk-abc123XYZ"},
		{"basic", "auth failed Basic dXNlcjpwYXNz here", "dXNlcjpwYXNz"},
		{"api_key assignment", "config has api_key=SUPERSECRET123 value", "SUPERSECRET123"},
		{"token assignment", "token: ghp_TOKENVALUE99 expired", "ghp_TOKENVALUE99"},
		{"url userinfo", "clone https://alice:hunter2@github.com/x/y.git failed", "hunter2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sanitizeReason(tc.in)
			if strings.Contains(got, tc.secret) {
				t.Fatalf("secret %q leaked in %q", tc.secret, got)
			}
			if !strings.Contains(got, "[REDACTED]") {
				t.Fatalf("expected [REDACTED] marker in %q", got)
			}
		})
	}
}

// Reasons are capped to maxReasonLen runes (rune-safe) and collapsed to one line.
func TestSanitizeReasonTruncatesAndSingleLine(t *testing.T) {
	long := strings.Repeat("a", maxReasonLen+200)
	got := sanitizeReason(long)
	if utf8.RuneCountInString(got) > maxReasonLen+1 { // +1 for the ellipsis
		t.Fatalf("not truncated: %d runes", utf8.RuneCountInString(got))
	}
	if strings.ContainsAny(sanitizeReason("line1\nline2\rline3"), "\n\r") {
		t.Fatal("newlines not collapsed")
	}
	// Multibyte input must not be cut mid-rune.
	multibyte := strings.Repeat("世", maxReasonLen+50)
	if !utf8.ValidString(sanitizeReason(multibyte)) {
		t.Fatal("truncation produced invalid UTF-8")
	}
}

// The redaction floor is enforced through the builder, not just the helper:
// a secret passed via MarkFailed must not reach the manifest.
func TestMarkFailedEnforcesRedaction(t *testing.T) {
	b := newBuilderWith("a")
	b.MarkFailed("a", FailureProvider, "died: api_key=LEAKED_TOKEN_42")
	m := b.Finalize(0)
	if got := m.Coverage.Failed[0].Reason; strings.Contains(got, "LEAKED_TOKEN_42") {
		t.Fatalf("secret leaked through MarkFailed: %q", got)
	}
}

// ItemID is a deterministic hex SHA-256 of the fingerprint, distinct from the
// raw fingerprint, so raw/hashed mix-ups are catchable.
func TestItemIDDerivation(t *testing.T) {
	fp := "review:workspace:payment.go:abc123"
	got := ItemID(fp)
	if got == fp {
		t.Fatal("item_id must differ from raw fingerprint")
	}
	if len(got) != 64 {
		t.Fatalf("item_id length = %d, want 64 hex chars", len(got))
	}
	if got != ItemID(fp) {
		t.Fatal("item_id not deterministic")
	}
	if ItemID("a") == ItemID("b") {
		t.Fatal("distinct fingerprints must yield distinct item_ids")
	}
}

// SetSweepClass colors the undispatched (swept) items — cancel/budget scenarios
// the design mandates instead of a blanket unknown.
func TestSweepClassCancelledAndBudget(t *testing.T) {
	for _, tc := range []struct {
		name  string
		class FailureClass
	}{
		{"cancel", FailureCancelled},
		{"budget", FailureBudget},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newBuilderWith("a", "b")
			b.MarkCompleted("a")
			b.SetSweepClass(tc.class)
			// "b" never marked -> swept with the configured class.
			m := b.Finalize(0)
			if len(m.Coverage.Failed) != 1 || m.Coverage.Failed[0].Classification != tc.class {
				t.Fatalf("swept class = %+v, want %s", m.Coverage.Failed, tc.class)
			}
			if m.TerminalState != StatePartial {
				t.Fatalf("terminal = %q, want partial", m.TerminalState)
			}
		})
	}
}

// Default sweep class (unset) remains unknown; invalid class is ignored.
func TestSweepClassDefaultsUnknown(t *testing.T) {
	b := newBuilderWith("a")
	b.SetSweepClass(FailureClass("bogus")) // ignored
	m := b.Finalize(0)
	if m.Coverage.Failed[0].Classification != FailureUnknown {
		t.Fatalf("class = %q, want unknown", m.Coverage.Failed[0].Classification)
	}
}

// sanitizeReason strips control chars, ANSI escapes and Unicode line separators,
// and coerces invalid UTF-8 — so nothing survives to inject into a terminal.
func TestSanitizeReasonStripsControlChars(t *testing.T) {
	in := "err\x1b[2J\x1b[H boom\x00\x07\x7f end next\nline"
	got := sanitizeReason(in)
	for _, bad := range []string{"\x1b", "\x00", "\x07", "\x7f", " ", "\n"} {
		if strings.Contains(got, bad) {
			t.Fatalf("control char %q survived in %q", bad, got)
		}
	}
	if !utf8.ValidString(got) {
		t.Fatal("output not valid UTF-8")
	}
	if strings.ContainsAny(got, "\n\r\v\f") {
		t.Fatalf("not single line: %q", got)
	}
	// invalid UTF-8 bytes are replaced, not preserved
	if got2 := sanitizeReason("bad\xff\xfebytes"); strings.ContainsRune(got2, 0xff) {
		t.Fatalf("invalid UTF-8 survived: %q", got2)
	}
}

// A quoted secret value with spaces must be fully redacted (no partial leak).
func TestSanitizeReasonQuotedValue(t *testing.T) {
	got := sanitizeReason(`token="a b c" trailing`)
	if strings.Contains(got, "a b c") {
		t.Fatalf("quoted secret leaked: %q", got)
	}
	if !strings.Contains(got, "[REDACTED]") {
		t.Fatalf("expected redaction marker: %q", got)
	}
}

// Finalize returns snapshots that own their coverage slices: mutating one
// caller's manifest must not affect another's (immutability under aliasing).
func TestFinalizeReturnsOwnedSlices(t *testing.T) {
	b := newBuilderWith("a")
	b.MarkFailed("a", FailureProvider, "boom")
	m1 := b.Finalize(0)
	m2 := b.Finalize(0)
	m1.Coverage.Failed[0].Reason = "MUTATED"
	m1.Coverage.Selected[0].Path = "MUTATED"
	if m2.Coverage.Failed[0].Reason == "MUTATED" || m2.Coverage.Selected[0].Path == "MUTATED" {
		t.Fatal("returned manifests share backing arrays")
	}
}

// A zero-value builder (not via NewManifestBuilder) must not panic on use.
func TestZeroValueBuilderSafe(t *testing.T) {
	b := &ManifestBuilder{}
	b.RegisterSelected(CoverageItem{ItemID: "a", Path: "a.go"})
	b.MarkCompleted("a")
	m := b.Finalize(0)
	if len(m.Coverage.Completed) != 1 || m.TerminalState != StateComplete {
		t.Fatalf("zero-value builder produced %+v", m.Coverage)
	}
}

// Concurrent registration and transitions on distinct items must be race-free
// (run with -race) and produce exactly-once coverage.
func TestConcurrentTransitions(t *testing.T) {
	const n = 200
	b := NewManifestBuilder("run-1", "review")
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("item-%03d", i)
		b.RegisterSelected(sel(id))
	}
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := fmt.Sprintf("item-%03d", i)
			switch i % 3 {
			case 0:
				b.MarkCompleted(id)
			case 1:
				b.MarkReused(id)
			case 2:
				b.MarkFailed(id, FailureProvider, "e")
			}
		}(i)
	}
	wg.Wait()
	m := b.Finalize(0)
	total := len(m.Coverage.Completed) + len(m.Coverage.Reused) + len(m.Coverage.Failed) + len(m.Coverage.Waived)
	if len(m.Coverage.Selected) != n || total != n {
		t.Fatalf("selected=%d total=%d, want %d each", len(m.Coverage.Selected), total, n)
	}
}
