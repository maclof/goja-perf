//go:build (!windows && !linux) || !amd64

package goja

import "testing"

func assertNativeTraceBound(*testing.T, *Runtime) {}

func warmNativeTraceBenchmark(*testing.B, Callable, *programTierState) {}

func TestNativeTraceUnsupportedAttemptedOnce(t *testing.T) {
	state := &typedTraceTierState{trace: lowerTypedIntLoopTrace(typedTraceInstructionProgram())}
	state.observeNativeEligibility(int64(nativeTraceActivationIterations), compileNativeTrace)
	if !state.nativeAttempted() {
		t.Fatalf("unsupported backend state: attempted=%t", state.nativeAttempted())
	}
	state.observeNativeEligibility(int64(nativeTraceActivationIterations), func(*typedIntLoopTrace) (*nativeTraceCode, error) {
		t.Fatal("unsupported backend was retried")
		return nil, nil
	})
}
