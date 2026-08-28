// Package benchjs hosts a reproducible JavaScript-execution comparison suite
// between this Goja fork and V8 (via the locally installed Node.js).
//
// The suite is documented in benchjs/README.md. The Go benchmarks and
// checksum-validation tests live in goja_bench_test.go (package benchjs_test);
// the workload sources under workloads/ are shared verbatim by both engines;
// node_driver.js collects the V8 side and compare.js summarizes both sides.
//
// This package intentionally contains no production code: it exists so the
// directory builds cleanly as part of ./... while all logic is test/harness
// code.
package benchjs
