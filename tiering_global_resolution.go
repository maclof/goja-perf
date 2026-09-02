package goja

import "github.com/maclof/goja-perf/unistring"

// quickenedResolveVar1 preserves dynamic environment resolution but reuses the
// immutable global reference object on the common miss-through-stashes path.
// The instruction lives only in a Runtime-owned quick Program, never in the
// shared immutable Program.
type quickenedResolveVar1 struct {
	name      unistring.String
	globalRef *objStrRef
}

func newQuickenedResolveVar1(name resolveVar1) *quickenedResolveVar1 {
	return &quickenedResolveVar1{name: unistring.String(name)}
}

func (q *quickenedResolveVar1) exec(vm *vm) {
	for stash := vm.stash; stash != nil; stash = stash.outer {
		if ref := stash.getRefByName(q.name, false); ref != nil {
			vm.refStack = append(vm.refStack, ref)
			vm.pc++
			return
		}
	}
	bindQuickenedGlobal(&q.globalRef, vm.r.globalObject, q.name, false)
	vm.refStack = append(vm.refStack, q.globalRef)
	vm.pc++
}

type quickenedResolveVar1Strict struct {
	name          unistring.String
	globalRef     *objStrRef
	unresolvedRef *unresolvedRef
}

func newQuickenedResolveVar1Strict(name resolveVar1Strict) *quickenedResolveVar1Strict {
	return &quickenedResolveVar1Strict{name: unistring.String(name)}
}

func bindQuickenedGlobal(ref **objStrRef, base *Object, name unistring.String, strict bool) {
	if *ref == nil || (*ref).base != base {
		// Allocate a new immutable reference when SetGlobalObject changes the
		// base. An older reference may still be live across RHS evaluation.
		*ref = &objStrRef{base: base, name: name, binding: true, strict: strict}
	}
}

func (q *quickenedResolveVar1Strict) exec(vm *vm) {
	for stash := vm.stash; stash != nil; stash = stash.outer {
		if ref := stash.getRefByName(q.name, true); ref != nil {
			vm.refStack = append(vm.refStack, ref)
			vm.pc++
			return
		}
	}
	if vm.r.globalObject.self.hasPropertyStr(q.name) {
		bindQuickenedGlobal(&q.globalRef, vm.r.globalObject, q.name, true)
		vm.refStack = append(vm.refStack, q.globalRef)
	} else {
		if q.unresolvedRef == nil || q.unresolvedRef.runtime != vm.r {
			q.unresolvedRef = &unresolvedRef{runtime: vm.r, name: q.name}
		}
		vm.refStack = append(vm.refStack, q.unresolvedRef)
	}
	vm.pc++
}
