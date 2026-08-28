package goja

import "testing"

// TestIdleStackRetentionPolicy verifies the value-stack retention policy applied by
// Runtime.leave(): buffers up to maxIdleStackSize are retained (for reuse by the next
// top-level call) and fully cleared of value references, while larger buffers (e.g.
// from deep recursion) are released entirely. It also covers the invariant that the
// stack is empty (len == 0, i.e. sp is at its base) whenever control returns to Go.
func TestIdleStackRetentionPolicy(t *testing.T) {
	vm := New()

	checkRetained := func(where string) {
		t.Helper()
		st := vm.vm.stack
		if len(st) != 0 {
			t.Fatalf("%s: stack not empty while idle: len=%d", where, len(st))
		}
		if cap(st) == 0 || cap(st) > maxIdleStackSize {
			t.Fatalf("%s: unexpected retained capacity: cap=%d, want (0, %d]", where, cap(st), maxIdleStackSize)
		}
		for i, v := range st[:cap(st)] {
			if v != nil {
				t.Fatalf("%s: retained buffer holds a value reference at slot %d: %#v", where, i, v)
			}
		}
	}

	if _, err := vm.RunString(`var x = {a: 1, b: "two"}; x.a + x.b.length`); err != nil {
		t.Fatal(err)
	}
	checkRetained("after RunString")
	t.Logf("retained after RunString: cap=%d (%d B)", cap(vm.vm.stack), cap(vm.vm.stack)*16)

	if _, err := vm.RunString(`function g(a){ return a * 2; }`); err != nil {
		t.Fatal(err)
	}
	g, ok := AssertFunction(vm.Get("g"))
	if !ok {
		t.Fatal("g is not a function")
	}
	if res, err := g(Undefined(), vm.ToValue(21)); err != nil {
		t.Fatal(err)
	} else if res.ToInteger() != 42 {
		t.Fatalf("unexpected result: %v", res)
	}
	checkRetained("after Callable call")
	t.Logf("retained after Callable call: cap=%d (%d B)", cap(vm.vm.stack), cap(vm.vm.stack)*16)

	if _, err := vm.New(vm.Get("Object")); err != nil {
		t.Fatal(err)
	}
	checkRetained("after New")

	// A workload that grows the stack beyond maxIdleStackSize must not retain it.
	const DEEP = `function f(n){ if (n <= 0) { return 0; } return f(n - 1) + 1; } f(20000);`
	if _, err := vm.RunString(DEEP); err != nil {
		t.Fatal(err)
	}
	if st := vm.vm.stack; len(st) != 0 || cap(st) != 0 {
		t.Fatalf("oversized stack not released while idle: len=%d cap=%d, want 0/0", len(st), cap(st))
	}

	// The runtime must remain fully usable after the release, and the next small
	// workload must be retained again.
	if v, err := vm.RunString(`2 + 3`); err != nil {
		t.Fatal(err)
	} else if v.ToInteger() != 5 {
		t.Fatalf("unexpected result after release: %v", v)
	}
	if _, err := vm.RunString(`f(10)`); err != nil {
		t.Fatal(err)
	}
	checkRetained("after small call following release")
}
