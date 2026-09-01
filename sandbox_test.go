package goja

import (
	"errors"
	"runtime"
	"strings"
	"testing"
	"time"
)

func mustSandbox(t *testing.T, policy SandboxPolicy) *Sandbox {
	t.Helper()
	s, err := NewSandbox(policy)
	if err != nil {
		t.Fatalf("NewSandbox() failed: %v", err)
	}
	return s
}

func TestSandboxDefaultDeniesGlobalBuiltins(t *testing.T) {
	s := mustSandbox(t, SandboxPolicy{})
	v, err := s.RunString(`[
		typeof Object, typeof Function, typeof eval, typeof JSON,
		typeof Promise, typeof Date, typeof globalThis, typeof undefined
	].join(",")`)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := v.String(), "undefined,undefined,undefined,undefined,undefined,undefined,object,undefined"; got != want {
		t.Fatalf("unexpected globals: got %q, want %q", got, want)
	}

	allowed := map[string]bool{"globalThis": true, "NaN": true, "undefined": true, "Infinity": true}
	for _, name := range s.runtime.GlobalObject().GetOwnPropertyNames() {
		if !allowed[name] {
			t.Errorf("zero policy leaked global %q", name)
		}
	}

	known := make(map[string]bool, len(sandboxBuiltinNames))
	for _, name := range sandboxBuiltinNames {
		known[name] = true
	}
	for _, name := range New().GlobalObject().GetOwnPropertyNames() {
		if !allowed[name] && !known[name] {
			t.Errorf("standard global %q is missing from sandboxBuiltinNames", name)
		}
	}
}

func TestSandboxBuiltinAllowlist(t *testing.T) {
	s := mustSandbox(t, SandboxPolicy{
		Builtins: SandboxBuiltins{Allow: []string{"JSON", "Math"}},
	})
	v, err := s.RunString(`JSON.stringify({answer: Math.max(40, 42), date: typeof Date})`)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := v.String(), `{"answer":42,"date":"undefined"}`; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestSandboxBuiltinDenylist(t *testing.T) {
	s := mustSandbox(t, SandboxPolicy{
		Builtins: SandboxBuiltins{AllowAll: true, Deny: []string{"Date", "eval"}},
	})
	v, err := s.RunString(`typeof Date + "," + typeof eval + "," + typeof JSON + "," + typeof Map`)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := v.String(), "undefined,undefined,object,function"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestSandboxDynamicCodeDeniedThroughAllConstructorPaths(t *testing.T) {
	s := mustSandbox(t, SandboxPolicy{
		Builtins: SandboxBuiltins{AllowAll: true},
	})

	attacks := map[string]string{
		"direct eval":                    `eval("40 + 2")`,
		"indirect eval":                  `(0, eval)("40 + 2")`,
		"global eval":                    `globalThis.eval("40 + 2")`,
		"Function":                       `Function("return 42")()`,
		"function constructor":           `(function() {}).constructor("return 42")()`,
		"arrow constructor":              `(() => {}).constructor("return 42")()`,
		"async function constructor":     `(async function() {}).constructor("return 42")()`,
		"generator function constructor": `(function*() {}).constructor("return 42")()`,
		"class constructor":              `(class {}).constructor("return 42")()`,
		"prototype constructor":          `Object.getPrototypeOf(function() {}).constructor("return 42")()`,
	}
	for name, source := range attacks {
		t.Run(name, func(t *testing.T) {
			_, err := s.RunString(source)
			if err == nil {
				t.Fatal("dynamic code generation unexpectedly succeeded")
			}
			if !strings.Contains(err.Error(), "dynamic code generation is disabled") {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestSandboxDynamicCodeCanBeExplicitlyAllowed(t *testing.T) {
	s := mustSandbox(t, SandboxPolicy{
		Builtins:         SandboxBuiltins{Allow: []string{"eval", "Function"}},
		AllowDynamicCode: true,
	})
	v, err := s.RunString(`eval("20 + 1") + Function("return 21")()`)
	if err != nil {
		t.Fatal(err)
	}
	if got := v.ToInteger(); got != 42 {
		t.Fatalf("got %d, want 42", got)
	}
}

func TestSandboxCapabilitiesAreExplicit(t *testing.T) {
	type hostAPI struct {
		Prefix string
	}
	s := mustSandbox(t, SandboxPolicy{
		Capabilities: map[string]interface{}{
			"add": func(a, b int) int { return a + b },
			"api": &hostAPI{Prefix: "safe"},
		},
	})
	v, err := s.RunString(`add(19, 23) + ":" + api.Prefix + ":" + typeof fetch + ":" + typeof require`)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := v.String(), "42:safe:undefined:undefined"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestSandboxPolicyValidation(t *testing.T) {
	tests := []struct {
		name   string
		policy SandboxPolicy
	}{
		{name: "unknown allow", policy: SandboxPolicy{Builtins: SandboxBuiltins{Allow: []string{"NotABuiltin"}}}},
		{name: "unknown deny", policy: SandboxPolicy{Builtins: SandboxBuiltins{Deny: []string{"NotABuiltin"}}}},
		{name: "allow all and list", policy: SandboxPolicy{Builtins: SandboxBuiltins{AllowAll: true, Allow: []string{"JSON"}}}},
		{name: "allow deny conflict", policy: SandboxPolicy{Builtins: SandboxBuiltins{Allow: []string{"JSON"}, Deny: []string{"JSON"}}}},
		{name: "duplicate", policy: SandboxPolicy{Builtins: SandboxBuiltins{Allow: []string{"JSON", "JSON"}}}},
		{name: "builtin capability", policy: SandboxPolicy{Capabilities: map[string]interface{}{"eval": func() {}}}},
		{name: "reserved capability", policy: SandboxPolicy{Capabilities: map[string]interface{}{"globalThis": 1}}},
		{name: "empty capability", policy: SandboxPolicy{Capabilities: map[string]interface{}{"": 1}}},
		{name: "negative timeout", policy: SandboxPolicy{ExecutionTimeout: -time.Nanosecond}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewSandbox(test.policy); err == nil {
				t.Fatal("NewSandbox() unexpectedly succeeded")
			}
		})
	}
}

func TestSandboxTimeoutAndReuse(t *testing.T) {
	s := mustSandbox(t, SandboxPolicy{ExecutionTimeout: 20 * time.Millisecond})
	_, err := s.RunString(`for (;;) {}`)
	if !errors.Is(err, ErrSandboxTimeout) {
		t.Fatalf("got %v, want an ErrSandboxTimeout", err)
	}

	// The timer interrupt must be cleared before reuse.
	v, err := s.RunString(`40 + 2`)
	if err != nil {
		t.Fatalf("reuse after timeout failed: %v", err)
	}
	if got := v.ToInteger(); got != 42 {
		t.Fatalf("got %d, want 42", got)
	}
}

func TestSandboxTimeoutCannotPreemptNativeCapability(t *testing.T) {
	const nativeWork = 30 * time.Millisecond
	s := mustSandbox(t, SandboxPolicy{
		Capabilities: map[string]interface{}{
			"block": func() { time.Sleep(nativeWork) },
		},
		ExecutionTimeout: 5 * time.Millisecond,
	})
	start := time.Now()
	_, err := s.RunString(`block()`)
	if elapsed := time.Since(start); elapsed < nativeWork {
		t.Fatalf("native capability was unexpectedly preempted after %s", elapsed)
	}
	if !errors.Is(err, ErrSandboxTimeout) {
		t.Fatalf("got %v, want an ErrSandboxTimeout after native return", err)
	}
}

func TestSandboxStoppedTimersDoNotLeakOrInterruptLaterRuns(t *testing.T) {
	before := runtime.NumGoroutine()
	s := mustSandbox(t, SandboxPolicy{ExecutionTimeout: time.Second})
	for i := 0; i < 100; i++ {
		v, err := s.RunString(`6 * 7`)
		if err != nil {
			t.Fatalf("run %d failed: %v", i, err)
		}
		if v.ToInteger() != 42 {
			t.Fatalf("run %d returned %v", i, v)
		}
	}
	if delta := runtime.NumGoroutine() - before; delta > 1 {
		t.Fatalf("sandbox runs leaked goroutines: before=%d, after=%d", before, before+delta)
	}
}

func TestSandboxReset(t *testing.T) {
	s := mustSandbox(t, SandboxPolicy{
		Capabilities: map[string]interface{}{"answer": 42},
	})
	if _, err := s.RunString(`var userState = 7`); err != nil {
		t.Fatal(err)
	}
	if err := s.Reset(); err != nil {
		t.Fatal(err)
	}
	v, err := s.RunString(`typeof userState + "," + answer`)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := v.String(), "undefined,42"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestSandboxRunProgram(t *testing.T) {
	program := MustCompile("sandbox.js", `21 * 2`, true)
	s := mustSandbox(t, SandboxPolicy{})
	v, err := s.RunProgram(program)
	if err != nil {
		t.Fatal(err)
	}
	if got := v.ToInteger(); got != 42 {
		t.Fatalf("got %d, want 42", got)
	}
	if _, err := s.RunProgram(nil); err == nil {
		t.Fatal("nil program unexpectedly succeeded")
	}
}

func TestSandboxBuiltinNamesReturnsCopy(t *testing.T) {
	names := SandboxBuiltinNames()
	if len(names) == 0 {
		t.Fatal("empty built-in list")
	}
	original := names[0]
	names[0] = "changed"
	if got := SandboxBuiltinNames()[0]; got != original {
		t.Fatalf("built-in list was mutated: got %q, want %q", got, original)
	}
}

func TestOrdinaryRuntimeDynamicCodeAndGlobalsUnchanged(t *testing.T) {
	r := New()
	v, err := r.RunString(`typeof Object + "," + eval("20 + 1") + "," + Function("return 21")()`)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := v.String(), "function,21,21"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
