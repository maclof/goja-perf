package goja

import "testing"

var sandboxBenchmarkValue Value
var sandboxBenchmarkSandbox *Sandbox
var sandboxBenchmarkRuntime *Runtime

func BenchmarkSandboxSetup(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		var err error
		sandboxBenchmarkSandbox, err = NewSandbox(SandboxPolicy{
			Builtins: SandboxBuiltins{Allow: []string{"Math", "JSON"}},
			Capabilities: map[string]interface{}{
				"seed": 42,
			},
		})
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRuntimeSetupForSandboxComparison(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		sandboxBenchmarkRuntime = New()
		if err := sandboxBenchmarkRuntime.Set("seed", 42); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSandboxReset(b *testing.B) {
	s, err := NewSandbox(SandboxPolicy{
		Builtins: SandboxBuiltins{Allow: []string{"Math", "JSON"}},
		Capabilities: map[string]interface{}{
			"seed": 42,
		},
	})
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := s.Reset(); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSandboxRunProgramOverhead(b *testing.B) {
	program := MustCompile("sandbox_overhead_bench.js", `42`, true)
	s, err := NewSandbox(SandboxPolicy{})
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sandboxBenchmarkValue, err = s.RunProgram(program)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRuntimeRunProgramForSandboxOverheadComparison(b *testing.B) {
	program := MustCompile("sandbox_overhead_bench.js", `42`, true)
	r := New()
	var err error
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sandboxBenchmarkValue, err = r.RunProgram(program)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSandboxSteadyState(b *testing.B) {
	program := MustCompile("sandbox_bench.js", `
		var sum = 0;
		for (var i = 0; i < 100; i++) {
			sum += (i * 3) ^ (i >>> 1);
		}
		sum;
	`, true)
	s, err := NewSandbox(SandboxPolicy{})
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sandboxBenchmarkValue, err = s.RunProgram(program)
		if err != nil {
			b.Fatal(err)
		}
	}
	if sandboxBenchmarkValue.ToInteger() == 0 {
		b.Fatal("benchmark result was not consumed")
	}
}

func BenchmarkRuntimeSteadyStateForSandboxComparison(b *testing.B) {
	program := MustCompile("sandbox_bench.js", `
		var sum = 0;
		for (var i = 0; i < 100; i++) {
			sum += (i * 3) ^ (i >>> 1);
		}
		sum;
	`, true)
	r := New()
	var err error
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sandboxBenchmarkValue, err = r.RunProgram(program)
		if err != nil {
			b.Fatal(err)
		}
	}
	if sandboxBenchmarkValue.ToInteger() == 0 {
		b.Fatal("benchmark result was not consumed")
	}
}
