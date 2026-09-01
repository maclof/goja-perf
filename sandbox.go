package goja

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/dop251/goja/unistring"
)

// ErrSandboxTimeout is wrapped by the *InterruptedError returned when a
// Sandbox execution exceeds its configured timeout.
var ErrSandboxTimeout = errors.New("goja sandbox execution timed out")

// SandboxBuiltins controls which standard global built-ins are visible to a
// sandboxed script. Its zero value denies all policy-controlled built-ins.
//
// If AllowAll is false, only names in Allow are visible. If AllowAll is true,
// all built-ins except names in Deny are visible. Allow and Deny may not contain
// the same name, and Allow must be empty when AllowAll is true.
//
// This policy controls global bindings. It does not remove ECMAScript language
// intrinsics from the prototype graph (for example, an array still has
// Array.prototype).
type SandboxBuiltins struct {
	AllowAll bool
	Allow    []string
	Deny     []string
}

// SandboxPolicy configures an opt-in Sandbox. The zero value is deny-by-default:
// no policy-controlled global built-ins or host capabilities are exposed, and
// dynamic code generation is disabled.
type SandboxPolicy struct {
	Builtins SandboxBuiltins

	// Capabilities is the complete set of host-provided values exposed as
	// globals. A granted Go function or object is trusted: the sandbox cannot
	// constrain what arbitrary Go code does after it is called.
	Capabilities map[string]interface{}

	// AllowDynamicCode permits eval and the Function, AsyncFunction, and
	// GeneratorFunction constructor families. It is false by default.
	AllowDynamicCode bool

	// ExecutionTimeout interrupts JavaScript after this wall-clock duration.
	// Zero disables the timeout. Native Go functions, including granted
	// capabilities, cannot be interrupted by goja.
	ExecutionTimeout time.Duration
}

// Sandbox is an isolated Runtime configured with an explicit policy. Its Run
// methods are safe to call concurrently; executions are serialized because a
// Runtime itself is not goroutine-safe.
//
// State created by one execution remains available to later executions. Reset
// discards that state and reapplies the original policy.
type Sandbox struct {
	mu             sync.Mutex
	policy         SandboxPolicy
	globalTemplate *objectTemplate
	runtime        *Runtime
}

var sandboxBuiltinNames = []string{
	"Object", "Function", "Array", "String", "Number", "BigInt", "RegExp", "Date", "Boolean",
	"Proxy", "Reflect", "Error", "AggregateError", "TypeError", "ReferenceError", "SyntaxError",
	"RangeError", "EvalError", "URIError", "GoError", "eval", "Math", "JSON", "ArrayBuffer",
	"DataView", "Uint8Array", "Uint8ClampedArray", "Int8Array", "Uint16Array", "Int16Array",
	"Uint32Array", "Int32Array", "Float32Array", "Float64Array", "BigInt64Array", "BigUint64Array",
	"Symbol", "WeakSet", "WeakMap", "Map", "Set", "Promise", "isNaN", "parseInt", "parseFloat",
	"isFinite", "decodeURI", "decodeURIComponent", "encodeURI", "encodeURIComponent", "escape", "unescape",
}

var sandboxBuiltinNameSet = func() map[string]struct{} {
	set := make(map[string]struct{}, len(sandboxBuiltinNames))
	for _, name := range sandboxBuiltinNames {
		set[name] = struct{}{}
	}
	return set
}()

var sandboxReservedCapabilityNames = map[string]struct{}{
	"globalThis":  {},
	"undefined":   {},
	"NaN":         {},
	"Infinity":    {},
	"__proto__":   {},
	"constructor": {},
	"prototype":   {},
}

// SandboxBuiltinNames returns the standard global names understood by
// SandboxBuiltins. The returned slice is a copy and may be modified by the
// caller.
func SandboxBuiltinNames() []string {
	return append([]string(nil), sandboxBuiltinNames...)
}

// NewSandbox creates a new opt-in sandbox and applies policy before any script
// can execute. It returns an error for unknown built-ins, contradictory rules,
// reserved capability names, or a negative timeout.
func NewSandbox(policy SandboxPolicy) (*Sandbox, error) {
	if err := validateSandboxPolicy(policy); err != nil {
		return nil, err
	}

	s := &Sandbox{policy: cloneSandboxPolicy(policy)}
	s.globalTemplate = newSandboxGlobalObjectTemplate(s.policy.Builtins)
	if err := s.resetLocked(); err != nil {
		return nil, err
	}
	return s, nil
}

// Reset discards all JavaScript state and creates a fresh Runtime with the
// original policy and capabilities.
func (s *Sandbox) Reset() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.resetLocked()
}

// RunString executes source in the sandbox's persistent global context.
func (s *Sandbox) RunString(source string) (Value, error) {
	return s.run(func(r *Runtime) (Value, error) {
		return r.RunString(source)
	})
}

// RunScript executes named source in the sandbox's persistent global context.
func (s *Sandbox) RunScript(name, source string) (Value, error) {
	return s.run(func(r *Runtime) (Value, error) {
		return r.RunScript(name, source)
	})
}

// RunProgram executes a precompiled Program in the sandbox's persistent global
// context.
func (s *Sandbox) RunProgram(program *Program) (Value, error) {
	if program == nil {
		return nil, errors.New("goja: nil sandbox program")
	}
	return s.run(func(r *Runtime) (Value, error) {
		return r.RunProgram(program)
	})
}

func (s *Sandbox) run(fn func(*Runtime) (Value, error)) (Value, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	r := s.runtime
	timeout := s.policy.ExecutionTimeout
	if timeout == 0 {
		return fn(r)
	}

	fired := make(chan struct{})
	timer := time.AfterFunc(timeout, func() {
		r.Interrupt(ErrSandboxTimeout)
		close(fired)
	})
	defer func() {
		if !timer.Stop() {
			<-fired
		}
		// If the timer won the race after execution completed, prevent its
		// interrupt from contaminating the next use of this runtime.
		r.ClearInterrupt()
	}()

	return fn(r)
}

func (s *Sandbox) resetLocked() error {
	r := New()
	r.disableDynamicCodeGeneration = !s.policy.AllowDynamicCode

	global, ok := r.globalObject.self.(*templatedObject)
	if !ok {
		return errors.New("goja: sandbox global object is not template-backed")
	}
	global.tmpl = s.globalTemplate

	for name, capability := range s.policy.Capabilities {
		if err := r.Set(name, capability); err != nil {
			return fmt.Errorf("install sandbox capability %q: %w", name, err)
		}
	}

	s.runtime = r
	return nil
}

func newSandboxGlobalObjectTemplate(policy SandboxBuiltins) *objectTemplate {
	base := getGlobalObjectTemplate()
	visible := func(name string) bool {
		names := policy.Allow
		result := false
		if policy.AllowAll {
			names = policy.Deny
			result = true
		}
		for _, candidate := range names {
			if name == candidate {
				return !result
			}
		}
		return result
	}

	count := 0
	for _, name := range base.propNames {
		if _, controlled := sandboxBuiltinNameSet[name.String()]; !controlled || visible(name.String()) {
			count++
		}
	}

	template := &objectTemplate{
		propNames:    make([]unistring.String, 0, count),
		props:        make(map[unistring.String]templatePropFactory, count),
		symProps:     base.symProps,
		symPropNames: base.symPropNames,
		protoFactory: base.protoFactory,
	}
	for _, name := range base.propNames {
		if _, controlled := sandboxBuiltinNameSet[name.String()]; controlled && !visible(name.String()) {
			continue
		}
		template.propNames = append(template.propNames, name)
		template.props[name] = base.props[name]
	}
	return template
}

func validateSandboxPolicy(policy SandboxPolicy) error {
	if policy.ExecutionTimeout < 0 {
		return errors.New("goja: sandbox execution timeout must not be negative")
	}
	if policy.Builtins.AllowAll && len(policy.Builtins.Allow) != 0 {
		return errors.New("goja: sandbox built-in Allow must be empty when AllowAll is true")
	}

	allow, err := validateSandboxBuiltinList("Allow", policy.Builtins.Allow, sandboxBuiltinNameSet)
	if err != nil {
		return err
	}
	deny, err := validateSandboxBuiltinList("Deny", policy.Builtins.Deny, sandboxBuiltinNameSet)
	if err != nil {
		return err
	}
	for name := range allow {
		if _, exists := deny[name]; exists {
			return fmt.Errorf("goja: sandbox built-in %q appears in both Allow and Deny", name)
		}
	}

	for name := range policy.Capabilities {
		if name == "" {
			return errors.New("goja: sandbox capability name must not be empty")
		}
		if _, exists := sandboxBuiltinNameSet[name]; exists {
			return fmt.Errorf("goja: sandbox capability %q conflicts with a standard built-in", name)
		}
		if _, reserved := sandboxReservedCapabilityNames[name]; reserved {
			return fmt.Errorf("goja: sandbox capability name %q is reserved", name)
		}
	}
	return nil
}

func validateSandboxBuiltinList(label string, names []string, known map[string]struct{}) (map[string]struct{}, error) {
	set := make(map[string]struct{}, len(names))
	for _, name := range names {
		if _, exists := known[name]; !exists {
			return nil, fmt.Errorf("goja: unknown sandbox built-in %q in %s", name, label)
		}
		if _, duplicate := set[name]; duplicate {
			return nil, fmt.Errorf("goja: duplicate sandbox built-in %q in %s", name, label)
		}
		set[name] = struct{}{}
	}
	return set, nil
}

func cloneSandboxPolicy(policy SandboxPolicy) SandboxPolicy {
	policy.Builtins.Allow = append([]string(nil), policy.Builtins.Allow...)
	policy.Builtins.Deny = append([]string(nil), policy.Builtins.Deny...)
	if policy.Capabilities != nil {
		capabilities := make(map[string]interface{}, len(policy.Capabilities))
		for name, capability := range policy.Capabilities {
			capabilities[name] = capability
		}
		policy.Capabilities = capabilities
	}
	return policy
}
