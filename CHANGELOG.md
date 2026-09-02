# Changelog

All notable goja-perf changes are documented here. This project follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and uses semantic
version tags for fork releases.

## [Unreleased]

## [0.2.0] - 2026-09-02

### Changed

- **Breaking:** changed the canonical Go module path from
  `github.com/dop251/goja` to `github.com/maclof/goja-perf`. Consumers must
  update imports and remove the v0.1.x replacement directive.
- Embedded the small `console`, `require`, and `util` compatibility subset used
  by the command-line interpreter so it shares goja-perf's Go type identity.

### Fixed

- Restored Linux/386 CI by avoiding an overflowing test-only `int` conversion.
- Restored static analysis on Linux by keeping Windows-only native-trace
  declarations in their platform-specific file and removing an unused helper.

### Documentation

- Added build-status and Go Reference badges and direct v0.2 installation and
  migration instructions.

## [0.1.1] - 2026-09-02

### Added

- Benchmarks for Base64 alphabets, whitespace, chunk modes, short destinations,
  error paths, result objects, large output encoding, and retained backing
  capacity.
- Sandbox coverage for denied and allowed `Uint8Array` use and trusted typed
  array capabilities.

### Changed

- Accelerated whitespace-heavy `Uint8Array.fromBase64()` and
  `Uint8Array.prototype.setFromBase64()` decoding while preserving exact read,
  write, error, padding, and partial-destination behavior.
- Halved large `Uint8Array.prototype.toBase64()` output allocation by removing
  a redundant byte-to-string copy.
- Reduced `setFromBase64()` and `setFromHex()` result construction by one
  allocation, and avoided retaining oversized decoded backing arrays for
  whitespace-heavy input.
- Bounded allocation amplification for all-whitespace and early-invalid large
  Base64 input.

## [0.1.0] - 2026-09-01

### Added

- Adaptive quickening, portable typed numeric-loop traces, and native integer
  loop traces on Windows/amd64 and Linux/amd64.
- Benchmarks for JavaScript execution, setup/compilation, Go↔JavaScript calls,
  boundary value shapes, tiering, and cross-engine pure-JavaScript workloads.
- An opt-in capability sandbox with built-in allow/deny policy, dynamic-code
  restrictions, execution timeouts, serialized access, and reset support.

### Changed

- Reduced allocations and execution overhead in VM stack reuse, primitive
  operations and property keys, global resolution, reflected Go methods, object
  export, and object-literal construction.
- Optimized sandbox creation and reset by reusing a policy-filtered global
  template.

[Unreleased]: https://github.com/maclof/goja-perf/compare/v0.2.0...HEAD
[0.2.0]: https://github.com/maclof/goja-perf/releases/tag/v0.2.0
[0.1.1]: https://github.com/maclof/goja-perf/releases/tag/v0.1.1
[0.1.0]: https://github.com/maclof/goja-perf/releases/tag/v0.1.0
