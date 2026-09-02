package goja

import (
	"sync/atomic"

	"github.com/maclof/goja-perf/unistring"
)

// globalCounterTrace is a Runtime-owned lowered representation of the exact
// empty global integer counter loop emitted for:
//
//	for (var i = 0; i < limit; i++) {}
//
// The loop has no calls or other observable body operations, so a guarded own
// writable data property may be kept in a Go integer register and materialised
// on every exit. The shared Program remains immutable.
type globalCounterTrace struct {
	entryPC     int
	exitPC      int
	backedgePC  int
	name        unistring.String
	limit       int64
	clearResult bool
}

type globalCounterTraceEntry struct {
	state *programTierState
	trace *globalCounterTrace
}

func (e *globalCounterTraceEntry) exec(vm *vm) {
	e.trace.execute(vm, e.state)
}

func (s *programTierState) installGlobalCounterTrace(trace *globalCounterTrace) {
	traceProgram := *s.quickProgram
	traceProgram.code = append([]instruction(nil), s.quickProgram.code...)
	traceProgram.code[trace.entryPC] = &globalCounterTraceEntry{state: s, trace: trace}
	s.typed = &typedTraceTierState{program: &traceProgram}
}

func (t *globalCounterTrace) execute(vm *vm, state *programTierState) {
	object, prop, counter, ok := t.guard(vm)
	if !ok {
		state.deoptGlobalCounterTrace(vm, t)
		return
	}
	if t.clearResult && counter < t.limit {
		vm.result = _undefined
	}
	for counter < t.limit {
		if counter >= maxInt {
			t.materialize(object, prop, counter)
			state.deoptGlobalCounterTrace(vm, t)
			return
		}
		counter++
		if atomic.LoadUint32(&vm.interrupted) != 0 || quickenedProfiling() {
			t.materialize(object, prop, counter)
			vm.prg = state.quickProgram
			vm.pc = t.entryPC
			return
		}
	}
	t.materialize(object, prop, counter)
	vm.pc = t.exitPC
}

func (t *globalCounterTrace) guard(vm *vm) (*baseObject, *valueProperty, int64, bool) {
	if vm.stash != &vm.r.global.stash {
		return nil, nil, 0, false
	}
	if _, exists := vm.r.global.stash.names[t.name]; exists {
		return nil, nil, 0, false
	}
	var global *baseObject
	switch object := vm.r.globalObject.self.(type) {
	case *templatedObject:
		global = &object.baseObject
	case *baseObject:
		global = object
	default:
		return nil, nil, 0, false
	}
	value := global.values[t.name]
	if prop, ok := value.(*valueProperty); ok {
		if !prop.writable || prop.accessor || prop.getterFunc != nil || prop.setterFunc != nil {
			return nil, nil, 0, false
		}
		counter, ok := prop.value.(valueInt)
		return global, prop, int64(counter), ok
	}
	counter, ok := value.(valueInt)
	return global, nil, int64(counter), ok
}

func (t *globalCounterTrace) materialize(object *baseObject, prop *valueProperty, counter int64) {
	value := valueInt(counter)
	if prop != nil {
		prop.value = value
	} else {
		object.values[t.name] = value
	}
}

func (s *programTierState) deoptGlobalCounterTrace(vm *vm, trace *globalCounterTrace) {
	s.typed.activation |= nativeTraceDisabledFlag
	s.typed.guardFailures++
	vm.prg = s.quickProgram
	vm.pc = trace.entryPC
}

func isGlobalCounterTraceAt(program *Program, hotBackedgePC int) bool {
	_, found := findGlobalCounterTraceAt(program, hotBackedgePC)
	return found
}

func lowerGlobalCounterTraceAt(program *Program, hotBackedgePC int) *globalCounterTrace {
	if trace, found := findGlobalCounterTraceAt(program, hotBackedgePC); found {
		return &trace
	}
	return nil
}

func findGlobalCounterTraceAt(program *Program, hotBackedgePC int) (globalCounterTrace, bool) {
	if trace, found := findGlobalCounterTraceShapeAt(program, hotBackedgePC, false); found {
		return trace, true
	}
	return findGlobalCounterTraceShapeAt(program, hotBackedgePC, true)
}

func findGlobalCounterTraceShapeAt(program *Program, hotBackedgePC int, clear bool) (globalCounterTrace, bool) {
	width := 9
	conditionOffset := 0
	bodyOffset := 4
	if clear {
		width = 10
		bodyOffset = 5
	}
	for entryPC := 0; entryPC+width <= len(program.code); entryPC++ {
		if clear {
			if _, ok := program.code[entryPC+4].(_clearResult); !ok {
				continue
			}
		}
		conditionName, conditionOK := program.code[entryPC+conditionOffset].(loadDynamic)
		limit, limitOK := program.code[entryPC+conditionOffset+1].(loadVal)
		_, lessOK := program.code[entryPC+conditionOffset+2].(_op_lt)
		exit, exitOK := program.code[entryPC+conditionOffset+3].(jneP)
		updateName, resolveOK := program.code[entryPC+bodyOffset].(resolveVar1Strict)
		_, getOK := program.code[entryPC+bodyOffset+1].(_getValue)
		_, incrementOK := program.code[entryPC+bodyOffset+2].(_inc)
		_, putOK := program.code[entryPC+bodyOffset+3].(_putValueP)
		backedgePC := entryPC + width - 1
		backedge, backedgeOK := program.code[backedgePC].(jump)
		limitValue, integerLimit := limit.v.(valueInt)
		if !conditionOK || !limitOK || !lessOK || !exitOK || !resolveOK || !getOK || !incrementOK || !putOK || !backedgeOK ||
			!integerLimit || unistring.String(conditionName) != unistring.String(updateName) ||
			backedgePC+int(backedge) != entryPC || hotBackedgePC >= 0 && hotBackedgePC != backedgePC {
			continue
		}
		exitPC := entryPC + conditionOffset + 3 + int(exit)
		if exit <= 0 || exitPC <= backedgePC || exitPC > len(program.code) {
			continue
		}
		return globalCounterTrace{
			entryPC: entryPC, exitPC: exitPC, backedgePC: backedgePC,
			name: unistring.String(conditionName), limit: int64(limitValue), clearResult: clear,
		}, true
	}
	return globalCounterTrace{}, false
}
