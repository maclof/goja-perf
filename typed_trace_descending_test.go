package goja

import (
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func setupTypedDescendingTraceTest(t *testing.T) (*Runtime, Callable) {
	t.Helper()
	runtime := New()
	program := MustCompile("typed_trace_descending_test.js", typedTraceShapeBenchmarkSource+`
var typedTraceDescendingThrowingSeed = {
	valueOf: function() { throw new Error("descending trace deopt"); }
};
var typedTraceDescendingThrowingLimit = {
	valueOf: function() { throw new Error("descending limit deopt"); }
};
`, false)
	if _, err := runtime.RunProgram(program); err != nil {
		t.Fatal(err)
	}
	call, ok := AssertFunction(runtime.Get("typedTraceDescending"))
	if !ok {
		t.Fatal("typedTraceDescending is not callable")
	}
	return runtime, call
}

func TestTypedDescendingTraceRejectsMismatchedInduction(t *testing.T) {
	for _, test := range []struct {
		name      string
		condition instruction
		update    instruction
	}{
		{name: "GreaterIncrement", condition: op_gt, update: inc},
		{name: "LessDecrement", condition: op_lt, update: dec},
	} {
		t.Run(test.name, func(t *testing.T) {
			program := typedDescendingTraceInstructionProgram()
			program.code[2] = test.condition
			program.code[9] = test.update
			if trace := lowerTypedIntLoopTrace(program); trace != nil {
				t.Fatalf("mismatched loop lowered: %+v", trace.code)
			}
		})
	}
}

func TestTypedDescendingTraceRemainingIterations(t *testing.T) {
	trace := lowerTypedIntLoopTrace(typedDescendingTraceInstructionProgram())
	registers := [typedTraceRegisterCount]int64{
		typedTraceCounter: 5,
		typedTraceLimit:   -2,
	}
	if got := trace.remainingIterations(registers); got != 7 {
		t.Fatalf("remaining iterations: got %d, want 7", got)
	}
	registers[typedTraceCounter] = -2
	if got := trace.remainingIterations(registers); got != 0 {
		t.Fatalf("zero-trip remaining iterations: got %d, want 0", got)
	}
}

func callTypedDescendingTraceTest(t *testing.T, call Callable, start, limit, seed Value) Value {
	t.Helper()
	result, err := call(_undefined, start, limit, seed)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func typedDescendingTraceInstructionProgram() *Program {
	return &Program{code: []instruction{
		loadStack(1), loadStackLex(-1), op_gt, jneP(9),
		loadStack(2), loadStack(1), add, storeStackP(2),
		loadStack(1), dec, storeStackP(1), jump(-11),
	}}
}

func TestTypedDescendingTraceLowersAndExecutes(t *testing.T) {
	runtime, call := setupTypedDescendingTraceTest(t)
	if result := callTypedDescendingTraceTest(t, call, valueInt(128), valueInt(0), valueInt(0)); result.ToInteger() != 8256 {
		t.Fatalf("unexpected tier-up result: %v", result)
	}
	state := typedTraceState(runtime)
	if state == nil {
		t.Fatal("hot descending loop did not lower to typed IR")
	}
	trace := state.typed.trace
	if len(trace.code) != 7 || trace.code[0].opcode != typedTraceExitUnlessGreater ||
		trace.code[2].opcode != typedTraceGuardDecrementRange || trace.code[4].opcode != typedTraceDecrement {
		t.Fatalf("unexpected descending IR: %+v", trace.code)
	}

	for _, test := range []struct {
		start, limit, seed int64
		want               int64
	}{
		{start: 0, limit: 0, seed: 7, want: 7},
		{start: 5, limit: 0, seed: 3, want: 18},
		{start: 3, limit: -2, want: 5},
		{start: -2, limit: -5, seed: 1, want: -8},
		{start: 1000, limit: 0, want: 500500},
	} {
		result := callTypedDescendingTraceTest(t, call, valueInt(test.start), valueInt(test.limit), valueInt(test.seed))
		if got := result.ToInteger(); got != test.want {
			t.Fatalf("start=%d limit=%d seed=%d: got %d, want %d", test.start, test.limit, test.seed, got, test.want)
		}
	}
	if state.typed.disabled() || state.typed.guardFailures != 0 {
		t.Fatalf("descending trace unexpectedly deoptimised: disabled=%t failures=%d", state.typed.disabled(), state.typed.guardFailures)
	}
}

func TestTypedDescendingTraceGuardFailureDoesNotThrash(t *testing.T) {
	runtime, call := setupTypedDescendingTraceTest(t)
	callTypedDescendingTraceTest(t, call, valueInt(128), valueInt(0), valueInt(0))
	state := typedTraceState(runtime)
	result := callTypedDescendingTraceTest(t, call, valueInt(5), valueInt(0), valueFloat(0.5))
	if !result.StrictEquals(valueFloat(15.5)) {
		t.Fatalf("guard fallback result: got %v, want 15.5", result)
	}
	if state == nil || !state.typed.disabled() || state.typed.guardFailures != 1 {
		t.Fatalf("guard failure state: %p", state)
	}
	for i := 0; i < 10; i++ {
		if result := callTypedDescendingTraceTest(t, call, valueInt(5), valueInt(0), valueInt(0)); result.ToInteger() != 15 {
			t.Fatalf("fallback result %d: %v", i, result)
		}
	}
	if state.typed.guardFailures != 1 {
		t.Fatalf("guard failure thrashed: got %d, want 1", state.typed.guardFailures)
	}
}

func TestTypedDescendingTraceGuardFailurePreservesThrowPosition(t *testing.T) {
	for _, test := range []struct {
		name        string
		limit, seed func(*Runtime) Value
		wantMessage string
	}{
		{
			name:        "Accumulator",
			limit:       func(*Runtime) Value { return valueInt(0) },
			seed:        func(runtime *Runtime) Value { return runtime.Get("typedTraceDescendingThrowingSeed") },
			wantMessage: "descending trace deopt",
		},
		{
			name:        "Limit",
			limit:       func(runtime *Runtime) Value { return runtime.Get("typedTraceDescendingThrowingLimit") },
			seed:        func(*Runtime) Value { return valueInt(0) },
			wantMessage: "descending limit deopt",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			runtime, call := setupTypedDescendingTraceTest(t)
			callTypedDescendingTraceTest(t, call, valueInt(128), valueInt(0), valueInt(0))
			_, err := call(_undefined, valueInt(5), test.limit(runtime), test.seed(runtime))
			if err == nil {
				t.Fatal("throwing value did not throw after guard deopt")
			}
			message := err.Error()
			if !strings.Contains(message, test.wantMessage) || !strings.Contains(message, "typed_trace_descending_test.js:") {
				t.Fatalf("unexpected throw/source position: %s", message)
			}
			state := typedTraceState(runtime)
			if state == nil || !state.typed.disabled() || state.typed.guardFailures != 1 {
				t.Fatalf("throwing guard failure state: %p", state)
			}
		})
	}
}

func TestTypedDescendingTraceSharedProgramRuntimeOwnership(t *testing.T) {
	program := MustCompile("typed_trace_descending_shared.js", typedTraceShapeBenchmarkSource, false)
	runtimes := []*Runtime{New(), New()}
	states := make([]*programTierState, len(runtimes))
	for i, runtime := range runtimes {
		if _, err := runtime.RunProgram(program); err != nil {
			t.Fatal(err)
		}
		call, ok := AssertFunction(runtime.Get("typedTraceDescending"))
		if !ok {
			t.Fatal("typedTraceDescending is not callable")
		}
		for j := 0; j < 6; j++ {
			callTypedDescendingTraceTest(t, call, valueInt(1000), valueInt(0), valueInt(0))
		}
		states[i] = typedTraceState(runtime)
		if states[i] == nil {
			t.Fatal("shared Program did not lower independently")
		}
	}
	if states[0] == states[1] || states[0].program != states[1].program ||
		states[0].quickProgram == states[1].quickProgram || states[0].typed.program == states[1].typed.program {
		t.Fatalf("Runtime tier ownership was shared: state=%p/%p base=%p/%p quick=%p/%p typed=%p/%p",
			states[0], states[1], states[0].program, states[1].program,
			states[0].quickProgram, states[1].quickProgram, states[0].typed.program, states[1].typed.program)
	}
}

func TestTypedDescendingTracePollMaterializesAtBackedge(t *testing.T) {
	for _, test := range []struct {
		name       string
		activate   func(*vm)
		deactivate func(*vm)
	}{
		{
			name: "Interrupt",
			activate: func(vm *vm) {
				atomic.StoreUint32(&vm.interrupted, 1)
			},
			deactivate: func(vm *vm) {
				atomic.StoreUint32(&vm.interrupted, 0)
			},
		},
		{
			name: "Profiler",
			activate: func(*vm) {
				atomic.StoreInt32(&globalProfiler.enabled, 1)
			},
			deactivate: func(*vm) {
				atomic.StoreInt32(&globalProfiler.enabled, 0)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			program := typedDescendingTraceInstructionProgram()
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
			vm.prg = state.typed.program
			vm.tier = state
			vm.pc = trace.entryPC
			vm.sb = 0
			vm.args = 1
			vm.stack = make(valueStack, 8)
			vm.stack[1] = valueInt(0)
			vm.stack[2] = valueInt(10)
			vm.stack[3] = valueInt(0)
			vm.sp = 4
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
			if vm.prg != state.quickProgram || vm.pc != trace.entryPC {
				t.Fatalf("poll did not deopt to loop header: program=%p pc=%d", vm.prg, vm.pc)
			}
			if vm.stack[2] != valueInt(9) || vm.stack[3] != valueInt(10) {
				t.Fatalf("registers were not materialised at backedge: counter=%v accumulator=%v", vm.stack[2], vm.stack[3])
			}
			if state.typed.disabled() || state.typed.guardFailures != 0 {
				t.Fatal("temporary poll permanently disabled descending trace")
			}
		})
	}
}

func TestTypedDescendingTraceProfilerUsesOriginalProgram(t *testing.T) {
	runtime, call := setupTypedDescendingTraceTest(t)
	callTypedDescendingTraceTest(t, call, valueInt(128), valueInt(0), valueInt(0))
	state := typedTraceState(runtime)
	if err := StartProfile(io.Discard); err != nil {
		t.Fatal(err)
	}
	result, err := call(_undefined, valueInt(128), valueInt(0), valueInt(0))
	StopProfile()
	if err != nil {
		t.Fatal(err)
	}
	if result.ToInteger() != 8256 || state.typed.disabled() || state.typed.guardFailures != 0 {
		t.Fatalf("profiled descending result/state: result=%v disabled=%t failures=%d", result, state.typed.disabled(), state.typed.guardFailures)
	}
}

func TestTypedDescendingTraceInterrupt(t *testing.T) {
	runtime, call := setupTypedDescendingTraceTest(t)
	for i := 0; i < 6; i++ {
		callTypedDescendingTraceTest(t, call, valueInt(1000), valueInt(0), valueInt(0))
	}
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		close(started)
		_, err := call(_undefined, valueInt(50_000_000), valueInt(-50_000_000), valueInt(0))
		done <- err
	}()
	<-started
	runtime.Interrupt("descending trace stop")
	select {
	case err := <-done:
		var interrupted *InterruptedError
		if !errors.As(err, &interrupted) {
			t.Fatalf("call returned %T (%v), want InterruptedError", err, err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("descending trace did not observe interrupt")
	}
	runtime.ClearInterrupt()
}
