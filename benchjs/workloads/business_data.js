// benchjs/workloads/business_data.js
//
// Shared JavaScript workload: in-memory business-data processing over an
// array of transaction records - grouping/aggregation into plain objects and a
// Map, sorting with a total-order comparator, moving averages, and report
// string building. Same source is run by Node/V8 and Goja; see the contract
// description in json_pipeline.js.
(function () {
  var TX_COUNT = 1200;
  var WINDOW = 800;
  var SAMPLE_STRIDE = 8;

  var CATEGORIES = ['grocery', 'electronics', 'apparel', 'home', 'health', 'sports', 'toys', 'auto'];
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
    var txns = new Array(TX_COUNT);
    for (var i = 0; i < TX_COUNT; i++) {
      txns[i] = {
        id: 'TX-' + i,
        ts: '2026-08-' + (10 + Math.floor(rnd() * 18)) + 'T' +
          (10 + Math.floor(rnd() * 13)) + ':' + (10 + Math.floor(rnd() * 49)) + ':00Z',
        account: 'ACC-' + Math.floor(rnd() * 900),
        region: REGIONS[Math.floor(rnd() * REGIONS.length)],
        category: CATEGORIES[Math.floor(rnd() * CATEGORIES.length)],
        amountCents: 50 + Math.floor(rnd() * 19950),
        qty: 1 + Math.floor(rnd() * 5)
      };
    }
    return { txns: txns, recent: [], tick: 0 };
  }

  function iterate(ctx, i) {
    var tx = ctx.txns;
    var n = tx.length;
    var off = (i * 7) % n;

    // Aggregate a rotating window by category.
    var agg = {};
    for (var k = 0; k < WINDOW; k++) {
      var t = tx[(off + k) % n];
      var a = agg[t.category];
      if (a === undefined) {
        a = agg[t.category] = { count: 0, revenueCents: 0, maxQty: 0 };
      }
      a.count++;
      a.revenueCents += t.amountCents * t.qty;
      if (t.qty > a.maxQty) {
        a.maxQty = t.qty;
      }
    }

    var rows = [];
    for (var key in agg) {
      rows.push({ category: key, count: agg[key].count, revenueCents: agg[key].revenueCents, maxQty: agg[key].maxQty });
    }
    rows.sort(function (x, y) {
      return y.revenueCents - x.revenueCents ||
        (x.category < y.category ? -1 : x.category > y.category ? 1 : 0);
    });
    var top = rows.slice(0, 5);

    // Regional sample totals via Map (accessed through the fixed REGIONS list,
    // so results do not depend on Map iteration order).
    var regionTotals = new Map();
    for (var k2 = 0; k2 < WINDOW; k2 += SAMPLE_STRIDE) {
      var t2 = tx[(off + k2) % n];
      regionTotals.set(t2.region, (regionTotals.get(t2.region) || 0) + t2.amountCents);
    }

    // Moving average over a 50-record window.
    var sum = 0;
    for (var k3 = 0; k3 < 50; k3++) {
      sum += tx[(off + k3) % n].amountCents;
    }
    var avgCents = Math.floor((sum * 100) / 50);

    var parts = [];
    for (var r = 0; r < top.length; r++) {
      parts.push(top[r].category + ':' + top[r].revenueCents + ':' + top[r].maxQty);
    }
    for (var ri = 0; ri < REGIONS.length; ri++) {
      if (regionTotals.has(REGIONS[ri])) {
        parts.push(REGIONS[ri] + '=' + regionTotals.get(REGIONS[ri]));
      }
    }

    ctx.tick++;
    ctx.recent.push(top[0].revenueCents);
    if (ctx.recent.length > 16) {
      ctx.recent.shift();
    }

    var report = parts.join('|');
    var h = hash32(report);
    h = Math.imul(h ^ avgCents, 2654435761) ^ (top[0].revenueCents | 0);
    return h | 0;
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
    name: 'business_data',
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
  (G.__workloads = G.__workloads || {})['business_data'] = WORKLOAD;
  return WORKLOAD;
})();
