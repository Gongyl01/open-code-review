package telemetry

import (
	"testing"
	"time"
)

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		dur  time.Duration
		want string
	}{
		{0, "0s"},
		{1500 * time.Millisecond, "1.5s"},
		{60 * time.Second, "1m0s"},
		{123 * time.Millisecond, "123ms"},
		{2*time.Minute + 30*time.Second, "2m30s"},
	}
	for _, tc := range tests {
		got := FormatDuration(tc.dur)
		if got != tc.want {
			t.Errorf("FormatDuration(%v) = %q, want %q", tc.dur, got, tc.want)
		}
	}
}

func TestSummarizeArgs(t *testing.T) {
	tests := []struct {
		name string
		args map[string]any
		want string
	}{
		{"nil map", nil, ""},
		{"empty map", map[string]any{}, ""},
		{"path key returns quoted", map[string]any{"path": "foo/bar.go"}, `"foo/bar.go"`},
		{"search key returns quoted", map[string]any{"search": "hello"}, `"hello"`},
		{"query key returns quoted", map[string]any{"query": "world"}, `"world"`},
		{"pattern key returns quoted", map[string]any{"pattern": "*.go"}, `"*.go"`},
		{"generic short value", map[string]any{"foo": "bar"}, "foo=bar"},
		{"long value skipped", map[string]any{"data": string(make([]byte, 60))}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := summarizeArgs(tc.args)
			if got != tc.want {
				t.Errorf("summarizeArgs(%v) = %q, want %q", tc.args, got, tc.want)
			}
		})
	}
}
