package goja

import (
	"fmt"
	"testing"
)

const tieringDefinitions = `
function tierArithmetic(n) {
	var x = 1.25;
	for (var i = 0; i < n; i++) {
		x = (x + 3.5) * 0.75;
	}
	return x;
}

function tierAdd(a, b) {
	return a + b;
}

function tierCalls(n) {
	var sum = 0;
	for (var i = 0; i < n; i++) {
		sum = tierAdd(sum, i);
	}
	return sum;
}

function tierLoop(n) {
	var sum = 0;
	for (var i = 0; i < n; i++) {
		sum += i;
	}
	return sum;
}
`

type tieringBenchmarkWorkload struct {
	name     string
	function string
	want     func(int) Value
}

var tieringBenchmarkWorkloads = []tieringBenchmarkWorkload{
	{
		name:     "Arithmetic",
		function: "tierArithmetic",
		want: func(n int) Value {
			x := 1.25
			for i := 0; i < n; i++ {
				x = (x + 3.5) * 0.75
			}
			return valueFloat(x)
		},
	},
	{
		name:     "Functions",
		function: "tierCalls",
		want: func(n int) Value {
			return intToValue(int64(n * (n - 1) / 2))
		},
	},
	{
		name:     "Loop",
		function: "tierLoop",
		want: func(n int) Value {
			return intToValue(int64(n * (n - 1) / 2))
		},
	},
}

func compileTieringCall(function string, iterations int) *Program {
	return MustCompile("tiering_call.js", fmt.Sprintf("%s(%d)", function, iterations), false)
}

func checkTieringBenchmarkResult(b *testing.B, got Value, workload tieringBenchmarkWorkload, iterations int) {
	b.Helper()
	want := workload.want(iterations)
	if !got.StrictEquals(want) {
		b.Fatalf("unexpected result: got %v (%T), want %v (%T)", got, got, want, want)
	}
}

// BenchmarkTieringRuntimeSetup isolates creation of a runtime. Compilation and
// execution are measured separately below.
func BenchmarkTieringRuntimeSetup(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		New()
	}
}

// BenchmarkTieringCompile measures parser/compiler setup independently for each
// representative workload. No Runtime is created or executed.
func BenchmarkTieringCompile(b *testing.B) {
	for _, workload := range tieringBenchmarkWorkloads {
		b.Run(workload.name, func(b *testing.B) {
			b.ReportAllocs()
			source := tieringDefinitions + "\n" + workload.function + "(1000);"
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := Compile("tiering_compile.js", source, false); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkTieringColdExecution measures the first call of a one-iteration
// workload in a fresh Runtime. Runtime creation and function installation are
// outside the timed section.
func BenchmarkTieringColdExecution(b *testing.B) {
	const iterations = 1
	definitions := MustCompile("tiering_definitions.js", tieringDefinitions, false)
	for _, workload := range tieringBenchmarkWorkloads {
		b.Run(workload.name, func(b *testing.B) {
			b.ReportAllocs()
			call := compileTieringCall(workload.function, iterations)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				runtime := New()
				if _, err := runtime.RunProgram(definitions); err != nil {
					b.Fatal(err)
				}
				b.StartTimer()
				result, err := runtime.RunProgram(call)
				if err != nil {
					b.Fatal(err)
				}
				checkTieringBenchmarkResult(b, result, workload, iterations)
			}
		})
	}
}

// BenchmarkTieringTierUpExecution measures one call long enough to cross the
// internal hot-loop threshold. Each iteration uses a fresh Runtime, while
// runtime creation and function installation remain outside the timer.
func BenchmarkTieringTierUpExecution(b *testing.B) {
	const iterations = 64
	definitions := MustCompile("tiering_definitions.js", tieringDefinitions, false)
	for _, workload := range tieringBenchmarkWorkloads {
		b.Run(workload.name, func(b *testing.B) {
			b.ReportAllocs()
			call := compileTieringCall(workload.function, iterations)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				runtime := New()
				if _, err := runtime.RunProgram(definitions); err != nil {
					b.Fatal(err)
				}
				b.StartTimer()
				result, err := runtime.RunProgram(call)
				if err != nil {
					b.Fatal(err)
				}
				checkTieringBenchmarkResult(b, result, workload, iterations)
			}
		})
	}
}

// BenchmarkTieringSteadyState measures already-hot execution. Runtime setup,
// compilation, function installation, and one tier-up call are excluded from
// the timed section.
func BenchmarkTieringSteadyState(b *testing.B) {
	const iterations = 1000
	definitions := MustCompile("tiering_definitions.js", tieringDefinitions, false)
	for _, workload := range tieringBenchmarkWorkloads {
		b.Run(workload.name, func(b *testing.B) {
			b.ReportAllocs()
			runtime := New()
			if _, err := runtime.RunProgram(definitions); err != nil {
				b.Fatal(err)
			}
			call := compileTieringCall(workload.function, iterations)
			result, err := runtime.RunProgram(call)
			if err != nil {
				b.Fatal(err)
			}
			checkTieringBenchmarkResult(b, result, workload, iterations)

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				result, err = runtime.RunProgram(call)
				if err != nil {
					b.Fatal(err)
				}
				checkTieringBenchmarkResult(b, result, workload, iterations)
			}
		})
	}
}
