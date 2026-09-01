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
