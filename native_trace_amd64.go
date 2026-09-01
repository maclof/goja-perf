//go:build (windows || linux) && amd64

package goja

import (
	"encoding/binary"
	"errors"
	"runtime"
	"sync/atomic"
	"unsafe"
)

const nativeTraceIterationBudget = 16 * 1024

type nativeTraceExit uint32

const (
	nativeTraceExitNormal nativeTraceExit = iota
	nativeTraceExitGuard
	nativeTraceExitInterrupt
	nativeTraceExitProfiler
	nativeTraceExitYield
	nativeTraceExitError
)

// nativeTraceFrame is the private ABI shared by Go and generated code. The
// assembly trampoline passes its address in R10. Generated code may mutate only
// counter, accumulator, and budget; all VM stack materialisation stays in Go.
// Pointer fields are real Go pointers, are never retained by generated code,
// and remain visible to the garbage collector for the synchronous call.
type nativeTraceFrame struct {
	counter     int64
	limit       int64
	accumulator int64
	budget      uint64
	interrupt   *uint32
	profiler    *int32
}

const (
	nativeTraceCounterOffset     = 0
	nativeTraceLimitOffset       = 8
	nativeTraceAccumulatorOffset = 16
	nativeTraceBudgetOffset      = 24
	nativeTraceInterruptOffset   = 32
	nativeTraceProfilerOffset    = 40
)

type nativeTraceCode struct {
	memory *nativeExecutableMemory
	yields atomic.Uint32
}

//go:noescape
func runNativeTrace(code uintptr, frame *nativeTraceFrame) nativeTraceExit

//go:noescape
func copyNativeTraceBytes(dst uintptr, src *byte, size uintptr)

//go:noescape
func readNativeTraceBytes(dst *byte, src uintptr, size uintptr)

func compileNativeTrace(trace *typedIntLoopTrace) (*nativeTraceCode, error) {
	shape, supported := nativeTraceIRLoopShape(trace)
	if !supported {
		return nil, nil
	}
	if err := checkNativeTraceFrameLayout(); err != nil {
		return nil, err
	}
	code, err := emitNativeTraceAMD64(shape)
	if err != nil {
		return nil, err
	}
	memory, err := allocateNativeExecutable(code)
	if err != nil {
		return nil, err
	}
	return &nativeTraceCode{memory: memory}, nil
}

func nativeTraceIRSupported(trace *typedIntLoopTrace) bool {
	_, supported := nativeTraceIRLoopShape(trace)
	return supported
}

func nativeTraceIRLoopShape(trace *typedIntLoopTrace) (shape typedTraceLoopShape, supported bool) {
	if trace == nil || len(trace.guards) != 3 || len(trace.deopts) != 1 || len(trace.code) != 7 {
		return shape, false
	}
	var guarded, mapped [typedTraceRegisterCount]bool
	for _, guard := range trace.guards {
		if guard.register >= typedTraceRegisterCount || guard.kind != typedTraceValueInt || guard.deopt != 0 || guarded[guard.register] {
			return shape, false
		}
		guarded[guard.register] = true
	}
	if len(trace.deopts[0].stackMap) != int(typedTraceRegisterCount) {
		return shape, false
	}
	for _, entry := range trace.deopts[0].stackMap {
		if entry.register >= typedTraceRegisterCount || mapped[entry.register] {
			return shape, false
		}
		mapped[entry.register] = true
	}
	code := trace.code
	common := code[0].left == typedTraceCounter && code[0].right == typedTraceLimit && code[0].deopt == 0 &&
		code[1].opcode == typedTraceGuardAddRange && code[1].left == typedTraceAccumulator && code[1].right == typedTraceCounter && code[1].deopt == 0 &&
		code[3].opcode == typedTraceAdd && code[3].dst == typedTraceAccumulator && code[3].left == typedTraceAccumulator && code[3].right == typedTraceCounter &&
		code[5].opcode == typedTracePollBackedge && code[5].deopt == 0 &&
		code[6].opcode == typedTraceJump && code[6].target == 0
	if !common {
		return shape, false
	}
	if code[0].opcode == typedTraceExitUnlessLess &&
		code[2].opcode == typedTraceGuardIncrementRange && code[2].left == typedTraceCounter && code[2].deopt == 0 &&
		code[4].opcode == typedTraceIncrement && code[4].dst == typedTraceCounter {
		return typedTraceLoopShape{direction: typedTraceLoopAscending, comparison: typedTraceLoopExclusive, step: 1}, true
	}
	if code[0].opcode == typedTraceExitUnlessLessOrEqual &&
		code[2].opcode == typedTraceGuardIncrementRange && code[2].left == typedTraceCounter && code[2].deopt == 0 &&
		code[4].opcode == typedTraceIncrement && code[4].dst == typedTraceCounter {
		return typedTraceLoopShape{direction: typedTraceLoopAscending, comparison: typedTraceLoopInclusive, step: 1}, true
	}
	if code[0].opcode == typedTraceExitUnlessGreater &&
		code[2].opcode == typedTraceGuardDecrementRange && code[2].left == typedTraceCounter && code[2].deopt == 0 &&
		code[4].opcode == typedTraceDecrement && code[4].dst == typedTraceCounter {
		return typedTraceLoopShape{direction: typedTraceLoopDescending, comparison: typedTraceLoopExclusive, step: -1}, true
	}
	if code[2].opcode == typedTraceGuardAddImmediateRange && code[2].left == typedTraceCounter && code[2].deopt == 0 &&
		code[4].opcode == typedTraceAddImmediate && code[4].dst == typedTraceCounter && code[2].exitPC == code[4].exitPC {
		step := int64(code[4].exitPC)
		if step < -1<<31 || step > 1<<31-1 || step >= -1 && step <= 1 {
			return shape, false
		}
		switch {
		case step > 0 && code[0].opcode == typedTraceExitUnlessLess:
			return typedTraceLoopShape{direction: typedTraceLoopAscending, comparison: typedTraceLoopExclusive, step: step}, true
		case step > 0 && code[0].opcode == typedTraceExitUnlessLessOrEqual:
			return typedTraceLoopShape{direction: typedTraceLoopAscending, comparison: typedTraceLoopInclusive, step: step}, true
		case step < 0 && code[0].opcode == typedTraceExitUnlessGreater:
			return typedTraceLoopShape{direction: typedTraceLoopDescending, comparison: typedTraceLoopExclusive, step: step}, true
		case step < 0 && code[0].opcode == typedTraceExitUnlessGreaterOrEqual:
			return typedTraceLoopShape{direction: typedTraceLoopDescending, comparison: typedTraceLoopInclusive, step: step}, true
		}
	}
	return shape, false
}

func checkNativeTraceFrameLayout() error {
	var frame nativeTraceFrame
	if unsafe.Offsetof(frame.counter) != nativeTraceCounterOffset ||
		unsafe.Offsetof(frame.limit) != nativeTraceLimitOffset ||
		unsafe.Offsetof(frame.accumulator) != nativeTraceAccumulatorOffset ||
		unsafe.Offsetof(frame.budget) != nativeTraceBudgetOffset ||
		unsafe.Offsetof(frame.interrupt) != nativeTraceInterruptOffset ||
		unsafe.Offsetof(frame.profiler) != nativeTraceProfilerOffset {
		return errors.New("native trace frame layout does not match private ABI")
	}
	return nil
}

func (n *nativeTraceCode) run(frame *nativeTraceFrame) nativeTraceExit {
	if n == nil || n.memory == nil || frame == nil {
		return nativeTraceExitError
	}
	address := n.memory.address.Load()
	if address == 0 || frame.interrupt == nil || frame.profiler == nil || frame.budget == 0 {
		return nativeTraceExitError
	}
	exit := runNativeTrace(address, frame)
	runtime.KeepAlive(frame)
	runtime.KeepAlive(n.memory)
	runtime.KeepAlive(n)
	return exit
}

func (t *typedIntLoopTrace) executeNative(vm *vm, state *programTierState, entry *typedTraceEntry, native *nativeTraceCode, registers [typedTraceRegisterCount]int64) {
	frame := nativeTraceFrame{
		counter:     registers[typedTraceCounter],
		limit:       registers[typedTraceLimit],
		accumulator: registers[typedTraceAccumulator],
		interrupt:   &vm.interrupted,
		profiler:    &globalProfiler.enabled,
	}
	deopt := t.deopts[0]
	for {
		frame.budget = nativeTraceIterationBudget
		exit := native.run(&frame)
		registers[typedTraceCounter] = frame.counter
		registers[typedTraceAccumulator] = frame.accumulator
		switch exit {
		case nativeTraceExitNormal:
			t.materialize(vm, registers, deopt.stackMap)
			vm.pc = t.code[0].exitPC
			return
		case nativeTraceExitGuard:
			state.deoptTypedTrace(vm, deopt, registers, true, true)
			return
		case nativeTraceExitInterrupt, nativeTraceExitProfiler:
			state.deoptTypedTrace(vm, deopt, registers, true, false)
			return
		case nativeTraceExitYield:
			native.yields.Add(1)
			runtime.Gosched()
		default:
			// A backend/ABI failure is never allowed to affect JS semantics.
			// Permanently retain the typed-Go executor for this Runtime state.
			entry.native = nil
			t.executeGo(vm, state, registers)
			return
		}
	}
}

type nativeTraceLabel uint8

const (
	nativeTraceLoop nativeTraceLabel = iota
	nativeTraceNormal
	nativeTraceGuard
	nativeTraceInterrupt
	nativeTraceProfiler
	nativeTraceYield
	nativeTraceStoreAndReturn
	nativeTraceLabelCount
)

type nativeTraceFixup struct {
	offset int
	label  nativeTraceLabel
}

type nativeTraceEmitter struct {
	code   []byte
	labels [nativeTraceLabelCount]int
	fixups []nativeTraceFixup
}

func newNativeTraceEmitter() *nativeTraceEmitter {
	emitter := &nativeTraceEmitter{}
	for i := range emitter.labels {
		emitter.labels[i] = -1
	}
	return emitter
}

func (e *nativeTraceEmitter) emit(bytes ...byte) {
	e.code = append(e.code, bytes...)
}

func (e *nativeTraceEmitter) label(label nativeTraceLabel) {
	e.labels[label] = len(e.code)
}

func (e *nativeTraceEmitter) jump(opcode []byte, label nativeTraceLabel) {
	e.emit(opcode...)
	offset := len(e.code)
	e.emit(0, 0, 0, 0)
	e.fixups = append(e.fixups, nativeTraceFixup{offset: offset, label: label})
}

func (e *nativeTraceEmitter) finish() ([]byte, error) {
	for _, fixup := range e.fixups {
		target := e.labels[fixup.label]
		if target < 0 {
			return nil, errors.New("unresolved native trace label")
		}
		displacement := int64(target - (fixup.offset + 4))
		if displacement < -1<<31 || displacement > 1<<31-1 {
			return nil, errors.New("native trace branch exceeds rel32 range")
		}
		binary.LittleEndian.PutUint32(e.code[fixup.offset:], uint32(int32(displacement)))
	}
	return e.code, nil
}

func emitNativeTraceAMD64(shape typedTraceLoopShape) ([]byte, error) {
	if shape.step == 0 || shape.direction == typedTraceLoopAscending && shape.step < 0 ||
		shape.direction == typedTraceLoopDescending && shape.step > 0 || shape.step < -1<<31 || shape.step > 1<<31-1 {
		return nil, errors.New("unsupported native trace induction step")
	}
	e := newNativeTraceEmitter()
	// R10 is the frame pointer. RAX=counter, RDX=accumulator, R8=budget,
	// R11=maxInt, RCX=-maxInt, and R9 is the sole scratch register. These
	// registers are volatile in both the Windows and System V AMD64 ABIs.
	e.emit(0x49, 0x8b, 0x02)                                           // MOV RAX, [R10+counter]
	e.emit(0x49, 0x8b, 0x52, 0x10)                                     // MOV RDX, [R10+accumulator]
	e.emit(0x4d, 0x8b, 0x42, 0x18)                                     // MOV R8, [R10+budget]
	e.emit(0x49, 0xbb, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x1f, 0x00) // MOV R11, maxInt
	e.emit(0x48, 0xb9, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0xe0, 0xff) // MOV RCX, -maxInt

	e.label(nativeTraceLoop)
	e.emit(0x49, 0x3b, 0x42, 0x08) // CMP RAX, [R10+limit]
	switch {
	case shape.direction == typedTraceLoopDescending && shape.comparison == typedTraceLoopExclusive:
		e.jump([]byte{0x0f, 0x8e}, nativeTraceNormal) // JLE
	case shape.direction == typedTraceLoopDescending && shape.comparison == typedTraceLoopInclusive:
		e.jump([]byte{0x0f, 0x8c}, nativeTraceNormal) // JL
	case shape.direction == typedTraceLoopAscending && shape.comparison == typedTraceLoopInclusive:
		e.jump([]byte{0x0f, 0x8f}, nativeTraceNormal) // JG
	case shape.direction == typedTraceLoopAscending && shape.comparison == typedTraceLoopExclusive:
		e.jump([]byte{0x0f, 0x8d}, nativeTraceNormal) // JGE
	default:
		return nil, errors.New("unsupported native trace loop shape")
	}
	if shape.step == 1 || shape.step == -1 {
		e.emit(0x49, 0x89, 0xd1) // MOV R9, RDX
		e.emit(0x49, 0x01, 0xc1) // ADD R9, RAX
		e.emit(0x4d, 0x39, 0xd9) // CMP R9, R11
		e.jump([]byte{0x0f, 0x8f}, nativeTraceGuard)
		e.emit(0x49, 0x39, 0xc9) // CMP R9, RCX
		e.jump([]byte{0x0f, 0x8c}, nativeTraceGuard)
		if shape.direction == typedTraceLoopDescending {
			e.emit(0x48, 0x39, 0xc8) // CMP RAX, RCX
			e.jump([]byte{0x0f, 0x8e}, nativeTraceGuard)
		} else {
			e.emit(0x4c, 0x39, 0xd8) // CMP RAX, R11
			e.jump([]byte{0x0f, 0x8d}, nativeTraceGuard)
		}
		e.emit(0x4c, 0x89, 0xca) // MOV RDX, R9
		if shape.direction == typedTraceLoopDescending {
			e.emit(0x48, 0xff, 0xc8) // DEC RAX
		} else {
			e.emit(0x48, 0xff, 0xc0) // INC RAX
		}
	} else {
		// Validate counter+step without committing it so a guard exit retains
		// the loop-header state required by deoptimisation.
		e.emit(0x49, 0x89, 0xc1) // MOV R9, RAX
		e.emit(0x49, 0x81, 0xc1) // ADD R9, imm32
		var immediate [4]byte
		binary.LittleEndian.PutUint32(immediate[:], uint32(int32(shape.step)))
		e.emit(immediate[:]...)
		e.emit(0x4d, 0x39, 0xd9) // CMP R9, R11
		e.jump([]byte{0x0f, 0x8f}, nativeTraceGuard)
		e.emit(0x49, 0x39, 0xc9) // CMP R9, RCX
		e.jump([]byte{0x0f, 0x8c}, nativeTraceGuard)

		e.emit(0x49, 0x89, 0xd1) // MOV R9, RDX
		e.emit(0x49, 0x01, 0xc1) // ADD R9, RAX
		e.emit(0x4d, 0x39, 0xd9) // CMP R9, R11
		e.jump([]byte{0x0f, 0x8f}, nativeTraceGuard)
		e.emit(0x49, 0x39, 0xc9) // CMP R9, RCX
		e.jump([]byte{0x0f, 0x8c}, nativeTraceGuard)
		e.emit(0x4c, 0x89, 0xca) // MOV RDX, R9
		e.emit(0x48, 0x05)       // ADD RAX, imm32
		e.emit(immediate[:]...)
	}
	e.emit(0x4d, 0x8b, 0x4a, 0x20) // MOV R9, [R10+interrupt]
	e.emit(0x41, 0x83, 0x39, 0x00) // CMP DWORD PTR [R9], 0
	e.jump([]byte{0x0f, 0x85}, nativeTraceInterrupt)
	e.emit(0x4d, 0x8b, 0x4a, 0x28) // MOV R9, [R10+profiler]
	e.emit(0x41, 0x83, 0x39, 0x01) // CMP DWORD PTR [R9], 1
	e.jump([]byte{0x0f, 0x84}, nativeTraceProfiler)
	e.emit(0x49, 0xff, 0xc8) // DEC R8
	e.jump([]byte{0x0f, 0x84}, nativeTraceYield)
	e.jump([]byte{0xe9}, nativeTraceLoop)

	for _, exit := range []struct {
		label  nativeTraceLabel
		reason nativeTraceExit
	}{
		{nativeTraceNormal, nativeTraceExitNormal},
		{nativeTraceGuard, nativeTraceExitGuard},
		{nativeTraceInterrupt, nativeTraceExitInterrupt},
		{nativeTraceProfiler, nativeTraceExitProfiler},
		{nativeTraceYield, nativeTraceExitYield},
	} {
		e.label(exit.label)
		e.emit(0x41, 0xb9) // MOV R9D, exit reason; preserve RAX counter
		var reason [4]byte
		binary.LittleEndian.PutUint32(reason[:], uint32(exit.reason))
		e.emit(reason[:]...)
		e.jump([]byte{0xe9}, nativeTraceStoreAndReturn)
	}

	e.label(nativeTraceStoreAndReturn)
	e.emit(0x49, 0x89, 0x02)       // MOV [R10+counter], RAX
	e.emit(0x49, 0x89, 0x52, 0x10) // MOV [R10+accumulator], RDX
	e.emit(0x4d, 0x89, 0x42, 0x18) // MOV [R10+budget], R8
	e.emit(0x44, 0x89, 0xc8)       // MOV EAX, R9D
	e.emit(0xc3)                   // RET
	return e.finish()
}
