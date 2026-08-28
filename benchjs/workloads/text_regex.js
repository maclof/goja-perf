// benchjs/workloads/text_regex.js
//
// Shared JavaScript workload: log-line text processing with regular
// expressions - field extraction, email validation, id normalization and
// whitespace collapsing with .replace, string building via array join.
// Regex patterns are plain ES5 constructs implemented identically by V8 and
// Goja (goja's regexp backend). Same contract as json_pipeline.js.
(function () {
  var LINE_COUNT = 900;
  var WINDOW = 160;

  var LEVELS = ['DEBUG', 'INFO', 'INFO', 'WARN', 'ERROR'];
  var OPS = ['checkout', 'search', 'login', 'profile_update', 'cart_view', 'payment', 'report_run'];
  var PATHS = ['/api/v1/cart', '/api/v1/users', '/api/v1/orders', '/api/v1/search', '/api/v1/payments'];
  var HOSTS = ['web-01', 'web-02', 'api-01', 'api-07'];

  var RE_MS = /\bms=(\d+)\b/;
  var RE_USER = /user=([\w.@-]+)/;
  var RE_EMAIL = /^[\w.+-]+@[\w-]+(\.[\w-]+)+$/;
  var RE_PATH_ID = /(\/api\/v1\/[a-z]+\/)(\d+)/g;
  var RE_WS = / {2,}/g;

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

  function padLevel(lvl) {
    var s = lvl;
    while (s.length < 7) {
      s += ' ';
    }
    return s;
  }

  function pad2(v) {
    return v < 10 ? '0' + v : '' + v;
  }

  function pad3(v) {
    return v < 10 ? '00' + v : v < 100 ? '0' + v : '' + v;
  }

  function createState(seed) {
    var rnd = rngFactory(seed);
    var lines = new Array(LINE_COUNT);
    for (var i = 0; i < LINE_COUNT; i++) {
      var ts = '2026-08-' + pad2(10 + Math.floor(rnd() * 18)) + 'T' +
        pad2(Math.floor(rnd() * 24)) + ':' + pad2(Math.floor(rnd() * 60)) + ':' +
        pad2(Math.floor(rnd() * 60)) + '.' + pad3(Math.floor(rnd() * 1000)) + 'Z';
      var level = LEVELS[Math.floor(rnd() * LEVELS.length)];
      var status = rnd() < 0.9 ? 200 : (rnd() < 0.5 ? 404 : 500);
      var ms = 1 + Math.floor(rnd() * 4000);
      var path = PATHS[Math.floor(rnd() * PATHS.length)] + '/' + Math.floor(rnd() * 99999);
      if (rnd() < 0.3) {
        path += '?ref=mail';
      }
      lines[i] = ts + ' ' + padLevel(level) + HOSTS[Math.floor(rnd() * HOSTS.length)] +
        ' user=u' + Math.floor(rnd() * 10000) + '@corp.example' +
        ' op=' + OPS[Math.floor(rnd() * OPS.length)] +
        ' ms=' + ms + ' status=' + status + ' path=' + path;
    }
    return { lines: lines, recent: [], tick: 0 };
  }

  function iterate(ctx, i) {
    var lines = ctx.lines;
    var n = lines.length;
    var off = (i * 11) % n;

    var errCount = 0;
    var warnCount = 0;
    var slowCount = 0;
    var msTotal = 0;
    var badEmails = 0;
    var out = new Array(WINDOW);

    for (var k = 0; k < WINDOW; k++) {
      var line = lines[(off + k) % n];

      if (line.indexOf(' ERROR ') >= 0) {
        errCount++;
      } else if (line.indexOf(' WARN ') >= 0) {
        warnCount++;
      }

      var mM = RE_MS.exec(line);
      if (mM !== null) {
        var ms = +mM[1];
        msTotal += ms;
        if (ms > 1000) {
          slowCount++;
        }
      }

      var norm = line.replace(RE_PATH_ID, '$1:id');
      norm = norm.replace(RE_WS, ' ');

      var mU = RE_USER.exec(line);
      var emailOk = mU !== null && RE_EMAIL.test(mU[1]);
      if (!emailOk) {
        badEmails++;
      }
      out[k] = emailOk ? norm : norm + ' [unverified]';
    }

    var joined = out.join('\n');

    ctx.tick++;
    ctx.recent.push(msTotal);
    if (ctx.recent.length > 16) {
      ctx.recent.shift();
    }

    var h = hash32(joined.slice(0, 4096)) ^ (joined.length | 0);
    h = Math.imul(h ^ msTotal, 2654435761) ^
      ((errCount * 31 + warnCount * 7 + slowCount * 3 + badEmails) | 0);
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
    name: 'text_regex',
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
  (G.__workloads = G.__workloads || {})['text_regex'] = WORKLOAD;
  return WORKLOAD;
})();
