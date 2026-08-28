package benchjs_test

// Goja-side harness for the benchjs cross-engine comparison suite.
//
// This file runs the exact same JavaScript workload sources that
// benchjs/node_driver.js runs on Node/V8:
//
//   - Benchmark<Workload>             warmed steady-state execution. The timed
//                                     region is ONE call to the workload's
//                                     shared JavaScript runBlock(state,
//                                     firstStart, batches, batchIters, wrap),
//                                     which performs the ENTIRE batches loop,
//                                     the (k % wrap) start-window policy and
//                                     checksum accumulation inside JS - the
//                                     exact function and policy the Node
//                                     driver calls once per timed sample.
//                                     b.ReportAllocs() emits the standard
//                                     per-call B/op and allocs/op; the
//                                     cross-engine comparison deliberately
//                                     uses the normalized ns/iter (plus
//                                     B/iter, allocs/iter) metrics instead,
//                                     because one Go benchmark op is a whole
//                                     block of batchIters workload iterations.
//   - BenchmarkCompile<Workload>      goja.Compile of b.N uniquely tagged but
//                                     semantically equivalent source variants
//                                     (and filenames), prebuilt OUTSIDE the
//                                     timed region. Unique sources defeat any
//                                     compilation caching; goja performs an
//                                     eager full parse + AST + bytecode
//                                     compilation (frontend/setup behavior,
//                                     not full-code-generation equivalence).
//   - BenchmarkContextSetup<Workload> fresh goja.Runtime creation plus first
//                                     program execution of a pre-compiled
//                                     program.
//
// TestGoldenChecksums validates that Goja produces exactly the same
// deterministic checksums as the fixtures recorded by Node/V8 in
// goldens.json - for run(seed, iters), the runBatch grid, and the runBlock
// grid (with batches > wrap, exercising the wrap policy) - so any behavioral
// divergence between the engines fails go test. The validation vectors are
// intentionally small (see harness.json): the workload sizes are tuned for
// the benchmarks, not for exhaustive golden testing; the small vectors still
// cover multiple seeds/counts, start offsets and wrap behavior with exact
// cross-engine equality.
//
// Methodology details are documented in benchjs/README.md.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/dop251/goja"
)

// workloadNames must match the *.js files in the workloads directory and the
// names used by node_driver.js.
var workloadNames = []string{
	"json_pipeline",
	"business_data",
	"text_regex",
	"matmul_compute",
}

type harnessConfig struct {
	SteadySeed           int64            `json:"steadySeed"`
	WarmupIters          int64            `json:"warmupIters"`
	WarmupTimeBudgetMsV8 float64          `json:"warmupTimeBudgetMsV8"`
	WarmupSafetyCapIters int64            `json:"warmupSafetyCapIters"`
	SteadyBlockTargetMs  float64          `json:"steadyBlockTargetMs"`
	CompileBlockTargetMs float64          `json:"compileBlockTargetMs"`
	ContextBlockTargetMs float64          `json:"contextBlockTargetMs"`
	SamplesPerBenchmark  int              `json:"samplesPerBenchmark"`
	MinSamplesRequired   int              `json:"minSamplesRequired"`
	BatchIters           map[string]int64 `json:"batchIters"`
	SteadyWindowWrapOps  int64            `json:"steadyWindowWrapOps"`
	ValidationVectors    [][2]int64       `json:"validationVectors"`
	BatchVectors         struct {
		Starts []int64 `json:"starts"`
		Counts []int64 `json:"counts"`
	} `json:"batchVectors"`
	BlockVectors []struct {
		Start   int64 `json:"start"`
		Batches int64 `json:"batches"`
		Wrap    int64 `json:"wrap"`
	} `json:"blockVectors"`
	SpotCheckSeed  int64 `json:"spotCheckSeed"`
	SpotCheckIters int64 `json:"spotCheckIters"`
}

type goldensFile struct {
	GeneratedBy string                       `json:"generatedBy"`
	Comment     string                       `json:"comment"`
	Checksums   map[string]map[string]uint32 `json:"checksums"`
	Batch       map[string]map[string]uint32 `json:"batchChecksums"`
	Block       map[string]map[string]uint32 `json:"blockChecksums"`
}

func loadHarnessConfig(tb testing.TB) harnessConfig {
	tb.Helper()
	data, err := os.ReadFile("harness.json")
	if err != nil {
		tb.Fatalf("read harness.json: %v", err)
	}
	var cfg harnessConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		tb.Fatalf("parse harness.json: %v", err)
	}
	if cfg.WarmupIters <= 0 || len(cfg.ValidationVectors) == 0 || cfg.SteadyWindowWrapOps <= 0 {
		tb.Fatalf("harness.json has invalid warmup/validation settings: %+v", cfg)
	}
	return cfg
}

func (c harnessConfig) batchIters(tb testing.TB, name string) int64 {
	tb.Helper()
	n, ok := c.BatchIters[name]
	if !ok || n <= 0 {
		tb.Fatalf("harness.json: missing/invalid batchIters for workload %s", name)
	}
	return n
}

func workloadSource(tb testing.TB, name string) string {
	tb.Helper()
	data, err := os.ReadFile(filepath.Join("workloads", name+".js"))
	if err != nil {
		tb.Fatalf("read workload %s: %v", name, err)
	}
	return string(data)
}

func compileWorkload(tb testing.TB, name, src string) *goja.Program {
	tb.Helper()
	prog, err := goja.Compile(name+".js", src, false)
	if err != nil {
		tb.Fatalf("compile %s: %v", name, err)
	}
	return prog
}

// evalWorkload runs the workload source in rt and returns the workload object.
// The workload file returns itself as the completion value; the fallback goes
// through the globalThis.__workloads registry it also installs.
func evalWorkload(tb testing.TB, rt *goja.Runtime, prog *goja.Program, name string) *goja.Object {
	tb.Helper()
	v, err := rt.RunProgram(prog)
	if err != nil {
		tb.Fatalf("run %s: %v", name, err)
	}
	if o, ok := v.(*goja.Object); ok && o != nil {
		if n := o.Get("name"); n != nil && n.String() == name {
			return o
		}
	}
	registry := rt.GlobalObject().Get("__workloads")
	if reg, ok := registry.(*goja.Object); ok && reg != nil {
		if o, ok := reg.Get(name).(*goja.Object); ok && o != nil {
			return o
		}
	}
	tb.Fatalf("workload %s did not produce a usable workload object", name)
	return nil
}

type workloadHandle struct {
	rt          *goja.Runtime
	state       *goja.Object
	createState goja.Callable
	runBatch    goja.Callable
	runBlock    goja.Callable
	run         goja.Callable
	batchSize   int64
}

// setupWorkload performs everything a benchmark needs before its timed
// section: source read, program compile, runtime creation, workload eval and
// steady-state creation. Callers invoke it before b.ResetTimer().
func setupWorkload(tb testing.TB, name string, seed int64) workloadHandle {
	tb.Helper()
	cfg := loadHarnessConfig(tb)
	src := workloadSource(tb, name)
	prog := compileWorkload(tb, name, src)
	rt := goja.New()
	wl := evalWorkload(tb, rt, prog, name)
	runBatch, ok := goja.AssertFunction(wl.Get("runBatch"))
	if !ok {
		tb.Fatalf("workload %s: runBatch is not callable", name)
	}
	runBlock, ok := goja.AssertFunction(wl.Get("runBlock"))
	if !ok {
		tb.Fatalf("workload %s: runBlock is not callable", name)
	}
	run, ok := goja.AssertFunction(wl.Get("run"))
	if !ok {
		tb.Fatalf("workload %s: run is not callable", name)
	}
	createState, ok := goja.AssertFunction(wl.Get("createState"))
	if !ok {
		tb.Fatalf("workload %s: createState is not callable", name)
	}
	w := workloadHandle{rt: rt, createState: createState, runBatch: runBatch, runBlock: runBlock, run: run,
		batchSize: cfg.batchIters(tb, name)}
	return w.withFreshState(tb, seed)
}

// withFreshState rebuilds only the state on the existing runtime (no recompilation),
// so validation loops can have a fresh state per vector cheaply.
func (w workloadHandle) withFreshState(tb testing.TB, seed int64) workloadHandle {
	tb.Helper()
	stateVal, err := w.createState(goja.Undefined(), w.rt.ToValue(seed))
	if err != nil {
		tb.Fatalf("createState(%d) failed: %v", seed, err)
	}
	state, ok := stateVal.(*goja.Object)
	if !ok {
		tb.Fatalf("createState(%d) returned a non-object", seed)
	}
	w.state = state
	return w
}

// warmFunction runs a tiny untimed warmup of the exact timed-block function on
// a throwaway state so runBlock's code is not first-hit cold, symmetric with
// the Node driver's runBlock engine warmup (which extends this through the
// V8-only JIT budget). The measured state is untouched by this.
func (w workloadHandle) warmFunction(tb testing.TB, warmupIters int64, wrap int64) uint32 {
	tb.Helper()
	thr := w.withFreshState(tb, 1)
	return thr.blockChecksum(tb, warmupIters, 2, wrap)
}

// warm runs the symmetric warmup: one runBatch call covering the shared
// warmupIters iterations on the measured state, exactly like the Node driver's
// per-state warmup.
func (w workloadHandle) warm(tb testing.TB, warmupIters int64) uint32 {
	tb.Helper()
	return w.batchChecksum(tb, 0, warmupIters)
}

func (w workloadHandle) batchChecksum(tb testing.TB, start, count int64) uint32 {
	tb.Helper()
	v, err := w.runBatch(goja.Undefined(), w.state, w.rt.ToValue(start), w.rt.ToValue(count))
	if err != nil {
		tb.Fatalf("runBatch(start=%d, count=%d) failed: %v", start, count, err)
	}
	return uint32(v.ToInteger())
}

// blockChecksum calls the shared JS timed-block function once.
func (w workloadHandle) blockChecksum(tb testing.TB, firstStart, batches, wrap int64) uint32 {
	tb.Helper()
	v, err := w.runBlock(goja.Undefined(), w.state, w.rt.ToValue(firstStart), w.rt.ToValue(batches),
		w.rt.ToValue(w.batchSize), w.rt.ToValue(wrap))
	if err != nil {
		tb.Fatalf("runBlock(start=%d, batches=%d, batchIters=%d, wrap=%d) failed: %v",
			firstStart, batches, w.batchSize, wrap, err)
	}
	return uint32(v.ToInteger())
}

func (w workloadHandle) checksum(tb testing.TB, seed, iters int64) uint32 {
	tb.Helper()
	v, err := w.run(goja.Undefined(), w.rt.ToValue(seed), w.rt.ToValue(iters))
	if err != nil {
		tb.Fatalf("run(%d, %d) failed: %v", seed, iters, err)
	}
	return uint32(v.ToInteger())
}

// mixGoja mirrors the workloads' mix(acc, h) = (Math.imul(acc, 31) + h) | 0 so
// the contract test can compute expected block checksums with its own loop.
func mixGoja(acc uint32, h uint32) uint32 {
	return uint32(int32(acc*31) + int32(h))
}

// benchmarkSink consumes benchmark results so they cannot be optimized away;
// a checksum of zero is a perfectly legal value, so no sentinel comparisons.
var benchmarkSink uint32

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestWorkloadContract(t *testing.T) {
	cfg := loadHarnessConfig(t)
	for _, name := range workloadNames {
		t.Run(name, func(t *testing.T) {
			w := setupWorkload(t, name, cfg.SteadySeed)
			// run(seed, iters) must equal createState(seed) + runBatch(0..iters).
			for _, vec := range [][2]int64{{cfg.SpotCheckSeed, 6}, {3, 4}} {
				viaRun := w.checksum(t, vec[0], vec[1])
				viaBatch := w.withFreshState(t, vec[0]).batchChecksum(t, 0, vec[1])
				if viaRun != viaBatch {
					t.Fatalf("run(%d, %d) = %d differs from runBatch(0..%d) = %d",
						vec[0], vec[1], viaRun, vec[1], viaBatch)
				}
			}
			// Determinism: identical batches on identical FRESH states must
			// give identical checksums. (runBatch mutates the state, so
			// calling it twice on one state is not the same computation -
			// both engines diverge identically there by design.)
			a := w.withFreshState(t, cfg.SteadySeed).batchChecksum(t, cfg.WarmupIters, 4)
			if b2 := w.withFreshState(t, cfg.SteadySeed).batchChecksum(t, cfg.WarmupIters, 4); a != b2 {
				t.Fatalf("workload %s is not deterministic: %d != %d", name, a, b2)
			}
			if a == w.withFreshState(t, cfg.SteadySeed).batchChecksum(t, cfg.WarmupIters+1, 4) {
				t.Fatalf("workload %s batch checksum ignores the start offset", name)
			}
			benchmarkSink ^= a
		})
	}
}

// TestWorkloadBlockContract validates the shared timed-block function within
// this engine: runBlock must equal a manual wrap-policy loop over runBatch
// with the same mixing, and the wrap must actually cycle (batches > wrap).
func TestWorkloadBlockContract(t *testing.T) {
	cfg := loadHarnessConfig(t)
	for _, name := range workloadNames {
		t.Run(name, func(t *testing.T) {
			batchSize := cfg.batchIters(t, name)
			const batches = int64(6)
			const wrap = int64(3) // batches > wrap: the window wrap is exercised

			w := setupWorkload(t, name, cfg.SteadySeed)
			got := w.blockChecksum(t, cfg.WarmupIters, batches, wrap)

			// Manual reference loop: identical sequence of runBatch calls on a
			// fresh identical state, with runBlock's checksum initialization
			// and mixing mirrored using uint32-wrapping arithmetic (uint32
			// multiply/xor are bit-identical to Math.imul/^(...) | 0). The
			// init goes through runtime variables because the constant
			// expression would overflow at compile time - exactly the wrap
			// JS performs at runtime.
			wd := setupWorkload(t, name, cfg.SteadySeed)
			batchesVar := batches
			sizeVar := batchSize
			expect := uint32(uint32(batchesVar)*uint32(2654435761) ^ uint32(sizeVar)*uint32(0x9e3779b9))
			for k := int64(0); k < batches; k++ {
				start := cfg.WarmupIters + (k%wrap)*batchSize
				expect = mixGoja(expect, wd.batchChecksum(t, start, batchSize))
			}
			if got != expect {
				t.Fatalf("workload %s: runBlock = %d differs from manual wrap loop = %d",
					name, got, expect)
			}

			// Determinism on a fresh state.
			if again := w.withFreshState(t, cfg.SteadySeed).blockChecksum(t, cfg.WarmupIters, batches, wrap); again != got {
				t.Fatalf("workload %s: runBlock is not deterministic: %d != %d", name, got, again)
			}
			// Different wrap must change the visited windows and the checksum.
			if alt := w.withFreshState(t, cfg.SteadySeed).blockChecksum(t, cfg.WarmupIters, batches, wrap+1); alt == got {
				t.Fatalf("workload %s: runBlock checksum ignores the wrap parameter", name)
			}
		})
	}
}

// TestGoldenChecksums proves cross-engine semantic equivalence: goldens.json
// is recorded from Node/V8 by node_driver.js and this test recomputes every
// checksum under Goja - run(seed, iters) vectors, the runBatch grid, and the
// runBlock grid (with batches > wrap to exercise the wrap policy). Set
// BENCHJS_UPDATE_GOLDENS=1 to regenerate the file from the Goja side
// (emergency use only - the canonical source of the goldens is
// node_driver.js).
func TestGoldenChecksums(t *testing.T) {
	cfg := loadHarnessConfig(t)

	update := os.Getenv("BENCHJS_UPDATE_GOLDENS") == "1"
	// want* stay untouched so the comparisons below can never degenerate into
	// comparing a value with itself.
	var want, wantBatch, wantBlock map[string]map[string]uint32
	if !update {
		data, err := os.ReadFile("goldens.json")
		if err != nil {
			t.Fatalf("read goldens.json (regenerate with: node node_driver.js --update-goldens): %v", err)
		}
		var g goldensFile
		if err := json.Unmarshal(data, &g); err != nil {
			t.Fatalf("parse goldens.json: %v", err)
		}
		if len(g.Checksums) == 0 || len(g.Batch) == 0 || len(g.Block) == 0 {
			t.Fatal("goldens.json is missing checksum sections (regenerate with: node node_driver.js --update-goldens)")
		}
		want, wantBatch, wantBlock = g.Checksums, g.Batch, g.Block
	}

	computed := map[string]map[string]uint32{}
	computedBatch := map[string]map[string]uint32{}
	computedBlock := map[string]map[string]uint32{}
	for _, name := range workloadNames {
		// One handle per workload; vectors get fresh states via withFreshState
		// (cheap: no recompile) so nothing is ever measured on mutated state.
		w := setupWorkload(t, name, cfg.SpotCheckSeed)
		computed[name] = map[string]uint32{}
		for _, vec := range cfg.ValidationVectors {
			key := fmt.Sprintf("%d:%d", vec[0], vec[1])
			got := w.checksum(t, vec[0], vec[1])
			computed[name][key] = got
			if !update {
				verify(t, name, "run", want, name, key, got)
			}
		}

		// runBatch grid on the shared steady seed: a FRESH state per vector,
		// so every entry is the order-independent function
		// runBatch(createState(steadySeed), start, count).
		batchSize := cfg.batchIters(t, name)
		computedBatch[name] = map[string]uint32{}
		starts := append([]int64{cfg.WarmupIters, cfg.WarmupIters + batchSize}, cfg.BatchVectors.Starts...)
		seen := map[string]bool{}
		for _, start := range starts {
			for _, count := range cfg.BatchVectors.Counts {
				key := fmt.Sprintf("%d:%d", start, count)
				if seen[key] {
					continue
				}
				seen[key] = true
				got := w.withFreshState(t, cfg.SteadySeed).batchChecksum(t, start, count)
				computedBatch[name][key] = got
				if !update {
					verify(t, name, "batch", wantBatch, name, key, got)
				}
			}
		}

		// runBlock grid (the exact timed-block function): fresh state per
		// vector; some vectors have batches > wrap so the wrap policy is
		// exercised and cross-validated.
		computedBlock[name] = map[string]uint32{}
		for _, vec := range cfg.BlockVectors {
			key := fmt.Sprintf("s%d:b%d:w%d", vec.Start, vec.Batches, vec.Wrap)
			got := w.withFreshState(t, cfg.SteadySeed).blockChecksum(t, vec.Start, vec.Batches, vec.Wrap)
			computedBlock[name][key] = got
			if !update {
				verify(t, name, "block", wantBlock, name, key, got)
			}
		}
	}

	if update {
		goldens := goldensFile{
			GeneratedBy: fmt.Sprintf("goja via %s %s/%s (BENCHJS_UPDATE_GOLDENS=1; canonical source is node_driver.js)",
				runtime.Version(), runtime.GOOS, runtime.GOARCH),
			Comment:   "Recorded checksums of run(seed, iters), runBatch(state(steadySeed), start, count) and runBlock(state(steadySeed), start, batches, batchIters, wrap) (fresh state per vector) for the vectors in harness.json. Both engines must reproduce these exactly.",
			Checksums: computed,
			Batch:     computedBatch,
			Block:     computedBlock,
		}
		data, err := json.MarshalIndent(goldens, "", "  ")
		if err != nil {
			t.Fatalf("marshal goldens: %v", err)
		}
		if err := os.WriteFile("goldens.json", append(data, '\n'), 0o644); err != nil {
			t.Fatalf("write goldens.json: %v", err)
		}
		t.Logf("rewrote goldens.json from the Goja side")
	}
}

// verify compares one freshly computed checksum against the recorded fixture,
// explicitly checking workload-section and key presence first. A zero is a
// perfectly legal checksum value, so absence must never be inferred from the
// value itself.
func verify(t *testing.T, workload, kind string, section map[string]map[string]uint32, name, key string, got uint32) {
	t.Helper()
	values, ok := section[name]
	if !ok {
		t.Errorf("workload %s: missing %s golden section", workload, kind)
		return
	}
	recorded, ok := values[key]
	if !ok {
		t.Errorf("workload %s: missing %s golden for vector %s", workload, kind, key)
		return
	}
	if got != recorded {
		t.Errorf("workload %s: %s checksum mismatch for %s: goja=%d v8(golden)=%d",
			workload, kind, key, got, recorded)
	}
}

// ---------------------------------------------------------------------------
// Benchmarks
// ---------------------------------------------------------------------------

// Steady state: the timed region is ONE runBlock call over b.N batches with
// the shared wrap policy; both engines execute the identical JS loop. Warmup
// on the measured state (one runBatch over warmupIters, identical to the Node
// driver) plus a tiny runBlock function warmup on a throwaway state happen
// outside the timed region. Metrics are normalized to one underlying workload
// iteration (ns/iter, B/iter, allocs/iter); the framework's ns/op remains per
// runBlock call, and the cross-engine comparison deliberately uses ns/iter.

func BenchmarkJSONPipeline(b *testing.B)  { benchSteady(b, "json_pipeline") }
func BenchmarkBusinessData(b *testing.B)  { benchSteady(b, "business_data") }
func BenchmarkTextRegex(b *testing.B)     { benchSteady(b, "text_regex") }
func BenchmarkMatmulCompute(b *testing.B) { benchSteady(b, "matmul_compute") }

func benchSteady(b *testing.B, name string) {
	cfg := loadHarnessConfig(b)
	w := setupWorkload(b, name, cfg.SteadySeed)
	w.warmFunction(b, cfg.WarmupIters, cfg.SteadyWindowWrapOps) // untimed, throwaway state
	w.warm(b, cfg.WarmupIters)                                  // untimed, measured state

	batchSize := w.batchSize
	wrap := cfg.SteadyWindowWrapOps

	// MemStats counters (TotalAlloc/Mallocs) are cumulative, so the delta
	// across the timed call captures its allocations regardless of GC timing;
	// no forced GC is needed. ReadMemStats itself runs outside the timed call.
	var msBefore, msAfter runtime.MemStats
	runtime.ReadMemStats(&msBefore)

	b.ReportAllocs()
	b.ResetTimer()
	start := time.Now()
	sink := w.blockChecksum(b, cfg.WarmupIters, int64(b.N), wrap)
	elapsed := time.Since(start)
	b.StopTimer()

	runtime.ReadMemStats(&msAfter)
	benchmarkSink ^= sink

	iters := float64(int64(b.N) * batchSize)
	b.ReportMetric(float64(elapsed)/iters, "ns/iter")
	b.ReportMetric(float64(msAfter.TotalAlloc-msBefore.TotalAlloc)/iters, "B/iter")
	b.ReportMetric(float64(msAfter.Mallocs-msBefore.Mallocs)/iters, "allocs/iter")
}

// Setup category 1: compile only. Every op compiles a uniquely tagged but
// semantically equivalent variant (unique leading comment and unique
// filename). All variant sources and filenames for the whole b.N block are
// prebuilt OUTSIDE the timed region, so harness string construction is never
// measured and no artificial pool ceiling exists. Unique sources defeat V8's
// compilation cache and any similar caching; goja.Compile performs an eager
// full parse + AST + bytecode compilation. This category is therefore
// frontend/setup behavior, not full-code-generation equivalence.

func BenchmarkCompileJSONPipeline(b *testing.B)  { benchCompile(b, "json_pipeline") }
func BenchmarkCompileBusinessData(b *testing.B)  { benchCompile(b, "business_data") }
func BenchmarkCompileTextRegex(b *testing.B)     { benchCompile(b, "text_regex") }
func BenchmarkCompileMatmulCompute(b *testing.B) { benchCompile(b, "matmul_compute") }

func benchCompile(b *testing.B, name string) {
	src := workloadSource(b, name)

	// Prebuild all unique variants and filenames for this b.N block outside
	// the timed region (untimed by construction: before ResetTimer).
	variants := make([]string, b.N)
	filenames := make([]string, b.N)
	for i := 0; i < b.N; i++ {
		variants[i] = fmt.Sprintf("/* benchjs compile-variant %s.r%d.%d */\n%s", name, compileRound, i, src)
		filenames[i] = fmt.Sprintf("%s.r%d.v%d.js", name, compileRound, i)
	}
	compileRound++

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := goja.Compile(filenames[i], variants[i], false); err != nil {
			b.Fatal(err)
		}
	}
}

// compileRound makes variant ids globally unique across b.N invocations so no
// source string (or filename) is ever compiled twice in one process.
var compileRound int64

// Setup category 2: fresh runtime (engine context) plus first program
// execution; compilation is amortized outside the timed loop.

func BenchmarkContextSetupJSONPipeline(b *testing.B)  { benchContextSetup(b, "json_pipeline") }
func BenchmarkContextSetupBusinessData(b *testing.B)  { benchContextSetup(b, "business_data") }
func BenchmarkContextSetupTextRegex(b *testing.B)     { benchContextSetup(b, "text_regex") }
func BenchmarkContextSetupMatmulCompute(b *testing.B) { benchContextSetup(b, "matmul_compute") }

func benchContextSetup(b *testing.B, name string) {
	src := workloadSource(b, name)
	prog := compileWorkload(b, name, src)
	b.ReportAllocs()
	b.ResetTimer()
	sink := 0
	for i := 0; i < b.N; i++ {
		rt := goja.New()
		if _, err := rt.RunProgram(prog); err != nil {
			b.Fatal(err)
		}
		sink++
	}
	b.StopTimer()
	benchmarkSink ^= uint32(sink)
}
