package telemetry

import (
	"fmt"
	"testing"

	"go.opentelemetry.io/otel/attribute"
)

func TestAnyToAttr(t *testing.T) {
	tests := []struct {
		name string
		key  string
		val  interface{}
		want attribute.KeyValue
	}{
		{"string", "k", "hello", attribute.String("k", "hello")},
		{"int", "k", 42, attribute.Int64("k", 42)},
		{"int64", "k", int64(100), attribute.Int64("k", 100)},
		{"bool", "k", true, attribute.Bool("k", true)},
		{"float64", "k", 3.14, attribute.Float64("k", 3.14)},
		{"default fallback", "k", []int{1, 2}, attribute.String("k", fmt.Sprintf("%v", []int{1, 2}))},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := AnyToAttr(tc.key, tc.val)
			if got != tc.want {
				t.Errorf("AnyToAttr(%q, %v) = %v, want %v", tc.key, tc.val, got, tc.want)
			}
		})
	}
}

func TestSetAttr_NilSpan(t *testing.T) {
	// Should not panic
	SetAttr(nil, "key", "value")
}

func TestRecordToolResult_NilSpan(t *testing.T) {
	// Should not panic
	RecordToolResult(nil, "tool", 100, nil)
	RecordToolResult(nil, "tool", 100, fmt.Errorf("err"))
}
