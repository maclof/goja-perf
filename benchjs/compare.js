#!/usr/bin/env node
'use strict';

/*
 * benchjs/compare.js - summarize a benchjs measurement run (one or two rounds).
 *
 * Inputs (defaults under benchjs/results; second-round files optional):
 *   goja.txt (, goja2.txt)   - go test -bench output; steady rows carry ns/iter
 *   node.txt (, node2.txt)   - node_driver.js output; steady rows carry ns/iter
 *   node.json (, node2.json) - raw Node samples + metadata (validation)
 *   ../goldens.json          - cross-engine checksum fixture
 *
 * Reports per benchmark: per-round medians, combined median, coefficient of
 * variation, median absolute deviation relative to the median (MAD%), and
 * p10..p90 over all samples of both rounds. Steady rows use the ns/iter unit
 * (one underlying workload iteration); setup rows use ns/op. Goja and Node
 * samples are measured in separate processes/rounds and are NOT paired.
 *
 * Discipline:
 *   - cells with CV > 25% are flagged UNSTABLE and listed in a conspicuous
 *     warning block; they must be read as order-of-magnitude only
 *   - fewer than minSamplesRequired (harness.json) samples per engine is
 *     reported and reflected in the exit code
 *   - checksum validation against goldens.json is mandatory; any mismatch is
 *     a hard failure
 *
 * Usage: node compare.js [--results results] [--markdown]
 * Exit codes: 0 ok, 2 checksum validation failure, 3 missing inputs,
 *             4 sample-count shortfall.
 * No npm dependencies.
 */

const fs = require('fs');
const path = require('path');

const args = {};
for (let i = 2; i < process.argv.length; i++) {
  if (process.argv[i].startsWith('--')) {
    const key = process.argv[i].slice(2);
    args[key] = i + 1 < process.argv.length && !process.argv[i + 1].startsWith('--') ? process.argv[++i] : 'true';
  }
}
const resultsDir = path.resolve(__dirname, args.results || 'results');
const markdown = args.markdown === 'true';
const cfg = JSON.parse(fs.readFileSync(path.join(__dirname, 'harness.json'), 'utf8'));
const CV_LIMIT = 25;

function readIfExists(file) {
  const p = path.join(resultsDir, file);
  return fs.existsSync(p) ? fs.readFileSync(p, 'utf8') : null;
}

// Parses Go-benchmark-format lines; returns map: base name (without the
// optional -N CPU suffix, stripped non-greedily so files produced with
// different GOMAXPROCS still pair) -> [{count, units:{...}, cpu}]
function parseBench(text) {
  const byName = new Map();
  const meta = {};
  text = text.replace(/^\uFEFF/, '');
  for (const raw of text.split('\n')) {
    const line = raw.replace(/\r$/, '').trim();
    if (!line) {
      continue;
    }
    const kv = line.match(/^(goos|goarch|pkg|cpu):\s*(.+)$/);
    if (kv) {
      meta[kv[1]] = kv[2];
      continue;
    }
    // Non-greedy name so the optional -<digits> suffix is stripped even when
    // more fields follow; the suffix must sit directly before whitespace.
    const head = line.match(/^(Benchmark.+?)(?:-(\d+))?\s+(\d+)\s+(.*)$/);
    if (head) {
      const name = head[1];
      const units = {};
      const re = /([\d.eE+-]+)\s+(ns\/iter|ns\/op|B\/iter|allocs\/iter|B\/op|allocs\/op|MB\/s)/g;
      let m;
      while ((m = re.exec(head[4])) !== null) {
        units[m[2]] = parseFloat(m[1]);
      }
      if (!byName.has(name)) {
        byName.set(name, []);
      }
      byName.get(name).push({ count: parseInt(head[3], 10), units, cpu: head[2] || null });
    }
  }
  return { byName, meta };
}

function median(xs) {
  const s = [...xs].sort((a, b) => a - b);
  const mid = s.length >> 1;
  return s.length % 2 ? s[mid] : (s[mid - 1] + s[mid]) / 2;
}

function stats(xs) {
  const n = xs.length;
  const mean = xs.reduce((a, b) => a + b, 0) / n;
  const s = [...xs].sort((a, b) => a - b);
  const q = (p) => s[Math.min(n - 1, Math.max(0, Math.round(p * (n - 1))))];
  const mid = n >> 1;
  const med = n % 2 ? s[mid] : (s[mid - 1] + s[mid]) / 2;
  const variance = n > 1 ? xs.reduce((a, b) => a + (b - mean) * (b - mean), 0) / (n - 1) : 0;
  const mad = median(xs.map((x) => Math.abs(x - med)));
  return {
    n,
    median: med,
    p10: q(0.10),
    p90: q(0.90),
    cv: mean > 0 ? (Math.sqrt(variance) / mean) * 100 : 0,
    madRel: med > 0 ? (mad / med) * 100 : 0
  };
}

function category(name) {
  if (name.startsWith('BenchmarkCompile')) {
    return 'compile';
  }
  if (name.startsWith('BenchmarkContextSetup')) {
    return 'context-setup';
  }
  return 'steady';
}

function unitFor(cat) {
  return cat === 'steady' ? 'ns/iter' : 'ns/op';
}

function fmtNs(ns) {
  if (ns >= 1e6) {
    return (ns / 1e6).toFixed(2) + ' ms';
  }
  if (ns >= 1e3) {
    return (ns / 1e3).toFixed(1) + ' us';
  }
  return ns.toFixed(0) + ' ns';
}

function fmtRatio(r) {
  return r >= 100 ? r.toFixed(0) + 'x' : r >= 10 ? r.toFixed(1) + 'x' : r.toFixed(2) + 'x';
}

function main() {
  const gojaFiles = [readIfExists('goja.txt'), readIfExists('goja2.txt')].filter(Boolean);
  const nodeFiles = [readIfExists('node.txt'), readIfExists('node2.txt')].filter(Boolean);
  const nodeJsons = [readIfExists('node.json'), readIfExists('node2.json')].filter(Boolean).map((t) => JSON.parse(t));
  let goldens;
  try {
    goldens = JSON.parse(fs.readFileSync(path.join(__dirname, 'goldens.json'), 'utf8'));
  } catch (e) {
    console.error('compare.js: cannot read goldens.json: ' + e.message);
    process.exit(3);
  }
  if (!gojaFiles.length || !nodeFiles.length || !nodeJsons.length) {
    console.error('compare.js: missing inputs in ' + resultsDir +
      ' (need goja.txt/node.txt/node.json from at least one round)');
    process.exit(3);
  }

  const rounds = Math.max(gojaFiles.length, nodeFiles.length);
  const gojaRounds = gojaFiles.map(parseBench);
  const nodeRounds = nodeFiles.map(parseBench);

  // merge samples across rounds (file order preserved)
  function mergeSamples(roundsParsed, name, unit) {
    const xs = [];
    for (const r of roundsParsed) {
      for (const s of r.byName.get(name) || []) {
        if (s.units[unit] !== undefined) {
          xs.push(s.units[unit]);
        }
      }
    }
    return xs;
  }

  const names = [...new Set([
    ...gojaRounds.flatMap((r) => [...r.byName.keys()]),
    ...nodeRounds.flatMap((r) => [...r.byName.keys()])
  ])].sort((a, b) => {
    const catOrder = { steady: 0, compile: 1, 'context-setup': 2 };
    const d = catOrder[category(a)] - catOrder[category(b)];
    return d !== 0 ? d : a.localeCompare(b);
  });

  // --- checksum validation: every node round vs goldens.json ---
  let valTotal = 0;
  let valFail = 0;
  for (const nj of nodeJsons) {
    for (const [wl, vecs] of Object.entries(goldens.checksums)) {
      for (const [key, want] of Object.entries(vecs)) {
        valTotal++;
        const got = nj.workloads[wl] && nj.workloads[wl].validation[key];
        if (got !== want) {
          valFail++;
          console.error('VALIDATION FAIL (run): ' + wl + ' ' + key + ': node=' + got + ' golden=' + want);
        }
      }
    }
    for (const section of ['batchChecksums', 'blockChecksums']) {
      for (const [wl, vecs] of Object.entries(goldens[section] || {})) {
        const jsonKey = section === 'batchChecksums' ? 'batchValidation' : 'blockValidation';
        for (const [key, want] of Object.entries(vecs)) {
          valTotal++;
          const got = nj.workloads[wl] && nj.workloads[wl][jsonKey][key];
          if (got !== want) {
            valFail++;
            console.error('VALIDATION FAIL (' + section + '): ' + wl + ' ' + key + ': node=' + got + ' golden=' + want);
          }
        }
      }
    }
  }
  const valVerdict = valFail === 0 ? 'PASS (' + valTotal + '/' + valTotal + ' checksums match goldens.json)' :
    'FAIL (' + valFail + '/' + valTotal + ' mismatches)';

  // --- per-benchmark rows ---
  const rows = [];
  const unstable = [];
  const shortSamples = [];
  const steadyRatiosPerRound = Array.from({ length: rounds }, () => []);
  for (const name of names) {
    const cat = category(name);
    const unit = unitFor(cat);
    const gAll = mergeSamples(gojaRounds, name, unit);
    const nAll = mergeSamples(nodeRounds, name, unit);
    if (!gAll.length || !nAll.length) {
      rows.push({ name, category: cat, unit, missing: !gAll.length ? 'goja' : 'node' });
      continue;
    }
    const gs = stats(gAll);
    const ns = stats(nAll);
    const roundVals = (roundsParsed, nm) => roundsParsed.map((r) => {
      const xs = (r.byName.get(nm) || []).map((x) => x.units[unit]).filter((v) => v !== undefined);
      return xs.length ? stats(xs).median : NaN;
    });
    const gRounds = roundVals(gojaRounds, name);
    const nRounds = roundVals(nodeRounds, name);
    const roundRatios = gRounds.map((gm, i) => gm / nRounds[i]);
    const cellUnstable = gs.cv > CV_LIMIT || ns.cv > CV_LIMIT;
    if (cellUnstable) {
      unstable.push(name + ' (goja CV ' + gs.cv.toFixed(0) + '%, node CV ' + ns.cv.toFixed(0) + '%)');
    }
    if (gs.n < cfg.minSamplesRequired || ns.n < cfg.minSamplesRequired) {
      shortSamples.push(name + ' (goja ' + gs.n + ', node ' + ns.n + ')');
    }
    roundRatios.forEach((r, i) => {
      if (isFinite(r) && cat === 'steady') {
        steadyRatiosPerRound[i].push(r);
      }
    });
    rows.push({ name, category: cat, unit, gs, ns, gRounds, nRounds, roundRatios, cellUnstable });
  }

  // --- output ---
  if (markdown) {
    console.log('| benchmark | unit | goja med (r1/r2) | goja p10..p90 | goja CV/MAD | node med (r1/r2) | node p10..p90 | node CV/MAD | node/goja (r1/r2) |');
    console.log('|---|---|---:|---:|---:|---:|---:|---:|---:|');
    for (const r of rows) {
      if (r.missing) {
        console.log('| `' + r.name + '` | ' + r.unit + ' | (missing from ' + r.missing + ') | | | | | | |');
        continue;
      }
      const f = (x) => Math.round(x).toLocaleString('en-US').replace(/,/g, '');
      const gtag = r.gs.cv.toFixed(0) + '%' + (r.gs.cv > CV_LIMIT ? ' **UNSTABLE**' : '') + ' / ' + r.gs.madRel.toFixed(0) + '%';
      const ntag = r.ns.cv.toFixed(0) + '%' + (r.ns.cv > CV_LIMIT ? ' **UNSTABLE**' : '') + ' / ' + r.ns.madRel.toFixed(0) + '%';
      console.log('| `' + r.name + '` | ' + r.unit +
        ' | ' + r.gRounds.map((m) => f(m)).join(' / ') +
        ' | ' + f(r.gs.p10) + '..' + f(r.gs.p90) +
        ' | ' + gtag +
        ' | ' + r.nRounds.map((m) => f(m)).join(' / ') +
        ' | ' + f(r.ns.p10) + '..' + f(r.ns.p90) +
        ' | ' + ntag +
        ' | ' + r.roundRatios.map((x) => (isFinite(x) ? fmtRatio(x) : '-')).join(' / ') + ' |');
    }
  } else {
    const hdr = ['benchmark', 'unit', 'goja med(r1,r2)', 'goja p10..p90', 'node med(r1,r2)', 'node p10..p90', 'node/goja', 'n CV%/MAD%'];
    const w = [40, 8, 26, 26, 26, 26, 15, 22];
    console.log(hdr.map((h, idx) => h.padEnd(w[idx])).join(''));
    console.log(w.map((x) => '-'.repeat(x)).join(''));
    for (const r of rows) {
      if (r.missing) {
        console.log(r.name.padEnd(w[0]) + unitFor(r.category).padEnd(w[1]) + ('missing from ' + r.missing));
        continue;
      }
      const f = (x) => Math.round(x).toLocaleString('en-US').replace(/,/g, '');
      const nfo = ' goja ' + r.gs.n + ' ' + r.gs.cv.toFixed(0) + '%/' + r.gs.madRel.toFixed(0) + '%' +
        ' | node ' + r.ns.n + ' ' + r.ns.cv.toFixed(0) + '%/' + r.ns.madRel.toFixed(0) + '%';
      console.log(
        r.name.padEnd(w[0]) + r.unit.padEnd(w[1]) +
        r.gRounds.map((m) => f(m)).join(',').padStart(w[2]) +
        (f(r.gs.p10) + '..' + f(r.gs.p90)).padStart(w[3]) +
        r.nRounds.map((m) => f(m)).join(',').padStart(w[4]) +
        (f(r.ns.p10) + '..' + f(r.ns.p90)).padStart(w[5]) +
        r.roundRatios.map((x) => (isFinite(x) ? fmtRatio(x) : '-')).join(',').padStart(w[6]) +
        nfo.padStart(w[7])
      );
    }
  }

  console.log('');
  if (unstable.length) {
    console.log('!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!');
    console.log('!! HIGH-VARIANCE CELLS (CV > ' + CV_LIMIT + '%) - ORDER-OF-MAGNITUDE ONLY,');
    console.log('!! NOT PRECISE POINT ESTIMATES. RERUN UNDER QUIETER CONDITIONS:');
    for (const u of unstable) {
      console.log('!!   ' + u);
    }
    console.log('!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!');
  }
  if (shortSamples.length) {
    console.log('WARNING: fewer than ' + cfg.minSamplesRequired + ' samples for: ' + shortSamples.join(', '));
  }
  console.log('checksum validation vs goldens.json: ' + valVerdict);
  const roundRatiosSteady = steadyRatiosPerRound.map((rs) => {
    if (!rs.length) {
      return null;
    }
    return Math.exp(rs.reduce((a, b) => a + Math.log(b), 0) / rs.length);
  });
  console.log('steady geomean node/goja per round: ' +
    roundRatiosSteady.map((r) => (r ? fmtRatio(r) : '-')).join(', ') +
    ' (round 1 = node-first, round 2 = goja-first)');
  const g0 = gojaRounds[0].meta || {};
  console.log('goja: ' + (g0.goos || '?') + '/' + (g0.goarch || '?') +
    ' | node: ' + nodeJsons[0].meta.node + ' (V8 ' + nodeJsons[0].meta.v8 + '), ' +
    nodeJsons[0].meta.platform + '/' + nodeJsons[0].meta.arch + ', ' + nodeJsons[0].meta.cpus + ' cpus');

  if (valFail > 0) {
    process.exit(2);
  }
  if (shortSamples.length) {
    process.exit(4);
  }
}

main();
