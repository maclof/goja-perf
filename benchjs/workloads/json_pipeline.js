// benchjs/workloads/json_pipeline.js
//
// Shared JavaScript workload: JSON stringify -> parse -> transform -> stringify
// pipeline over a business-shaped order list. The same source file is executed
// by both engines (Node/V8 via benchjs/node_driver.js, Goja via
// benchjs/goja_bench_test.go). It must therefore stick to ECMAScript features
// whose observable results are fully specified and identical across engines:
// integer arithmetic with Math.imul/|0/>>>0, insertion-ordered plain objects,
// JSON.stringify/JSON.parse, and comparator sorts with total orders.
//
// Contract expected by both drivers:
//   createState(seed) -> opaque state object
//   iterate(state, i) -> int32 hash that depends on i, the seed-built state and
//                        state mutated by previous iterations
//   run(seed, iters)  -> uint32 checksum over `iters` sequential iterations
// The workload object is returned as the completion value of this script and
// also registered on globalThis.__workloads[name] for drivers without
// completion-value support.
(function () {
  var ORDER_COUNT = 80;

  var TIERS = ['gold', 'silver', 'bronze'];
  var REGIONS = ['north', 'south', 'east', 'west', 'central'];

  function hash32(s) {
    var h = 0x811c9dc5 | 0;
    for (var i = 0; i < s.length; i++) {
      h ^= s.charCodeAt(i);
      h = Math.imul(h, 0x01000193);
    }
    return h >>> 0;
  }

  function rngFactory(seed) {
    var t = seed >>> 0;
    return function () {
      t = (t + 0x6D2B79F5) | 0;
      var x = Math.imul(t ^ (t >>> 15), 1 | t);
      x = (x + Math.imul(x ^ (x >>> 7), 61 | x)) ^ x;
      return ((x ^ (x >>> 14)) >>> 0) / 4294967296;
    };
  }

  function createState(seed) {
    var rnd = rngFactory(seed);
    var orders = new Array(ORDER_COUNT);
    for (var i = 0; i < ORDER_COUNT; i++) {
      var nItems = 3 + Math.floor(rnd() * 4); // 3..6
      var items = new Array(nItems);
      for (var j = 0; j < nItems; j++) {
        items[j] = {
          sku: 'SKU-' + Math.floor(rnd() * 100000),
          qty: 1 + Math.floor(rnd() * 9),
          unitPriceCents: 100 + Math.floor(rnd() * 9900),
          discountPct: Math.floor(rnd() * 30)
        };
      }
      var tagCount = Math.floor(rnd() * 4);
      var tags = new Array(tagCount);
      for (var k = 0; k < tagCount; k++) {
        tags[k] = 'tag-' + Math.floor(rnd() * 20);
      }
      orders[i] = {
        id: 'ORD-' + i,
        customer: {
          id: 'CUS-' + Math.floor(rnd() * 5000),
          name: 'cust' + Math.floor(rnd() * 5000),
          email: 'user' + Math.floor(rnd() * 5000) + '@corp.example',
          tier: TIERS[Math.floor(rnd() * TIERS.length)]
        },
        region: REGIONS[Math.floor(rnd() * REGIONS.length)],
        createdAt: '2026-08-' + (10 + Math.floor(rnd() * 18)) + 'T12:00:00Z',
        tags: tags,
        items: items
      };
    }
    return { orders: orders, recent: [], tick: 0 };
  }

  function iterate(ctx, i) {
    var orders = ctx.orders;
    var n = orders.length;
    var off = i % n;
    // Rotate a copy so the stringify input varies every iteration while the
    // amount of work stays constant.
    var rot = orders.slice(off);
    for (var k = 0; k < off; k++) {
      rot.push(orders[k]);
    }

    var s1 = JSON.stringify(rot);
    var back = JSON.parse(s1);

    var thr = (i % 5) * 50; // discount threshold varies per iteration
    var byRegion = {};
    var grand = 0;
    var discounted = 0;
    for (var m = 0; m < back.length; m++) {
      var o = back[m];
      var items = o.items;
      var total = 0;
      var maxDisc = 0;
      for (var q = 0; q < items.length; q++) {
        var it = items[q];
        if (it.discountPct > maxDisc) {
          maxDisc = it.discountPct;
        }
        total += it.qty * (it.unitPriceCents - Math.floor(it.unitPriceCents * it.discountPct / 100));
      }
      var r = byRegion[o.region];
      if (r === undefined) {
        r = byRegion[o.region] = { region: o.region, count: 0, revenueCents: 0 };
      }
      r.count++;
      r.revenueCents += total;
      grand += total;
      if (maxDisc >= thr) {
        discounted++;
      }
    }

    var regions = [];
    for (var key in byRegion) {
      regions.push(byRegion[key]);
    }
    regions.sort(function (a, b) {
      return b.revenueCents - a.revenueCents ||
        (a.region < b.region ? -1 : a.region > b.region ? 1 : 0);
    });

    ctx.tick++;
    ctx.recent.push(grand);
    if (ctx.recent.length > 16) {
      ctx.recent.shift();
    }

    var summary = {
      iter: i,
      tick: ctx.tick,
      regions: regions,
      grandTotalCents: grand,
      discounted: discounted
    };
    var s2 = JSON.stringify(summary);
    // Hash a bounded prefix of the big string plus its length, and the small
    // summary fully: length/prefix keeps order sensitivity without paying a
    // full interpreter pass over ~80KB per iteration.
    var h = hash32(s1.slice(0, 2048)) ^ (s1.length | 0) ^ hash32(s2);
    h = Math.imul(h ^ ctx.recent.length, 2654435761) ^ (grand | 0);
    return h | 0;
  }

  function mix(acc, h) {
    return (Math.imul(acc, 31) + h) | 0;
  }

  // runBatch executes `count` sequential workload iterations entirely inside
  // JavaScript and returns a uint32 checksum. Both harnesses time steady-state
  // work by calling runBatch, so the per-iteration loop, state progression and
  // checksum accumulation live in JS on both engines and host-call overhead is
  // one amortized call per batch on each side.
  function runBatch(state, start, count) {
    var acc = (Math.imul(start | 0, 2654435761) ^ Math.imul(count | 0, 0x9e3779b9)) | 0;
    for (var k = 0; k < count; k++) {
      acc = mix(acc, iterate(state, start + k));
    }
    return acc >>> 0;
  }

  // runBlock executes an entire timed block inside JavaScript: `batches`
  // sequential runBatch calls on one state, with the start window of batch k
  // being firstStart + (k % wrap) * batchIters - the identical wrap policy on
  // both engines. It returns a uint32 checksum over the block. The harnesses
  // time steady state by calling runBlock exactly once per timed unit (Go:
  // once per b.N block; Node: once per sample), so all looping, state
  // progression and checksum accumulation happen in JS symmetrically.
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
    name: 'json_pipeline',
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
  (G.__workloads = G.__workloads || {})['json_pipeline'] = WORKLOAD;
  return WORKLOAD;
})();
