# benchjs — Goja vs V8 JavaScript-execution comparison suite

A reproducible, apples-to-apples comparison of **pure JavaScript execution** between
this Goja fork and V8 (via the locally installed Node.js). The identical JavaScript
workload sources under `workloads/` are executed by both engines; deterministic
checksums prove the engines compute identical results, and identical benchmark names
make the outputs `benchstat`- and table-comparable.

## Scope

**In scope:** JavaScript language execution only — object/array manipulation, JSON,
regex/text processing, numeric computation, plus engine setup categories (source
compilation, context creation + first eval).

**Explicitly out of scope:** Go↔JS host-boundary costs. Node cannot fairly represent
Goja's host boundary: calling a Go function from Goja is an in-process Go call, while
leaving V8 from Node crosses a C++/addon or IPC boundary. To keep the pure-JS scope
honest, steady-state timing on **both** engines calls one shared JavaScript block
function (see below), so the Go→JS transition happens exactly once per timed block on
the Goja side and never per iteration; the Node side has no host boundary at all. A
future phase will address boundary workloads with an appropriate methodology.

## Files

| file | role |
|---|---|
| `workloads/*.js` | The four shared workload sources, run verbatim by both engines |
| `goja_bench_test.go` | Goja side: steady/compile/context benchmarks + golden checksum tests |
| `node_driver.js` | V8 side: compiles, warms, samples, validates, writes results |
| `compare.js` | Pairs both engines' outputs; enforces checksums; flags unstable cells |
| `harness.json` | Single shared configuration (seeds, warmup, batch sizes, wrap, samples, vectors) |
| `goldens.json` | Cross-engine checksum fixture, recorded by `node_driver.js --update-goldens`, enforced by `TestGoldenChecksums` |
| `results/` | Output directory (contents gitignored): `goja.txt`, `goja2.txt`, `node.txt`, `node2.txt`, `node*.json` |

## Workloads

All workloads are self-contained ES5-style JavaScript using only fully specified
semantics (integer ops via `Math.imul`/`|0`, IEEE-754 `+`/`*` in fixed order,
insertion-ordered objects, total-order sort comparators, spec-defined JSON output),
so V8 and Goja must agree bit-for-bit. Each exports `createState(seed)`,
`iterate(state, i)`, `runBatch(state, start, count)`, `runBlock(...)` and
`run(seed, iters)`:

- **`json_pipeline`** — rotate a copy of an order list, `JSON.stringify`, `JSON.parse`,
  per-order totals/discount filtering, region aggregation + sort, `JSON.stringify` of
  the summary. 80 orders × 3–6 items per iteration.
- **`business_data`** — windowed aggregation of 1,200 transactions into plain objects
  and a `Map`, top-5 sort, moving averages, report-string building.
- **`text_regex`** — log-line processing over a 900-line synthetic corpus:
  field extraction (`exec`), email validation (`test`), ID normalization and
  whitespace collapsing (`replace`), 160-line window per iteration, output join + hash.
- **`matmul_compute`** — 20×20 double-precision matrix multiply over plain nested
  arrays with a deterministic per-iteration perturbation (compute-heavy representative).

Every `iterate(state, i)` sees iteration-dependent inputs (rotation offsets,
thresholds, element perturbations) and mutates bounded state (capped ring buffer,
counters), so no engine can constant-fold across iterations and results are consumed
by the checksum accumulated inside the batch/block functions.

The workload sizes are tuned for the benchmarks (≥10 ms per Goja iteration);
the golden/contract vectors in `harness.json` are deliberately tiny so the standing
`go test` validation stays cheap — they are **not** an exhaustive test of large
iteration counts, only of cross-engine semantic equality (multiple seeds, counts,
start offsets and wrap behavior, all compared exactly).

## Methodology

- **Identical inputs, identical code.** Both engines read the same files, run them
  sloppy mode (`vm.Script` vs `goja.Compile(..., false)`), and call the same
  functions with the same seeds from `harness.json`.
- **Steady state = one shared JS timed-block call per timed unit.** Each workload
  implements `runBlock(state, firstStart, batches, batchIters, wrap)`: it performs
  the ENTIRE batches loop inside JavaScript — batch k executes
  `runBatch(state, firstStart + (k % wrap) * batchIters, batchIters)` — and returns a
  checksum over the block. The Go benchmark's timed region is **one** `runBlock`
  call over `b.N` batches; each Node timed sample is **one** `runBlock` call over a
  calibrated batch count (block ≥ `steadyBlockTargetMs`). The identical JS function,
  wrap policy and window bounds execute on both engines; host-call overhead is one
  amortized call per block on Goja and zero on Node. Metrics are normalized to
  **ns/iter**: one underlying workload iteration (plus B/iter and allocs/iter from
  MemStats deltas). `b.ReportAllocs()` additionally emits the standard per-call
  B/op and allocs/op; Go's framework still reports ns/op per `runBlock` call, and
  the cross-engine comparison deliberately uses ns/iter instead, because one Go
  benchmark op is a whole block of `batchIters` workload iterations.
- **Symmetric state handling.** Both engines: fresh `createState(steadySeed)` per
  timed unit (Node: per sample; Go: per `-count` invocation, outside the timed
  region), warmed by the identical single `runBatch` call over `warmupIters`, then
  windows starting at `warmupIters` with the shared `(k % wrap)` policy. No engine
  measures an unbounded state progression, and per-iterate work is
  position-independent by construction.
- **Warmup.** The measured state receives the identical `warmupIters` (100) semantic
  `runBatch` iterations on both engines. On top of that, the exact timed function
  (`runBlock`) is warmed untimed on throwaway states: a tiny fixed warmup on the
  Goja side (so its `runBlock` code is not first-hit cold) and, V8-only, a
  time-budget extension until `warmupTimeBudgetMsV8` (2 s) with recalibration of the
  sample block size afterwards, so V8 tiers `runBlock` up fully before anything is
  measured (observed 830–21k warmup iterations). Warmup is never timed. This
  asymmetry favors V8, i.e. it makes the comparison harder, not softer, for Goja.
- **Setup categories.**
  - *compile*: every timed compilation uses a **uniquely tagged but semantically
    equivalent variant** (unique leading comment + unique filename); all variant
    strings and filenames for a block are **prebuilt outside the timed region** on
    both engines, with no artificial pool ceiling (Go prebuilds `b.N` variants per
    invocation; Node prebuilds a calibrated pool per sample). Unique sources defeat
    V8's compilation cache. Fairness caveat, deliberate: `new vm.Script` measures
    V8's frontend (parse + lazy bytecode), while `goja.Compile` eagerly builds the
    full AST + bytecode. This category compares setup/frontend behavior, not
    full-code-generation equivalence.
  - *context-setup*: fresh `vm.createContext` + first script execution vs fresh
    `goja.New()` + first `RunProgram` of a pre-compiled program (Node samples blocked
    until ≥ `contextBlockTargetMs`). Compares engine context-construction design
    points, not JS speed.
- **Sampling discipline.** ≥15 samples required (`samplesPerBenchmark`: 20) per
  benchmark per engine, in **two rounds with engine order alternated** (round 1
  Node-first, round 2 Goja-first, each pair back-to-back). compare.js reports
  per-round medians, combined median, CV, **median absolute deviation relative to
  the median (MAD%)** and **p10..p90** — computed separately over each engine's own
  samples. Goja and Node samples are measured in separate processes and are **not
  paired**. Benchmark names are compared with the optional `-N` CPU suffix stripped
  non-greedily, so results from machines with different CPU counts still pair.
  Cells with CV > 25% are flagged **UNSTABLE** in a conspicuous warning block; they
  must be read as order-of-magnitude only.
- **Validation, fail-closed.** `node_driver.js --update-goldens` records, with a
  fresh state per vector: `run(seed, iters)` checksums, the `runBatch(start, count)`
  grid, and the `runBlock(start, batches, batchIters, wrap)` grid (vectors with
  `batches > wrap` exercise the wrap policy) into `goldens.json`.
  `TestGoldenChecksums` recomputes all of them under Goja and fails `go test` on any
  divergence. compare.js re-checks every Node round against the same fixture; the
  Node driver additionally requires every steady sample of a workload to produce the
  identical block checksum. `TestWorkloadBlockContract` proves within-Goja that
  `runBlock` equals a manual wrap-policy loop over `runBatch` and is wrap-sensitive;
  `TestWorkloadContract` proves `run(seed, n) ≡ runBatch(createState(seed), 0, n)`.
- **Process startup** is excluded from every number; a separately labeled probe
  measures it for reference only.

## Reproducing

```sh
# Round 1: Node first
node benchjs\node_driver.js --samples 20 --startup          # -> results\node.{txt,json}
go test ./benchjs -run=NONE -bench . -benchmem -count 20 -timeout 40m |
  Out-File -Encoding ascii benchjs\results\goja.txt          # note: PS 5.1 default redirection is UTF-16

# Round 2: Goja first (order alternated)
go test ./benchjs -run=NONE -bench . -benchmem -count 20 -timeout 40m |
  Out-File -Encoding ascii benchjs\results\goja2.txt
node benchjs\node_driver.js --samples 20 --tag 2             # -> results\node2.{txt,json}

# Compare (exits non-zero on checksum mismatch or sample shortfall)
node benchjs\compare.js            # human table + warnings
node benchjs\compare.js --markdown # markdown table

# Optional: benchstat for within-engine distributions (it does not pair the
# engines' files; cross-engine ratios come from compare.js)
benchstat benchjs\results\goja.txt benchjs\results\node.txt

# Regenerate the checksum fixture (after intentional workload changes only)
node benchjs\node_driver.js --update-goldens --quick

# Standing correctness checks (cheap: ~5-7s)
go test ./benchjs -run 'TestWorkloadContract|TestWorkloadBlockContract|TestGoldenChecksums'
```

## Results (two alternated rounds, 20 samples/benchmark/engine)

Environment: Windows 11 Pro, amd64, 16 CPUs, AMD Ryzen 9 PRO 7940HS;
Go 1.26.2 windows/amd64; Node v24.4.0 / V8 13.6.233.10-node.17.
Steady rows: ns/iter (one workload iteration); setup rows: ns/op.
Medians are (round 1 / round 2); p10..p90 and CV/MAD span both rounds' samples.

| benchmark | unit | goja med (r1/r2) | goja p10..p90 | goja CV/MAD | node med (r1/r2) | node p10..p90 | node CV/MAD | node/goja (r1/r2) |
|---|---|---:|---:|---:|---:|---:|---:|---:|
| `BenchmarkBusinessData` | ns/iter | 2,756,458 / 1,908,466 | 1.66M..3.89M | 70% **UNSTABLE** / 23% | 111,475 / 102,197 | 80.4k..124.5k | 19% / 11% | 24.7x / 18.7x |
| `BenchmarkJSONPipeline` | ns/iter | 12,458,918 / 13,836,754 | 9.99M..34.8M | 63% **UNSTABLE** / 20% | 1,549,805 / 1,570,723 | 1.23M..2.01M | 23% / 11% | 8.0x / 8.8x |
| `BenchmarkMatmulCompute` | ns/iter | 6,728,692 / 6,437,180 | 5.45M..9.83M | 29% **UNSTABLE** / 15% | 186,697 / 115,601 | 102k..196k | 28% **UNSTABLE** / 21% | 36.0x / 55.7x |
| `BenchmarkTextRegex` | ns/iter | 11,677,906 / 8,065,636 | 7.49M..18.3M | 81% **UNSTABLE** / 22% | 1,803,783 / 1,449,898 | 1.21M..2.12M | 25% / 18% | 6.5x / 5.6x |
| `BenchmarkCompileBusinessData` | ns/op | 1,952,966 / 570,511 | 515k..2.88M | 159% **UNSTABLE** / 51% | 214,901 / 142,929 | 115k..240k | 27% **UNSTABLE** / 16% | 9.1x / 4.0x |
| `BenchmarkCompileJSONPipeline` | ns/op | 624,263 / 801,046 | 576k..1.14M | 209% **UNSTABLE** / 15% | 154,704 / 141,931 | 123k..205k | 25% **UNSTABLE** / 15% | 4.0x / 5.6x |
| `BenchmarkCompileMatmulCompute` | ns/op | 752,219 / 512,318 | 346k..1.01M | 52% **UNSTABLE** / 25% | 164,680 / 161,919 | 119k..219k | 24% / 19% | 4.6x / 3.2x |
| `BenchmarkCompileTextRegex` | ns/op | 1,516,666 / 539,264 | 506k..1.79M | 50% **UNSTABLE** / 48% | 196,870 / 168,284 | 149k..260k | 24% / 14% | 7.7x / 3.2x |
| `BenchmarkContextSetupBusinessData` | ns/op | 34,257 / 20,794 | 17.7k..63.0k | 67% **UNSTABLE** / 27% | 945,038 / 751,310 | 652k..1.07M | 35% **UNSTABLE** / 15% | 0.04x / 0.03x |
| `BenchmarkContextSetupJSONPipeline` | ns/op | 28,517 / 18,526 | 15.8k..37.6k | 40% **UNSTABLE** / 22% | 883,817 / 621,848 | 586k..1.02M | 23% / 16% | 0.03x / 0.03x |
| `BenchmarkContextSetupMatmulCompute` | ns/op | 101,500 / 13,583 | 12.1k..128k | 104% **UNSTABLE** / 70% | 1,266,581 / 1,000,638 | 764k..1.50M | 26% **UNSTABLE** / 19% | 0.08x / 0.01x |
| `BenchmarkContextSetupTextRegex` | ns/op | 219,441 / 45,046 | 37.2k..266k | 78% **UNSTABLE** / 64% | 853,622 / 779,116 | 662k..1.15M | 23% / 14% | 0.26x / 0.06x |

Checksum validation: **PASS, 120/120** (20 `run` vectors + 32 batch-grid entries +
8 block-grid entries per Node round, checked for both rounds against
`goldens.json`; the same fixture is enforced against Goja by `TestGoldenChecksums`).
Steady-state geomean node/goja per round: **14.7x (round 1), 15.0x (round 2)** —
the two alternated rounds agree closely. After warming the exact timed function
through V8's budget, the Node side is well-behaved (CV 19–28%, MAD 11–21%); the
Goja side remains noisier on this shared machine (CV 29–209%), so **Goja cells are
indicative, not precise**, and all steady cells are flagged UNSTABLE by the tool.

Statements the data supports:

- Steady state: V8 leads on every workload; geomean ~15x, consistent across
  alternated rounds. Per-workload ratios: json ~8–9x, business ~19–25x,
  regex ~5.5–6.5x, matmul ~36–56x (numeric loops favor TurboFan the most).
- Compile: with cache-defeating prebuilt unique variants, V8's frontend is ~2–9x
  faster than goja's eager full compilation. (An earlier draft reported ~120–280x;
  that measured V8's compilation cache and was an artifact.)
- Context setup: reversed — a fresh Goja runtime is ~25–60x cheaper to construct
  than a fresh V8 context. Relevant to hosts creating many short-lived isolated
  engines.
- Goja allocations per steady iteration (MemStats deltas, CV ≈ 0 across samples; no
  V8-side counterpart is captured): JSON ~1.68 MB / ~74.5k allocs, regex ~1.1 MB /
  ~55k, matmul ~283 KB / ~36k, business ~84 KB / ~7.2k.

Separately labeled process-startup probe (excluded from all tables):
`node -e "0"` spawn ≈ 90–103 ms median (7 samples per round).

## Limitations and fairness risks

1. **Goja-side numbers remain noisy on this machine** (shared Windows laptop;
   CV 29–209% across rounds while the warmed V8 side sits at 19–28%). Ratios are
   quoted per round and remain indicative; publishable precision needs a quiet
   machine or CI runner. The harness needs no changes for that.
2. **Different measurement machinery per engine** (Go: one timed `runBlock` call per
   adaptive `b.N` batch count; Node: one timed `runBlock` call per calibrated
   sample). Both normalize to ns/iter over identical JS loops with identical wrap
   policy, fresh state and identical warmup per timed unit.
3. **Warmup asymmetry by design**: identical minimum iterations on the measured
   state; the V8-only engine-level budget additionally warms the exact timed
   function on throwaway states. Under-warming V8 under-reports its advantage, so
   this favors V8.
4. **Compile category is frontend/setup behavior only** (V8 lazy vs goja eager);
   do not quote it as total code-generation cost. First-execution tier-up surfaces
   in steady state, not here.
5. **No V8-side allocation counts** (possible follow-up: `PerformanceObserver` GC
   accounting). Different GCs (Go's GC manages Goja's heap; V8 has its own); not
   compared.
6. **Context-setup compares design points**, not language speed.
7. **Window/progression symmetry is by construction, not proof**: per-iterate cost
   is position-independent by design (bounded rotations, capped ring, value-only
   drift) and the wrap policy is shared and cross-validated, but a workload whose
   later iterations are genuinely more expensive would need its own symmetry check.
8. **Workload sizing** targets ≥10 ms per Goja iteration for benchmark practicality;
   ratios should not be extrapolated to whole programs.

## Adding a workload

1. Add `workloads/<name>.js` following the existing contract (`createState`,
   `iterate`, `runBatch`, `runBlock`, `run`, completion-value return,
   `__workloads` registration), using only cross-engine-deterministic semantics.
   Keep per-iterate cost independent of the iteration index.
2. Add the name to `workloadNames` in `goja_bench_test.go` plus the three benchmark
   funcs; add it to `ALL_WORKLOADS` and `batchIters` in the driver/harness. Names
   must match verbatim (see `PASCAL_OVERRIDES`).
3. `node node_driver.js --update-goldens` (Node records the fixture), then
   `go test ./benchjs` must pass, then re-measure both engines in alternated rounds.
