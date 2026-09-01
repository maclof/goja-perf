package goja

import (
	"fmt"
	"io"
	"math/big"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func quickenedProgramCount(runtime *Runtime) int {
	count := 0
	for _, state := range runtime.vm.tiering.programs {
		if state.quickProgram != nil {
			count++
		}
	}
	return count
}

func TestRuntimeTieringQuickensHotLoop(t *testing.T) {
	program := MustCompile("tiering_hot_loop.js", `
		(function(n) {
			var sum = 0;
			for (var i = 0; i < n; i++) {
				sum += i;
			}
			return sum;
		})(128);
	`, false)
	runtime := New()
	result, err := runtime.RunProgram(program)
	if err != nil {
		t.Fatal(err)
	}
	if got := result.ToInteger(); got != 8128 {
		t.Fatalf("unexpected result: got %d, want 8128", got)
	}

	quickened := 0
	for _, state := range runtime.vm.tiering.programs {
		if state.quickProgram == nil {
			continue
		}
		quickened++
		if state.blockCount == 0 {
			t.Fatal("quickened program contains no fused blocks")
		}
		if !state.primarySet && len(state.backedges) == 0 {
			t.Fatal("quickened loop has no recorded backedge")
		}
		if state.quickProgram == state.program {
			t.Fatal("shared Program was used as runtime-owned quickened code")
		}
	}
	if quickened == 0 {
		t.Fatal("hot loop did not activate tiering")
	}
}

func TestRuntimeTieringExceptionFallback(t *testing.T) {
	runtime := New()
	_, err := runtime.RunString(`
		var tierConversions = 0;
		var tierOperand = {
			valueOf: function() {
				tierConversions++;
				if (tierConversions === 40) {
					throw new Error("tier boom");
				}
				return 1;
			}
		};
		function tierThrow(value) {
			var sum = 0;
			for (var i = 0; i < 100; i++) {
				sum += value;
			}
			return sum;
		}
		tierThrow(tierOperand);
	`)
	if err == nil || !strings.Contains(err.Error(), "tier boom") {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := runtime.Get("tierConversions").ToInteger(); got != 40 {
		t.Fatalf("unexpected conversion count: got %d, want 40", got)
	}
	if quickenedProgramCount(runtime) == 0 {
		t.Fatal("exception occurred before quickened execution activated")
	}
}

func TestRuntimeTieringExceptionUnwindState(t *testing.T) {
	runtime := New()
	vm := runtime.vm
	outerProgram := &Program{}
	outerTier := &programTierState{program: outerProgram}
	contextStash := &stash{}
	contextPrivate := &privateEnv{}
	tryStash := &stash{}
	tryPrivate := &privateEnv{}
	wantTarget := valueInt(11)
	wantResult := valueInt(12)
	vm.callStack = append(vm.callStack, context{
		prg:       outerProgram,
		tier:      outerTier,
		stash:     contextStash,
		privEnv:   contextPrivate,
		newTarget: wantTarget,
		result:    wantResult,
		pc:        17,
		sb:        3,
		args:      4,
	})
	vm.tryStack = append(vm.tryStack, tryFrame{
		callStackLen: 0,
		sp:           1,
		stash:        tryStash,
		privEnv:      tryPrivate,
		catchPos:     23,
		finallyPos:   -1,
		finallyRet:   -1,
	})
	vm.prg = &Program{}
	vm.tier = &programTierState{}
	vm.stash = &stash{}
	vm.privEnv = &privateEnv{}
	vm.stack = make(valueStack, 4)
	vm.sp = 2

	if exception := vm.handleThrow(valueInt(99)); exception != nil {
		t.Fatalf("caught exception was returned: %v", exception)
	}
	if len(vm.callStack) != 0 {
		t.Fatalf("call stack length: got %d, want 0", len(vm.callStack))
	}
	if vm.prg != outerProgram || vm.tier != outerTier {
		t.Fatalf("program/tier not restored together: got %p/%p, want %p/%p", vm.prg, vm.tier, outerProgram, outerTier)
	}
	if vm.newTarget != wantTarget || vm.result != wantResult || vm.sb != 3 || vm.args != 4 {
		t.Fatalf("context fields not restored: target=%v result=%v sb=%d args=%d", vm.newTarget, vm.result, vm.sb, vm.args)
	}
	if vm.pc != 23 {
		t.Fatalf("catch PC: got %d, want 23", vm.pc)
	}
	if vm.stash != tryStash || vm.privEnv != tryPrivate {
		t.Fatalf("try environment not restored: stash/private=%p/%p, want %p/%p", vm.stash, vm.privEnv, tryStash, tryPrivate)
	}
	if vm.sp != 2 || vm.stack[1] != valueInt(99) {
		t.Fatalf("caught value not pushed at restored SP: sp=%d value=%v", vm.sp, vm.stack[1])
	}
}

func TestRuntimeTieringInterrupt(t *testing.T) {
	runtime := New()
	started := make(chan struct{})
	var once sync.Once
	if err := runtime.Set("tierStarted", func() {
		once.Do(func() { close(started) })
	}); err != nil {
		t.Fatal(err)
	}
	program := MustCompile("tiering_interrupt.js", `
		(function() {
			var i = 0;
			for (; i < 64; i++) {}
			tierStarted();
			for (;;) { i++; }
		})();
	`, false)
	result := make(chan error, 1)
	go func() {
		_, err := runtime.RunProgram(program)
		result <- err
	}()

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for quickened loop")
	}
	runtime.Interrupt("tier stop")
	select {
	case err := <-result:
		interrupted, ok := err.(*InterruptedError)
		if !ok || interrupted.Value() != "tier stop" {
			t.Fatalf("unexpected interrupt result: %T %v", err, err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("quickened loop did not observe interrupt")
	}
}

func TestRuntimeTieringProfilerFallback(t *testing.T) {
	runtime := New()
	var startErr error
	profileStarted := false
	if err := runtime.Set("tierStartProfile", func() {
		startErr = StartProfile(io.Discard)
		profileStarted = startErr == nil
	}); err != nil {
		t.Fatal(err)
	}

	result, err := runtime.RunString(`
		(function() {
			var sum = 0;
			for (var i = 0; i < 64; i++) { sum += i; }
			tierStartProfile();
			for (i = 0; i < 128; i++) { sum += i; }
			return sum;
		})();
	`)
	if profileStarted {
		StopProfile()
	}
	if startErr != nil {
		t.Fatal(startErr)
	}
	if err != nil {
		t.Fatal(err)
	}
	if got := result.ToInteger(); got != 10144 {
		t.Fatalf("unexpected result: got %d, want 10144", got)
	}
	if quickenedProgramCount(runtime) == 0 {
		t.Fatal("profiler started before quickened execution activated")
	}
}

func TestRuntimeTieringSharedProgramConcurrentRuntimes(t *testing.T) {
	program := MustCompile("tiering_concurrent.js", `
		(function(n) {
			var sum = 0;
			for (var i = 0; i < n; i++) { sum += i; }
			return sum;
		})(128);
	`, false)

	const workers = 8
	errors := make(chan error, workers)
	var wg sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			runtime := New()
			for iteration := 0; iteration < 20; iteration++ {
				result, err := runtime.RunProgram(program)
				if err != nil {
					errors <- err
					return
				}
				if got := result.ToInteger(); got != 8128 {
					errors <- fmt.Errorf("unexpected result: got %d, want 8128", got)
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		t.Error(err)
	}
}

func TestRuntimeTieringCacheIsBounded(t *testing.T) {
	var tiering runtimeTiering
	programs := make([]*Program, maxTieringPrograms+1)
	states := make([]*programTierState, maxTieringPrograms)
	for i := range programs {
		program := &Program{hasBackedge: true}
		programs[i] = program
		state := tiering.startTracking(program, 1, tieringTrackingThreshold)
		if i < maxTieringPrograms {
			states[i] = state
			if state == nil {
				t.Fatalf("program %d was refused before the admission limit", i)
			}
		} else if state != nil {
			t.Fatal("program was admitted after the tiering limit was reached")
		}
	}
	if got := tiering.lookup(programs[0]); got != states[0] {
		t.Fatalf("first admitted state changed after reaching the limit: got %p, want %p", got, states[0])
	}
	if got := tiering.lookup(programs[len(programs)-1]); got != nil {
		t.Fatalf("refused program unexpectedly became reachable: %p", got)
	}
}

func TestRuntimeTieringBoundWithLiveFunctions(t *testing.T) {
	const functionCount = maxTieringPrograms + 16
	var source strings.Builder
	source.WriteString("var tierLiveFunctions = [\n")
	for i := 0; i < functionCount; i++ {
		source.WriteString("function(n) { var sum = 0; for (var i = 0; i < n; i++) { sum += i; } return sum; },\n")
	}
	source.WriteString("];\n")
	for i := 0; i < functionCount; i++ {
		fmt.Fprintf(&source, "tierLiveFunctions[%d](40); tierLiveFunctions[%d](40);\n", i, i)
	}

	runtime := New()
	if _, err := runtime.RunString(source.String()); err != nil {
		t.Fatal(err)
	}
	if got := len(runtime.vm.tiering.programs); got != maxTieringPrograms {
		t.Fatalf("retained state count: got %d, want %d", got, maxTieringPrograms)
	}
	if got := quickenedProgramCount(runtime); got != maxTieringPrograms {
		t.Fatalf("retained quickened program count: got %d, want %d", got, maxTieringPrograms)
	}
	if got := typedTraceCount(runtime); got != maxTieringPrograms {
		t.Fatalf("retained typed trace count: got %d, want %d", got, maxTieringPrograms)
	}

	functions := runtime.Get("tierLiveFunctions").ToObject(runtime)
	first := functions.Get("0").(*Object).self.(*funcObject).prg
	last := functions.Get(strconv.Itoa(functionCount - 1)).(*Object).self.(*funcObject).prg
	firstState := runtime.vm.tiering.lookup(first)
	if firstState == nil {
		t.Fatal("first live function lost its admitted state")
	}
	if state := runtime.vm.tiering.lookup(last); state != nil {
		t.Fatalf("function beyond the limit retained tier state: %p", state)
	}
	call, ok := AssertFunction(functions.Get("0"))
	if !ok {
		t.Fatal("first retained value is not callable")
	}
	result, err := call(_undefined, valueInt(40))
	if err != nil {
		t.Fatal(err)
	}
	if got := result.ToInteger(); got != 780 {
		t.Fatalf("unexpected result after reaching admission limit: got %d, want 780", got)
	}
	if state := runtime.vm.tiering.lookup(first); state != firstState {
		t.Fatalf("admitted state was duplicated: got %p, want %p", state, firstState)
	}
}

func TestQuickenedProfilerStopsAtInnerBoundary(t *testing.T) {
	runtime := New()
	operand := runtime.NewObject()
	if err := operand.Set("valueOf", func() int {
		atomic.StoreInt32(&globalProfiler.enabled, 1)
		return 2
	}); err != nil {
		t.Fatal(err)
	}

	program := &Program{code: []instruction{
		loadStack(1), loadStack(2), add, storeStackP(3),
	}}
	code, blocks := buildQuickenedCode(program)
	if blocks != 1 {
		t.Fatalf("unexpected quickened block count: got %d, want 1", blocks)
	}
	vm := runtime.vm
	vm.prg = program
	vm.pc = 0
	vm.sb = 0
	vm.args = 0
	vm.stack = make(valueStack, 8)
	vm.stack[1] = valueInt(1)
	vm.stack[2] = operand
	vm.stack[3] = valueInt(-1)
	vm.sp = 4

	atomic.StoreInt32(&globalProfiler.enabled, 0)
	defer atomic.StoreInt32(&globalProfiler.enabled, 0)
	code[0].exec(vm)
	if vm.pc != 3 {
		t.Fatalf("quickened block crossed a profiler boundary: pc=%d, want 3", vm.pc)
	}
	if got := vm.stack[3]; got != valueInt(-1) {
		t.Fatalf("store ran after profiler activation: got %v, want -1", got)
	}
}

func TestQuickenedThrowPCEquivalence(t *testing.T) {
	tests := []struct {
		name   string
		code   []instruction
		slots  func() []Value
		wantPC int
		blocks int
	}{
		{
			name:   "LoopConditionLoadLexical",
			code:   []instruction{loadStack(1), loadStackLex(2), op_lt},
			slots:  func() []Value { return []Value{_undefined, valueInt(1), nil} },
			wantPC: 1,
			blocks: 1,
		},
		{
			name:   "LoopConditionCompare",
			code:   []instruction{loadStack(1), loadStackLex(2), op_lt},
			slots:  func() []Value { return []Value{_undefined, NewSymbol("left"), valueInt(1)} },
			wantPC: 2,
			blocks: 1,
		},
		{
			name:   "AddStoreAdd",
			code:   []instruction{loadStack(1), loadStack(2), add, storeStackP(3)},
			slots:  func() []Value { return []Value{_undefined, NewSymbol("left"), valueInt(1), valueInt(0)} },
			wantPC: 2,
			blocks: 1,
		},
		{
			name:   "ArithmeticStoreAdd",
			code:   []instruction{loadStack(1), loadVal{valueInt(1)}, add, loadVal{valueInt(2)}, mul, storeStackP(2)},
			slots:  func() []Value { return []Value{_undefined, NewSymbol("left"), valueInt(0)} },
			wantPC: 2,
			blocks: 1,
		},
		{
			name: "ArithmeticStoreMultiply",
			code: []instruction{
				loadStack(1),
				loadVal{(*valueBigInt)(big.NewInt(1))},
				add,
				loadVal{valueInt(2)},
				mul,
				storeStackP(2),
			},
			slots: func() []Value {
				return []Value{_undefined, (*valueBigInt)(big.NewInt(1)), valueInt(0)}
			},
			wantPC: 4,
			blocks: 1,
		},
		{
			name:   "IncrementStoreIncrement",
			code:   []instruction{loadStack(1), inc, storeStackP(2)},
			slots:  func() []Value { return []Value{_undefined, NewSymbol("value"), valueInt(0)} },
			wantPC: 1,
			blocks: 1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			program := &Program{code: test.code}
			program.srcMap = make([]srcMapItem, len(program.code))
			for pc := range program.srcMap {
				program.srcMap[pc] = srcMapItem{pc: pc, srcPos: 100 + pc}
			}
			quickCode, blocks := buildQuickenedCode(program)
			if blocks != test.blocks {
				t.Fatalf("unexpected quickened block count: got %d, want %d", blocks, test.blocks)
			}
			quickProgram := *program
			quickProgram.code = quickCode

			originalPC, originalSource := runTieringThrowProgram(t, program, test.slots())
			quickPC, quickSource := runTieringThrowProgram(t, &quickProgram, test.slots())
			if originalPC != test.wantPC {
				t.Fatalf("unexpected interpreter throw PC: got %d, want %d", originalPC, test.wantPC)
			}
			if quickPC != originalPC || quickSource != originalSource {
				t.Fatalf("throw location differs: interpreter pc/source=%d/%d, quickened=%d/%d", originalPC, originalSource, quickPC, quickSource)
			}
		})
	}
}

func runTieringThrowProgram(t *testing.T, program *Program, slots []Value) (pc, sourceOffset int) {
	t.Helper()
	runtime := New()
	vm := runtime.vm
	vm.prg = program
	vm.pc = 0
	vm.sb = 0
	vm.args = 0
	vm.stack = make(valueStack, len(slots)+8)
	copy(vm.stack, slots)
	vm.sp = len(slots)
	exception := vm.runTry()
	if exception == nil {
		t.Fatal("instruction program did not throw")
	}
	if len(exception.stack) == 0 {
		t.Fatal("exception contains no stack frame")
	}
	frame := exception.stack[0]
	return frame.pc, frame.prg.sourceOffset(frame.pc)
}

func TestTieringCompilerBackedgeCoverage(t *testing.T) {
	tests := []struct {
		name   string
		source string
	}{
		{name: "DoWhile", source: `var i = 0; do { i++; } while (i < 3);`},
		{name: "For", source: `for (var i = 0; i < 3; i++) {}`},
		{name: "While", source: `var i = 0; while (i < 3) { i++; }`},
		{name: "ForIn", source: `for (var key in {a: 1, b: 2}) {}`},
		{name: "ForOf", source: `for (var value of [1, 2]) {}`},
		{name: "Continue", source: `for (var i = 0; i < 3; i++) { if (i) continue; }`},
		{name: "LabelledContinue", source: `outer: for (var i = 0; i < 3; i++) { continue outer; }`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			program := MustCompile("tiering_backedge_"+test.name+".js", test.source, false)
			if !program.hasBackedge {
				t.Fatal("compiler did not mark loop Program as having a backedge")
			}
			found := false
			for pc, ins := range program.code {
				offset, relative := tieringRelativeOffset(ins)
				if !relative || offset >= 0 {
					continue
				}
				found = true
				switch ins.(type) {
				case jump, jeqP:
				default:
					t.Fatalf("untracked negative branch at pc %d: %T(%d)", pc, ins, offset)
				}
			}
			if !found {
				t.Fatal("loop Program contains no negative relative branch")
			}
		})
	}
}

func tieringRelativeOffset(ins instruction) (int32, bool) {
	switch ins := ins.(type) {
	case jump:
		return int32(ins), true
	case jneP:
		return int32(ins), true
	case jeqP:
		return int32(ins), true
	case jeq:
		return int32(ins), true
	case jne:
		return int32(ins), true
	case jdef:
		return int32(ins), true
	case jdefP:
		return int32(ins), true
	case jopt:
		return int32(ins), true
	case joptc:
		return int32(ins), true
	case joptdel:
		return int32(ins), true
	case joptdelc:
		return int32(ins), true
	case joptdelP:
		return int32(ins), true
	case joptdelcP:
		return int32(ins), true
	case jcoalesc:
		return int32(ins), true
	case jcoalescP:
		return int32(ins), true
	case enumNext:
		return int32(ins), true
	case iterNext:
		return int32(ins), true
	default:
		return 0, false
	}
}
