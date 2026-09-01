# Goja Performance Project

## Mission

This repository is a performance-focused fork of `github.com/dop251/goja`.
Build representative Go benchmarks, find measurable bottlenecks, and reduce
execution time and allocations without changing JavaScript or Go API behavior.

## Roles

- The primary agent is a coordinator only. It plans work, delegates every code
  and test change to the Go performance subagent, reviews evidence and diffs,
  and creates Git commits.
- The primary agent must not edit implementation or test files and must not run
  tests, benchmarks, profilers, formatters, or other development commands.
- The Go performance subagent owns all implementation, benchmark, test,
  profiling, formatting, and verification work.
- The Go performance subagent must not create commits or delegate to another
  agent. It reports its changes and evidence to the primary agent.

## Benchmark Coverage

Maintain benchmarks that cover at least these workloads:

- JavaScript-only computation and function calls.
- JavaScript repeatedly calling Go-defined functions and methods.
- Go repeatedly calling JavaScript functions through the public Goja API.
- Mixed workloads that cross the Go/JavaScript boundary in both directions.
- Representative argument and return values, including primitives, objects,
  slices, maps, and structs where useful.
- Runtime setup/compilation and steady-state execution as separate benchmarks.

Benchmarks must consume or validate results so work cannot be optimized away.
Keep setup outside the timed section unless setup is the behavior being
measured. Use `b.ReportAllocs()` and avoid I/O, sleeps, and nondeterminism in
timed sections.

## Optimization Workflow

1. Establish a repeatable baseline before changing implementation code.
2. Profile the relevant benchmark and identify a concrete bottleneck.
3. Make the smallest targeted optimization that preserves behavior.
4. Run focused correctness tests, the full suite, and the same benchmark.
5. Compare multiple samples, preferably with `benchstat`, and report time and
   allocation changes along with the exact commands and environment.
6. Revert ineffective complexity rather than keeping speculative fast paths.

Do not weaken assertions, skip behavior, cache across operations unrealistically,
or specialize production code only for benchmark constants. Treat regressions in
unrelated workloads, correctness, or public compatibility as failures.

## Verification

The coding subagent should run, as applicable:

```text
gofmt -w <changed-go-files>
go test ./...
go test -run '^$' -bench '<BenchmarkPattern>' -benchmem -count=10 ./...
```

Record the Go version, OS/architecture, benchmark command, before/after samples,
and profiler findings. Commit generated profiles or benchmark binaries only when
explicitly requested.

## Git

Keep commits small and focused. Benchmark coverage should normally be committed
before or separately from implementation optimizations so performance changes
remain reviewable. Never rewrite unrelated user changes.
