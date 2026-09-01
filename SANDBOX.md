# Sandboxing untrusted JavaScript

`goja.NewSandbox` is an opt-in, capability-based wrapper around a Goja runtime.
Its zero-value policy exposes no policy-controlled standard global built-ins or
host values and disables runtime code generation. Normal `goja.New()` runtimes
are unchanged.

```go
sandbox, err := goja.NewSandbox(goja.SandboxPolicy{
	Builtins: goja.SandboxBuiltins{
		Allow: []string{"JSON", "Math"},
	},
	Capabilities: map[string]interface{}{
		"lookupPrice": func(productID string) (float64, error) {
			return prices.Lookup(productID)
		},
	},
	ExecutionTimeout: 100 * time.Millisecond,
})
if err != nil {
	return err
}

value, err := sandbox.RunString(`
	JSON.stringify({price: Math.round(lookupPrice("sku-42") * 100) / 100})
`)
if errors.Is(err, goja.ErrSandboxTimeout) {
	return errors.New("script exceeded its execution budget")
}
```

## Policy

`SandboxBuiltins` supports both common policy styles:

- The default (`AllowAll: false`) is a whitelist. Only names in `Allow` are
  visible.
- `AllowAll: true` is a blacklist. Every standard global is visible except
  names in `Deny`.

Unknown, duplicate, and contradictory entries are rejected. Use
`goja.SandboxBuiltinNames()` to obtain the accepted names. The always-present
language globals `globalThis`, `undefined`, `NaN`, and `Infinity` are not policy
capabilities.

`Capabilities` is the complete set of host-provided global values. Standard and
reserved names cannot be replaced. The map is copied when the sandbox is
created, and `Reset` creates a fresh runtime with the original policy and
capabilities. JavaScript state otherwise persists between runs.

## Host capabilities

`Capabilities` is the bridge from JavaScript to the embedding Go application.
Each map key becomes a JavaScript global name and each map value is the Go value
or function exposed at that name. Supplying this map when creating a sandbox is
the sandbox equivalent of using `Runtime.Set` to install host-provided globals;
the wrapped runtime is intentionally not exposed for later mutation:

```go
sandbox, err := goja.NewSandbox(goja.SandboxPolicy{
	Capabilities: map[string]interface{}{
		"add": func(a, b int) int { return a + b },
	},
})
// The script can call add(20, 22). It cannot access other application code.
```

An absent entry means no host access under that name. Standard JavaScript
built-ins are not capabilities: configure them separately with `Builtins.Allow`
or `Builtins.AllowAll` and `Builtins.Deny`.

Prefer narrow wrapper functions that validate their inputs and perform one
specific operation:

```go
Capabilities: map[string]interface{}{
	"readPublicSetting": func(name string) (string, error) {
		if name != "theme" {
			return "", errors.New("setting is not script-readable")
		}
		return settings.Theme, nil
	},
}
```

Avoid exposing a broad application, database, filesystem, HTTP client, or
service object:

```go
// Unsafe for untrusted scripts: every exported field and method may become
// reachable, including operations with side effects.
Capabilities: map[string]interface{}{
	"app": application,
}
```

The capabilities map itself is copied, but its values are not deep-copied or
made read-only. Pointers, maps, slices, structs, and functions may share host
state, and their methods or calls may perform arbitrary side effects. In
particular, exposing a Go struct or pointer grants scripts its Goja-visible
exported fields and method surface. Treat every granted value as fully trusted
authority and prefer narrow wrapper functions.

Dynamic code generation is denied unless `AllowDynamicCode` is true. The check
is enforced at Goja's central eval path, so it covers direct and indirect
`eval`, the global `Function` constructor, and Function, AsyncFunction, and
GeneratorFunction constructors reached through prototypes or a function's
`constructor` property. Removing only the global names would not provide this
protection. Ordinary function declarations, closures, object methods, and class
methods defined in the original script still work; this policy blocks runtime
source compilation, not normal JavaScript functions.

`ExecutionTimeout` uses Goja's interrupt mechanism. A sandbox serializes calls,
stops and synchronizes its timer callback, and clears any stale interrupt before
reuse. `RunString`, `RunScript`, and `RunProgram` all apply the limit. A zero
duration means no execution timeout.

## Threat model and limitations

The sandbox is designed to keep untrusted JavaScript from obtaining host
authority that the embedding application did not explicitly grant. Goja does
not provide filesystem, network, process, environment, or module-loading APIs
by itself. Those capabilities appear only when the host exposes them.

The wrapper is defense in depth, not an operating-system security boundary:

- A granted Go function, object, map, slice, or struct is trusted in full. Goja
  cannot inspect or constrain arbitrary actions performed by Go code, including
  I/O, process access, or access obtained through methods and fields.
- Goja interrupts while executing JavaScript bytecode. It cannot preempt a
  blocking or CPU-bound native Go function, including a built-in or granted
  capability. Put independent deadlines and resource limits around host
  capabilities.
- The built-in policy controls global bindings, not ECMAScript syntax or the
  intrinsic prototype graph. For example, denying the global `Array` name does
  not stop array literals from having `Array.prototype`. Dynamic Function
  constructors remain blocked centrally when dynamic code is denied.
- The timeout is a wall-clock execution guard, not a deterministic CPU,
  allocation, stack, or memory quota. Run the process inside OS/container
  resource limits when those guarantees matter.
- Values returned by a sandbox should be treated as untrusted. Calling a
  returned JavaScript function directly through `goja.AssertFunction` would be
  outside the sandbox wrapper's timeout. Prefer completing script work through
  the sandbox `Run*` methods.
- Source parsing and compilation are not preemptible. If a timer fires during
  compilation, the interrupt is observed when JavaScript execution starts.

Keep the capability surface small, expose narrow functions instead of broad
application objects, validate inputs and outputs at the Go boundary, and use a
fresh sandbox (`Reset`) between mutually untrusted tenants.
