package goja

import (
	"math"
	"sync/atomic"
)

const (
	// Native compilation costs approximately 7.44us on the reference machine,
	// while native execution saves approximately 8.59ns per eligible loop
	// iteration. Requiring 896 remaining iterations puts the current invocation
	// beyond that measured break-even; 4096 cumulative iterations additionally
	// require sustained or substantially long-running work before admission.
	nativeTraceActivationIterations   uint64 = 4096
	nativeTraceMinRemainingIterations int64  = 896
)

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
	kind     typedTraceValueKind
	deopt    uint8
}

type typedTraceStackMapEntry struct {
	register typedTraceRegister
	slot     typedTraceStackSlot
}

type typedTraceValueKind uint8

const (
	typedTraceValueInt typedTraceValueKind = iota
	typedTraceValueFloat
)

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
	typedTraceFloatAddLiteral
	typedTraceFloatMultiplyLiteral
	typedTraceFloatLiteralHigh
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
	activation    uint32
	guardFailures uint32
}

type typedTraceEntry struct {
	state  *programTierState
	native *nativeTraceCode
}

func (e *typedTraceEntry) exec(vm *vm) {
	e.state.typed.trace.execute(vm, e.state, e)
}

func (t *typedIntLoopTrace) execute(vm *vm, state *programTierState, entry *typedTraceEntry) {
	var registers [typedTraceRegisterCount]int64
	for _, guard := range t.guards {
		idx, exists := guard.slot.index(vm)
		if !exists {
			state.deoptTypedTrace(vm, t.deopts[guard.deopt], registers, false, true)
			return
		}
		switch guard.kind {
		case typedTraceValueInt:
			value, ok := vm.stack[idx].(valueInt)
			if !ok {
				state.deoptTypedTrace(vm, t.deopts[guard.deopt], registers, false, true)
				return
			}
			registers[guard.register] = int64(value)
		case typedTraceValueFloat:
			value, ok := vm.stack[idx].(valueFloat)
			if !ok {
				state.deoptTypedTrace(vm, t.deopts[guard.deopt], registers, false, true)
				return
			}
			registers[guard.register] = int64(math.Float64bits(float64(value)))
		default:
			state.deoptTypedTrace(vm, t.deopts[guard.deopt], registers, false, true)
			return
		}
	}
	if entry.native == nil {
		entry.native = state.typed.observeNativeEligibility(registers[typedTraceLimit]-registers[typedTraceCounter], compileNativeTrace)
	}
	if native := entry.native; native != nil {
		t.executeNative(vm, state, entry, native, registers)
		return
	}
	t.executeGo(vm, state, registers)
}

type nativeTraceCompiler func(*typedIntLoopTrace) (*nativeTraceCode, error)

const (
	nativeTraceEligibleMask  uint32 = 1<<13 - 1
	nativeTraceAttemptedFlag uint32 = 1 << 13
	nativeTraceDisabledFlag  uint32 = 1 << 14
)

func (s *typedTraceTierState) nativeEligibleIterations() uint64 {
	return uint64(s.activation & nativeTraceEligibleMask)
}

func (s *typedTraceTierState) nativeAttempted() bool {
	return s.activation&nativeTraceAttemptedFlag != 0
}

func (s *typedTraceTierState) disabled() bool {
	return s.activation&nativeTraceDisabledFlag != 0
}

func (s *typedTraceTierState) nativeCode() *nativeTraceCode {
	if s == nil || s.program == nil || s.trace == nil || s.trace.entryPC < 0 || s.trace.entryPC >= len(s.program.code) {
		return nil
	}
	entry, _ := s.program.code[s.trace.entryPC].(*typedTraceEntry)
	if entry == nil {
		return nil
	}
	return entry.native
}

func (s *typedTraceTierState) observeNativeEligibility(remaining int64, compile nativeTraceCompiler) *nativeTraceCode {
	if s == nil || s.disabled() || s.nativeAttempted() || remaining <= 0 {
		return nil
	}
	current := s.nativeEligibleIterations()
	eligible := uint64(remaining)
	if eligible >= nativeTraceActivationIterations-current {
		current = nativeTraceActivationIterations
	} else {
		current += eligible
	}
	s.activation = s.activation&^nativeTraceEligibleMask | uint32(current)
	if current < nativeTraceActivationIterations || remaining < nativeTraceMinRemainingIterations {
		return nil
	}
	s.activation |= nativeTraceAttemptedFlag
	native, err := compile(s.trace)
	if err == nil {
		return native
	}
	return nil
}

func (t *typedIntLoopTrace) executeGo(vm *vm, state *programTierState, registers [typedTraceRegisterCount]int64) {
	if addend, multiplier, ok := t.floatArithmeticConstants(); ok {
		t.executeFloatGo(vm, state, registers, addend, multiplier)
		return
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
		case typedTraceFloatAddLiteral:
			ip++
			literalBits := uint64(uint32(operation.exitPC)) | uint64(uint32(t.code[ip].exitPC))<<32
			value := math.Float64frombits(uint64(registers[operation.left]))
			registers[operation.dst] = int64(math.Float64bits(value + math.Float64frombits(literalBits)))
		case typedTraceFloatMultiplyLiteral:
			ip++
			literalBits := uint64(uint32(operation.exitPC)) | uint64(uint32(t.code[ip].exitPC))<<32
			value := math.Float64frombits(uint64(registers[operation.left]))
			registers[operation.dst] = int64(math.Float64bits(value * math.Float64frombits(literalBits)))
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

func (t *typedIntLoopTrace) floatArithmeticConstants() (float64, float64, bool) {
	if len(t.code) != 9 || t.code[0].opcode != typedTraceExitUnlessLess ||
		t.code[1].opcode != typedTraceGuardIncrementRange ||
		t.code[2].opcode != typedTraceFloatAddLiteral || t.code[3].opcode != typedTraceFloatLiteralHigh ||
		t.code[4].opcode != typedTraceFloatMultiplyLiteral || t.code[5].opcode != typedTraceFloatLiteralHigh ||
		t.code[6].opcode != typedTraceIncrement || t.code[7].opcode != typedTracePollBackedge ||
		t.code[8].opcode != typedTraceJump {
		return 0, 0, false
	}
	addendBits := uint64(uint32(t.code[2].exitPC)) | uint64(uint32(t.code[3].exitPC))<<32
	multiplierBits := uint64(uint32(t.code[4].exitPC)) | uint64(uint32(t.code[5].exitPC))<<32
	return math.Float64frombits(addendBits), math.Float64frombits(multiplierBits), true
}

func (t *typedIntLoopTrace) executeFloatGo(vm *vm, state *programTierState, registers [typedTraceRegisterCount]int64, addend, multiplier float64) {
	counter := registers[typedTraceCounter]
	limit := registers[typedTraceLimit]
	accumulator := math.Float64frombits(uint64(registers[typedTraceAccumulator]))
	deopt := t.deopts[t.code[0].deopt]
	for counter < limit {
		if counter >= maxInt {
			registers[typedTraceCounter] = counter
			registers[typedTraceAccumulator] = int64(math.Float64bits(accumulator))
			state.deoptTypedTrace(vm, deopt, registers, true, true)
			return
		}
		// Keep the operations separate: JavaScript observes IEEE-754 rounding
		// after both the addition and multiplication.
		accumulator += addend
		accumulator *= multiplier
		counter++
		if atomic.LoadUint32(&vm.interrupted) != 0 || quickenedProfiling() {
			registers[typedTraceCounter] = counter
			registers[typedTraceAccumulator] = int64(math.Float64bits(accumulator))
			state.deoptTypedTrace(vm, deopt, registers, true, false)
			return
		}
	}
	registers[typedTraceCounter] = counter
	registers[typedTraceAccumulator] = int64(math.Float64bits(accumulator))
	t.materialize(vm, registers, deopt.stackMap)
	vm.pc = t.code[0].exitPC
}

func (t *typedIntLoopTrace) materialize(vm *vm, registers [typedTraceRegisterCount]int64, stackMap []typedTraceStackMapEntry) {
	for _, entry := range stackMap {
		idx, exists := entry.slot.index(vm)
		if exists {
			kind := typedTraceValueInt
			for _, guard := range t.guards {
				if guard.register == entry.register {
					kind = guard.kind
					break
				}
			}
			switch kind {
			case typedTraceValueInt:
				vm.stack[idx] = valueInt(registers[entry.register])
			case typedTraceValueFloat:
				vm.stack[idx] = floatToValue(math.Float64frombits(uint64(registers[entry.register])))
			}
		}
	}
}

func (s *programTierState) deoptTypedTrace(vm *vm, deopt typedTraceDeopt, registers [typedTraceRegisterCount]int64, materialize, disable bool) {
	if materialize {
		s.typed.trace.materialize(vm, registers, deopt.stackMap)
	}
	if disable {
		s.typed.activation |= nativeTraceDisabledFlag
		s.typed.guardFailures++
	}
	vm.prg = s.quickProgram
	vm.pc = deopt.pc
}

func lowerTypedIntLoopTrace(program *Program) *typedIntLoopTrace {
	return lowerTypedIntLoopTraceAt(program, -1)
}

func lowerTypedIntLoopTraceAt(program *Program, hotBackedgePC int) *typedIntLoopTrace {
	if trace := lowerTypedIntegerLoopTraceAt(program, hotBackedgePC); trace != nil {
		return trace
	}
	return lowerTypedFloatLoopTraceAt(program, hotBackedgePC)
}

func lowerTypedIntegerLoopTraceAt(program *Program, hotBackedgePC int) *typedIntLoopTrace {
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

type typedFloatLoopMatch struct {
	entryPC, exitPC    int
	counter            loadStack
	limit              loadStackLex
	accumulator        loadStack
	addend, multiplier valueFloat
}

func findTypedFloatLoopTrace(program *Program, hotBackedgePC int) (typedFloatLoopMatch, bool) {
	for entryPC := 0; entryPC+14 <= len(program.code); entryPC++ {
		counter, counterOK := program.code[entryPC].(loadStack)
		limit, limitOK := program.code[entryPC+1].(loadStackLex)
		_, lessOK := program.code[entryPC+2].(_op_lt)
		exit, exitOK := program.code[entryPC+3].(jneP)
		accumulator, accumulatorOK := program.code[entryPC+4].(loadStack)
		addend, addendOK := program.code[entryPC+5].(loadVal)
		_, addOK := program.code[entryPC+6].(_add)
		multiplier, multiplierOK := program.code[entryPC+7].(loadVal)
		_, multiplyOK := program.code[entryPC+8].(_mul)
		accumulatorStore, accumulatorStoreOK := program.code[entryPC+9].(storeStackP)
		updateCounter, updateCounterOK := program.code[entryPC+10].(loadStack)
		_, incrementOK := program.code[entryPC+11].(_inc)
		counterStore, counterStoreOK := program.code[entryPC+12].(storeStackP)
		backedge, backedgeOK := program.code[entryPC+13].(jump)
		addendFloat, addendFloatOK := addend.v.(valueFloat)
		multiplierFloat, multiplierFloatOK := multiplier.v.(valueFloat)
		if !counterOK || !limitOK || !lessOK || !exitOK || !accumulatorOK || !addendOK || !addOK ||
			!multiplierOK || !multiplyOK || !accumulatorStoreOK || !updateCounterOK || !incrementOK || !counterStoreOK || !backedgeOK ||
			!addendFloatOK || !multiplierFloatOK {
			continue
		}
		if counter <= 0 || limit >= 0 || accumulator <= 0 || counter == accumulator ||
			int(counter) != int(updateCounter) || int(counter) != int(counterStore) ||
			int(accumulator) != int(accumulatorStore) ||
			entryPC+13+int(backedge) != entryPC || hotBackedgePC >= 0 && hotBackedgePC != entryPC+13 {
			continue
		}
		exitPC := entryPC + 3 + int(exit)
		if exit <= 0 || exitPC <= entryPC+13 || exitPC > len(program.code) {
			continue
		}
		return typedFloatLoopMatch{
			entryPC: entryPC, exitPC: exitPC,
			counter: counter, limit: limit, accumulator: accumulator,
			addend: addendFloat, multiplier: multiplierFloat,
		}, true
	}
	return typedFloatLoopMatch{}, false
}

func isTypedFloatLoopTraceAt(program *Program, hotBackedgePC int) bool {
	_, ok := findTypedFloatLoopTrace(program, hotBackedgePC)
	return ok
}

func lowerTypedFloatLoopTraceAt(program *Program, hotBackedgePC int) *typedIntLoopTrace {
	match, ok := findTypedFloatLoopTrace(program, hotBackedgePC)
	if !ok {
		return nil
	}
	counterSlot := typedTraceStackSlot{operand: int(match.counter)}
	limitSlot := typedTraceStackSlot{operand: int(match.limit), lexical: true}
	accumulatorSlot := typedTraceStackSlot{operand: int(match.accumulator)}
	stackMap := []typedTraceStackMapEntry{
		{register: typedTraceCounter, slot: counterSlot},
		{register: typedTraceLimit, slot: limitSlot},
		{register: typedTraceAccumulator, slot: accumulatorSlot},
	}
	addendBits := math.Float64bits(float64(match.addend))
	multiplierBits := math.Float64bits(float64(match.multiplier))
	return &typedIntLoopTrace{
		entryPC: match.entryPC,
		guards: []typedTraceGuard{
			{register: typedTraceCounter, slot: counterSlot},
			{register: typedTraceLimit, slot: limitSlot},
			{register: typedTraceAccumulator, slot: accumulatorSlot, kind: typedTraceValueFloat},
		},
		deopts: []typedTraceDeopt{{pc: match.entryPC, stackMap: stackMap}},
		code: []typedTraceIR{
			{opcode: typedTraceExitUnlessLess, left: typedTraceCounter, right: typedTraceLimit, exitPC: match.exitPC},
			{opcode: typedTraceGuardIncrementRange, left: typedTraceCounter},
			{opcode: typedTraceFloatAddLiteral, dst: typedTraceAccumulator, left: typedTraceAccumulator, exitPC: int(uint32(addendBits))},
			{opcode: typedTraceFloatLiteralHigh, exitPC: int(uint32(addendBits >> 32))},
			{opcode: typedTraceFloatMultiplyLiteral, dst: typedTraceAccumulator, left: typedTraceAccumulator, exitPC: int(uint32(multiplierBits))},
			{opcode: typedTraceFloatLiteralHigh, exitPC: int(uint32(multiplierBits >> 32))},
			{opcode: typedTraceIncrement, dst: typedTraceCounter},
			{opcode: typedTracePollBackedge},
			{opcode: typedTraceJump, target: 0},
		},
	}
}
