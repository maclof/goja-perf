package goja

import "testing"

const typedTraceBenchmarkSource = `
function typedTraceBenchmarkLoop(n, seed) {
	var sum = seed;
	for (var i = 0; i < n; i++) {
		sum += i;
	}
	return sum;
}
`

const typedFloatTraceBenchmarkSource = `
function typedFloatTraceBenchmarkLoop(n) {
	var x = 1.25;
	for (var i = 0; i < n; i++) {
		x = (x + 3.5) * 0.75;
	}
	return x;
}
`

const typedFloatDeoptBenchmarkSource = `
function typedFloatDeoptBenchmarkLoop(n) {
	var x = 1.25;
	for (var i = 0; i < n; i++) {
		x = (x + 3.5) * 0.75;
	}
	return x;
}
`

func typedFloatDeoptBenchmarkWant(iterations int64) Value {
	x := 1.25
	for i := int64(0); i < iterations; i++ {
		x = (x + 3.5) * 0.75
	}
	return valueFloat(x)
}

func setupTypedTraceBenchmark(b *testing.B) (*Runtime, Callable) {
	b.Helper()
	runtime := New()
	if _, err := runtime.RunProgram(MustCompile("typed_trace_benchmark.js", typedTraceBenchmarkSource, false)); err != nil {
		b.Fatal(err)
	}
	call, ok := AssertFunction(runtime.Get("typedTraceBenchmarkLoop"))
	if !ok {
		b.Fatal("typedTraceBenchmarkLoop is not callable")
	}
	return runtime, call
}

func checkTypedTraceBenchmarkResult(b *testing.B, result Value, want Value) {
	b.Helper()
	if !result.StrictEquals(want) {
		b.Fatalf("unexpected result: got %v (%T), want %v (%T)", result, result, want, want)
	}
}

// BenchmarkTypedTraceSetup measures Runtime creation and function installation
// separately from tier-up and steady-state execution.
func BenchmarkTypedTraceSetup(b *testing.B) {
	program := MustCompile("typed_trace_benchmark.js", typedTraceBenchmarkSource, false)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		runtime := New()
		if _, err := runtime.RunProgram(program); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkTypedTraceTierUp measures the call that crosses the hotness
// threshold and constructs both quickened code and typed IR.
func BenchmarkTypedTraceTierUp(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		runtime, call := setupTypedTraceBenchmark(b)
		b.StartTimer()
		result, err := call(_undefined, valueInt(64), valueInt(0))
		b.StopTimer()
		if err != nil {
			b.Fatal(err)
		}
		checkTypedTraceBenchmarkResult(b, result, valueInt(2016))
		if state := typedTraceState(runtime); state == nil {
			b.Fatal("tier-up did not produce typed IR")
		}
		b.StartTimer()
	}
}

// BenchmarkTypedTraceSteadyState measures a monomorphic integer loop after
// typed IR has been installed.
func BenchmarkTypedTraceSteadyState(b *testing.B) {
	runtime, call := setupTypedTraceBenchmark(b)
	result, err := call(_undefined, valueInt(1000), valueInt(0))
	if err != nil {
		b.Fatal(err)
	}
	checkTypedTraceBenchmarkResult(b, result, valueInt(499500))
	state := typedTraceState(runtime)
	if state == nil {
		b.Fatal("warm-up did not produce typed IR")
	}
	warmNativeTraceBenchmark(b, call, state)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		result, err = call(_undefined, valueInt(1000), valueInt(0))
		if err != nil {
			b.Fatal(err)
		}
		checkTypedTraceBenchmarkResult(b, result, valueInt(499500))
	}
}

// BenchmarkTypedFloatTraceSteadyState measures the representative floating-
// point loop shape independently from Runtime setup and compilation.
func BenchmarkTypedFloatTraceSteadyState(b *testing.B) {
	runtime := New()
	if _, err := runtime.RunProgram(MustCompile("typed_float_trace_benchmark.js", typedFloatTraceBenchmarkSource, false)); err != nil {
		b.Fatal(err)
	}
	call, ok := AssertFunction(runtime.Get("typedFloatTraceBenchmarkLoop"))
	if !ok {
		b.Fatal("typedFloatTraceBenchmarkLoop is not callable")
	}
	wantFloat := 1.25
	for i := 0; i < 1000; i++ {
		wantFloat = (wantFloat + 3.5) * 0.75
	}
	want := valueFloat(wantFloat)
	result, err := call(_undefined, valueInt(1000))
	if err != nil {
		b.Fatal(err)
	}
	checkTypedTraceBenchmarkResult(b, result, want)
	if state := typedTraceState(runtime); state == nil || state.typed.trace.code[2].opcode != typedTraceFloatAddLiteral {
		b.Fatal("warm-up did not produce floating-point typed IR")
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		result, err = call(_undefined, valueInt(1000))
		if err != nil {
			b.Fatal(err)
		}
		checkTypedTraceBenchmarkResult(b, result, want)
	}
}

// BenchmarkTypedFloatTraceDeoptimizedSteadyState measures the ordinary
// quickened fallback after a float trace has permanently failed an entry
// guard. On revisions without float tracing it measures the equivalent hot
// quickened program, making it suitable for isolated before/after comparison.
func BenchmarkTypedFloatTraceDeoptimizedSteadyState(b *testing.B) {
	runtime := New()
	if _, err := runtime.RunString(typedFloatDeoptBenchmarkSource); err != nil {
		b.Fatal(err)
	}
	call, ok := AssertFunction(runtime.Get("typedFloatDeoptBenchmarkLoop"))
	if !ok {
		b.Fatal("typedFloatDeoptBenchmarkLoop is not callable")
	}
	if _, err := call(_undefined, valueInt(128)); err != nil {
		b.Fatal(err)
	}
	state := typedTraceState(runtime)
	if result, err := call(_undefined, valueFloat(5.5)); err != nil {
		b.Fatal(err)
	} else if !result.StrictEquals(typedFloatDeoptBenchmarkWant(6)) {
		b.Fatalf("deopt result: got %v, want %v", result, typedFloatDeoptBenchmarkWant(6))
	}
	if state != nil && (!state.typed.disabled() || state.typed.guardFailures != 1) {
		b.Fatal("floating-point trace did not deopt exactly once")
	}

	want := typedFloatDeoptBenchmarkWant(1000)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		result, err := call(_undefined, valueInt(1000))
		if err != nil {
			b.Fatal(err)
		}
		if !result.StrictEquals(want) {
			b.Fatalf("fallback result: got %v, want %v", result, want)
		}
	}
	if state != nil && state.typed.guardFailures != 1 {
		b.Fatalf("guard failures thrashed: got %d, want 1", state.typed.guardFailures)
	}
}

// BenchmarkTypedTraceGuardFailure measures a polymorphic call that fails an
// entry guard, deoptimises once, and finishes in the exact interpreter path.
func BenchmarkTypedTraceGuardFailure(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		runtime, call := setupTypedTraceBenchmark(b)
		if _, err := call(_undefined, valueInt(1000), valueInt(0)); err != nil {
			b.Fatal(err)
		}
		b.StartTimer()
		result, err := call(_undefined, valueInt(1000), valueFloat(0.5))
		b.StopTimer()
		if err != nil {
			b.Fatal(err)
		}
		checkTypedTraceBenchmarkResult(b, result, valueFloat(499500.5))
		state := typedTraceState(runtime)
		if state == nil || !state.typed.disabled() || state.typed.guardFailures != 1 {
			b.Fatal("polymorphic call did not deoptimise exactly once")
		}
		b.StartTimer()
	}
}

// BenchmarkTypedTraceDeoptimizedSteadyState measures repeated polymorphic
// calls after the trace has been disabled, ensuring failures do not thrash.
func BenchmarkTypedTraceDeoptimizedSteadyState(b *testing.B) {
	runtime, call := setupTypedTraceBenchmark(b)
	if _, err := call(_undefined, valueInt(1000), valueInt(0)); err != nil {
		b.Fatal(err)
	}
	result, err := call(_undefined, valueInt(1000), valueFloat(0.5))
	if err != nil {
		b.Fatal(err)
	}
	checkTypedTraceBenchmarkResult(b, result, valueFloat(499500.5))
	state := typedTraceState(runtime)
	if state == nil || !state.typed.disabled() || state.typed.guardFailures != 1 {
		b.Fatal("warm-up did not leave one disabled trace")
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		result, err = call(_undefined, valueInt(1000), valueFloat(0.5))
		if err != nil {
			b.Fatal(err)
		}
		checkTypedTraceBenchmarkResult(b, result, valueFloat(499500.5))
	}
	if state.typed.guardFailures != 1 {
		b.Fatalf("guard failures thrashed: got %d, want 1", state.typed.guardFailures)
	}
}
