package goja

import (
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const typedConstantStepTestSource = `
function typedTracePlusTwoFrom(start, limit, seed) {
	var sum = seed;
	for (var i = start; i < limit; i += 2) sum += i;
	return [sum, i];
}
function typedTracePlusThreeInclusiveFrom(start, limit, seed) {
	var sum = seed;
	for (var i = start; i <= limit; i += 3) sum += i;
	return [sum, i];
}
function typedTraceMinusTwoFrom(start, limit, seed) {
	var sum = seed;
	for (var i = start; i > limit; i -= 2) sum += i;
	return [sum, i];
}
function typedTraceMinusTwoInclusiveFrom(start, limit, seed) {
	var sum = seed;
	for (var i = start; i >= limit; i -= 2) sum += i;
	return [sum, i];
}
function typedTracePlusNegativeTwoFrom(start, limit, seed) {
	var sum = seed;
	for (var i = start; i > limit; i += -2) sum += i;
	return [sum, i];
}
function typedTraceMinusNegativeThreeFrom(start, limit, seed) {
	var sum = seed;
	for (var i = start; i < limit; i -= -3) sum += i;
	return [sum, i];
}
var typedConstantStepThrowingSeed = {
	valueOf: function() { throw new Error("constant-step seed deopt"); }
};
var typedConstantStepThrowingLimit = {
	valueOf: function() { throw new Error("constant-step limit deopt"); }
};
`

func typedConstantStepInstructionProgram(condition, operation instruction, literal valueInt) *Program {
	return &Program{code: []instruction{
		loadStack(1), loadStackLex(-1), condition, jneP(10),
		loadStack(2), loadStack(1), add, storeStackP(2),
		loadStack(1), loadVal{v: literal}, operation, storeStackP(1), jump(-12),
	}}
}

func setupTypedConstantStepTest(t *testing.T, name string) (*Runtime, Callable) {
	t.Helper()
	runtime := New()
	program := MustCompile("typed_trace_constant_step_test.js", typedConstantStepTestSource, false)
	if _, err := runtime.RunProgram(program); err != nil {
		t.Fatal(err)
	}
	call, ok := AssertFunction(runtime.Get(name))
	if !ok {
		t.Fatalf("%s is not callable", name)
	}
	return runtime, call
}

func callTypedConstantStepTest(t *testing.T, runtime *Runtime, call Callable, start, limit, seed Value) (Value, Value) {
	t.Helper()
	result, err := call(_undefined, start, limit, seed)
	if err != nil {
		t.Fatal(err)
	}
	object := result.ToObject(runtime)
	return object.Get("0"), object.Get("1")
}

func TestTypedConstantStepTraceLowers(t *testing.T) {
	for _, test := range []struct {
		name      string
		condition instruction
		operation instruction
		literal   valueInt
	}{
		{name: "PlusTwo", condition: op_lt, operation: add, literal: 2},
		{name: "PlusThreeInclusive", condition: op_lte, operation: add, literal: 3},
		{name: "MinusTwo", condition: op_gt, operation: sub, literal: 2},
		{name: "MinusNegativeThree", condition: op_lt, operation: sub, literal: -3},
	} {
		t.Run(test.name, func(t *testing.T) {
			if trace := lowerTypedIntLoopTrace(typedConstantStepInstructionProgram(test.condition, test.operation, test.literal)); trace == nil {
				t.Fatal("constant-step loop did not lower")
			}
		})
	}
}

func TestTypedConstantStepTraceRejectsUnsafeShapes(t *testing.T) {
	for _, test := range []struct {
		name      string
		condition instruction
		operation instruction
		literal   valueInt
		mutate    func(*Program)
	}{
		{name: "AscendingNegative", condition: op_lt, operation: add, literal: -2},
		{name: "DescendingPositive", condition: op_gt, operation: add, literal: 2},
		{name: "Zero", condition: op_lt, operation: add, literal: 0},
		{name: "UnitPositive", condition: op_lt, operation: add, literal: 1},
		{name: "UnitNegative", condition: op_gt, operation: add, literal: -1},
		{name: "TooLarge", condition: op_lt, operation: add, literal: 1 << 31},
		{name: "WrongStore", condition: op_lt, operation: add, literal: 2, mutate: func(program *Program) { program.code[11] = storeStackP(2) }},
		{name: "DynamicStep", condition: op_lt, operation: add, literal: 2, mutate: func(program *Program) { program.code[9] = loadStack(3) }},
		{name: "FloatStep", condition: op_lt, operation: add, literal: 2, mutate: func(program *Program) { program.code[9] = loadVal{v: valueFloat(2.5)} }},
	} {
		t.Run(test.name, func(t *testing.T) {
			program := typedConstantStepInstructionProgram(test.condition, test.operation, test.literal)
			if test.mutate != nil {
				test.mutate(program)
			}
			if trace := lowerTypedIntLoopTrace(program); trace != nil {
				t.Fatalf("unsafe constant-step loop lowered: %+v", trace.code)
			}
		})
	}
}

func TestTypedConstantStepTraceRemainingIterations(t *testing.T) {
	for _, test := range []struct {
		name              string
		condition, update instruction
		literal           valueInt
		counter, limit    int64
		want              int64
	}{
		{name: "AscendingExclusiveRoundUp", condition: op_lt, update: add, literal: 2, counter: 0, limit: 5, want: 3},
		{name: "AscendingExclusiveZero", condition: op_lt, update: add, literal: 2, counter: 5, limit: 5},
		{name: "AscendingInclusiveExact", condition: op_lte, update: add, literal: 3, counter: 0, limit: 3, want: 2},
		{name: "AscendingInclusiveRemainder", condition: op_lte, update: add, literal: 3, counter: 0, limit: 4, want: 2},
		{name: "DescendingExclusiveRoundUp", condition: op_gt, update: sub, literal: 2, counter: 5, limit: 0, want: 3},
		{name: "DescendingInclusive", condition: op_gte, update: sub, literal: 3, counter: 5, limit: 1, want: 2},
		{name: "FullSafeRangeExclusive", condition: op_lt, update: add, literal: 2, counter: -maxInt, limit: maxInt, want: maxInt},
		{name: "FullSafeRangeInclusive", condition: op_lte, update: add, literal: 2, counter: -maxInt, limit: maxInt, want: maxInt + 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			trace := lowerTypedIntLoopTrace(typedConstantStepInstructionProgram(test.condition, test.update, test.literal))
			if trace == nil {
				t.Fatal("test loop did not lower")
			}
			registers := [typedTraceRegisterCount]int64{typedTraceCounter: test.counter, typedTraceLimit: test.limit}
			if got := trace.remainingIterations(registers); got != test.want {
				t.Fatalf("remaining iterations: got %d, want %d", got, test.want)
			}
		})
	}
}

func TestTypedConstantStepTraceExecutesVariants(t *testing.T) {
	for _, test := range []struct {
		name                 string
		start, limit, seed   int64
		wantSum, wantCounter int64
		wantStep             int64
		wantComparison       typedTraceLoopComparison
		wantDirection        typedTraceLoopDirection
	}{
		{name: "typedTracePlusTwoFrom", start: 0, limit: 10, seed: 1, wantSum: 21, wantCounter: 10, wantStep: 2, wantDirection: typedTraceLoopAscending},
		{name: "typedTracePlusThreeInclusiveFrom", start: 0, limit: 10, seed: 1, wantSum: 19, wantCounter: 12, wantStep: 3, wantComparison: typedTraceLoopInclusive, wantDirection: typedTraceLoopAscending},
		{name: "typedTraceMinusTwoFrom", start: 10, limit: 0, seed: 1, wantSum: 31, wantCounter: 0, wantStep: -2, wantDirection: typedTraceLoopDescending},
		{name: "typedTraceMinusTwoInclusiveFrom", start: 10, limit: 0, seed: 1, wantSum: 31, wantCounter: -2, wantStep: -2, wantComparison: typedTraceLoopInclusive, wantDirection: typedTraceLoopDescending},
		{name: "typedTracePlusNegativeTwoFrom", start: 10, limit: 0, seed: 1, wantSum: 31, wantCounter: 0, wantStep: -2, wantDirection: typedTraceLoopDescending},
		{name: "typedTraceMinusNegativeThreeFrom", start: 0, limit: 10, seed: 1, wantSum: 19, wantCounter: 12, wantStep: 3, wantDirection: typedTraceLoopAscending},
	} {
		t.Run(test.name, func(t *testing.T) {
			runtime, call := setupTypedConstantStepTest(t, test.name)
			// A longer first call crosses the typed threshold before checking the
			// small, easy-to-audit result below.
			if test.wantStep > 0 {
				callTypedConstantStepTest(t, runtime, call, valueInt(0), valueInt(256), valueInt(0))
			} else {
				callTypedConstantStepTest(t, runtime, call, valueInt(256), valueInt(0), valueInt(0))
			}
			sum, counter := callTypedConstantStepTest(t, runtime, call, valueInt(test.start), valueInt(test.limit), valueInt(test.seed))
			if sum.ToInteger() != test.wantSum || counter.ToInteger() != test.wantCounter {
				t.Fatalf("sum/counter=%v/%v, want %d/%d", sum, counter, test.wantSum, test.wantCounter)
			}
			state := typedTraceState(runtime)
			if state == nil || state.typed.disabled() || state.typed.guardFailures != 0 {
				t.Fatalf("typed state: %p", state)
			}
			gotStep, ok := state.typed.trace.inductionStep()
			if !ok || gotStep != test.wantStep {
				t.Fatalf("induction step: got %d/%t, want %d", gotStep, ok, test.wantStep)
			}
			wantExit := typedTraceExitUnlessLess
			if test.wantDirection == typedTraceLoopDescending {
				wantExit = typedTraceExitUnlessGreater
			}
			if test.wantComparison == typedTraceLoopInclusive {
				if test.wantDirection == typedTraceLoopDescending {
					wantExit = typedTraceExitUnlessGreaterOrEqual
				} else {
					wantExit = typedTraceExitUnlessLessOrEqual
				}
			}
			if got := state.typed.trace.code[0].opcode; got != wantExit {
				t.Fatalf("exit opcode: got %d, want %d", got, wantExit)
			}
		})
	}
}

func TestTypedConstantStepTraceOverflowPreservesFinalInduction(t *testing.T) {
	for _, test := range []struct {
		name         string
		start, limit int64
		wantSum      valueInt
	}{
		{name: "typedTracePlusTwoFrom", start: maxInt - 1, limit: maxInt, wantSum: maxInt - 1},
		{name: "typedTraceMinusTwoFrom", start: -maxInt + 1, limit: -maxInt, wantSum: -maxInt + 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			runtime, call := setupTypedConstantStepTest(t, test.name)
			if test.start > 0 {
				callTypedConstantStepTest(t, runtime, call, valueInt(0), valueInt(256), valueInt(0))
			} else {
				callTypedConstantStepTest(t, runtime, call, valueInt(256), valueInt(0), valueInt(0))
			}
			sum, counter := callTypedConstantStepTest(t, runtime, call, valueInt(test.start), valueInt(test.limit), valueInt(0))
			if !sum.StrictEquals(test.wantSum) {
				t.Fatalf("sum: got %v, want %v", sum, test.wantSum)
			}
			if _, ok := counter.(valueFloat); !ok {
				t.Fatalf("overflowed induction value: got %v (%T), want valueFloat", counter, counter)
			}
			state := typedTraceState(runtime)
			if state == nil || !state.typed.disabled() || state.typed.guardFailures != 1 {
				t.Fatalf("overflow guard state: %p", state)
			}
		})
	}
}

func TestTypedConstantStepTraceGuardFailurePreservesThrowPosition(t *testing.T) {
	for _, test := range []struct {
		name        string
		limit, seed func(*Runtime) Value
		wantMessage string
	}{
		{name: "Seed", limit: func(*Runtime) Value { return valueInt(4) }, seed: func(runtime *Runtime) Value { return runtime.Get("typedConstantStepThrowingSeed") }, wantMessage: "constant-step seed deopt"},
		{name: "Limit", limit: func(runtime *Runtime) Value { return runtime.Get("typedConstantStepThrowingLimit") }, seed: func(*Runtime) Value { return valueInt(0) }, wantMessage: "constant-step limit deopt"},
	} {
		t.Run(test.name, func(t *testing.T) {
			plainRuntime, plainCall := setupTypedConstantStepTest(t, "typedTracePlusTwoFrom")
			_, wantErr := plainCall(_undefined, valueInt(0), test.limit(plainRuntime), test.seed(plainRuntime))
			if wantErr == nil {
				t.Fatal("interpreter reference did not throw")
			}
			runtime, call := setupTypedConstantStepTest(t, "typedTracePlusTwoFrom")
			callTypedConstantStepTest(t, runtime, call, valueInt(0), valueInt(256), valueInt(0))
			_, err := call(_undefined, valueInt(0), test.limit(runtime), test.seed(runtime))
			if err == nil {
				t.Fatal("trace call did not throw")
			}
			if message := err.Error(); message != wantErr.Error() || !strings.Contains(message, test.wantMessage) || !strings.Contains(message, "typed_trace_constant_step_test.js:") {
				t.Fatalf("trace/interpreter throw mismatch:\ntrace: %s\ninterpreter: %s", message, wantErr)
			}
		})
	}
}

func TestTypedConstantStepTraceSharedProgramRuntimeOwnership(t *testing.T) {
	program := MustCompile("typed_trace_constant_step_shared.js", typedConstantStepTestSource, false)
	states := make([]*programTierState, 2)
	for i := range states {
		runtime := New()
		if _, err := runtime.RunProgram(program); err != nil {
			t.Fatal(err)
		}
		call, _ := AssertFunction(runtime.Get("typedTracePlusTwoFrom"))
		callTypedConstantStepTest(t, runtime, call, valueInt(0), valueInt(2000), valueInt(0))
		states[i] = typedTraceState(runtime)
	}
	if states[0] == nil || states[1] == nil || states[0] == states[1] || states[0].program != states[1].program ||
		states[0].quickProgram == states[1].quickProgram || states[0].typed.program == states[1].typed.program {
		t.Fatal("constant-step typed state was shared between Runtimes")
	}
}

func TestTypedConstantStepTracePollMaterializesAtBackedge(t *testing.T) {
	for _, test := range []struct {
		name       string
		activate   func(*vm)
		deactivate func(*vm)
	}{
		{name: "Interrupt", activate: func(vm *vm) { atomic.StoreUint32(&vm.interrupted, 1) }, deactivate: func(vm *vm) { atomic.StoreUint32(&vm.interrupted, 0) }},
		{name: "Profiler", activate: func(*vm) { atomic.StoreInt32(&globalProfiler.enabled, 1) }, deactivate: func(*vm) { atomic.StoreInt32(&globalProfiler.enabled, 0) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			program := typedConstantStepInstructionProgram(op_lt, add, 2)
			trace := lowerTypedIntLoopTrace(program)
			quickProgram := *program
			state := &programTierState{program: program, quickProgram: &quickProgram}
			traceProgram := quickProgram
			state.typed = &typedTraceTierState{trace: trace, program: &traceProgram}
			entry := &typedTraceEntry{state: state}
			traceProgram.code = append([]instruction(nil), traceProgram.code...)
			traceProgram.code[trace.entryPC] = entry
			vm := New().vm
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
			if vm.prg != state.quickProgram || vm.pc != trace.entryPC || vm.stack[2] != valueInt(3) || vm.stack[3] != valueInt(6) {
				t.Fatalf("poll materialization: program=%p pc=%d counter=%v accumulator=%v", vm.prg, vm.pc, vm.stack[2], vm.stack[3])
			}
			if state.typed.disabled() || state.typed.guardFailures != 0 {
				t.Fatal("temporary poll permanently disabled constant-step trace")
			}
		})
	}
}

func TestTypedConstantStepTraceProfilerAndInterrupt(t *testing.T) {
	runtime, call := setupTypedConstantStepTest(t, "typedTracePlusTwoFrom")
	for i := 0; i < 6; i++ {
		callTypedConstantStepTest(t, runtime, call, valueInt(0), valueInt(2000), valueInt(0))
	}
	state := typedTraceState(runtime)
	if err := StartProfile(io.Discard); err != nil {
		t.Fatal(err)
	}
	sum, counter := callTypedConstantStepTest(t, runtime, call, valueInt(0), valueInt(10), valueInt(0))
	StopProfile()
	if sum.ToInteger() != 20 || counter.ToInteger() != 10 || state.typed.disabled() {
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
	runtime.Interrupt("constant-step trace stop")
	select {
	case err := <-done:
		var interrupted *InterruptedError
		if !errors.As(err, &interrupted) {
			t.Fatalf("call returned %T (%v), want InterruptedError", err, err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("constant-step trace did not observe interrupt")
	}
	runtime.ClearInterrupt()
}
