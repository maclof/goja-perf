#!/usr/bin/env node
'use strict';

/*
 * benchjs/node_driver.js - Node.js/V8 side of the benchjs comparison suite.
 *
 * Runs the shared workload sources (workloads/*.js) on this Node's V8 with the
 * exact same semantics the Goja harness applies in goja_bench_test.go:
 *
 *   - steady-state samples: ONE timed call per sample to the workload's shared
 *     JavaScript runBlock(state, firstStart, batches, batchIters, wrap), which
 *     performs the entire batches loop, the (k % wrap) start-window policy and
 *     checksum accumulation inside JS - the identical function the Goja side
 *     calls once for its whole b.N timed block. Each sample uses a freshly
 *     created state (untimed) warmed with the same warmupIters runBatch call
 *     the Goja harness applies. Host-boundary overhead: none on Node, one
 *     amortized call per block on Goja. Reported as ns/iter (one underlying
 *     workload iteration).
 *   - compile samples: uniquely tagged but semantically equivalent variants
 *     (unique leading comment + unique filename), PREBUILT outside each timed
 *     block; only the vm.Script compilations of those prebuilt variants are
 *     timed (>= compileBlockTargetMs). Unique sources defeat V8's compilation
 *     cache, so this measures the V8 frontend/lazy-compilation path, NOT the
 *     full-code-generation work goja.Compile performs (goja eagerly builds AST
 *     + bytecode). Setup behavior, explicitly not equivalence.
 *   - context-setup samples: fresh vm context + first script execution,
 *     blocked until >= contextBlockTargetMs.
 *
 * Warmup: the measured state per sample receives the identical `warmupIters`
 * runBatch iterations Goja applies. Additionally an engine-level warmup runs
 * on throwaway states: the semantic minimum plus a V8-only extension through
 * `warmupTimeBudgetMsV8` that repeatedly calls the EXACT timed function
 * (runBlock) so V8 tiers it up fully before calibration and sampling. Warmup
 * is never timed; the V8 budget favors V8 and is documented as such.
 *
 * Usage:
 *   node node_driver.js [--results results] [--samples N] [--workloads a,b]
 *                       [--quick] [--update-goldens] [--startup] [--tag 2]
 *
 * --tag 2 writes node2.txt/node2.json instead of node.txt/node.json (used for
 * the second, engine-order-alternated measurement round).
 *
 * Outputs (in the results directory):
 *   node[TAG].txt   - Go-benchmark-format lines, one per sample; steady rows
 *                     carry the ns/iter unit, setup rows ns/op
 *   node[TAG].json  - machine-readable raw samples and metadata
 *   ../goldens.json - written only with --update-goldens (the cross-engine
 *                     checksum fixture consumed by TestGoldenChecksums and
 *                     compare.js)
 *
 * No npm dependencies; Node core modules only.
 */

const fs = require('fs');
const path = require('path');
const vm = require('vm');
const os = require('os');
const { spawnSync } = require('child_process');

const ROOT = __dirname;
const ALL_WORKLOADS = ['json_pipeline', 'business_data', 'text_regex', 'matmul_compute'];

function parseArgs(argv) {
  const out = {};
  for (let i = 0; i < argv.length; i++) {
    const a = argv[i];
    if (a.startsWith('--')) {
      const key = a.slice(2);
      const val = i + 1 < argv.length && !argv[i + 1].startsWith('--') ? argv[++i] : 'true';
      out[key] = val;
    }
  }
  return out;
}

const cfg = JSON.parse(fs.readFileSync(path.join(ROOT, 'harness.json'), 'utf8'));
const args = parseArgs(process.argv.slice(2));
const cpus = os.cpus().length;
const quick = args.quick === 'true';
const samples = Math.max(3, parseInt(args.samples || cfg.samplesPerBenchmark, 10));
const only = args.workloads && args.workloads !== 'all' ? args.workloads.split(',') : ALL_WORKLOADS;
const resultsDir = path.resolve(ROOT, args.results || 'results');
const updateGoldens = args['update-goldens'] === 'true';
const wantStartup = args.startup === 'true';
const tag = args.tag ? String(args.tag) : '';

function hrNs() {
  return process.hrtime.bigint();
}

function msToNs(ms) {
  return BigInt(Math.max(1, Math.round(ms * 1e6)));
}

// Benchmark names must match the Go side in goja_bench_test.go verbatim
// (compare.js pairs them; benchstat pairs them too).
const PASCAL_OVERRIDES = { json: 'JSON' };

function pascal(name) {
  return name.split('_').map((p) => PASCAL_OVERRIDES[p] || p.charAt(0).toUpperCase() + p.slice(1)).join('');
}

function benchLabel(name, prefix) {
  return 'Benchmark' + (prefix || '') + pascal(name) + '-' + cpus;
}

function median(xs) {
  const s = [...xs].sort((a, b) => a - b);
  const mid = s.length >> 1;
  return s.length % 2 ? s[mid] : (s[mid - 1] + s[mid]) / 2;
}

function loadWorkload(name) {
  const file = path.join(ROOT, 'workloads', name + '.js');
  const src = fs.readFileSync(file, 'utf8');
  const script = new vm.Script(src, { filename: name + '.js' });
  const sandbox = {};
  vm.createContext(sandbox);
  const completion = script.runInContext(sandbox);
  let wl = completion && typeof completion === 'object' && completion.name === name ? completion : null;
  if (!wl) {
    wl = sandbox.__workloads && sandbox.__workloads[name];
  }
  for (const fn of ['createState', 'iterate', 'runBatch', 'runBlock', 'run']) {
    if (!wl || typeof wl[fn] !== 'function') {
      throw new Error('workload contract violated for ' + name + ' (missing ' + fn + ')');
    }
  }
  return { src, script, wl };
}

function measureWorkload(name) {
  const { src, script, wl } = loadWorkload(name);
  const batchIters = cfg.batchIters[name];
  const wrap = cfg.steadyWindowWrapOps;
  if (!(batchIters >= 1)) {
    throw new Error('harness.json: missing/invalid batchIters for ' + name);
  }
  const result = {
    batchIters: batchIters,
    steadyWrap: wrap,
    compileNsPerOp: [],
    compileOpsPerBlock: [],
    contextSetupNsPerOp: [],
    contextOpsPerBlock: [],
    steadyNsPerIter: [],
    steadyBlockIters: 0,
    steadyBlockChecksum: null,
    warmupItersActual: 0,
    validation: {},
    batchValidation: {},
    blockValidation: {}
  };

  // --- setup category 1: compilation of uniquely tagged equivalent variants ---
  // Each timed block only compiles PREBUILT unique variants; the variant
  // string/filename construction happens outside the timed region. The block
  // size is calibrated from the previous sample's rate (with a safety factor)
  // and every reported block runs >= compileBlockTargetMs or is flagged.
  let variantId = 0;
  const makeVariant = () => {
    variantId++;
    return {
      src: '/* benchjs compile-variant ' + name + '.' + variantId + ' */\n' + src,
      file: name + '.v' + variantId + '.js'
    };
  };
  const compileTargetNs = Number(msToNs(quick ? 10 : cfg.compileBlockTargetMs));
  // Untimed rate probe (its variants are consumed and never reported).
  {
    const N0 = 8;
    const t0 = hrNs();
    for (let i = 0; i < N0; i++) {
      const v = makeVariant();
      // eslint-disable-next-line no-new
      new vm.Script(v.src, { filename: v.file });
    }
    var compileRateNs = Number(hrNs() - t0) / N0;
  }
  for (let s = 0; s < samples; s++) {
    const M = Math.max(4, Math.ceil((1.6 * compileTargetNs) / compileRateNs));
    const pool = [];
    for (let i = 0; i < M; i++) {
      pool.push(makeVariant()); // untimed construction
    }
    const t0 = hrNs();
    for (let i = 0; i < M; i++) {
      // eslint-disable-next-line no-new
      new vm.Script(pool[i].src, { filename: pool[i].file });
    }
    const dt = Number(hrNs() - t0);
    if (dt < compileTargetNs) {
      console.error('[node_driver] warning: ' + name + ' compile block ' + s + ' was ' +
        Math.round(dt / 1e6) + 'ms (< target); treated as indicative');
    }
    result.compileNsPerOp.push(dt / M);
    result.compileOpsPerBlock.push(M);
    compileRateNs = dt / M;
  }

  // --- setup category 2: fresh engine context + first execution, blocked ---
  const ctxTargetNs = msToNs(quick ? 10 : cfg.contextBlockTargetMs);
  for (let s = 0; s < samples; s++) {
    const t0 = hrNs();
    let ops = 0;
    do {
      const sb = {};
      vm.createContext(sb);
      script.runInContext(sb);
      ops++;
    } while (hrNs() - t0 < ctxTargetNs);
    result.contextSetupNsPerOp.push(Number(hrNs() - t0) / ops);
    result.contextOpsPerBlock.push(ops);
  }

  // --- steady state ---
  // Engine-level warmup on throwaway states, in two phases, never timed:
  //   1. the same warmupIters semantic runBatch iterations the Goja harness
  //      applies (minimum);
  //   2. a V8-only JIT-budget extension that warms the EXACT timed function
  //      (runBlock) on rotating throwaway states until warmupTimeBudgetMsV8.
  // The measured state per sample is fresh and only receives the identical
  // runBatch warmup below, so both engines measure identically warmed states;
  // the budget extension is extra V8 JIT opportunity and favors V8.
  const budgetNs = msToNs(quick ? 0 : cfg.warmupTimeBudgetMsV8);
  const warmStates = [0, 1, 2, 3].map(() => wl.createState(cfg.steadySeed));
  const wStart = hrNs();
  let w = 0;
  for (const st of warmStates) {
    wl.runBatch(st, 0, cfg.warmupIters);
  }
  w = cfg.warmupIters;
  let wi = 0;
  while (budgetNs > 0n && hrNs() - wStart < budgetNs) {
    wl.runBlock(warmStates[wi % warmStates.length], cfg.warmupIters, 4, batchIters, wrap);
    wi++;
    w += 4 * batchIters;
    if (w >= cfg.warmupSafetyCapIters) {
      break;
    }
  }
  result.warmupItersActual = w;

  // Calibrate batches-per-sample so one runBlock call is >= steadyBlockTargetMs.
  // This deliberately happens AFTER the tier-up warmup so K reflects the
  // fully-JITed rate.
  let K;
  {
    const scratch = wl.createState(cfg.steadySeed);
    wl.runBatch(scratch, 0, cfg.warmupIters);
    const t0 = hrNs();
    wl.runBlock(scratch, cfg.warmupIters, 1, batchIters, wrap);
    const perBatchNs = Number(hrNs() - t0);
    const target = Number(msToNs(quick ? 10 : cfg.steadyBlockTargetMs));
    K = Math.max(1, Math.min(Math.ceil(target / Math.max(perBatchNs, 1)), 5e6));
  }
  result.steadyBlockIters = K * batchIters;

  // Samples: fresh state per sample (untimed), per-sample warm identical to
  // the Goja harness, then ONE timed runBlock call per sample - the same
  // function, wrap policy and window bounds the Goja side uses. The block
  // checksum must be identical across samples (identical inputs).
  const aggs = new Set();
  for (let s = 0; s < samples; s++) {
    const st = wl.createState(cfg.steadySeed);
    wl.runBatch(st, 0, cfg.warmupIters);
    const t0 = hrNs();
    const blockChecksum = wl.runBlock(st, cfg.warmupIters, K, batchIters, wrap) >>> 0;
    const dt = Number(hrNs() - t0);
    result.steadyNsPerIter.push(dt / (K * batchIters));
    aggs.add(blockChecksum);
    result.steadyBlockChecksum = blockChecksum;
  }
  if (aggs.size !== 1) {
    throw new Error(name + ': per-sample block checksums differ (nondeterminism)');
  }

  // Validation checksums (fixed seeds/iteration counts, shared with Goja).
  for (const [seed, iters] of cfg.validationVectors) {
    result.validation[seed + ':' + iters] = wl.run(seed, iters);
  }
  // runBatch grid: a FRESH state per vector, so every entry is the
  // order-independent function runBatch(createState(steadySeed), start, count).
  const starts = [cfg.warmupIters, cfg.warmupIters + batchIters].concat(cfg.batchVectors.starts);
  for (const start of new Set(starts)) {
    for (const count of cfg.batchVectors.counts) {
      result.batchValidation[start + ':' + count] =
        wl.runBatch(wl.createState(cfg.steadySeed), start, count);
    }
  }
  // runBlock grid (the exact timed-block function): fresh state per vector;
  // some vectors have batches > wrap so the wrap policy is cross-validated.
  for (const vec of cfg.blockVectors) {
    result.blockValidation['s' + vec.start + ':b' + vec.batches + ':w' + vec.wrap] =
      wl.runBlock(wl.createState(cfg.steadySeed), vec.start, vec.batches, batchIters, vec.wrap);
  }

  const spotA = wl.run(cfg.spotCheckSeed, cfg.spotCheckIters);
  const spotB = wl.run(cfg.spotCheckSeed, cfg.spotCheckIters);
  if (spotA !== spotB) {
    throw new Error(name + ': spot-check checksum changed during the run (nondeterminism)');
  }

  return result;
}

function measureNodeStartup() {
  const out = [];
  for (let s = 0; s < 7; s++) {
    const t0 = hrNs();
    spawnSync(process.execPath, ['-e', '0'], { stdio: 'ignore' });
    out.push(Number(hrNs() - t0) / 1e6);
  }
  return out;
}

function main() {
  if (quick) {
    console.error('[node_driver] --quick: reduced targets; NOT suitable for publication');
  }
  const benchLines = [];
  const workloads = {};
  const effectiveBudgetMs = quick ? 0 : cfg.warmupTimeBudgetMsV8;

  for (const name of only) {
    if (ALL_WORKLOADS.indexOf(name) < 0) {
      throw new Error('unknown workload: ' + name);
    }
    process.stderr.write('[node_driver] ' + name + ': ');
    const savedBudget = cfg.warmupTimeBudgetMsV8;
    cfg.warmupTimeBudgetMsV8 = effectiveBudgetMs;
    const r = measureWorkload(name);
    cfg.warmupTimeBudgetMsV8 = savedBudget;
    workloads[name] = r;

    const steadyLbl = benchLabel(name);
    for (const nsIter of r.steadyNsPerIter) {
      benchLines.push(steadyLbl + ' ' + r.steadyBlockIters + ' ' + nsIter.toFixed(4) + ' ns/iter');
    }
    const compileLbl = benchLabel(name, 'Compile');
    for (let s = 0; s < r.compileNsPerOp.length; s++) {
      benchLines.push(compileLbl + ' ' + r.compileOpsPerBlock[s] + ' ' + r.compileNsPerOp[s].toFixed(2) + ' ns/op');
    }
    const ctxLbl = benchLabel(name, 'ContextSetup');
    for (let s = 0; s < r.contextSetupNsPerOp.length; s++) {
      benchLines.push(ctxLbl + ' ' + r.contextOpsPerBlock[s] + ' ' + r.contextSetupNsPerOp[s].toFixed(2) + ' ns/op');
    }
    process.stderr.write(
      'steady median ' + Math.round(median(r.steadyNsPerIter)) + ' ns/iter (block=' + r.steadyBlockIters +
      ' iters, warmup=' + r.warmupItersActual + '), compile median ' +
      Math.round(median(r.compileNsPerOp)) + ' ns/op\n'
    );
  }

  fs.mkdirSync(resultsDir, { recursive: true });
  fs.writeFileSync(path.join(resultsDir, 'node' + tag + '.txt'), benchLines.join('\n') + '\n');

  const meta = {
    engine: 'node',
    node: process.version,
    v8: process.versions.v8,
    platform: process.platform,
    arch: process.arch,
    cpus: cpus,
    round: tag || '1',
    samplesPerBenchmark: samples,
    warmupItersConfigured: cfg.warmupIters,
    warmupTimeBudgetMsV8Applied: effectiveBudgetMs,
    steadyBlockTargetMs: cfg.steadyBlockTargetMs,
    compileBlockTargetMs: cfg.compileBlockTargetMs,
    quick: quick,
    measuredAt: new Date().toISOString()
  };
  const out = { meta, workloads };
  if (wantStartup) {
    const s = measureNodeStartup();
    meta.processStartupNodeMs = { samples: s, median: median(s), note: 'separately labeled; excluded from all steady-state numbers and from cross-engine tables' };
  }
  fs.writeFileSync(path.join(resultsDir, 'node' + tag + '.json'), JSON.stringify(out, null, 2) + '\n');

  if (updateGoldens) {
    const checksums = {};
    const batchChecksums = {};
    const blockChecksums = {};
    for (const name of only) {
      checksums[name] = workloads[name].validation;
      batchChecksums[name] = workloads[name].batchValidation;
      blockChecksums[name] = workloads[name].blockValidation;
    }
    const goldens = {
      generatedBy: 'node ' + process.version + ' (V8 ' + process.versions.v8 + '), ' + process.platform + '/' + process.arch,
      comment: 'Recorded checksums of run(seed, iters), runBatch(state(steadySeed), start, count) and runBlock(state(steadySeed), start, batches, batchIters, wrap) (fresh state per vector) for the vectors in harness.json. Both engines must reproduce these exactly.',
      checksums,
      batchChecksums,
      blockChecksums
    };
    fs.writeFileSync(path.join(ROOT, 'goldens.json'), JSON.stringify(goldens, null, 2) + '\n');
    console.error('[node_driver] wrote goldens.json');
  }

  console.log('[node_driver] results written to ' + resultsDir +
    ' (node' + tag + '.txt: ' + benchLines.length + ' bench lines)');
}

main();
