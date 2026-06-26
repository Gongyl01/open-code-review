package telemetry

import (
	"context"
	"testing"
)

func TestIsEnabled_NotInitialized(t *testing.T) {
	// Reset global state
	initialized = false
	shutdownFuncs = nil

	if IsEnabled() {
		t.Error("expected IsEnabled()=false when not initialized")
	}
}

func TestIsEnabled_InitializedButNoShutdowns(t *testing.T) {
	initialized = true
	shutdownFuncs = nil
	defer func() {
		initialized = false
	}()

	if IsEnabled() {
		t.Error("expected IsEnabled()=false when no shutdown funcs registered")
	}
}

func TestIsEnabled_WithShutdowns(t *testing.T) {
	initialized = true
	shutdownFuncs = []func(context.Context) error{
		func(ctx context.Context) error { return nil },
	}
	defer func() {
		initialized = false
		shutdownFuncs = nil
	}()

	if !IsEnabled() {
		t.Error("expected IsEnabled()=true when shutdown funcs registered")
	}
}

func TestContentLogging_Disabled(t *testing.T) {
	initialized = false
	shutdownFuncs = nil

	if ContentLogging() {
		t.Error("expected ContentLogging()=false when telemetry disabled")
	}
}
