package goja

import (
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const typedInclusiveTraceTestSource = `
function typedTraceInclusiveFrom(start, limit, seed) {
	var sum = seed;
	for (var i = start; i <= limit; i++) {
		sum += i;
	}
	return [sum, i];
}
var typedTraceInclusiveThrowingSeed = {
	valueOf: function() { throw new Error("inclusive trace deopt"); }
};
var typedTraceInclusiveThrowingLimit = {
	valueOf: function() { throw new Error("inclusive limit deopt"); }
};
`

func setupTypedInclusiveTraceTest(t *testing.T) (*Runtime, Callable) {
	t.Helper()
	runtime := New()
	program := MustCompile("typed_trace_inclusive_test.js", typedInclusiveTraceTestSource, false)
	if _, err := runtime.RunProgram(program); err != nil {
		t.Fatal(err)
	}
	call, ok := AssertFunction(runtime.Get("typedTraceInclusiveFrom"))
	if !ok {
		t.Fatal("typedTraceInclusiveFrom is not callable")
	}
	return runtime, call
}

func typedInclusiveTraceInstructionProgram() *Program {
	return &Program{code: []instruction{
		loadStack(1), loadStackLex(-1), op_lte, jneP(9),
		loadStack(2), loadStack(1), add, storeStackP(2),
		loadStack(1), inc, storeStackP(1), jump(-11),
	}}
}

func callTypedInclusiveTraceTest(t *testing.T, runtime *Runtime, call Callable, start, limit, seed Value) (Value, Value) {
	t.Helper()
	result, err := call(_undefined, start, limit, seed)
	if err != nil {
		t.Fatal(err)
	}
	object := result.ToObject(runtime)
	return object.Get("0"), object.Get("1")
}

func TestTypedInclusiveTraceRejectsMismatchedInduction(t *testing.T) {
	program := typedInclusiveTraceInstructionProgram()
	program.code[9] = dec
	if trace := lowerTypedIntLoopTrace(program); trace != nil {
		t.Fatalf("inclusive decrement loop lowered: %+v", trace.code)
	}
}

func TestTypedInclusiveTraceLowersAndExecutes(t *testing.T) {
	runtime, call := setupTypedInclusiveTraceTest(t)
	sum, counter := callTypedInclusiveTraceTest(t, runtime, call, valueInt(0), valueInt(127), valueInt(0))
	if sum.ToInteger() != 8128 || counter.ToInteger() != 128 {
		t.Fatalf("tier-up result: sum/counter=%v/%v, want 8128/128", sum, counter)
	}
	state := typedTraceState(runtime)
	if state == nil {
		t.Fatal("hot inclusive loop did not lower to typed IR")
	}
	trace := state.typed.trace
	if len(trace.code) != 7 || trace.code[0].opcode != typedTraceExitUnlessLessOrEqual ||
		trace.code[2].opcode != typedTraceGuardIncrementRange ||
		trace.code[4].opcode != typedTraceIncrement {
		t.Fatalf("unexpected inclusive IR: %+v", trace.code)
	}

	for _, test := range []struct {
		start, limit, seed int64
		wantSum, wantNext  int64
	}{
		{start: 1, limit: 0, seed: 7, wantSum: 7, wantNext: 1},
		{start: 0, limit: 0, seed: 3, wantSum: 3, wantNext: 1},
		{start: 1, limit: 5, seed: 2, wantSum: 17, wantNext: 6},
		{start: -2, limit: 2, seed: 1, wantSum: 1, wantNext: 3},
		{start: 0, limit: 999, wantSum: 499500, wantNext: 1000},
	} {
		sum, counter := callTypedInclusiveTraceTest(t, runtime, call, valueInt(test.start), valueInt(test.limit), valueInt(test.seed))
		if sum.ToInteger() != test.wantSum || counter.ToInteger() != test.wantNext {
			t.Fatalf("start=%d limit=%d seed=%d: sum/counter=%v/%v, want %d/%d", test.start, test.limit, test.seed, sum, counter, test.wantSum, test.wantNext)
		}
	}
	if state.typed.disabled() || state.typed.guardFailures != 0 {
		t.Fatalf("inclusive trace unexpectedly deoptimised: disabled=%t failures=%d", state.typed.disabled(), state.typed.guardFailures)
	}
}

func TestTypedInclusiveTraceRemainingIterations(t *testing.T) {
	trace := lowerTypedIntLoopTrace(typedInclusiveTraceInstructionProgram())
	if trace == nil {
		t.Fatal("inclusive loop did not lower")
	}
	for _, test := range []struct {
		counter, limit int64
		want           int64
	}{
		{counter: 1, limit: 0, want: 0},
		{counter: 0, limit: 0, want: 1},
		{counter: -2, limit: 2, want: 5},
		{counter: 0, limit: maxInt, want: maxInt + 1},
		{counter: -maxInt, limit: maxInt, want: 2*maxInt + 1},
	} {
		registers := [typedTraceRegisterCount]int64{typedTraceCounter: test.counter, typedTraceLimit: test.limit}
		if got := trace.remainingIterations(registers); got != test.want {
			t.Fatalf("counter=%d limit=%d: got %d, want %d", test.counter, test.limit, got, test.want)
		}
	}
}

func TestTypedInclusiveTraceFinalInductionState(t *testing.T) {
	runtime, call := setupTypedInclusiveTraceTest(t)
	callTypedInclusiveTraceTest(t, runtime, call, valueInt(0), valueInt(127), valueInt(0))
	sum, counter := callTypedInclusiveTraceTest(t, runtime, call, valueInt(maxInt-1), valueInt(maxInt-1), valueInt(0))
	if !sum.StrictEquals(valueInt(maxInt-1)) || !counter.StrictEquals(valueInt(maxInt)) {
		t.Fatalf("final induction: sum/counter=%v/%v, want %d/%d", sum, counter, int64(maxInt-1), int64(maxInt))
	}
	state := typedTraceState(runtime)
	if state == nil || state.typed.disabled() || state.typed.guardFailures != 0 {
		t.Fatalf("final induction state: %p", state)
	}
}

func TestTypedInclusiveTraceGuardFailureDoesNotThrash(t *testing.T) {
	runtime, call := setupTypedInclusiveTraceTest(t)
	callTypedInclusiveTraceTest(t, runtime, call, valueInt(0), valueInt(127), valueInt(0))
	state := typedTraceState(runtime)
	sum, counter := callTypedInclusiveTraceTest(t, runtime, call, valueInt(0), valueInt(2), valueFloat(0.5))
	if !sum.StrictEquals(valueFloat(3.5)) || counter.ToInteger() != 3 {
		t.Fatalf("guard fallback: sum/counter=%v/%v, want 3.5/3", sum, counter)
	}
	if state == nil || !state.typed.disabled() || state.typed.guardFailures != 1 {
		t.Fatalf("guard failure state: %p", state)
	}
	for i := 0; i < 10; i++ {
		callTypedInclusiveTraceTest(t, runtime, call, valueInt(0), valueInt(2), valueInt(0))
	}
	if state.typed.guardFailures != 1 {
		t.Fatalf("guard failure thrashed: got %d, want 1", state.typed.guardFailures)
	}
}

func TestTypedInclusiveTraceGuardFailurePreservesThrowPosition(t *testing.T) {
	for _, test := range []struct {
		name        string
		limit, seed func(*Runtime) Value
		wantMessage string
	}{
		{
			name:        "Accumulator",
			limit:       func(*Runtime) Value { return valueInt(5) },
			seed:        func(runtime *Runtime) Value { return runtime.Get("typedTraceInclusiveThrowingSeed") },
			wantMessage: "inclusive trace deopt",
		},
		{
			name:        "Limit",
			limit:       func(runtime *Runtime) Value { return runtime.Get("typedTraceInclusiveThrowingLimit") },
			seed:        func(*Runtime) Value { return valueInt(0) },
			wantMessage: "inclusive limit deopt",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			plainRuntime, plainCall := setupTypedInclusiveTraceTest(t)
			_, wantErr := plainCall(_undefined, valueInt(0), test.limit(plainRuntime), test.seed(plainRuntime))
			if wantErr == nil {
				t.Fatal("interpreter reference did not throw")
			}

			runtime, call := setupTypedInclusiveTraceTest(t)
			callTypedInclusiveTraceTest(t, runtime, call, valueInt(0), valueInt(127), valueInt(0))
			_, err := call(_undefined, valueInt(0), test.limit(runtime), test.seed(runtime))
			if err == nil {
				t.Fatal("throwing value did not throw after guard deopt")
			}
			message := err.Error()
			if !strings.Contains(message, test.wantMessage) || !strings.Contains(message, "typed_trace_inclusive_test.js:") {
				t.Fatalf("unexpected throw/source position: %s", message)
			}
			if message != wantErr.Error() {
				t.Fatalf("trace throw/source differs from interpreter:\ntrace: %s\ninterpreter: %s", message, wantErr)
			}
		})
	}
}

func TestTypedInclusiveTraceSharedProgramRuntimeOwnership(t *testing.T) {
	program := MustCompile("typed_trace_inclusive_shared.js", typedInclusiveTraceTestSource, false)
	runtimes := []*Runtime{New(), New()}
	states := make([]*programTierState, len(runtimes))
	for i, runtime := range runtimes {
		if _, err := runtime.RunProgram(program); err != nil {
			t.Fatal(err)
		}
		call, ok := AssertFunction(runtime.Get("typedTraceInclusiveFrom"))
		if !ok {
			t.Fatal("typedTraceInclusiveFrom is not callable")
		}
		for j := 0; j < 6; j++ {
			callTypedInclusiveTraceTest(t, runtime, call, valueInt(0), valueInt(999), valueInt(0))
		}
		states[i] = typedTraceState(runtime)
		if states[i] == nil {
			t.Fatal("shared Program did not lower independently")
		}
	}
	if states[0] == states[1] || states[0].program != states[1].program ||
		states[0].quickProgram == states[1].quickProgram || states[0].typed.program == states[1].typed.program {
		t.Fatal("Runtime tier ownership was shared")
	}
}

func TestTypedInclusiveTracePollMaterializesAtBackedge(t *testing.T) {
	for _, test := range []struct {
		name       string
		activate   func(*vm)
		deactivate func(*vm)
	}{
		{
			name:       "Interrupt",
			activate:   func(vm *vm) { atomic.StoreUint32(&vm.interrupted, 1) },
			deactivate: func(vm *vm) { atomic.StoreUint32(&vm.interrupted, 0) },
		},
		{
			name:       "Profiler",
			activate:   func(*vm) { atomic.StoreInt32(&globalProfiler.enabled, 1) },
			deactivate: func(*vm) { atomic.StoreInt32(&globalProfiler.enabled, 0) },
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			program := typedInclusiveTraceInstructionProgram()
			trace := lowerTypedIntLoopTrace(program)
			quickProgram := *program
			state := &programTierState{program: program, quickProgram: &quickProgram}
			traceProgram := quickProgram
			state.typed = &typedTraceTierState{trace: trace, program: &traceProgram}
			entry := &typedTraceEntry{state: state}
			traceProgram.code = append([]instruction(nil), traceProgram.code...)
			traceProgram.code[trace.entryPC] = entry
			runtime := New()
			vm := runtime.vm
			vm.prg, vm.tier, vm.pc, vm.sb, vm.args = state.typed.program, state, trace.entryPC, 0, 1
			vm.stack = make(valueStack, 8)
			vm.stack[1], vm.stack[2], vm.stack[3], vm.sp = valueInt(10), valueInt(1), valueInt(5), 4
			test.activate(vm)
			deactivated := false
			deactivate := func() {
				if !deactivated {
					test.deactivate(vm)
					deactivated = true
				}
			}
			defer deactivate()
			trace.execute(vm, state, entry)
			deactivate()
			if vm.prg != state.quickProgram || vm.pc != trace.entryPC || vm.stack[2] != valueInt(2) || vm.stack[3] != valueInt(6) {
				t.Fatalf("poll materialization: program=%p pc=%d counter=%v accumulator=%v", vm.prg, vm.pc, vm.stack[2], vm.stack[3])
			}
			if state.typed.disabled() || state.typed.guardFailures != 0 {
				t.Fatal("temporary poll permanently disabled inclusive trace")
			}
		})
	}
}

func TestTypedInclusiveTraceProfilerAndInterrupt(t *testing.T) {
	runtime, call := setupTypedInclusiveTraceTest(t)
	for i := 0; i < 6; i++ {
		callTypedInclusiveTraceTest(t, runtime, call, valueInt(0), valueInt(999), valueInt(0))
	}
	state := typedTraceState(runtime)
	if err := StartProfile(io.Discard); err != nil {
		t.Fatal(err)
	}
	sum, counter := callTypedInclusiveTraceTest(t, runtime, call, valueInt(0), valueInt(127), valueInt(0))
	StopProfile()
	if sum.ToInteger() != 8128 || counter.ToInteger() != 128 || state.typed.disabled() {
		t.Fatalf("profiled result/state: sum/counter=%v/%v disabled=%t", sum, counter, state.typed.disabled())
	}

	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		close(started)
		_, err := call(_undefined, valueInt(-50_000_000), valueInt(50_000_000), valueInt(0))
		done <- err
	}()
	<-started
	runtime.Interrupt("inclusive trace stop")
	select {
	case err := <-done:
		var interrupted *InterruptedError
		if !errors.As(err, &interrupted) {
			t.Fatalf("call returned %T (%v), want InterruptedError", err, err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("inclusive trace did not observe interrupt")
	}
	runtime.ClearInterrupt()
}
