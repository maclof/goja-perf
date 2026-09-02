package goja

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/maclof/goja-perf/unistring"
)

func globalCounterState(runtime *Runtime, program *Program) *programTierState {
	return runtime.vm.tiering.lookup(program)
}

func globalCounterTraceForState(state *programTierState) *globalCounterTrace {
	if state == nil || state.typed == nil || state.typed.program == nil {
		return nil
	}
	for _, instruction := range state.typed.program.code {
		if entry, ok := instruction.(*globalCounterTraceEntry); ok {
			return entry.trace
		}
	}
	return nil
}

func TestLowerGlobalCounterTraceShapes(t *testing.T) {
	tests := []struct {
		name, source          string
		entry, backedge, exit int
		clear                 bool
	}{
		{"Consumed", "for (var i = 0; i < 128; i++) {} i;", 4, 12, 13, false},
		{"Discarded", "for (var i = 0; i < 128; i++) {}", 5, 14, 15, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			program := MustCompile("global_counter_shape.js", test.source, true)
			trace := lowerGlobalCounterTraceAt(program, test.backedge)
			if trace == nil {
				t.Fatal("global counter loop was not lowered")
			}
			if trace.entryPC != test.entry || trace.backedgePC != test.backedge || trace.exitPC != test.exit || trace.clearResult != test.clear {
				t.Fatalf("positions got %d/%d/%d/%t, want %d/%d/%d/%t", trace.entryPC, trace.backedgePC, trace.exitPC, trace.clearResult, test.entry, test.backedge, test.exit, test.clear)
			}
			if trace.name != "i" || trace.limit != 128 {
				t.Fatalf("operands got %q/%d, want i/128", trace.name, trace.limit)
			}
		})
	}
}

func TestGlobalCounterTraceDiscardedCompletionEquivalence(t *testing.T) {
	for _, test := range []struct {
		name         string
		counter      int64
		guardFailure bool
		wantResult   Value
	}{
		{name: "ZeroIterations", counter: 128, wantResult: valueInt(41)},
		{name: "OneIteration", counter: 127, wantResult: _undefined},
		{name: "GuardFailureZeroIterations", counter: 128, guardFailure: true, wantResult: valueInt(41)},
	} {
		t.Run(test.name, func(t *testing.T) {
			interpreterResult, interpreterCounter := runGlobalCounterCompletionContinuation(t, test.counter, false, false)
			traceResult, traceCounter := runGlobalCounterCompletionContinuation(t, test.counter, true, test.guardFailure)
			if !interpreterResult.SameAs(test.wantResult) || !traceResult.SameAs(interpreterResult) {
				t.Fatalf("completion: interpreter=%v trace=%v want=%v", interpreterResult, traceResult, test.wantResult)
			}
			if interpreterCounter != 128 || traceCounter != interpreterCounter {
				t.Fatalf("counter: interpreter=%d trace=%d want=128", interpreterCounter, traceCounter)
			}
		})
	}
}

func runGlobalCounterCompletionContinuation(t *testing.T, counter int64, traced, guardFailure bool) (Value, int64) {
	t.Helper()
	program := MustCompile("global_counter_completion.js", "for (var i = 0; i < 128; i++) {}", true)
	runtime := New()
	if _, err := runtime.RunProgram(program); err != nil {
		t.Fatal(err)
	}
	state := globalCounterState(runtime, program)
	trace := globalCounterTraceForState(state)
	if trace == nil {
		t.Fatal("discarded global counter trace did not activate")
	}
	prop := runtime.globalObject.self.(*templatedObject).values[trace.name].(*valueProperty)
	prop.value = valueInt(counter)
	vm := runtime.vm
	// Model a completion-producing statement/eval immediately before entry.
	vm.result = valueInt(41)
	vm.pc = trace.entryPC
	vm.stash = &runtime.global.stash
	if traced {
		vm.prg = state.typed.program
		vm.tier = state
		if guardFailure {
			vm.stash = &stash{outer: &runtime.global.stash}
		}
	} else {
		vm.prg = state.program
		vm.tier = nil
		vm.tiering.programs = make(map[*Program]*programTierState, maxTieringPrograms)
		for i := 0; i < maxTieringPrograms; i++ {
			full := &Program{hasBackedge: true}
			vm.tiering.programs[full] = &programTierState{program: full}
		}
	}
	vm.run()
	return vm.result, prop.value.ToInteger()
}

func TestGlobalCounterTraceLazyActivationAndResult(t *testing.T) {
	for _, test := range []struct {
		limit      int
		wantActive bool
	}{{64, false}, {128, true}} {
		t.Run(fmt.Sprint(test.limit), func(t *testing.T) {
			program := MustCompile("global_counter_activation.js", fmt.Sprintf("for (var i = 0; i < %d; i++) {} i;", test.limit), true)
			runtime := New()
			result, err := runtime.RunProgram(program)
			if err != nil || result.ToInteger() != int64(test.limit) {
				t.Fatalf("result=%v err=%v", result, err)
			}
			state := globalCounterState(runtime, program)
			if state == nil || state.quickProgram == nil {
				t.Fatal("loop did not quicken")
			}
			if active := globalCounterTraceForState(state) != nil; active != test.wantActive {
				t.Fatalf("global trace activation got %t, want %t", active, test.wantActive)
			}
			if !test.wantActive {
				if !state.globalCandidate {
					t.Fatal("short loop lost its delayed candidate")
				}
				return
			}
			if state.typed.disabled() || state.typed.guardFailures != 0 {
				t.Fatalf("successful trace disabled=%t failures=%d", state.typed.disabled(), state.typed.guardFailures)
			}
			if _, tracked := state.quickProgram.code[state.primaryPC].(quickenedTrackedJump); tracked {
				t.Fatal("finalized candidate retained tracked backedge")
			}
		})
	}
}

func TestGlobalCounterTraceRevalidatesReplacementGlobal(t *testing.T) {
	program := MustCompile("global_counter_replacement.js", "for (var i = 0; i < 128; i++) {} i;", true)
	runtime := New()
	if _, err := runtime.RunProgram(program); err != nil {
		t.Fatal(err)
	}
	state := globalCounterState(runtime, program)
	replacement := runtime.NewObject()
	runtime.SetGlobalObject(replacement)
	for run := 0; run < 2; run++ {
		result, err := runtime.RunProgram(program)
		if err != nil || result.ToInteger() != 128 || replacement.Get("i").ToInteger() != 128 {
			t.Fatalf("run %d result=%v property=%v err=%v", run, result, replacement.Get("i"), err)
		}
	}
	if state.typed.disabled() || state.typed.guardFailures != 0 {
		t.Fatalf("plain replacement guard disabled=%t failures=%d", state.typed.disabled(), state.typed.guardFailures)
	}
}

func TestGlobalCounterTraceAcceptsOwnWritableRawProperty(t *testing.T) {
	program := MustCompile("global_counter_raw.js", "for (var i = 0; i < 128; i++) {} i;", true)
	runtime := New()
	if err := runtime.Set("i", 41); err != nil {
		t.Fatal(err)
	}
	result, err := runtime.RunProgram(program)
	if err != nil || result.ToInteger() != 128 {
		t.Fatalf("result=%v err=%v", result, err)
	}
	state := globalCounterState(runtime, program)
	if state == nil || globalCounterTraceForState(state) == nil || state.typed.disabled() {
		t.Fatalf("raw writable property did not remain traceable: state=%p", state)
	}
}

func TestGlobalCounterTracePreservesAccessorAndProxySemantics(t *testing.T) {
	program := MustCompile("global_counter_exotic.js", "for (var i = 0; i < 128; i++) {} i;", true)
	tests := []struct {
		name  string
		setup func(*testing.T, *Runtime) (*Object, *int, *int)
	}{
		{"Accessor", func(t *testing.T, runtime *Runtime) (*Object, *int, *int) {
			object := runtime.NewObject()
			gets, sets, value := 0, 0, int64(0)
			getter := runtime.ToValue(func(FunctionCall) Value { gets++; return valueInt(value) })
			setter := runtime.ToValue(func(call FunctionCall) Value { sets++; value = call.Argument(0).ToInteger(); return _undefined })
			if err := object.DefineAccessorProperty("i", getter, setter, FLAG_TRUE, FLAG_TRUE); err != nil {
				t.Fatal(err)
			}
			return object, &gets, &sets
		}},
		{"Proxy", func(t *testing.T, runtime *Runtime) (*Object, *int, *int) {
			target := runtime.NewObject()
			if err := target.Set("i", 0); err != nil {
				t.Fatal(err)
			}
			gets, sets := 0, 0
			proxy := runtime.NewProxy(target, &ProxyTrapConfig{
				Get: func(target *Object, property string, receiver Value) Value {
					if property == "i" {
						gets++
					}
					return target.Get(property)
				},
				Set: func(target *Object, property string, value Value, receiver Value) bool {
					if property == "i" {
						sets++
					}
					return target.Set(property, value) == nil
				},
			})
			return runtime.ToValue(proxy).(*Object), &gets, &sets
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runtime := New()
			if _, err := runtime.RunProgram(program); err != nil {
				t.Fatal(err)
			}
			state := globalCounterState(runtime, program)
			global, gets, sets := test.setup(t, runtime)
			runtime.SetGlobalObject(global)
			result, err := runtime.RunProgram(program)
			if err != nil || result.ToInteger() != 128 || global.Get("i").ToInteger() != 128 {
				t.Fatalf("result=%v property=%v err=%v", result, global.Get("i"), err)
			}
			if *gets < 129 || *sets < 129 {
				t.Fatalf("traps skipped: gets=%d sets=%d", *gets, *sets)
			}
			if !state.typed.disabled() || state.typed.guardFailures != 1 {
				t.Fatalf("guard disabled=%t failures=%d", state.typed.disabled(), state.typed.guardFailures)
			}
			if _, err := runtime.RunProgram(program); err != nil {
				t.Fatal(err)
			}
			if state.typed.guardFailures != 1 {
				t.Fatalf("disabled trace retried: failures=%d", state.typed.guardFailures)
			}
		})
	}
}

func TestGlobalCounterTracePreservesNonWritableError(t *testing.T) {
	program := MustCompile("global_counter_readonly.js", "for (var i = 0; i < 128; i++) {}", true)
	runtime := New()
	if _, err := runtime.RunProgram(program); err != nil {
		t.Fatal(err)
	}
	replacement := runtime.NewObject()
	if err := replacement.DefineDataProperty("i", valueInt(7), FLAG_FALSE, FLAG_TRUE, FLAG_TRUE); err != nil {
		t.Fatal(err)
	}
	runtime.SetGlobalObject(replacement)
	_, err := runtime.RunProgram(program)
	if err == nil || !strings.Contains(err.Error(), "read only") {
		t.Fatalf("non-writable error: %v", err)
	}
	if got := replacement.Get("i").ToInteger(); got != 7 {
		t.Fatalf("property got %d, want 7", got)
	}
}

func TestGlobalCounterTraceRejectsDynamicEnvironment(t *testing.T) {
	for _, test := range []struct {
		name  string
		stash func(*Runtime) *stash
	}{
		{"EvalLexical", func(runtime *Runtime) *stash {
			return &stash{names: map[unistring.String]uint32{"i": 0}, values: []Value{valueInt(7)}, outer: &runtime.global.stash}
		}},
		{"With", func(runtime *Runtime) *stash {
			object := runtime.NewObject()
			if err := object.Set("i", 7); err != nil {
				panic(err)
			}
			return &stash{obj: object, outer: &runtime.global.stash}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			program := MustCompile("global_counter_dynamic_environment.js", "for (var i = 0; i < 128; i++) {} i;", true)
			runtime := New()
			if _, err := runtime.RunProgram(program); err != nil {
				t.Fatal(err)
			}
			state := globalCounterState(runtime, program)
			trace := globalCounterTraceForState(state)
			runtime.vm.stash = test.stash(runtime)
			runtime.vm.prg, runtime.vm.pc = state.typed.program, trace.entryPC
			trace.execute(runtime.vm, state)
			if !state.typed.disabled() || state.typed.guardFailures != 1 || runtime.vm.prg != state.quickProgram || runtime.vm.pc != trace.entryPC {
				t.Fatalf("dynamic environment did not deopt: disabled=%t failures=%d program=%p pc=%d", state.typed.disabled(), state.typed.guardFailures, runtime.vm.prg, runtime.vm.pc)
			}
		})
	}
}

func TestGlobalCounterTracePollMaterializes(t *testing.T) {
	program := MustCompile("global_counter_poll.js", "for (var i = 0; i < 128; i++) {} i;", true)
	runtime := New()
	if _, err := runtime.RunProgram(program); err != nil {
		t.Fatal(err)
	}
	state := globalCounterState(runtime, program)
	trace := globalCounterTraceForState(state)
	prop := runtime.globalObject.self.(*templatedObject).values[trace.name].(*valueProperty)
	tests := []struct {
		name            string
		enable, disable func()
	}{
		{"Interrupt", func() { atomic.StoreUint32(&runtime.vm.interrupted, 1) }, func() { atomic.StoreUint32(&runtime.vm.interrupted, 0) }},
		{"Profiler", func() { atomic.StoreInt32(&globalProfiler.enabled, 1) }, func() { atomic.StoreInt32(&globalProfiler.enabled, 0) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			prop.value = valueInt(0)
			runtime.vm.prg, runtime.vm.pc = state.typed.program, trace.entryPC
			test.enable()
			defer test.disable()
			trace.execute(runtime.vm, state)
			if prop.value.ToInteger() != 1 || runtime.vm.prg != state.quickProgram || runtime.vm.pc != trace.entryPC {
				t.Fatalf("materialization/deopt value=%v program=%p pc=%d", prop.value, runtime.vm.prg, runtime.vm.pc)
			}
		})
	}
}

func TestGlobalCounterTraceSharedProgramConcurrentRuntimes(t *testing.T) {
	program := MustCompile("global_counter_shared.js", "for (var i = 0; i < 256; i++) {} i;", true)
	const count = 8
	var wg sync.WaitGroup
	errs := make(chan error, count)
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			runtime := New()
			result, err := runtime.RunProgram(program)
			state := globalCounterState(runtime, program)
			if err != nil || result.ToInteger() != 256 || state == nil || globalCounterTraceForState(state) == nil || state.typed.program == program {
				errs <- fmt.Errorf("result=%v state=%p err=%v", result, state, err)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}
