package telemetry

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestRecordFunctions_DisabledTelemetry(t *testing.T) {
	// Reset state so telemetry is disabled
	initialized = false
	shutdownFuncs = nil

	ctx := context.Background()

	// These should all be no-ops when telemetry is disabled
	RecordReviewDuration(ctx, 5*time.Second)
	RecordFilesReviewed(ctx, 10)
	RecordCommentsGenerated(ctx, 3)
	RecordLLMRequest(ctx, "gpt-4", 2*time.Second, 1000, "ok")
	RecordToolCall(ctx, "file_read", 100*time.Millisecond, true)
	RecordToolCall(ctx, "file_read", 100*time.Millisecond, false)
}

func TestCheckMetricErr(t *testing.T) {
	// Should not panic
	checkMetricErr(nil)
	checkMetricErr(fmt.Errorf("some error"))
}
