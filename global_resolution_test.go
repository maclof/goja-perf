package goja

import (
	"errors"
	"sync"
	"testing"
)

func hasQuickenedGlobalResolver(runtime *Runtime, strict bool) bool {
	for _, state := range runtime.vm.tiering.programs {
		if state.quickProgram == nil {
			continue
		}
		for _, ins := range state.quickProgram.code {
			switch ins.(type) {
			case *quickenedResolveVar1:
				if !strict {
					return true
				}
			case *quickenedResolveVar1Strict:
				if strict {
					return true
				}
			}
		}
	}
	return false
}

func TestQuickenedStrictGlobalReferenceRevalidatesObject(t *testing.T) {
	program := MustCompile("quickened_global_strict.js", `
for (var cacheI = 0; cacheI < 64; cacheI++) {
	cacheTarget = cacheI;
}
cacheTarget;
`, true)
	runtime := New()
	if err := runtime.Set("cacheTarget", 0); err != nil {
		t.Fatal(err)
	}
	if result, err := runtime.RunProgram(program); err != nil || result.ToInteger() != 63 {
		t.Fatalf("initial run: result=%v err=%v", result, err)
	}
	if !hasQuickenedGlobalResolver(runtime, true) {
		t.Fatal("strict global resolver was not quickened")
	}

	setterCalls := 0
	accessorValue := int64(-1)
	getter := runtime.ToValue(func(FunctionCall) Value { return valueInt(accessorValue) })
	setter := runtime.ToValue(func(call FunctionCall) Value {
		setterCalls++
		accessorValue = call.Argument(0).ToInteger()
		return _undefined
	})
	if err := runtime.GlobalObject().DefineAccessorProperty("cacheTarget", getter, setter, FLAG_TRUE, FLAG_TRUE); err != nil {
		t.Fatal(err)
	}
	if result, err := runtime.RunProgram(program); err != nil || result.ToInteger() != 63 || setterCalls != 64 {
		t.Fatalf("accessor redefine: result=%v setters=%d err=%v", result, setterCalls, err)
	}

	if err := runtime.GlobalObject().Delete("cacheTarget"); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.RunProgram(program); err == nil {
		t.Fatal("deleted strict global did not produce ReferenceError")
	} else {
		var exception *Exception
		if !errors.As(err, &exception) || exception.Value().ToObject(runtime).Get("name").String() != "ReferenceError" {
			t.Fatalf("deleted strict global error: %T %v", err, err)
		}
	}
	if err := runtime.Set("cacheTarget", 0); err != nil {
		t.Fatal(err)
	}
	if result, err := runtime.RunProgram(program); err != nil || result.ToInteger() != 63 {
		t.Fatalf("re-added global: result=%v err=%v", result, err)
	}

	oldGlobal := runtime.GlobalObject()
	newGlobal := runtime.NewObject()
	if err := newGlobal.Set("cacheTarget", 0); err != nil {
		t.Fatal(err)
	}
	runtime.SetGlobalObject(newGlobal)
	if result, err := runtime.RunProgram(program); err != nil || result.ToInteger() != 63 {
		t.Fatalf("replacement global: result=%v err=%v", result, err)
	}
	if got := newGlobal.Get("cacheTarget").ToInteger(); got != 63 {
		t.Fatalf("replacement global value: got %d, want 63", got)
	}
	if got := oldGlobal.Get("cacheTarget").ToInteger(); got != 63 {
		t.Fatalf("old global was unexpectedly changed: got %d, want 63", got)
	}
}

func TestQuickenedGlobalReferencePreservesProxyTraps(t *testing.T) {
	runtime := New()
	target := runtime.NewObject()
	if err := target.Set("cacheTarget", 0); err != nil {
		t.Fatal(err)
	}
	hasCalls, setCalls := 0, 0
	proxy := runtime.NewProxy(target, &ProxyTrapConfig{
		Has: func(target *Object, property string) bool {
			if property == "cacheTarget" {
				hasCalls++
			}
			return target.Get(property) != nil
		},
		Set: func(target *Object, property string, value Value, receiver Value) bool {
			if property == "cacheTarget" {
				setCalls++
			}
			return target.Set(property, value) == nil
		},
	})
	runtime.SetGlobalObject(runtime.ToValue(proxy).(*Object))
	program := MustCompile("quickened_global_proxy.js", `
for (var cacheI = 0; cacheI < 64; cacheI++) {
	cacheTarget = cacheI;
}
cacheTarget;
`, true)
	result, err := runtime.RunProgram(program)
	if err != nil || result.ToInteger() != 63 {
		t.Fatalf("proxy run: result=%v err=%v", result, err)
	}
	if !hasQuickenedGlobalResolver(runtime, true) || hasCalls != 64 || setCalls != 64 {
		t.Fatalf("proxy traps/quickening: has=%d set=%d quickened=%t", hasCalls, setCalls, hasQuickenedGlobalResolver(runtime, true))
	}
}

func TestQuickenedGlobalReferenceRechecksEvalLexicalAndWith(t *testing.T) {
	runtime := New()
	if err := runtime.Set("dynamicTarget", 100); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.RunString(`
function writeThroughEval(useEval) {
	if (useEval) {
		eval("var dynamicTarget = 200");
	}
	for (var i = 0; i < 64; i++) {
		dynamicTarget = i;
	}
	return dynamicTarget;
}
function writeThroughWith(object) {
	with (object) {
		for (var i = 0; i < 64; i++) {
			dynamicTarget = i;
		}
		return dynamicTarget;
	}
}
function writeLateLexical() {
	for (var i = 0; i < 64; i++) {
		lateTarget = i;
	}
	return lateTarget;
}
`); err != nil {
		t.Fatal(err)
	}
	evalCall, _ := AssertFunction(runtime.Get("writeThroughEval"))
	if result, err := evalCall(_undefined, valueFalse); err != nil || result.ToInteger() != 63 {
		t.Fatalf("global eval run: result=%v err=%v", result, err)
	}
	if err := runtime.Set("dynamicTarget", 100); err != nil {
		t.Fatal(err)
	}
	if result, err := evalCall(_undefined, valueTrue); err != nil || result.ToInteger() != 63 || runtime.GlobalObject().Get("dynamicTarget").ToInteger() != 100 {
		t.Fatalf("direct eval binding: result=%v global=%v err=%v", result, runtime.GlobalObject().Get("dynamicTarget"), err)
	}

	withCall, _ := AssertFunction(runtime.Get("writeThroughWith"))
	object := runtime.NewObject()
	if err := object.Set("dynamicTarget", 0); err != nil {
		t.Fatal(err)
	}
	if result, err := withCall(_undefined, object); err != nil || result.ToInteger() != 63 || object.Get("dynamicTarget").ToInteger() != 63 || runtime.GlobalObject().Get("dynamicTarget").ToInteger() != 100 {
		t.Fatalf("with binding: result=%v object=%v global=%v err=%v", result, object.Get("dynamicTarget"), runtime.GlobalObject().Get("dynamicTarget"), err)
	}
	if err := object.Delete("dynamicTarget"); err != nil {
		t.Fatal(err)
	}
	if result, err := withCall(_undefined, object); err != nil || result.ToInteger() != 63 || runtime.GlobalObject().Get("dynamicTarget").ToInteger() != 63 {
		t.Fatalf("with fallback: result=%v global=%v err=%v", result, runtime.GlobalObject().Get("dynamicTarget"), err)
	}

	if err := runtime.Set("lateTarget", 0); err != nil {
		t.Fatal(err)
	}
	lateCall, _ := AssertFunction(runtime.Get("writeLateLexical"))
	if _, err := lateCall(_undefined); err != nil {
		t.Fatal(err)
	}
	if err := runtime.GlobalObject().Delete("lateTarget"); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.RunString("let lateTarget = 5;"); err != nil {
		t.Fatal(err)
	}
	if result, err := lateCall(_undefined); err != nil || result.ToInteger() != 63 || runtime.GlobalObject().Get("lateTarget") != nil {
		t.Fatalf("late lexical binding: result=%v object=%v err=%v", result, runtime.GlobalObject().Get("lateTarget"), err)
	}
	if !hasQuickenedGlobalResolver(runtime, false) {
		t.Fatal("non-strict global resolver was not quickened")
	}
}

func TestQuickenedGlobalReferenceIsRuntimeOwned(t *testing.T) {
	program := MustCompile("quickened_global_shared.js", `
for (var sharedI = 0; sharedI < 64; sharedI++) {
	sharedResult = sharedInput;
}
sharedResult;
`, true)
	runtimes := []*Runtime{New(), New()}
	for i, runtime := range runtimes {
		if err := runtime.Set("sharedInput", i+1); err != nil {
			t.Fatal(err)
		}
		if err := runtime.Set("sharedResult", 0); err != nil {
			t.Fatal(err)
		}
		if result, err := runtime.RunProgram(program); err != nil || result.ToInteger() != int64(i+1) {
			t.Fatalf("runtime %d: result=%v err=%v", i, result, err)
		}
		if !hasQuickenedGlobalResolver(runtime, true) {
			t.Fatalf("runtime %d did not install strict resolver", i)
		}
	}
	first := runtimes[0].vm.tiering.programs[program].quickProgram
	second := runtimes[1].vm.tiering.programs[program].quickProgram
	if first == nil || second == nil || first == second {
		t.Fatalf("quick Programs are not Runtime-owned: first=%p second=%p", first, second)
	}
}

func TestQuickenedGlobalReferenceSurvivesReentrantGlobalReplacement(t *testing.T) {
	runtime := New()
	oldGlobal := runtime.GlobalObject()
	newGlobal := runtime.NewObject()
	if err := oldGlobal.Set("cacheTarget", 0); err != nil {
		t.Fatal(err)
	}
	if err := newGlobal.Set("cacheTarget", 0); err != nil {
		t.Fatal(err)
	}
	replace := false
	if err := runtime.Set("replaceGlobal", func() int {
		if replace {
			runtime.SetGlobalObject(newGlobal)
		}
		return 42
	}); err != nil {
		t.Fatal(err)
	}
	program := MustCompile("quickened_global_reentrant.js", `
for (var cacheI = 0; cacheI < 64; cacheI++) {
}
cacheTarget = replaceGlobal();
cacheTarget;
`, true)
	if _, err := runtime.RunProgram(program); err != nil {
		t.Fatal(err)
	}
	if !hasQuickenedGlobalResolver(runtime, true) {
		t.Fatal("strict global resolver was not quickened")
	}
	if err := oldGlobal.Set("cacheTarget", 0); err != nil {
		t.Fatal(err)
	}
	replace = true
	result, err := runtime.RunProgram(program)
	if err != nil {
		t.Fatal(err)
	}
	// Reference resolution precedes RHS evaluation, so replacement during the
	// call must not retarget the already-live reference.
	if got := oldGlobal.Get("cacheTarget").ToInteger(); got != 42 {
		t.Fatalf("live reference was retargeted: old global=%d, want 42", got)
	}
	if got := newGlobal.Get("cacheTarget").ToInteger(); got != 0 {
		t.Fatalf("replacement global was written through old reference: got %d", got)
	}
	if result.ToInteger() != 0 {
		t.Fatalf("post-replacement lookup: got %v, want replacement-global value 0", result)
	}
}

func TestQuickenedGlobalReferenceSharedProgramConcurrentRuntimes(t *testing.T) {
	program := MustCompile("quickened_global_concurrent.js", `
for (var sharedI = 0; sharedI < 256; sharedI++) {
	sharedResult = sharedInput;
}
sharedResult;
`, true)
	const runtimeCount = 8
	runtimes := make([]*Runtime, runtimeCount)
	for i := range runtimes {
		runtimes[i] = New()
		if err := runtimes[i].Set("sharedInput", i+1); err != nil {
			t.Fatal(err)
		}
		if err := runtimes[i].Set("sharedResult", 0); err != nil {
			t.Fatal(err)
		}
	}
	results := make([]Value, runtimeCount)
	errs := make([]error, runtimeCount)
	var wg sync.WaitGroup
	for i, runtime := range runtimes {
		wg.Add(1)
		go func(i int, runtime *Runtime) {
			defer wg.Done()
			results[i], errs[i] = runtime.RunProgram(program)
		}(i, runtime)
	}
	wg.Wait()
	for i, runtime := range runtimes {
		if errs[i] != nil || results[i].ToInteger() != int64(i+1) || !hasQuickenedGlobalResolver(runtime, true) {
			t.Fatalf("runtime %d: result=%v err=%v quickened=%t", i, results[i], errs[i], hasQuickenedGlobalResolver(runtime, true))
		}
	}
}
