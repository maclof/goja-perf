package goja

import "sync/atomic"

const (
	tieringTrackingThreshold = 8
	tieringBackedgeThreshold = 32
	// Float traces allocate a second Program and lowered metadata. Delay them
	// past the representative 64-iteration tier-up call, while admitting a
	// sustained loop after 64 additional quickened backedges.
	typedFloatTraceThreshold = 96
	maxTieringPrograms       = 256
)

// runtimeTiering owns all mutable tiering data for one Runtime. Shared Programs
// remain immutable and may execute in several runtimes concurrently.
type runtimeTiering struct {
	programs map[*Program]*programTierState
}

type programTierState struct {
	program        *Program
	primaryPC      int
	primaryCount   uint16
	primarySet     bool
	backedges      map[int]uint16
	quickProgram   *Program
	typed          *typedTraceTierState
	attempted      bool
	floatCandidate bool
	blockCount     int
}

func (t *runtimeTiering) lookup(program *Program) *programTierState {
	if program == nil || !program.hasBackedge {
		return nil
	}
	return t.programs[program]
}

func (t *runtimeTiering) startTracking(program *Program, pc int, count uint16) *programTierState {
	if state := t.lookup(program); state != nil {
		return state
	}
	if len(t.programs) >= maxTieringPrograms {
		return nil
	}
	if t.programs == nil {
		t.programs = make(map[*Program]*programTierState)
	}
	state := &programTierState{
		program:      program,
		primaryPC:    pc,
		primaryCount: count,
		primarySet:   true,
	}
	t.programs[program] = state
	return state
}

func (s *programTierState) recordBackedge(pc int) {
	if s.quickProgram != nil {
		if s.floatCandidate && pc == s.primaryPC {
			if s.primaryCount < typedFloatTraceThreshold {
				s.primaryCount++
			}
			if s.primaryCount == typedFloatTraceThreshold {
				s.floatCandidate = false
				if tracked, ok := s.quickProgram.code[pc].(quickenedTrackedJump); ok {
					s.quickProgram.code[pc] = quickenedJump(tracked)
				}
				if trace := lowerTypedFloatLoopTraceAt(s.program, pc); trace != nil {
					s.installTypedTrace(trace)
				}
			}
		}
		return
	}
	if s.attempted {
		return
	}
	if !s.primarySet {
		s.primaryPC = pc
		s.primarySet = true
	} else if s.primaryPC == pc {
		if s.primaryCount < tieringBackedgeThreshold {
			s.primaryCount++
		}
		if s.primaryCount == tieringBackedgeThreshold {
			s.quicken(pc)
		}
		return
	}
	if s.backedges == nil {
		s.backedges = make(map[int]uint16)
	}
	count := s.backedges[pc]
	if count < tieringBackedgeThreshold {
		count++
		s.backedges[pc] = count
	}
	if count == tieringBackedgeThreshold {
		s.quicken(pc)
	}
}

func (s *programTierState) quicken(hotBackedgePC int) {
	s.attempted = true
	floatCandidate := isTypedFloatLoopTraceAt(s.program, hotBackedgePC)
	trackedBackedgePC := -1
	if floatCandidate {
		trackedBackedgePC = hotBackedgePC
	}
	code, blocks := buildQuickenedCodeWithTrackedBackedge(s.program, trackedBackedgePC)
	if blocks == 0 {
		return
	}
	quickProgram := *s.program
	quickProgram.code = code
	s.quickProgram = &quickProgram
	s.blockCount = blocks
	if trace := lowerTypedIntegerLoopTraceAt(s.program, hotBackedgePC); trace != nil {
		s.installTypedTrace(trace)
	} else if floatCandidate {
		s.floatCandidate = true
		s.primaryPC = hotBackedgePC
		s.primaryCount = tieringBackedgeThreshold
		s.primarySet = true
	}
}

func (s *programTierState) installTypedTrace(trace *typedIntLoopTrace) {
	traceProgram := *s.quickProgram
	traceProgram.code = append([]instruction(nil), s.quickProgram.code...)
	traceProgram.code[trace.entryPC] = &typedTraceEntry{state: s}
	s.typed = &typedTraceTierState{trace: trace, program: &traceProgram}
}

func (s *programTierState) executableProgram() *Program {
	if s.typed != nil && !s.typed.disabled() {
		return s.typed.program
	}
	return s.quickProgram
}

func quickenedCanContinue(vm *vm) bool {
	return atomic.LoadUint32(&vm.interrupted) == 0 && !quickenedProfiling()
}

func quickenedProfiling() bool {
	return atomic.LoadInt32(&globalProfiler.enabled) == 1
}

type quickenedLoopCondition struct {
	left  loadStack
	right loadStackLex
}

func (q quickenedLoopCondition) exec(vm *vm) {
	q.left.exec(vm)
	if !quickenedCanContinue(vm) {
		return
	}
	q.right.exec(vm)
	if !quickenedCanContinue(vm) {
		return
	}
	op_lt.exec(vm)
}

type quickenedAddStoreStack struct {
	left, right loadStack
	dest        storeStackP
}

func (q quickenedAddStoreStack) exec(vm *vm) {
	q.left.exec(vm)
	if !quickenedCanContinue(vm) {
		return
	}
	q.right.exec(vm)
	if !quickenedCanContinue(vm) {
		return
	}
	add.exec(vm)
	if !quickenedCanContinue(vm) {
		return
	}
	q.dest.exec(vm)
}

type quickenedAddStoreStackLex struct {
	left  loadStack
	right loadStackLex
	dest  storeStackP
}

func (q quickenedAddStoreStackLex) exec(vm *vm) {
	q.left.exec(vm)
	if !quickenedCanContinue(vm) {
		return
	}
	q.right.exec(vm)
	if !quickenedCanContinue(vm) {
		return
	}
	add.exec(vm)
	if !quickenedCanContinue(vm) {
		return
	}
	q.dest.exec(vm)
}

type quickenedArithmeticStore struct {
	left       loadStack
	addend     loadVal
	multiplier loadVal
	dest       storeStackP
}

func (q quickenedArithmeticStore) exec(vm *vm) {
	q.left.exec(vm)
	if !quickenedCanContinue(vm) {
		return
	}
	q.addend.exec(vm)
	if !quickenedCanContinue(vm) {
		return
	}
	add.exec(vm)
	if !quickenedCanContinue(vm) {
		return
	}
	q.multiplier.exec(vm)
	if !quickenedCanContinue(vm) {
		return
	}
	mul.exec(vm)
	if !quickenedCanContinue(vm) {
		return
	}
	q.dest.exec(vm)
}

type quickenedIncStoreStack struct {
	value loadStack
	dest  storeStackP
}

func (q quickenedIncStoreStack) exec(vm *vm) {
	q.value.exec(vm)
	if !quickenedCanContinue(vm) {
		return
	}
	inc.exec(vm)
	if !quickenedCanContinue(vm) {
		return
	}
	q.dest.exec(vm)
}

type quickenedJump int32

func (j quickenedJump) exec(vm *vm) {
	vm.pc += int(j)
}

type quickenedTrackedJump int32

func (j quickenedTrackedJump) exec(vm *vm) {
	if state := vm.tier; state != nil && state.floatCandidate {
		state.recordBackedge(vm.pc)
	}
	vm.pc += int(j)
}

type quickenedJeqP int32

func (j quickenedJeqP) exec(vm *vm) {
	vm.sp--
	if vm.stack[vm.sp].ToBoolean() {
		vm.pc += int(j)
	} else {
		vm.pc++
	}
}

func buildQuickenedCode(program *Program) ([]instruction, int) {
	return buildQuickenedCodeWithTrackedBackedge(program, -1)
}

func buildQuickenedCodeWithTrackedBackedge(program *Program, trackedBackedgePC int) ([]instruction, int) {
	if program == nil || len(program.code) == 0 {
		return nil, 0
	}
	code := append([]instruction(nil), program.code...)
	blocks := 0
	for pc := 0; pc < len(program.code); {
		if pc+6 <= len(program.code) {
			left, leftOK := program.code[pc].(loadStack)
			addend, addendOK := program.code[pc+1].(loadVal)
			_, addOK := program.code[pc+2].(_add)
			multiplier, multiplierOK := program.code[pc+3].(loadVal)
			_, mulOK := program.code[pc+4].(_mul)
			dest, destOK := program.code[pc+5].(storeStackP)
			if leftOK && addendOK && addOK && multiplierOK && mulOK && destOK {
				code[pc] = quickenedArithmeticStore{left: left, addend: addend, multiplier: multiplier, dest: dest}
				blocks++
				pc += 6
				continue
			}
		}
		if pc+4 <= len(program.code) {
			left, leftOK := program.code[pc].(loadStack)
			dest, destOK := program.code[pc+3].(storeStackP)
			_, addOK := program.code[pc+2].(_add)
			if right, ok := program.code[pc+1].(loadStack); leftOK && ok && addOK && destOK {
				code[pc] = quickenedAddStoreStack{left: left, right: right, dest: dest}
				blocks++
				pc += 4
				continue
			}
			if right, ok := program.code[pc+1].(loadStackLex); leftOK && ok && addOK && destOK {
				code[pc] = quickenedAddStoreStackLex{left: left, right: right, dest: dest}
				blocks++
				pc += 4
				continue
			}
		}
		if pc+3 <= len(program.code) {
			if left, ok := program.code[pc].(loadStack); ok {
				if right, ok := program.code[pc+1].(loadStackLex); ok {
					if _, ok := program.code[pc+2].(_op_lt); ok {
						code[pc] = quickenedLoopCondition{left: left, right: right}
						blocks++
						pc += 3
						continue
					}
				}
				if _, ok := program.code[pc+1].(_inc); ok {
					if dest, ok := program.code[pc+2].(storeStackP); ok {
						code[pc] = quickenedIncStoreStack{value: left, dest: dest}
						blocks++
						pc += 3
						continue
					}
				}
			}
		}
		pc++
	}
	if blocks == 0 {
		return nil, 0
	}
	for pc, ins := range code {
		switch ins := ins.(type) {
		case jump:
			if pc == trackedBackedgePC {
				code[pc] = quickenedTrackedJump(ins)
			} else {
				code[pc] = quickenedJump(ins)
			}
		case jeqP:
			code[pc] = quickenedJeqP(ins)
		}
	}
	return code, blocks
}

func (vm *vm) setProgram(program *Program) {
	vm.prg = program
	vm.tier = vm.tiering.lookup(program)
	if vm.tier != nil && vm.tier.quickProgram != nil && !quickenedProfiling() {
		vm.prg = vm.tier.executableProgram()
	}
}

func (vm *vm) recordBackedge(pc int) {
	state := vm.tier
	base := vm.prg
	if state != nil && (state.quickProgram == base || state.typed != nil && state.typed.program == base) {
		base = state.program
	}
	if state == nil {
		if vm.tierCandidateProgram == base && vm.tierCandidatePC == int32(pc) {
			if vm.tierCandidateCount < tieringTrackingThreshold {
				vm.tierCandidateCount++
			}
		} else {
			vm.tierCandidateProgram = base
			vm.tierCandidatePC = int32(pc)
			vm.tierCandidateCount = 1
		}
		if vm.tierCandidateCount < tieringTrackingThreshold {
			return
		}
		state = vm.tiering.startTracking(base, pc, uint16(vm.tierCandidateCount))
		vm.tier = state
		vm.tierCandidateProgram = nil
		vm.tierCandidateCount = 0
		if state == nil {
			return
		}
	} else if state.program != base {
		// A nested call may have changed the active tier without a context
		// restore. Rebind only on this uncommon mismatch path.
		state = vm.tiering.lookup(base)
		vm.tier = state
		if state == nil {
			return
		}
	}
	state.recordBackedge(pc)
	if state.quickProgram != nil && !quickenedProfiling() {
		vm.prg = state.executableProgram()
	}
}
