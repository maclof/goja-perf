package goja

import (
	"errors"
	"testing"
)

type boundaryMethodSemanticsHost struct {
	callback Callable
	total    int64
}

type boundaryMovableMethodValue struct {
	Value int64
}

func (v *boundaryMovableMethodValue) Add(delta int64) int64 {
	v.Value += delta
	return v.Value
}

func (h *boundaryMethodSemanticsHost) Add(value int64) int64 {
	h.total += value
	return h.total
}

func (h *boundaryMethodSemanticsHost) Reenter(value int64) int64 {
	if value == 1 {
		result, err := h.callback(_undefined, valueInt(2))
		if err != nil {
			panic(err)
		}
		return result.ToInteger() + 1
	}
	return value
}

func (h *boundaryMethodSemanticsHost) Fail() error {
	return errors.New("cached method failure")
}

func (h *boundaryMethodSemanticsHost) Construct(call ConstructorCall, runtime *Runtime) *Object {
	if runtime == nil {
		panic("nil runtime")
	}
	call.This.Set("value", call.Argument(0))
	return nil
}

func TestReflectMethodCalleeCachePreservesSemantics(t *testing.T) {
	runtime := New()
	host := &boundaryMethodSemanticsHost{}
	if err := runtime.Set("host", host); err != nil {
		t.Fatal(err)
	}
	result, err := runtime.RunString(`
		var first = host.Add;
		var second = host.Add;
		if (first === second) throw new Error("method reads unexpectedly share identity");
		first.marker = 42;
		if (host.Add.marker !== undefined) throw new Error("method wrapper state leaked");
		Object.defineProperty(first, "name", {value: "changed"});
		Object.defineProperty(first, "length", {value: 99});
		if (host.Add.name === "changed" || host.Add.length === 99) throw new Error("built-in method property state leaked");
		host.Add(2) + host.Add(3);
	`)
	if err != nil {
		t.Fatal(err)
	}
	if got := result.ToInteger(); got != 7 || host.total != 5 {
		t.Fatalf("direct calls: result=%d total=%d, want 7/5", got, host.total)
	}

	if _, err := runtime.RunString(`function callReentrant(value) { return host.Reenter(value); }`); err != nil {
		t.Fatal(err)
	}
	callback, ok := AssertFunction(runtime.Get("callReentrant"))
	if !ok {
		t.Fatal("callReentrant is not callable")
	}
	host.callback = callback
	if result, err := callback(_undefined, valueInt(1)); err != nil || result.ToInteger() != 3 {
		t.Fatalf("reentrant call: result=%v err=%v", result, err)
	}

	for i := 0; i < 2; i++ {
		result, err := runtime.RunString(`
			try { host.Fail(); } catch (e) { e.message; }
		`)
		if err != nil || result.String() != "cached method failure" {
			t.Fatalf("error call %d: result=%v err=%v", i, result, err)
		}
	}

	result, err = runtime.RunString(`
		var firstConstructor = host.Construct;
		var secondConstructor = host.Construct;
		if (firstConstructor === secondConstructor) throw new Error("constructor reads unexpectedly share identity");
		if (firstConstructor.prototype === secondConstructor.prototype) throw new Error("constructor prototypes unexpectedly share identity");
		firstConstructor.prototype.marker = 42;
		if (secondConstructor.prototype.marker !== undefined) throw new Error("constructor prototype state leaked");
		new host.Construct(42).value;
	`)
	if err != nil || result.ToInteger() != 42 {
		t.Fatalf("constructor call: result=%v err=%v", result, err)
	}
}

func TestReflectMethodCalleeCacheRebindsMovedContainerValue(t *testing.T) {
	runtime := New()
	values := []boundaryMovableMethodValue{{Value: 1}, {Value: 10}}
	if err := runtime.Set("values", values); err != nil {
		t.Fatal(err)
	}
	result, err := runtime.RunString(`
		var first = values[0];
		first.Add(1);
		values.reverse();
		first.Add(2);
		first.Value * 100 + values[0].Value * 10 + values[1].Value;
	`)
	if err != nil {
		t.Fatal(err)
	}
	if result.ToInteger() != 502 {
		t.Fatalf("result=%v values=%+v, want encoded 502", result, values)
	}
}
