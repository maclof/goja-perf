# goja-perf

`goja-perf` is a performance-focused, API-compatible fork of
[`dop251/goja`](https://github.com/dop251/goja). It keeps upstream Goja's pure-Go
JavaScript engine and public import path, while adding measured execution and
Go/JavaScript-boundary optimizations, a representative benchmark suite, and an
opt-in capability sandbox. It is intended as a drop-in replacement, but it is an
independent fork rather than an upstream Goja release.

## What is different

- Hot code progresses through adaptive quickening and typed traces. Supported
  numeric loop shapes include guarded integer and floating-point accumulation;
  integer counter loops cover ascending/descending, inclusive/exclusive, and
  constant-step forms. Eligible integer traces can compile to native code on
  Windows/amd64 and Linux/amd64. Other platforms use the portable typed Go
  executor.
- Boundary paths avoid redundant primitive boxing, reuse bounded VM stack
  storage, cache reflected Go method templates, reduce object-export cache
  overhead, reuse quickened global references, and preallocate object literals.
- Benchmarks separately cover JavaScript execution, runtime setup/compilation,
  JavaScript calling Go functions and methods, Go calling JavaScript, mixed
  bidirectional calls, and primitive/object/slice/map/struct values. The
  [`benchjs`](benchjs/README.md) suite compares identical pure-JavaScript
  workloads with V8.
- [`NewSandbox`](SANDBOX.md) provides opt-in built-in allow/deny policy, explicit
  host capabilities, dynamic-code control, serialized runtime access, reset,
  and wall-clock execution timeouts. Ordinary `goja.New()` runtimes are
  unchanged.

## Measured performance

These are the statistically significant rows from a controlled comparison of
fork commit `35cc441` with its exact upstream merge base `8f1c069`. Ten samples
per version were interleaved on Go 1.26.2, Windows/amd64, AMD Ryzen 9 PRO 7940HS.
Of 55 shared benchmarks, seven improved significantly and none had a significant
timing regression.

| Benchmark | Upstream time | goja-perf time | Speedup | B/op (upstream → fork) | allocs/op (upstream → fork) |
|---|---:|---:|---:|---:|---:|
| `MainLoop` | 24.541 ms | 98.277 µs | 249.71× | 5,598,185 → 324 | 199,748 → 40 |
| `CallJS` | 267.10 ns | 93.56 ns | 2.86× | 224 → 0 | 3 → 0 |
| `CallNative` | 191.60 ns | 92.67 ns | 2.07× | 96 → 0 | 2 → 0 |
| `VmNOP2` | 1.483 ns | 0.7576 ns | 1.96× | 0 → 0 | 0 → 0 |
| `FuncCall` | 280.95 ns | 155.45 ns | 1.81× | 400 → 112 | 2 → 1 |
| `MapDelete` | 570.00 ns | 384.70 ns | 1.48× | 480 → 480 | 5 → 5 |
| `CallReflect` | 1.068 µs | 768.50 ns | 1.39× | 120 → 24 | 4 → 2 |

The table is evidence for these workloads and this machine, not a promise that
every program receives the same speedup. Benefits depend on workload shape,
warmup, data types, platform, and how often code crosses the Go/JavaScript
boundary.

## Install as a drop-in replacement

Keep existing imports unchanged:

```go
import "github.com/dop251/goja"
```

Require Goja normally, then replace it with a tagged goja-perf release:

```sh
go get github.com/dop251/goja@latest
go mod edit -replace=github.com/dop251/goja=github.com/maclof/goja-perf@v0.1.1
go mod tidy
go test ./...
```

The resulting `go.mod` contains a rule like:

```go.mod
replace github.com/dop251/goja => github.com/maclof/goja-perf v0.1.1
```

This repository deliberately retains `module github.com/dop251/goja` in its own
`go.mod`: existing source code and dependent modules continue to agree on one
Go type identity and no imports need rewriting. The consumer-side `replace`
selects this fork's source. Pin a release tag for reproducible builds. To test a
specific commit or branch before a release, substitute its pseudo-version or
`@master`; `go mod tidy` resolves a branch to a pseudo-version.

## Sandbox example

The default sandbox policy denies all policy-controlled built-ins and all host
capabilities. Each `Capabilities` map key becomes a JavaScript global name, and
its value is the Go function or value available at that name. If it is absent,
the script has no access to it. Standard JavaScript built-ins such as `JSON` and
`Math` are configured separately through `Builtins`. This is the sandbox-safe
replacement for calling `Runtime.Set` on the wrapped runtime. Grant only what a
script needs:

```go
sandbox, err := goja.NewSandbox(goja.SandboxPolicy{
	Builtins: goja.SandboxBuiltins{
		Allow: []string{"JSON", "Math"}, // allowlist (whitelist)
	},
	Capabilities: map[string]interface{}{
		"lookupPrice": func(id string) float64 { return prices[id] },
	},
	ExecutionTimeout: 100 * time.Millisecond,
})
if err != nil {
	return err // invalid policy
}

value, err := sandbox.RunString(`
	JSON.stringify({price: Math.round(lookupPrice("sku-42") * 100) / 100})
`)
if errors.Is(err, goja.ErrSandboxTimeout) {
	return fmt.Errorf("script timed out: %w", err)
}
if err != nil {
	return fmt.Errorf("script failed: %w", err)
}
_ = value
```

For blacklist-style policy, use
`SandboxBuiltins{AllowAll: true, Deny: []string{"Date", "eval"}}` instead.
Normal script-defined functions, closures, object methods, and class methods
still work. By default, only runtime compilation through `eval` and the
`Function` constructor family is blocked.
Read [SANDBOX.md](SANDBOX.md) before running untrusted code: granted Go values
are trusted, native Go calls cannot be preempted, and memory/CPU quotas still
require process or container limits.

## Benchmarking

```sh
go test ./...
go test -run '^$' -bench . -benchmem -count=10 ./...
```

The original upstream documentation follows.

---

goja
====

ECMAScript 5.1(+) implementation in Go.

[![Go Reference](https://pkg.go.dev/badge/github.com/dop251/goja.svg)](https://pkg.go.dev/github.com/dop251/goja)

Goja is an implementation of ECMAScript 5.1 in pure Go with emphasis on standard compliance and
performance.

This project was largely inspired by [otto](https://github.com/robertkrimen/otto).

The minimum required Go version is 1.25.

Features
--------

 * Full ECMAScript 5.1 support (including regex and strict mode).
 * Passes nearly all [tc39 tests](https://github.com/tc39/test262) for the features implemented so far. The goal is to
   pass all of them. See .tc39_test262_checkout.sh for the latest working commit id.
 * Capable of running Babel, Typescript compiler and pretty much anything written in ES5.
 * Sourcemaps.
 * Most of ES6 functionality, still work in progress, see https://github.com/dop251/goja/milestone/1?closed=1

Known incompatibilities and caveats
-----------------------------------

### JSON
`JSON.parse()` uses the standard Go library which operates in UTF-8. Therefore, it cannot correctly parse broken UTF-16
surrogate pairs, for example:

```javascript
JSON.parse(`"\\uD800"`).charCodeAt(0).toString(16) // returns "fffd" instead of "d800"
```

### Date
Conversion from calendar date to epoch timestamp uses the standard Go library which uses `int`, rather than `float` as per
ECMAScript specification. This means if you pass arguments that overflow int to the `Date()` constructor or  if there is
an integer overflow, the result will be incorrect, for example:

```javascript
Date.UTC(1970, 0, 1, 80063993375, 29, 1, -288230376151711740) // returns 29256 instead of 29312
```

FAQ
---

### How fast is it?

Although it's faster than many scripting language implementations in Go I have seen
(for example it's 6-7 times faster than otto on average) it is not a
replacement for V8 or SpiderMonkey or any other general-purpose JavaScript engine.
You can find some benchmarks [here](https://github.com/dop251/goja/issues/2).

### Why would I want to use it over a V8 wrapper?

It greatly depends on your usage scenario. If most of the work is done in javascript
(for example crypto or any other heavy calculations) you are definitely better off with V8.

If you need a scripting language that drives an engine written in Go so that
you need to make frequent calls between Go and javascript passing complex data structures
then the cgo overhead may outweigh the benefits of having a faster javascript engine.

Because it's written in pure Go there are no cgo dependencies, it's very easy to build and it
should run on any platform supported by Go.

It gives you a much better control over execution environment so can be useful for research.

### Is it goroutine-safe?

No. An instance of goja.Runtime can only be used by a single goroutine
at a time. You can create as many instances of Runtime as you like but
it's not possible to pass object values between runtimes.

### Where is setTimeout()/setInterval()?

setTimeout() and setInterval() are common functions to provide concurrent execution in ECMAScript environments, but the two functions are not part of the ECMAScript standard.
Browsers and NodeJS just happen to provide similar, but not identical, functions. The hosting application need to control the environment for concurrent execution, e.g. an event loop, and supply the functionality to script code.

There is a [separate project](https://github.com/dop251/goja_nodejs) aimed at providing some NodeJS functionality,
and it includes an event loop.

### Can you implement (feature X from ES6 or higher)?

I will be adding features in their dependency order and as quickly as time permits. Please do not ask
for ETAs. Features that are open in the [milestone](https://github.com/dop251/goja/milestone/1) are either in progress
or will be worked on next.

The ongoing work is done in separate feature branches which are merged into master when appropriate.
Every commit in these branches represents a relatively stable state (i.e. it compiles and passes all enabled tc39 tests),
however because the version of tc39 tests I use is quite old, it may be not as well tested as the ES5.1 functionality. Because there are (usually) no major breaking changes between ECMAScript revisions
it should not break your existing code. You are encouraged to give it a try and report any bugs found. Please do not submit fixes though without discussing it first, as the code could be changed in the meantime.

### How do I contribute?

Before submitting a pull request please make sure that:

- You followed ECMA standard as close as possible. If adding a new feature make sure you've read the specification,
do not just base it on a couple of examples that work fine.
- Your change does not have a significant negative impact on performance (unless it's a bugfix and it's unavoidable)
- It passes all relevant tc39 tests.

Current Status
--------------

 * There should be no breaking changes in the API, however it may be extended.
 * Some of the AnnexB functionality is missing.

Basic Example
-------------

Run JavaScript and get the result value.

```go
vm := goja.New()
v, err := vm.RunString("2 + 2")
if err != nil {
    panic(err)
}
if num := v.Export().(int64); num != 4 {
    panic(num)
}
```

Passing Values to JS
--------------------
Any Go value can be passed to JS using Runtime.ToValue() method. See the method's [documentation](https://pkg.go.dev/github.com/dop251/goja#Runtime.ToValue) for more details.

Exporting Values from JS
------------------------
A JS value can be exported into its default Go representation using Value.Export() method.

Alternatively it can be exported into a specific Go variable using [Runtime.ExportTo()](https://pkg.go.dev/github.com/dop251/goja#Runtime.ExportTo) method.

Within a single export operation the same Object will be represented by the same Go value (either the same map, slice or
a pointer to the same struct). This includes circular objects and makes it possible to export them.

Calling JS functions from Go
----------------------------
There are 2 approaches:

- Using [AssertFunction()](https://pkg.go.dev/github.com/dop251/goja#AssertFunction):
```go
const SCRIPT = `
function sum(a, b) {
    return +a + b;
}
`

vm := goja.New()
_, err := vm.RunString(SCRIPT)
if err != nil {
    panic(err)
}
sum, ok := goja.AssertFunction(vm.Get("sum"))
if !ok {
    panic("Not a function")
}

res, err := sum(goja.Undefined(), vm.ToValue(40), vm.ToValue(2))
if err != nil {
    panic(err)
}
fmt.Println(res)
// Output: 42
```
- Using [Runtime.ExportTo()](https://pkg.go.dev/github.com/dop251/goja#Runtime.ExportTo):
```go
const SCRIPT = `
function sum(a, b) {
    return +a + b;
}
`

vm := goja.New()
_, err := vm.RunString(SCRIPT)
if err != nil {
    panic(err)
}

var sum func(int, int) int
err = vm.ExportTo(vm.Get("sum"), &sum)
if err != nil {
    panic(err)
}

fmt.Println(sum(40, 2)) // note, _this_ value in the function will be undefined.
// Output: 42
```

The first one is more low level and allows specifying _this_ value, whereas the second one makes the function look like
a normal Go function.

Mapping struct field and method names
-------------------------------------
By default, the names are passed through as is which means they are capitalised. This does not match
the standard JavaScript naming convention, so if you need to make your JS code look more natural or if you are
dealing with a 3rd party library, you can use a [FieldNameMapper](https://pkg.go.dev/github.com/dop251/goja#FieldNameMapper):

```go
vm := goja.New()
vm.SetFieldNameMapper(TagFieldNameMapper("json", true))
type S struct {
    Field int `json:"field"`
}
vm.Set("s", S{Field: 42})
res, _ := vm.RunString(`s.field`) // without the mapper it would have been s.Field
fmt.Println(res.Export())
// Output: 42
```

There are two standard mappers: [TagFieldNameMapper](https://pkg.go.dev/github.com/dop251/goja#TagFieldNameMapper) and
[UncapFieldNameMapper](https://pkg.go.dev/github.com/dop251/goja#UncapFieldNameMapper), or you can use your own implementation.

Native Constructors
-------------------

In order to implement a constructor function in Go use `func (goja.ConstructorCall) *goja.Object`.
See [Runtime.ToValue()](https://pkg.go.dev/github.com/dop251/goja#Runtime.ToValue) documentation for more details.

Regular Expressions
-------------------

Goja uses the embedded Go regexp library where possible, otherwise it falls back to [regexp2](https://github.com/dlclark/regexp2).

Exceptions
----------

Any exception thrown in JavaScript is returned as an error of type *Exception. It is possible to extract the value thrown
by using the Value() method:

```go
vm := goja.New()
_, err := vm.RunString(`

throw("Test");

`)

if jserr, ok := err.(*Exception); ok {
    if jserr.Value().Export() != "Test" {
        panic("wrong value")
    }
} else {
    panic("wrong type")
}
```

If a native Go function panics with a Value, it is thrown as a Javascript exception (and therefore can be caught):

```go
var vm *Runtime

func Test() {
    panic(vm.ToValue("Error"))
}

vm = goja.New()
vm.Set("Test", Test)
_, err := vm.RunString(`

try {
    Test();
} catch(e) {
    if (e !== "Error") {
        throw e;
    }
}

`)

if err != nil {
    panic(err)
}
```

Interrupting
------------

```go
func TestInterrupt(t *testing.T) {
    const SCRIPT = `
    var i = 0;
    for (;;) {
        i++;
    }
    `

    vm := goja.New()
    time.AfterFunc(200 * time.Millisecond, func() {
        vm.Interrupt("halt")
    })

    _, err := vm.RunString(SCRIPT)
    if err == nil {
        t.Fatal("Err is nil")
    }
    // err is of type *InterruptError and its Value() method returns whatever has been passed to vm.Interrupt()
}
```

NodeJS Compatibility
--------------------

There is a [separate project](https://github.com/dop251/goja_nodejs) aimed at providing some of the NodeJS functionality.
