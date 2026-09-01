package goja

import "testing"

const globalResolutionBenchmarkSource = `
for (var i = 0; i < 100000; i++) {
}
i;
`

// BenchmarkGlobalResolutionLoop isolates the strict global-reference load and
// store path used by BenchmarkMainLoop while also consuming the result.
func BenchmarkGlobalResolutionLoop(b *testing.B) {
	program := MustCompile("global_resolution_benchmark.js", globalResolutionBenchmarkSource, true)
	runtime := New()
	b.ReportAllocs()
	b.ResetTimer()
	var result Value
	for i := 0; i < b.N; i++ {
		var err error
		result, err = runtime.RunProgram(program)
		if err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	if got := result.ToInteger(); got != 100000 {
		b.Fatalf("unexpected loop result: got %d, want 100000", got)
	}
}

// BenchmarkGlobalCounterTraceTierUp catches one-off-loop regressions below the
// delayed global trace activation threshold.
func BenchmarkGlobalCounterTraceTierUp(b *testing.B) {
	program := MustCompile("global_counter_tier_up.js", `for (var i = 0; i < 64; i++) {} i;`, true)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		runtime := New()
		result, err := runtime.RunProgram(program)
		if err != nil || result.ToInteger() != 64 {
			b.Fatalf("result=%v err=%v", result, err)
		}
	}
}

// BenchmarkGlobalCounterTraceCompile measures Runtime-owned lowering and
// Program cloning independently from execution.
func BenchmarkGlobalCounterTraceCompile(b *testing.B) {
	program := MustCompile("global_counter_compile.js", globalResolutionBenchmarkSource, true)
	quickCode, blocks := buildQuickenedCodeWithTrackedBackedge(program, 12)
	if blocks == 0 {
		b.Fatal("program did not quicken")
	}
	quickProgram := *program
	quickProgram.code = quickCode
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		trace := lowerGlobalCounterTraceAt(program, 12)
		state := &programTierState{program: program, quickProgram: &quickProgram}
		state.installGlobalCounterTrace(trace)
		if globalCounterTraceForState(state) == nil {
			b.Fatal("trace was not installed")
		}
	}
}

// BenchmarkGlobalCounterTraceDeoptimizedSteadyState measures the permanent
// quickened fallback after an unsupported replacement global object.
func BenchmarkGlobalCounterTraceDeoptimizedSteadyState(b *testing.B) {
	program := MustCompile("global_counter_deopt.js", `for (var i = 0; i < 128; i++) {} i;`, true)
	runtime := New()
	if _, err := runtime.RunProgram(program); err != nil {
		b.Fatal(err)
	}
	replacement := runtime.NewArray()
	runtime.SetGlobalObject(replacement)
	if _, err := runtime.RunProgram(program); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		result, err := runtime.RunProgram(program)
		if err != nil || result.ToInteger() != 128 {
			b.Fatalf("result=%v err=%v", result, err)
		}
	}
}
