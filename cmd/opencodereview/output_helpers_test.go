package main

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/open-code-review/open-code-review/internal/agent"
	"github.com/open-code-review/open-code-review/internal/model"
)

func TestHasSubtaskErrors(t *testing.T) {
	tests := []struct {
		name     string
		warnings []agent.AgentWarning
		want     bool
	}{
		{"nil warnings", nil, false},
		{"empty", []agent.AgentWarning{}, false},
		{"no subtask errors", []agent.AgentWarning{{Type: "other", Message: "msg"}}, false},
		{"has subtask error", []agent.AgentWarning{{Type: "subtask_error", Message: "fail"}}, true},
		{"mixed", []agent.AgentWarning{{Type: "warn"}, {Type: "subtask_error"}}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := hasSubtaskErrors(tc.warnings)
			if got != tc.want {
				t.Errorf("hasSubtaskErrors() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestWrapByRunes(t *testing.T) {
	tests := []struct {
		name  string
		text  string
		maxW  int
		lines int
	}{
		{"empty", "", 80, 0},
		{"short line", "hello", 80, 1},
		{"exact width", strings.Repeat("a", 10), 10, 1},
		{"wraps long line", strings.Repeat("word ", 25), 20, 7},
		{"respects newlines", "line1\nline2\nline3", 80, 3},
		{"wrap with newlines", "short\n" + strings.Repeat("x", 50), 20, 4},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := wrapByRunes(tc.text, tc.maxW)
			if len(got) != tc.lines {
				t.Errorf("wrapByRunes() got %d lines, want %d\nlines: %v", len(got), tc.lines, got)
			}
		})
	}
}

func TestWrapSingleRuneLine(t *testing.T) {
	tests := []struct {
		name string
		line string
		maxW int
		min  int
	}{
		{"short line unchanged", "hello", 100, 1},
		{"wraps at space", "hello world foo bar baz", 12, 2},
		{"no space to wrap", strings.Repeat("x", 30), 10, 3},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := wrapSingleRuneLine(tc.line, tc.maxW)
			if len(got) < tc.min {
				t.Errorf("got %d lines, want at least %d", len(got), tc.min)
			}
		})
	}
}

func TestRuneWrapCut(t *testing.T) {
	// Short line returns full length
	runes := []rune("short")
	cut := runeWrapCut(runes, 100)
	if cut != len(runes) {
		t.Errorf("expected %d, got %d", len(runes), cut)
	}

	// Cuts at space
	runes = []rune("hello world test")
	cut = runeWrapCut(runes, 11)
	if runes[cut] != ' ' && cut != 11 {
		t.Errorf("expected cut at space boundary, got %d (char=%c)", cut, runes[cut])
	}
}

func TestVisibleRunesLen(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"hello", 5},
		{"", 0},
		{"\x01\x02\x03", 0},
		{"a\x01b", 2},
		{"\x7f", 0},
	}
	for _, tc := range tests {
		got := visibleRunesLen([]rune(tc.input))
		if got != tc.want {
			t.Errorf("visibleRunesLen(%q) = %d, want %d", tc.input, got, tc.want)
		}
	}
}

func TestSplitToLines(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"a\nb\nc", 3},
		{"a\nb\nc\n", 3},
		{"single", 1},
		{"crlf\r\nline", 2},
		{"", 0},
	}
	for _, tc := range tests {
		got := splitToLines(tc.input)
		if len(got) != tc.want {
			t.Errorf("splitToLines(%q) = %d lines, want %d", tc.input, len(got), tc.want)
		}
	}
}

func TestBuildDiffLines(t *testing.T) {
	t.Run("empty suggestion returns nil", func(t *testing.T) {
		c := model.LlmComment{ExistingCode: "old", SuggestionCode: ""}
		got := buildDiffLines(c)
		if got != nil {
			t.Errorf("expected nil, got %v", got)
		}
	})

	t.Run("empty existing returns nil", func(t *testing.T) {
		c := model.LlmComment{ExistingCode: "", SuggestionCode: "new"}
		got := buildDiffLines(c)
		if got != nil {
			t.Errorf("expected nil, got %v", got)
		}
	})

	t.Run("diff computed", func(t *testing.T) {
		c := model.LlmComment{
			ExistingCode:   "line1\nline2\n",
			SuggestionCode: "line1\nmodified\n",
		}
		got := buildDiffLines(c)
		if len(got) == 0 {
			t.Error("expected non-empty diff lines")
		}
	})
}

func TestStatusBadge(t *testing.T) {
	tests := []struct {
		status string
		substr string
	}{
		{"added", "[A]"},
		{"modified", "[M]"},
		{"deleted", "[D]"},
		{"renamed", "[R]"},
		{"binary", "[B]"},
		{"scan", "[S]"},
		{"unknown", "[?]"},
	}
	for _, tc := range tests {
		got := statusBadge(tc.status)
		if !strings.Contains(got, tc.substr) {
			t.Errorf("statusBadge(%q) = %q, expected to contain %q", tc.status, got, tc.substr)
		}
	}
}

func TestOutputJSON(t *testing.T) {
	// Redirect stdout to capture output
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	comments := []model.LlmComment{
		{Path: "a.go", Content: "fix bug", StartLine: 1, EndLine: 5},
	}
	err := outputJSON(comments)

	w.Close()
	os.Stdout = old

	if err != nil {
		t.Fatalf("outputJSON error: %v", err)
	}

	var buf bytes.Buffer
	buf.ReadFrom(r)

	var out jsonOutput
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}
	if out.Status != "success" {
		t.Errorf("status = %q, want success", out.Status)
	}
	if len(out.Comments) != 1 {
		t.Errorf("expected 1 comment, got %d", len(out.Comments))
	}
}

func TestOutputJSON_NoComments(t *testing.T) {
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := outputJSON(nil)

	w.Close()
	os.Stdout = old

	if err != nil {
		t.Fatalf("outputJSON error: %v", err)
	}

	var buf bytes.Buffer
	buf.ReadFrom(r)

	var out jsonOutput
	json.Unmarshal(buf.Bytes(), &out)
	if out.Message == "" {
		t.Error("expected non-empty message when no comments")
	}
}

func TestOutputJSONWithWarnings(t *testing.T) {
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	comments := []model.LlmComment{{Path: "b.go", Content: "test"}}
	warnings := []agent.AgentWarning{{Type: "subtask_error", File: "c.go", Message: "failed"}}
	err := outputJSONWithWarnings(comments, warnings, 5, 100, 50, 150, 10, 5, 3*time.Second, "summary", map[string]int64{"file_read": 3})

	w.Close()
	os.Stdout = old

	if err != nil {
		t.Fatalf("error: %v", err)
	}

	var buf bytes.Buffer
	buf.ReadFrom(r)

	var out jsonOutput
	json.Unmarshal(buf.Bytes(), &out)
	if out.Status != "completed_with_errors" {
		t.Errorf("status = %q, want completed_with_errors", out.Status)
	}
	if out.Summary == nil {
		t.Fatal("expected non-nil summary")
	}
	if out.Summary.FilesReviewed != 5 {
		t.Errorf("FilesReviewed = %d, want 5", out.Summary.FilesReviewed)
	}
	if out.ToolCalls == nil || out.ToolCalls.Total != 3 {
		t.Errorf("ToolCalls.Total = %v", out.ToolCalls)
	}
}

func TestOutputJSONWithWarnings_NoCommentsNoErrors(t *testing.T) {
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	warnings := []agent.AgentWarning{{Type: "warning", Message: "something"}}
	err := outputJSONWithWarnings(nil, warnings, 2, 50, 20, 70, 0, 0, time.Second, "", nil)

	w.Close()
	os.Stdout = old

	if err != nil {
		t.Fatalf("error: %v", err)
	}

	var buf bytes.Buffer
	buf.ReadFrom(r)

	var out jsonOutput
	json.Unmarshal(buf.Bytes(), &out)
	if out.Status != "completed_with_warnings" {
		t.Errorf("status = %q, want completed_with_warnings", out.Status)
	}
	if out.Message == "" {
		t.Error("expected non-empty message")
	}
}

func TestOutputJSONNoFiles(t *testing.T) {
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := outputJSONNoFiles()

	w.Close()
	os.Stdout = old

	if err != nil {
		t.Fatalf("error: %v", err)
	}

	var buf bytes.Buffer
	buf.ReadFrom(r)

	var out jsonOutput
	json.Unmarshal(buf.Bytes(), &out)
	if out.Status != "skipped" {
		t.Errorf("status = %q, want skipped", out.Status)
	}
}
