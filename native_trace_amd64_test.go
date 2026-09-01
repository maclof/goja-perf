//go:build (windows || linux) && amd64

package goja

import (
	"bytes"
	"errors"
	goruntime "runtime"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"
)

func compileTestNativeTrace(t *testing.T) (*typedIntLoopTrace, *nativeTraceCode) {
	t.Helper()
	trace := lowerTypedIntLoopTrace(typedTraceInstructionProgram())
	if trace == nil {
		t.Fatal("test loop did not lower to typed IR")
	}
	return trace, compileNativeTraceForTest(t, trace)
}

func compileNativeTraceForTest(t *testing.T, trace *typedIntLoopTrace) *nativeTraceCode {
	t.Helper()
	native, err := compileNativeTrace(trace)
	if err != nil {
		t.Fatal(err)
	}
	if native == nil {
		t.Fatal("supported typed IR did not compile to native code")
	}
	t.Cleanup(func() {
		if err := native.memory.release(); err != nil {
			t.Error(err)
		}
	})
	return native
}

func TestNativeTraceRejectsTypedFloatIR(t *testing.T) {
	runtime, call := setupTypedFloatTraceTest(t)
	if _, err := call(_undefined, valueInt(128)); err != nil {
		t.Fatal(err)
	}
	state := typedTraceState(runtime)
	if state == nil {
		t.Fatal("floating-point trace was not produced")
	}
	native, err := compileNativeTrace(state.typed.trace)
	if err != nil {
		t.Fatal(err)
	}
	if native != nil {
		t.Fatal("integer native backend accepted floating-point typed IR")
	}
}

func setupNativeTraceTest(t *testing.T) (*Runtime, Callable, *programTierState) {
	t.Helper()
	runtime, call := setupTypedTraceTest(t)
	callTypedTraceTest(t, call, valueInt(128), valueInt(0))
	state := typedTraceState(runtime)
	if state == nil {
		t.Fatal("hot loop did not install typed IR")
	}
	for i := 0; i < 4 && state.typed.nativeCode() == nil; i++ {
		callTypedTraceTest(t, call, valueInt(1000), valueInt(0))
	}
	if state.typed.nativeCode() == nil || !state.typed.nativeAttempted() {
		t.Fatal("sustained eligible work did not install native code")
	}
	return runtime, call, state
}

func TestNativeTraceLazyActivation(t *testing.T) {
	runtime, call := setupTypedTraceTest(t)
	result := callTypedTraceTest(t, call, valueInt(64), valueInt(0))
	if result.ToInteger() != 2016 {
		t.Fatalf("tier-up result: got %v, want 2016", result)
	}
	state := typedTraceState(runtime)
	if state == nil {
		t.Fatal("tier-up did not produce typed IR")
	}
	if state.typed.nativeCode() != nil {
		t.Fatal("one-off 64-iteration tier-up allocated native code")
	}
	if state.typed.nativeAttempted() || state.typed.nativeEligibleIterations() != 33 {
		t.Fatalf("lazy activation state: attempted=%t eligible=%d, want false/33", state.typed.nativeAttempted(), state.typed.nativeEligibleIterations())
	}
}

func TestNativeDescendingTraceLazyActivation(t *testing.T) {
	runtime, call := setupTypedDescendingTraceTest(t)
	result := callTypedDescendingTraceTest(t, call, valueInt(64), valueInt(0), valueInt(0))
	if result.ToInteger() != 2080 {
		t.Fatalf("tier-up result: got %v, want 2080", result)
	}
	state := typedTraceState(runtime)
	if state == nil {
		t.Fatal("tier-up did not produce descending typed IR")
	}
	if state.typed.nativeCode() != nil {
		t.Fatal("one-off descending 64-iteration tier-up allocated native code")
	}
	if state.typed.nativeAttempted() || state.typed.nativeEligibleIterations() != 33 {
		t.Fatalf("lazy descending activation: attempted=%t eligible=%d, want false/33", state.typed.nativeAttempted(), state.typed.nativeEligibleIterations())
	}
}

func TestNativeInclusiveTraceLazyActivation(t *testing.T) {
	runtime, call := setupTypedInclusiveTraceTest(t)
	sum, counter := callTypedInclusiveTraceTest(t, runtime, call, valueInt(0), valueInt(63), valueInt(0))
	if sum.ToInteger() != 2016 || counter.ToInteger() != 64 {
		t.Fatalf("tier-up result: sum/counter=%v/%v, want 2016/64", sum, counter)
	}
	state := typedTraceState(runtime)
	if state == nil {
		t.Fatal("tier-up did not produce inclusive typed IR")
	}
	if state.typed.nativeCode() != nil {
		t.Fatal("one-off inclusive 64-iteration tier-up allocated native code")
	}
	if state.typed.nativeAttempted() || state.typed.nativeEligibleIterations() != 33 {
		t.Fatalf("lazy inclusive activation: attempted=%t eligible=%d, want false/33", state.typed.nativeAttempted(), state.typed.nativeEligibleIterations())
	}
}

func TestNativeTraceEventuallyActivates(t *testing.T) {
	runtime, call := setupTypedTraceTest(t)
	callTypedTraceTest(t, call, valueInt(64), valueInt(0))
	state := typedTraceState(runtime)
	for i := 0; i < 4; i++ {
		callTypedTraceTest(t, call, valueInt(1000), valueInt(0))
	}
	if state.typed.nativeCode() != nil || state.typed.nativeAttempted() {
		t.Fatal("native code activated before the cumulative threshold")
	}
	callTypedTraceTest(t, call, valueInt(1000), valueInt(0))
	if state.typed.nativeCode() == nil || !state.typed.nativeAttempted() {
		t.Fatal("repeated medium loops did not eventually install native code")
	}
}

func TestNativeTraceFirstLongLoopActivation(t *testing.T) {
	tests := []struct {
		name       string
		iterations int64
		wantNative bool
	}{
		{name: "BelowCumulativeThreshold", iterations: int64(nativeTraceActivationIterations) + 30},
		{name: "AtCumulativeThreshold", iterations: int64(nativeTraceActivationIterations) + 31, wantNative: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runtime, call := setupTypedTraceTest(t)
			callTypedTraceTest(t, call, valueInt(test.iterations), valueInt(0))
			state := typedTraceState(runtime)
			if got := state.typed.nativeCode() != nil; got != test.wantNative {
				t.Fatalf("native activation for %d iterations: got %t, want %t", test.iterations, got, test.wantNative)
			}
		})
	}
}

func TestNativeTraceLongLoopActivationRequiresAmortisation(t *testing.T) {
	runtime, call := setupTypedTraceTest(t)
	callTypedTraceTest(t, call, valueInt(128), valueInt(0))
	state := typedTraceState(runtime)
	for i := 0; i < 5; i++ {
		callTypedTraceTest(t, call, valueInt(nativeTraceMinRemainingIterations-1), valueInt(0))
	}
	if state.typed.nativeCode() != nil || state.typed.nativeAttempted() {
		t.Fatal("native code activated without enough remaining work to amortise compilation")
	}
	callTypedTraceTest(t, call, valueInt(nativeTraceMinRemainingIterations), valueInt(0))
	if state.typed.nativeCode() == nil || !state.typed.nativeAttempted() {
		t.Fatal("amortised long loop did not install native code")
	}
}

func assertNativeTraceBound(t *testing.T, runtime *Runtime) {
	t.Helper()
	count := 0
	for _, state := range runtime.vm.tiering.programs {
		if state.typed != nil && state.typed.nativeCode() != nil {
			count++
		}
	}
	if count != maxTieringPrograms {
		t.Fatalf("retained native trace count: got %d, want %d", count, maxTieringPrograms)
	}
}

func TestNativeTracePrivateABIAndWX(t *testing.T) {
	if size := unsafe.Sizeof(typedTraceTierState{}); size != 24 {
		t.Fatalf("lazy typed tier state size: got %d, want 24", size)
	}
	if size := unsafe.Sizeof(typedTraceEntry{}); size != 16 {
		t.Fatalf("typed trace entry size: got %d, want 16", size)
	}
	if err := checkNativeTraceFrameLayout(); err != nil {
		t.Fatal(err)
	}
	var frame nativeTraceFrame
	offsets := []struct {
		name string
		got  uintptr
		want uintptr
	}{
		{"counter", unsafe.Offsetof(frame.counter), nativeTraceCounterOffset},
		{"limit", unsafe.Offsetof(frame.limit), nativeTraceLimitOffset},
		{"accumulator", unsafe.Offsetof(frame.accumulator), nativeTraceAccumulatorOffset},
		{"budget", unsafe.Offsetof(frame.budget), nativeTraceBudgetOffset},
		{"interrupt", unsafe.Offsetof(frame.interrupt), nativeTraceInterruptOffset},
		{"profiler", unsafe.Offsetof(frame.profiler), nativeTraceProfilerOffset},
	}
	for _, offset := range offsets {
		if offset.got != offset.want {
			t.Errorf("%s offset: got %d, want %d", offset.name, offset.got, offset.want)
		}
	}

	_, native := compileTestNativeTrace(t)
	protection, err := native.memory.protection()
	if err != nil {
		t.Fatal(err)
	}
	if protection != pageExecuteRead || protection == pageReadWrite || protection == pageExecuteReadWrite {
		t.Fatalf("native memory protection: got %#x, want PAGE_EXECUTE_READ (%#x)", protection, pageExecuteRead)
	}
	want, err := emitNativeTraceAMD64(typedTraceLoopShape{direction: typedTraceLoopAscending, comparison: typedTraceLoopExclusive})
	if err != nil {
		t.Fatal(err)
	}
	if got := native.memory.bytes(); !bytes.Equal(got, want) {
		t.Fatalf("RX memory differs from emitted machine code: got %x, want %x", got, want)
	}
	t.Logf("native trace machine code (%d bytes): %x", len(want), want)
}

func TestNativeTraceRejectsUnsupportedIR(t *testing.T) {
	for _, test := range []struct {
		name   string
		trace  *typedIntLoopTrace
		mutate func([]typedTraceIR)
	}{
		{
			name:   "UnsupportedBody",
			trace:  lowerTypedIntLoopTrace(typedTraceInstructionProgram()),
			mutate: func(code []typedTraceIR) { code[3].opcode = typedTracePollBackedge },
		},
		{
			name:   "MismatchedDescendingUpdate",
			trace:  lowerTypedIntLoopTrace(typedDescendingTraceInstructionProgram()),
			mutate: func(code []typedTraceIR) { code[4].opcode = typedTraceIncrement },
		},
		{
			name:   "MismatchedInclusiveUpdate",
			trace:  lowerTypedIntLoopTrace(typedInclusiveTraceInstructionProgram()),
			mutate: func(code []typedTraceIR) { code[4].opcode = typedTraceDecrement },
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			test.trace.code = append([]typedTraceIR(nil), test.trace.code...)
			test.mutate(test.trace.code)
			native, err := compileNativeTrace(test.trace)
			if err != nil {
				t.Fatal(err)
			}
			if native != nil {
				t.Fatal("unsupported IR unexpectedly compiled to native code")
			}
		})
	}
}

func TestNativeTraceEmitterRejectsUnsupportedLoopShape(t *testing.T) {
	_, err := emitNativeTraceAMD64(typedTraceLoopShape{direction: typedTraceLoopDescending, comparison: typedTraceLoopInclusive})
	if err == nil {
		t.Fatal("emitter accepted unsupported descending-inclusive loop shape")
	}
}

func TestNativeInclusiveTraceExitReasons(t *testing.T) {
	trace := lowerTypedIntLoopTrace(typedInclusiveTraceInstructionProgram())
	if trace == nil {
		t.Fatal("inclusive loop did not lower to typed IR")
	}
	native := compileNativeTraceForTest(t, trace)
	shape := typedTraceLoopShape{direction: typedTraceLoopAscending, comparison: typedTraceLoopInclusive}
	wantCode, err := emitNativeTraceAMD64(shape)
	if err != nil {
		t.Fatal(err)
	}
	if got := native.memory.bytes(); !bytes.Equal(got, wantCode) {
		t.Fatalf("inclusive RX memory differs from emitted code: got %x, want %x", got, wantCode)
	}
	t.Logf("inclusive native trace machine code (%d bytes): %x", len(wantCode), wantCode)
	exclusiveCode, err := emitNativeTraceAMD64(typedTraceLoopShape{direction: typedTraceLoopAscending, comparison: typedTraceLoopExclusive})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(wantCode, exclusiveCode) {
		t.Fatal("inclusive emitter produced exclusive machine code")
	}

	var interrupt uint32
	var profiler int32
	for _, test := range []struct {
		name            string
		frame           nativeTraceFrame
		interrupt       uint32
		profiler        int32
		wantExit        nativeTraceExit
		wantCounter     int64
		wantAccumulator int64
		wantBudget      uint64
	}{
		{
			name: "Normal", frame: nativeTraceFrame{counter: 2, limit: 4, accumulator: 0, budget: 8},
			wantExit: nativeTraceExitNormal, wantCounter: 5, wantAccumulator: 9, wantBudget: 5,
		},
		{
			name: "ZeroTrip", frame: nativeTraceFrame{counter: 5, limit: 4, accumulator: 7, budget: 8},
			wantExit: nativeTraceExitNormal, wantCounter: 5, wantAccumulator: 7, wantBudget: 8,
		},
		{
			name: "IncrementGuard", frame: nativeTraceFrame{counter: maxInt, limit: maxInt, accumulator: 0, budget: 8},
			wantExit: nativeTraceExitGuard, wantCounter: maxInt, wantAccumulator: 0, wantBudget: 8,
		},
		{
			name: "Interrupt", frame: nativeTraceFrame{counter: 1, limit: 3, accumulator: 0, budget: 8}, interrupt: 1,
			wantExit: nativeTraceExitInterrupt, wantCounter: 2, wantAccumulator: 1, wantBudget: 8,
		},
		{
			name: "Profiler", frame: nativeTraceFrame{counter: 1, limit: 3, accumulator: 0, budget: 8}, profiler: 1,
			wantExit: nativeTraceExitProfiler, wantCounter: 2, wantAccumulator: 1, wantBudget: 8,
		},
		{
			name: "BoundedYield", frame: nativeTraceFrame{counter: 0, limit: 10, accumulator: 0, budget: 2},
			wantExit: nativeTraceExitYield, wantCounter: 2, wantAccumulator: 1, wantBudget: 0,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			atomic.StoreUint32(&interrupt, test.interrupt)
			atomic.StoreInt32(&profiler, test.profiler)
			frame := test.frame
			frame.interrupt = &interrupt
			frame.profiler = &profiler
			if got := native.run(&frame); got != test.wantExit {
				t.Fatalf("exit: got %d, want %d", got, test.wantExit)
			}
			if frame.counter != test.wantCounter || frame.accumulator != test.wantAccumulator || frame.budget != test.wantBudget {
				t.Fatalf("frame: counter/accumulator/budget=%d/%d/%d, want %d/%d/%d", frame.counter, frame.accumulator, frame.budget, test.wantCounter, test.wantAccumulator, test.wantBudget)
			}
		})
	}
}

func TestNativeDescendingTraceExitReasons(t *testing.T) {
	trace := lowerTypedIntLoopTrace(typedDescendingTraceInstructionProgram())
	if trace == nil {
		t.Fatal("descending loop did not lower to typed IR")
	}
	native := compileNativeTraceForTest(t, trace)
	wantCode, err := emitNativeTraceAMD64(typedTraceLoopShape{direction: typedTraceLoopDescending, comparison: typedTraceLoopExclusive})
	if err != nil {
		t.Fatal(err)
	}
	if got := native.memory.bytes(); !bytes.Equal(got, wantCode) {
		t.Fatalf("descending RX memory differs from emitted code: got %x, want %x", got, wantCode)
	}
	t.Logf("descending native trace machine code (%d bytes): %x", len(wantCode), wantCode)
	ascendingCode, err := emitNativeTraceAMD64(typedTraceLoopShape{direction: typedTraceLoopAscending, comparison: typedTraceLoopExclusive})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(wantCode, ascendingCode) {
		t.Fatal("descending emitter produced ascending machine code")
	}

	var interrupt uint32
	var profiler int32
	for _, test := range []struct {
		name            string
		frame           nativeTraceFrame
		interrupt       uint32
		profiler        int32
		wantExit        nativeTraceExit
		wantCounter     int64
		wantAccumulator int64
		wantBudget      uint64
	}{
		{
			name: "Normal", frame: nativeTraceFrame{counter: 3, limit: 0, accumulator: 0, budget: 8},
			wantExit: nativeTraceExitNormal, wantCounter: 0, wantAccumulator: 6, wantBudget: 5,
		},
		{
			name: "AddUpperGuard", frame: nativeTraceFrame{counter: 1, limit: 0, accumulator: maxInt, budget: 8},
			wantExit: nativeTraceExitGuard, wantCounter: 1, wantAccumulator: maxInt, wantBudget: 8,
		},
		{
			name: "DecrementGuard", frame: nativeTraceFrame{counter: -maxInt, limit: -maxInt - 1, accumulator: 0, budget: 8},
			wantExit: nativeTraceExitGuard, wantCounter: -maxInt, wantAccumulator: 0, wantBudget: 8,
		},
		{
			name: "Interrupt", frame: nativeTraceFrame{counter: 3, limit: 0, accumulator: 0, budget: 8}, interrupt: 1,
			wantExit: nativeTraceExitInterrupt, wantCounter: 2, wantAccumulator: 3, wantBudget: 8,
		},
		{
			name: "Profiler", frame: nativeTraceFrame{counter: 3, limit: 0, accumulator: 0, budget: 8}, profiler: 1,
			wantExit: nativeTraceExitProfiler, wantCounter: 2, wantAccumulator: 3, wantBudget: 8,
		},
		{
			name: "BoundedYield", frame: nativeTraceFrame{counter: 3, limit: -10, accumulator: 0, budget: 2},
			wantExit: nativeTraceExitYield, wantCounter: 1, wantAccumulator: 5, wantBudget: 0,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			atomic.StoreUint32(&interrupt, test.interrupt)
			atomic.StoreInt32(&profiler, test.profiler)
			frame := test.frame
			frame.interrupt = &interrupt
			frame.profiler = &profiler
			if got := native.run(&frame); got != test.wantExit {
				t.Fatalf("exit: got %d, want %d", got, test.wantExit)
			}
			if frame.counter != test.wantCounter || frame.accumulator != test.wantAccumulator || frame.budget != test.wantBudget {
				t.Fatalf("frame: counter/accumulator/budget=%d/%d/%d, want %d/%d/%d", frame.counter, frame.accumulator, frame.budget, test.wantCounter, test.wantAccumulator, test.wantBudget)
			}
		})
	}
}

func TestNativeTraceExitReasons(t *testing.T) {
	_, native := compileTestNativeTrace(t)
	var interrupt uint32
	var profiler int32
	tests := []struct {
		name            string
		frame           nativeTraceFrame
		interrupt       uint32
		profiler        int32
		wantExit        nativeTraceExit
		wantCounter     int64
		wantAccumulator int64
		wantBudget      uint64
	}{
		{
			name: "Normal", frame: nativeTraceFrame{counter: 5, limit: 5, accumulator: 10, budget: 8},
			wantExit: nativeTraceExitNormal, wantCounter: 5, wantAccumulator: 10, wantBudget: 8,
		},
		{
			name: "AddUpperGuard", frame: nativeTraceFrame{counter: 1, limit: 2, accumulator: maxInt, budget: 8},
			wantExit: nativeTraceExitGuard, wantCounter: 1, wantAccumulator: maxInt, wantBudget: 8,
		},
		{
			name: "AddLowerGuard", frame: nativeTraceFrame{counter: -1, limit: 0, accumulator: -maxInt, budget: 8},
			wantExit: nativeTraceExitGuard, wantCounter: -1, wantAccumulator: -maxInt, wantBudget: 8,
		},
		{
			name: "IncrementGuard", frame: nativeTraceFrame{counter: maxInt, limit: maxInt + 1, accumulator: 0, budget: 8},
			wantExit: nativeTraceExitGuard, wantCounter: maxInt, wantAccumulator: 0, wantBudget: 8,
		},
		{
			name: "Interrupt", frame: nativeTraceFrame{counter: 0, limit: 10, accumulator: 0, budget: 8}, interrupt: 1,
			wantExit: nativeTraceExitInterrupt, wantCounter: 1, wantAccumulator: 0, wantBudget: 8,
		},
		{
			name: "Profiler", frame: nativeTraceFrame{counter: 0, limit: 10, accumulator: 0, budget: 8}, profiler: 1,
			wantExit: nativeTraceExitProfiler, wantCounter: 1, wantAccumulator: 0, wantBudget: 8,
		},
		{
			name: "InactiveProfilerValue", frame: nativeTraceFrame{counter: 0, limit: 1, accumulator: 0, budget: 8}, profiler: 2,
			wantExit: nativeTraceExitNormal, wantCounter: 1, wantAccumulator: 0, wantBudget: 7,
		},
		{
			name: "BoundedYield", frame: nativeTraceFrame{counter: 0, limit: 10, accumulator: 0, budget: 2},
			wantExit: nativeTraceExitYield, wantCounter: 2, wantAccumulator: 1, wantBudget: 0,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			atomic.StoreUint32(&interrupt, test.interrupt)
			atomic.StoreInt32(&profiler, test.profiler)
			frame := test.frame
			frame.interrupt = &interrupt
			frame.profiler = &profiler
			if got := native.run(&frame); got != test.wantExit {
				t.Fatalf("exit: got %d, want %d", got, test.wantExit)
			}
			if frame.counter != test.wantCounter || frame.accumulator != test.wantAccumulator || frame.budget != test.wantBudget {
				t.Fatalf("frame: counter/accumulator/budget=%d/%d/%d, want %d/%d/%d", frame.counter, frame.accumulator, frame.budget, test.wantCounter, test.wantAccumulator, test.wantBudget)
			}
		})
	}
}

func TestNativeTraceRuntimeExecutionAndYield(t *testing.T) {
	_, call, state := setupNativeTraceTest(t)
	native := state.typed.nativeCode()
	native.yields.Store(0)
	n := int64(nativeTraceIterationBudget*2 + 7)
	result := callTypedTraceTest(t, call, valueInt(n), valueInt(3))
	want := int64(3) + n*(n-1)/2
	if got := result.ToInteger(); got != want {
		t.Fatalf("yielded native result: got %d, want %d", got, want)
	}
	if got := native.yields.Load(); got != 2 {
		t.Fatalf("bounded yields: got %d, want 2", got)
	}
}

func TestNativeDescendingTraceRuntimeExecutionAndYield(t *testing.T) {
	runtime, call := setupTypedDescendingTraceTest(t)
	for i := 0; i < 6; i++ {
		callTypedDescendingTraceTest(t, call, valueInt(1000), valueInt(0), valueInt(0))
	}
	state := typedTraceState(runtime)
	if state == nil || state.typed.nativeCode() == nil {
		t.Fatal("descending loop did not activate native code")
	}
	native := state.typed.nativeCode()
	native.yields.Store(0)
	n := int64(nativeTraceIterationBudget*2 + 7)
	result := callTypedDescendingTraceTest(t, call, valueInt(n), valueInt(0), valueInt(0))
	want := n * (n + 1) / 2
	if got := result.ToInteger(); got != want {
		t.Fatalf("yielded descending result: got %d, want %d", got, want)
	}
	if got := native.yields.Load(); got != 2 {
		t.Fatalf("bounded descending yields: got %d, want 2", got)
	}
}

func TestNativeInclusiveTraceRuntimeExecutionAndYield(t *testing.T) {
	runtime, call := setupTypedInclusiveTraceTest(t)
	for i := 0; i < 6; i++ {
		callTypedInclusiveTraceTest(t, runtime, call, valueInt(0), valueInt(999), valueInt(0))
	}
	state := typedTraceState(runtime)
	if state == nil || state.typed.nativeCode() == nil {
		t.Fatal("inclusive loop did not activate native code")
	}
	native := state.typed.nativeCode()
	native.yields.Store(0)
	limit := int64(nativeTraceIterationBudget*2 + 6)
	sum, counter := callTypedInclusiveTraceTest(t, runtime, call, valueInt(0), valueInt(limit), valueInt(0))
	want := limit * (limit + 1) / 2
	if sum.ToInteger() != want || counter.ToInteger() != limit+1 {
		t.Fatalf("yielded inclusive result: sum/counter=%v/%v, want %d/%d", sum, counter, want, limit+1)
	}
	if got := native.yields.Load(); got != 2 {
		t.Fatalf("bounded inclusive yields: got %d, want 2", got)
	}
}

func TestNativeTraceInterrupt(t *testing.T) {
	runtime, call, _ := setupNativeTraceTest(t)
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		close(started)
		_, err := call(_undefined, valueInt(maxInt), valueInt(0))
		done <- err
	}()
	<-started
	runtime.Interrupt("native interrupt")
	select {
	case err := <-done:
		var interrupted *InterruptedError
		if !errors.As(err, &interrupted) {
			t.Fatalf("call returned %T (%v), want InterruptedError", err, err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("native execution did not observe interrupt")
	}
	runtime.ClearInterrupt()
}

func TestNativeTraceBoundedYieldAllowsGC(t *testing.T) {
	runtime, call, state := setupNativeTraceTest(t)
	native := state.typed.nativeCode()
	native.yields.Store(0)
	done := make(chan error, 1)
	go func() {
		_, err := call(_undefined, valueInt(maxInt), valueInt(0))
		done <- err
	}()
	deadline := time.After(2 * time.Second)
	for native.yields.Load() == 0 {
		select {
		case <-deadline:
			runtime.Interrupt("yield timeout")
			t.Fatal("native execution did not reach its bounded yield")
		default:
			goruntime.Gosched()
		}
	}
	gcDone := make(chan struct{})
	go func() {
		goruntime.GC()
		close(gcDone)
	}()
	select {
	case <-gcDone:
	case <-time.After(2 * time.Second):
		runtime.Interrupt("GC timeout")
		t.Fatal("stop-the-world GC did not progress during native execution")
	}
	runtime.Interrupt("native GC progress complete")
	select {
	case err := <-done:
		var interrupted *InterruptedError
		if !errors.As(err, &interrupted) {
			t.Fatalf("call returned %T (%v), want InterruptedError", err, err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("native execution did not stop after GC progress check")
	}
	runtime.ClearInterrupt()
}

func TestNativeTraceMemoryRelease(t *testing.T) {
	trace := lowerTypedIntLoopTrace(typedTraceInstructionProgram())
	native, err := compileNativeTrace(trace)
	if err != nil {
		t.Fatal(err)
	}
	if native == nil || native.memory.address.Load() == 0 {
		t.Fatal("native allocation was not created")
	}
	if err := native.memory.release(); err != nil {
		t.Fatal(err)
	}
	if err := native.memory.release(); err != nil {
		t.Fatalf("second release was not idempotent: %v", err)
	}
	if address := native.memory.address.Load(); address != 0 {
		t.Fatalf("released address: got %#x, want 0", address)
	}
	if bytes := native.memory.bytes(); bytes != nil {
		t.Fatalf("released code bytes: got %x, want nil", bytes)
	}
	if _, err := native.memory.protection(); err == nil {
		t.Fatal("released mapping still reported memory protection")
	}
	var interrupt uint32
	var profiler int32
	frame := nativeTraceFrame{budget: 1, interrupt: &interrupt, profiler: &profiler}
	if exit := native.run(&frame); exit != nativeTraceExitError {
		t.Fatalf("released code exit: got %d, want error", exit)
	}
}
