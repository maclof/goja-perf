package goja

import "sync/atomic"

type typedTraceRegister uint8

const (
	typedTraceCounter typedTraceRegister = iota
	typedTraceLimit
	typedTraceAccumulator
	typedTraceRegisterCount
)

type typedTraceStackSlot struct {
	operand int
	lexical bool
}

func (s typedTraceStackSlot) index(vm *vm) (int, bool) {
	if s.lexical && s.operand <= 0 {
		arg := -s.operand
		if arg > vm.args {
			return 0, false
		}
		return vm.sb + arg, true
	}
	return vm.sb + vm.args + s.operand, true
}

type typedTraceGuard struct {
	register typedTraceRegister
	slot     typedTraceStackSlot
	deopt    uint8
}

type typedTraceStackMapEntry struct {
	register typedTraceRegister
	slot     typedTraceStackSlot
}

type typedTraceDeopt struct {
	pc       int
	stackMap []typedTraceStackMapEntry
}

type typedTraceOpcode uint8

const (
	typedTraceExitUnlessLess typedTraceOpcode = iota
	typedTraceGuardAddRange
	typedTraceGuardIncrementRange
	typedTraceAdd
	typedTraceIncrement
	typedTracePollBackedge
	typedTraceJump
)

type typedTraceIR struct {
	opcode typedTraceOpcode
	dst    typedTraceRegister
	left   typedTraceRegister
	right  typedTraceRegister
	target uint8
	deopt  uint8
	exitPC int
}

// typedIntLoopTrace is a Runtime-owned lowered representation. It describes
// guards, virtual integer registers, their VM stack locations, deoptimisation
// PCs, and a small typed IR consumed by the Go trace executor below.
type typedIntLoopTrace struct {
	entryPC int
	guards  []typedTraceGuard
	deopts  []typedTraceDeopt
	code    []typedTraceIR
}

type typedTraceTierState struct {
	trace         *typedIntLoopTrace
	program       *Program
	disabled      bool
	guardFailures uint32
}

type typedTraceEntry struct {
	state *programTierState
	trace *typedIntLoopTrace
}

func (e *typedTraceEntry) exec(vm *vm) {
	e.trace.execute(vm, e.state)
}

func (t *typedIntLoopTrace) execute(vm *vm, state *programTierState) {
	var registers [typedTraceRegisterCount]int64
	for _, guard := range t.guards {
		idx, exists := guard.slot.index(vm)
		if !exists {
			state.deoptTypedTrace(vm, t.deopts[guard.deopt], registers, false, true)
			return
		}
		value, ok := vm.stack[idx].(valueInt)
		if !ok {
			state.deoptTypedTrace(vm, t.deopts[guard.deopt], registers, false, true)
			return
		}
		registers[guard.register] = int64(value)
	}

	for ip := 0; ; ip++ {
		operation := t.code[ip]
		switch operation.opcode {
		case typedTraceExitUnlessLess:
			if registers[operation.left] >= registers[operation.right] {
				t.materialize(vm, registers, t.deopts[operation.deopt].stackMap)
				vm.pc = operation.exitPC
				return
			}
		case typedTraceGuardAddRange:
			result := registers[operation.left] + registers[operation.right]
			if result < -maxInt || result > maxInt {
				state.deoptTypedTrace(vm, t.deopts[operation.deopt], registers, true, true)
				return
			}
		case typedTraceGuardIncrementRange:
			if registers[operation.left] >= maxInt {
				state.deoptTypedTrace(vm, t.deopts[operation.deopt], registers, true, true)
				return
			}
		case typedTraceAdd:
			registers[operation.dst] = registers[operation.left] + registers[operation.right]
		case typedTraceIncrement:
			registers[operation.dst]++
		case typedTracePollBackedge:
			if atomic.LoadUint32(&vm.interrupted) != 0 || quickenedProfiling() {
				state.deoptTypedTrace(vm, t.deopts[operation.deopt], registers, true, false)
				return
			}
		case typedTraceJump:
			ip = int(operation.target) - 1
		}
	}
}

func (t *typedIntLoopTrace) materialize(vm *vm, registers [typedTraceRegisterCount]int64, stackMap []typedTraceStackMapEntry) {
	for _, entry := range stackMap {
		idx, exists := entry.slot.index(vm)
		if exists {
			vm.stack[idx] = valueInt(registers[entry.register])
		}
	}
}

func (s *programTierState) deoptTypedTrace(vm *vm, deopt typedTraceDeopt, registers [typedTraceRegisterCount]int64, materialize, disable bool) {
	if materialize {
		s.typed.trace.materialize(vm, registers, deopt.stackMap)
	}
	if disable {
		s.typed.disabled = true
		s.typed.guardFailures++
	}
	vm.prg = s.quickProgram
	vm.pc = deopt.pc
}

func lowerTypedIntLoopTrace(program *Program) *typedIntLoopTrace {
	return lowerTypedIntLoopTraceAt(program, -1)
}

func lowerTypedIntLoopTraceAt(program *Program, hotBackedgePC int) *typedIntLoopTrace {
	for entryPC := 0; entryPC+12 <= len(program.code); entryPC++ {
		counter, counterOK := program.code[entryPC].(loadStack)
		limit, limitOK := program.code[entryPC+1].(loadStackLex)
		_, lessOK := program.code[entryPC+2].(_op_lt)
		exit, exitOK := program.code[entryPC+3].(jneP)
		accumulator, accumulatorOK := program.code[entryPC+4].(loadStack)
		bodyCounter, bodyCounterOK := program.code[entryPC+5].(loadStack)
		_, addOK := program.code[entryPC+6].(_add)
		accumulatorStore, accumulatorStoreOK := program.code[entryPC+7].(storeStackP)
		updateCounter, updateCounterOK := program.code[entryPC+8].(loadStack)
		_, incrementOK := program.code[entryPC+9].(_inc)
		counterStore, counterStoreOK := program.code[entryPC+10].(storeStackP)
		backedge, backedgeOK := program.code[entryPC+11].(jump)
		if !counterOK || !limitOK || !lessOK || !exitOK || !accumulatorOK || !bodyCounterOK || !addOK ||
			!accumulatorStoreOK || !updateCounterOK || !incrementOK || !counterStoreOK || !backedgeOK {
			continue
		}
		if counter <= 0 || limit >= 0 || accumulator <= 0 || counter == accumulator ||
			int(counter) != int(bodyCounter) || int(counter) != int(updateCounter) ||
			int(counter) != int(counterStore) || int(accumulator) != int(accumulatorStore) ||
			entryPC+11+int(backedge) != entryPC || hotBackedgePC >= 0 && hotBackedgePC != entryPC+11 {
			continue
		}
		exitPC := entryPC + 3 + int(exit)
		if exit <= 0 || exitPC <= entryPC+11 || exitPC > len(program.code) {
			continue
		}

		counterSlot := typedTraceStackSlot{operand: int(counter)}
		limitSlot := typedTraceStackSlot{operand: int(limit), lexical: true}
		accumulatorSlot := typedTraceStackSlot{operand: int(accumulator)}
		stackMap := []typedTraceStackMapEntry{
			{register: typedTraceCounter, slot: counterSlot},
			{register: typedTraceLimit, slot: limitSlot},
			{register: typedTraceAccumulator, slot: accumulatorSlot},
		}
		return &typedIntLoopTrace{
			entryPC: entryPC,
			guards: []typedTraceGuard{
				{register: typedTraceCounter, slot: counterSlot},
				{register: typedTraceLimit, slot: limitSlot},
				{register: typedTraceAccumulator, slot: accumulatorSlot},
			},
			deopts: []typedTraceDeopt{{pc: entryPC, stackMap: stackMap}},
			code: []typedTraceIR{
				{opcode: typedTraceExitUnlessLess, left: typedTraceCounter, right: typedTraceLimit, exitPC: exitPC},
				{opcode: typedTraceGuardAddRange, left: typedTraceAccumulator, right: typedTraceCounter},
				{opcode: typedTraceGuardIncrementRange, left: typedTraceCounter},
				{opcode: typedTraceAdd, dst: typedTraceAccumulator, left: typedTraceAccumulator, right: typedTraceCounter},
				{opcode: typedTraceIncrement, dst: typedTraceCounter},
				{opcode: typedTracePollBackedge},
				{opcode: typedTraceJump, target: 0},
			},
		}
	}
	return nil
}
