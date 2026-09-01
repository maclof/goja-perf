//go:build (windows || linux) && amd64

package goja

import "testing"

func warmNativeTraceBenchmark(b *testing.B, call Callable, state *programTierState) {
	b.Helper()
	for i := 0; i < 4 && state.typed.nativeCode() == nil; i++ {
		if _, err := call(_undefined, valueInt(1000), valueInt(0)); err != nil {
			b.Fatal(err)
		}
	}
	if state.typed.nativeCode() == nil {
		b.Fatal("sustained warm-up did not install native code")
	}
}

// BenchmarkNativeTraceCompile isolates amd64 emission, RW allocation, the RX
// protection transition, and instruction-cache flushing. Region release stays
// outside the timed interval.
func BenchmarkNativeTraceCompile(b *testing.B) {
	trace := lowerTypedIntLoopTrace(typedTraceInstructionProgram())
	if trace == nil {
		b.Fatal("test loop did not lower")
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		native, err := compileNativeTrace(trace)
		if err != nil {
			b.Fatal(err)
		}
		if native == nil {
			b.Fatal("native compilation was unavailable")
		}
		b.StopTimer()
		if err := native.memory.release(); err != nil {
			b.Fatal(err)
		}
		b.StartTimer()
	}
}

// BenchmarkNativeTraceYield measures a long finite loop that returns through
// two bounded-yield safepoints before completing in native code.
func BenchmarkNativeTraceYield(b *testing.B) {
	runtime, call := setupTypedTraceBenchmark(b)
	if _, err := call(_undefined, valueInt(128), valueInt(0)); err != nil {
		b.Fatal(err)
	}
	state := typedTraceState(runtime)
	if state == nil {
		b.Fatal("warm-up did not install typed IR")
	}
	warmNativeTraceBenchmark(b, call, state)
	n := int64(nativeTraceIterationBudget*2 + 7)
	want := valueInt(n * (n - 1) / 2)
	native := state.typed.nativeCode()
	native.yields.Store(0)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		result, err := call(_undefined, valueInt(n), valueInt(0))
		if err != nil {
			b.Fatal(err)
		}
		checkTypedTraceBenchmarkResult(b, result, want)
	}
	b.StopTimer()
	if got, expected := uint64(native.yields.Load()), uint64(b.N)*2; got != expected {
		b.Fatalf("bounded yields: got %d, want %d", got, expected)
	}
}
