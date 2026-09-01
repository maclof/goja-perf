package goja

import "testing"

const typedTraceShapeBenchmarkSource = `
function typedTraceDescending(start, limit, seed) {
	var sum = seed;
	for (var i = start; i > limit; i--) {
		sum += i;
	}
	return sum;
}

function typedTraceInclusive(limit, seed) {
	var sum = seed;
	for (var i = 0; i <= limit; i++) {
		sum += i;
	}
	return sum;
}

function typedTraceStepTwo(limit, seed) {
	var sum = seed;
	for (var i = 0; i < limit; i += 2) {
		sum += i;
	}
	return sum;
}
`

func setupTypedTraceShapeBenchmark(b *testing.B, name string) (*Runtime, Callable) {
	b.Helper()
	runtime := New()
	if _, err := runtime.RunProgram(MustCompile("typed_trace_shape_benchmark.js", typedTraceShapeBenchmarkSource, false)); err != nil {
		b.Fatal(err)
	}
	call, ok := AssertFunction(runtime.Get(name))
	if !ok {
		b.Fatalf("%s is not callable", name)
	}
	return runtime, call
}

func checkTypedTraceShapeBenchmarkResult(b *testing.B, result Value, want int64) {
	b.Helper()
	if !result.StrictEquals(valueInt(want)) {
		b.Fatalf("unexpected result: got %v (%T), want %d", result, result, want)
	}
}

func BenchmarkTypedTraceShapeSteadyState(b *testing.B) {
	for _, benchmark := range []struct {
		name      string
		arguments []Value
		want      int64
	}{
		{name: "typedTraceDescending", arguments: []Value{valueInt(1000), valueInt(0), valueInt(0)}, want: 500500},
		{name: "typedTraceInclusive", arguments: []Value{valueInt(999), valueInt(0)}, want: 499500},
		{name: "typedTraceStepTwo", arguments: []Value{valueInt(1000), valueInt(0)}, want: 249500},
	} {
		b.Run(benchmark.name, func(b *testing.B) {
			_, call := setupTypedTraceShapeBenchmark(b, benchmark.name)
			for i := 0; i < 6; i++ {
				result, err := call(_undefined, benchmark.arguments...)
				if err != nil {
					b.Fatal(err)
				}
				checkTypedTraceShapeBenchmarkResult(b, result, benchmark.want)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				result, err := call(_undefined, benchmark.arguments...)
				if err != nil {
					b.Fatal(err)
				}
				checkTypedTraceShapeBenchmarkResult(b, result, benchmark.want)
			}
		})
	}
}

func BenchmarkTypedTraceDescendingSetup(b *testing.B) {
	program := MustCompile("typed_trace_shape_benchmark.js", typedTraceShapeBenchmarkSource, false)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		runtime := New()
		if _, err := runtime.RunProgram(program); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkTypedTraceDescendingTierUp(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		_, call := setupTypedTraceShapeBenchmark(b, "typedTraceDescending")
		b.StartTimer()
		result, err := call(_undefined, valueInt(64), valueInt(0), valueInt(0))
		b.StopTimer()
		if err != nil {
			b.Fatal(err)
		}
		checkTypedTraceShapeBenchmarkResult(b, result, 2080)
		b.StartTimer()
	}
}

func BenchmarkTypedTraceDescendingSteadyState(b *testing.B) {
	_, call := setupTypedTraceShapeBenchmark(b, "typedTraceDescending")
	for i := 0; i < 6; i++ {
		result, err := call(_undefined, valueInt(1000), valueInt(0), valueInt(0))
		if err != nil {
			b.Fatal(err)
		}
		checkTypedTraceShapeBenchmarkResult(b, result, 500500)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		result, err := call(_undefined, valueInt(1000), valueInt(0), valueInt(0))
		if err != nil {
			b.Fatal(err)
		}
		checkTypedTraceShapeBenchmarkResult(b, result, 500500)
	}
}
