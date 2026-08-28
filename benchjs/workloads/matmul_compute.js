// benchjs/workloads/matmul_compute.js
//
// Shared JavaScript workload: dense double-precision matrix multiply over
// plain nested arrays (no typed arrays, no engine-specific APIs). Only fully
// specified IEEE-754 double operations (+ and *) in a fixed order are used, so
// V8 and Goja must produce bit-identical results; the checksum truncates the
// doubles back to int32 and mixes with Math.imul. A deterministic per-call
// perturbation of one matrix element and a per-call cyclic row start keep the
// inputs changing every iteration.
(function () {
  var SZ = 20;

  function rngFactory(seed) {
    var t = seed >>> 0;
    return function () {
      t = (t + 0x6D2B79F5) | 0;
      var x = Math.imul(t ^ (t >>> 15), 1 | t);
      x = (x + Math.imul(x ^ (x >>> 7), 61 | x)) ^ x;
      return ((x ^ (x >>> 14)) >>> 0) / 4294967296;
    };
  }

  function makeMatrix(rnd) {
    var m = new Array(SZ);
    for (var r = 0; r < SZ; r++) {
      var row = new Array(SZ);
      for (var c = 0; c < SZ; c++) {
        row[c] = rnd();
      }
      m[r] = row;
    }
    return m;
  }

  function createState(seed) {
    var rnd = rngFactory(seed);
    return {
      A: makeMatrix(rnd),
      B: makeMatrix(rnd),
      C: makeMatrix(function () { return 0; })
    };
  }

  function iterate(ctx, i) {
    var A = ctx.A;
    var B = ctx.B;
    var C = ctx.C;

    // Deterministic, iteration-dependent perturbation of one element keeps
    // consecutive iterations from ever seeing identical inputs.
    A[0][(i * 3) % SZ] += ((i % 13) - 6) * 1e-9;

    var start = i % SZ;
    for (var rr = 0; rr < SZ; rr++) {
      var r = (rr + start) % SZ;
      var Ar = A[r];
      var Cr = C[r];
      for (var c = 0; c < SZ; c++) {
        var sum = 0;
        for (var k = 0; k < SZ; k++) {
          sum += Ar[k] * B[k][c];
        }
        Cr[c] = sum;
      }
    }

    var acc = 0;
    for (var r2 = 0; r2 < SZ; r2++) {
      var row = C[r2];
      var s = 0;
      for (var c2 = 0; c2 < SZ; c2++) {
        s = (Math.imul(s, 31) + ((row[c2] * 1024) | 0)) | 0;
      }
      acc = (Math.imul(acc, 31) + s) | 0;
    }
    return acc | 0;
  }

  function mix(acc, h) {
    return (Math.imul(acc, 31) + h) | 0;
  }

  // See json_pipeline.js for the runBatch contract.
  function runBatch(state, start, count) {
    var acc = (Math.imul(start | 0, 2654435761) ^ Math.imul(count | 0, 0x9e3779b9)) | 0;
    for (var k = 0; k < count; k++) {
      acc = mix(acc, iterate(state, start + k));
    }
    return acc >>> 0;
  }

  // See json_pipeline.js for the runBlock contract.
  function runBlock(state, firstStart, batches, batchIters, wrap) {
    var acc = (Math.imul(batches | 0, 2654435761) ^ Math.imul(batchIters | 0, 0x9e3779b9)) | 0;
    for (var k = 0; k < batches; k++) {
      acc = mix(acc, runBatch(state, firstStart + (k % wrap) * batchIters, batchIters));
    }
    return acc >>> 0;
  }

  function run(seed, iters) {
    return runBatch(createState(seed), 0, iters);
  }

  var WORKLOAD = {
    name: 'matmul_compute',
    createState: createState,
    iterate: iterate,
    runBatch: runBatch,
    runBlock: runBlock,
    run: run
  };

  if (typeof module !== 'undefined' && module.exports) {
    module.exports = WORKLOAD;
  }
  var G = typeof globalThis !== 'undefined' ? globalThis : this;
  (G.__workloads = G.__workloads || {})['matmul_compute'] = WORKLOAD;
  return WORKLOAD;
})();
