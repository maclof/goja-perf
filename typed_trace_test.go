package goja

import (
	"io"
	"strings"
	"sync/atomic"
	"testing"
)

const typedTraceTestSource = `
function typedTraceSum(n, seed) {
	var sum = seed;
	for (var i = 0; i < n; i++) {
		sum += i;
	}
	return sum;
}
var typedTraceThrowingSeed = {
	valueOf: function() {
		throw new Error("typed trace deopt");
	}
};
`

func typedTraceState(runtime *Runtime) *programTierState {
	for _, state := range runtime.vm.tiering.programs {
		if state.typed != nil {
			return state
		}
	}
	return nil
}

func typedTraceCount(runtime *Runtime) int {
	count := 0
	for _, state := range runtime.vm.tiering.programs {
		if state.typed != nil {
			count++
		}
	}
	return count
}

func setupTypedTraceTest(t *testing.T) (*Runtime, Callable) {
	t.Helper()
	runtime := New()
	if _, err := runtime.RunString(typedTraceTestSource); err != nil {
		t.Fatal(err)
	}
	call, ok := AssertFunction(runtime.Get("typedTraceSum"))
	if !ok {
		t.Fatal("typedTraceSum is not callable")
	}
	return runtime, call
}

func callTypedTraceTest(t *testing.T, call Callable, n, seed Value) Value {
	t.Helper()
	result, err := call(_undefined, n, seed)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestTypedTraceLowersAndExecutes(t *testing.T) {
	runtime, call := setupTypedTraceTest(t)
	if result := callTypedTraceTest(t, call, valueInt(128), valueInt(0)); result.ToInteger() != 8128 {
		t.Fatalf("unexpected tier-up result: %v", result)
	}
	state := typedTraceState(runtime)
	if state == nil {
		t.Fatal("hot integer loop did not lower to typed IR")
	}
	if state.typed.program == nil || state.typed.program == state.quickProgram || state.typed.program == state.program {
		t.Fatal("typed trace does not have a distinct Runtime-owned Program")
	}
	trace := state.typed.trace
	if len(trace.guards) != 3 || len(trace.deopts) != 1 || len(trace.deopts[0].stackMap) != 3 {
		t.Fatalf("incomplete guard/deopt metadata: guards=%d deopts=%d stack-map=%d", len(trace.guards), len(trace.deopts), len(trace.deopts[0].stackMap))
	}
	if len(trace.code) != 7 {
		t.Fatalf("unexpected typed IR length: got %d, want 7", len(trace.code))
	}

	tests := []struct {
		n, seed int64
		want    int64
	}{
		{n: 0, seed: 7, want: 7},
		{n: 1, seed: -3, want: -3},
		{n: 5, seed: 3, want: 13},
		{n: 1000, seed: 0, want: 499500},
	}
	for _, test := range tests {
		result := callTypedTraceTest(t, call, valueInt(test.n), valueInt(test.seed))
		if got := result.ToInteger(); got != test.want {
			t.Fatalf("typed trace result for n=%d seed=%d: got %d, want %d", test.n, test.seed, got, test.want)
		}
	}
	if state.typed.disabled || state.typed.guardFailures != 0 {
		t.Fatalf("monomorphic trace unexpectedly deoptimised: disabled=%t failures=%d", state.typed.disabled, state.typed.guardFailures)
	}
}

func TestTypedTraceGuardFailureDisablesWithoutThrash(t *testing.T) {
	runtime, call := setupTypedTraceTest(t)
	callTypedTraceTest(t, call, valueInt(128), valueInt(0))
	state := typedTraceState(runtime)
	if state == nil {
		t.Fatal("typed trace was not produced")
	}
	wantTrace := state.typed.trace
	wantProgram := state.typed.program

	result := callTypedTraceTest(t, call, valueInt(5), valueFloat(0.5))
	if !result.StrictEquals(valueFloat(10.5)) {
		t.Fatalf("polymorphic deopt result: got %v, want 10.5", result)
	}
	if !state.typed.disabled || state.typed.guardFailures != 1 {
		t.Fatalf("guard failure state: disabled=%t failures=%d", state.typed.disabled, state.typed.guardFailures)
	}
	for i := 0; i < 10; i++ {
		result = callTypedTraceTest(t, call, valueInt(5), valueInt(0))
		if result.ToInteger() != 10 {
			t.Fatalf("fallback result %d: %v", i, result)
		}
	}
	if state.typed.trace != wantTrace || state.typed.program != wantProgram || state.typed.guardFailures != 1 {
		t.Fatalf("guard failure recompiled or thrashed: trace=%p/%p program=%p/%p failures=%d", state.typed.trace, wantTrace, state.typed.program, wantProgram, state.typed.guardFailures)
	}
}

func TestTypedTraceRangeDeoptMatchesInterpreter(t *testing.T) {
	hotRuntime, hotCall := setupTypedTraceTest(t)
	callTypedTraceTest(t, hotCall, valueInt(128), valueInt(0))
	hot := callTypedTraceTest(t, hotCall, valueInt(2), valueInt(maxInt))
	state := typedTraceState(hotRuntime)
	if state == nil || !state.typed.disabled || state.typed.guardFailures != 1 {
		t.Fatalf("range guard did not disable trace: state=%p", state)
	}

	_, coldCall := setupTypedTraceTest(t)
	cold := callTypedTraceTest(t, coldCall, valueInt(2), valueInt(maxInt))
	if !hot.StrictEquals(cold) {
		t.Fatalf("range deopt differs from interpreter: hot=%v (%T), cold=%v (%T)", hot, hot, cold, cold)
	}
}

func TestTypedTraceExceptionDeoptPreservesSource(t *testing.T) {
	tests := []struct {
		name string
		args func(*Runtime) (Value, Value)
	}{
		{
			name: "LimitComparison",
			args: func(runtime *Runtime) (Value, Value) {
				return runtime.Get("typedTraceThrowingSeed"), valueInt(0)
			},
		},
		{
			name: "AccumulatorAdd",
			args: func(runtime *Runtime) (Value, Value) {
				return valueInt(2), runtime.Get("typedTraceThrowingSeed")
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			hotRuntime, hotCall := setupTypedTraceTest(t)
			callTypedTraceTest(t, hotCall, valueInt(128), valueInt(0))
			hotN, hotSeed := test.args(hotRuntime)
			_, hotErr := hotCall(_undefined, hotN, hotSeed)
			if hotErr == nil || !strings.Contains(hotErr.Error(), "typed trace deopt") {
				t.Fatalf("unexpected hot error: %v", hotErr)
			}
			state := typedTraceState(hotRuntime)
			if state == nil || !state.typed.disabled || state.typed.guardFailures != 1 {
				t.Fatalf("exception guard did not disable trace: state=%p", state)
			}

			coldRuntime, coldCall := setupTypedTraceTest(t)
			coldN, coldSeed := test.args(coldRuntime)
			_, coldErr := coldCall(_undefined, coldN, coldSeed)
			if coldErr == nil {
				t.Fatal("cold interpreter did not throw")
			}
			hotException := hotErr.(*Exception)
			coldException := coldErr.(*Exception)
			if len(hotException.stack) < 2 || len(coldException.stack) < 2 {
				t.Fatalf("incomplete stacks: hot=%d cold=%d", len(hotException.stack), len(coldException.stack))
			}
			hotFrame := hotException.stack[1]
			coldFrame := coldException.stack[1]
			if hotFrame.pc != coldFrame.pc || hotFrame.Position() != coldFrame.Position() {
				t.Fatalf("deopt source differs: hot pc/position=%d/%v, cold=%d/%v", hotFrame.pc, hotFrame.Position(), coldFrame.pc, coldFrame.Position())
			}
		})
	}
}

func TestTypedTraceBackedgePolls(t *testing.T) {
	tests := []struct {
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
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			program := typedTraceInstructionProgram()
			trace := lowerTypedIntLoopTrace(program)
			if trace == nil {
				t.Fatal("test loop did not lower")
			}
			quickProgram := *program
			state := &programTierState{program: program, quickProgram: &quickProgram}
			traceProgram := quickProgram
			state.typed = &typedTraceTierState{trace: trace, program: &traceProgram}
			runtime := New()
			vm := runtime.vm
			vm.prg = state.typed.program
			vm.tier = state
			vm.pc = trace.entryPC
			vm.sb = 0
			vm.args = 1
			vm.stack = make(valueStack, 8)
			vm.stack[1] = valueInt(10)
			vm.stack[2] = valueInt(0)
			vm.stack[3] = valueInt(0)
			vm.sp = 4
			test.activate(vm)
			defer test.deactivate(vm)

			trace.execute(vm, state)
			if vm.prg != state.quickProgram || vm.pc != trace.entryPC {
				t.Fatalf("poll did not deopt to loop header: program=%p pc=%d", vm.prg, vm.pc)
			}
			if vm.stack[2] != valueInt(1) || vm.stack[3] != valueInt(0) {
				t.Fatalf("registers were not materialised at backedge: counter=%v accumulator=%v", vm.stack[2], vm.stack[3])
			}
			if state.typed.disabled || state.typed.guardFailures != 0 {
				t.Fatalf("temporary poll disabled trace: disabled=%t failures=%d", state.typed.disabled, state.typed.guardFailures)
			}
		})
	}
}

func typedTraceInstructionProgram() *Program {
	return &Program{code: []instruction{
		loadStack(1), loadStackLex(-1), op_lt, jneP(9),
		loadStack(2), loadStack(1), add, storeStackP(2),
		loadStack(1), inc, storeStackP(1), jump(-11),
	}}
}

func TestTypedTraceLowersOnlyHotBackedge(t *testing.T) {
	first := typedTraceInstructionProgram()
	program := &Program{code: append(append([]instruction(nil), first.code...), first.code...)}
	trace := lowerTypedIntLoopTraceAt(program, 23)
	if trace == nil || trace.entryPC != 12 {
		t.Fatalf("lowered entry: got %v, want second loop at pc 12", trace)
	}
	if trace := lowerTypedIntLoopTraceAt(program, 10); trace != nil {
		t.Fatalf("lowered a loop other than the hot backedge: entry=%d", trace.entryPC)
	}
}

func TestTypedTraceProfilerUsesOriginalProgram(t *testing.T) {
	runtime, call := setupTypedTraceTest(t)
	callTypedTraceTest(t, call, valueInt(128), valueInt(0))
	state := typedTraceState(runtime)
	if state == nil {
		t.Fatal("typed trace was not produced")
	}
	if err := StartProfile(io.Discard); err != nil {
		t.Fatal(err)
	}
	result, err := call(_undefined, valueInt(128), valueInt(0))
	StopProfile()
	if err != nil {
		t.Fatal(err)
	}
	if result.ToInteger() != 8128 {
		t.Fatalf("profiled result: got %v, want 8128", result)
	}
	if state.typed.disabled || state.typed.guardFailures != 0 {
		t.Fatal("profiling permanently disabled typed trace")
	}
}
