package goja

import "testing"

const typedTraceConstantStepBenchmarkSource = `
function typedTracePlusTwo(limit, seed) {
	var sum = seed;
	for (var i = 0; i < limit; i += 2) {
		sum += i;
	}
	return sum;
}

function typedTracePlusThreeInclusive(limit, seed) {
	var sum = seed;
	for (var i = 0; i <= limit; i += 3) {
		sum += i;
	}
	return sum;
}

function typedTraceMinusTwo(start, limit, seed) {
	var sum = seed;
	for (var i = start; i > limit; i -= 2) {
		sum += i;
	}
	return sum;
}

function typedTraceMinusThree(start, limit, seed) {
	var sum = seed;
	for (var i = start; i > limit; i -= 3) {
		sum += i;
	}
	return sum;
}
`

type typedTraceConstantStepBenchmark struct {
	name      string
	arguments []Value
	want      int64
	tierArgs  []Value
	tierWant  int64
}

var typedTraceConstantStepBenchmarks = []typedTraceConstantStepBenchmark{
	{name: "typedTracePlusTwo", arguments: []Value{valueInt(2000), valueInt(0)}, want: 999000, tierArgs: []Value{valueInt(128), valueInt(0)}, tierWant: 4032},
	{name: "typedTracePlusThreeInclusive", arguments: []Value{valueInt(2997), valueInt(0)}, want: 1498500, tierArgs: []Value{valueInt(189), valueInt(0)}, tierWant: 6048},
	{name: "typedTraceMinusTwo", arguments: []Value{valueInt(2000), valueInt(0), valueInt(0)}, want: 1001000, tierArgs: []Value{valueInt(128), valueInt(0), valueInt(0)}, tierWant: 4160},
	{name: "typedTraceMinusThree", arguments: []Value{valueInt(3000), valueInt(0), valueInt(0)}, want: 1501500, tierArgs: []Value{valueInt(192), valueInt(0), valueInt(0)}, tierWant: 6240},
}

func setupTypedTraceConstantStepBenchmark(b *testing.B, name string) Callable {
	b.Helper()
	runtime := New()
	if _, err := runtime.RunProgram(MustCompile("typed_trace_constant_step_benchmark.js", typedTraceConstantStepBenchmarkSource, false)); err != nil {
		b.Fatal(err)
	}
	call, ok := AssertFunction(runtime.Get(name))
	if !ok {
		b.Fatalf("%s is not callable", name)
	}
	return call
}

func BenchmarkTypedTraceConstantStepSetup(b *testing.B) {
	program := MustCompile("typed_trace_constant_step_benchmark.js", typedTraceConstantStepBenchmarkSource, false)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		runtime := New()
		if _, err := runtime.RunProgram(program); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkTypedTraceConstantStepTierUp(b *testing.B) {
	for _, benchmark := range typedTraceConstantStepBenchmarks {
		b.Run(benchmark.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				call := setupTypedTraceConstantStepBenchmark(b, benchmark.name)
				b.StartTimer()
				result, err := call(_undefined, benchmark.tierArgs...)
				b.StopTimer()
				if err != nil {
					b.Fatal(err)
				}
				if !result.StrictEquals(valueInt(benchmark.tierWant)) {
					b.Fatalf("unexpected result: got %v, want %d", result, benchmark.tierWant)
				}
				b.StartTimer()
			}
		})
	}
}

func BenchmarkTypedTraceConstantStepSteadyState(b *testing.B) {
	for _, benchmark := range typedTraceConstantStepBenchmarks {
		b.Run(benchmark.name, func(b *testing.B) {
			call := setupTypedTraceConstantStepBenchmark(b, benchmark.name)
			for i := 0; i < 16; i++ {
				result, err := call(_undefined, benchmark.arguments...)
				if err != nil {
					b.Fatal(err)
				}
				if !result.StrictEquals(valueInt(benchmark.want)) {
					b.Fatalf("unexpected result: got %v, want %d", result, benchmark.want)
				}
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				result, err := call(_undefined, benchmark.arguments...)
				if err != nil {
					b.Fatal(err)
				}
				if !result.StrictEquals(valueInt(benchmark.want)) {
					b.Fatalf("unexpected result: got %v, want %d", result, benchmark.want)
				}
			}
		})
	}
}
