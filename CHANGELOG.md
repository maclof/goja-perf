# Changelog

All notable goja-perf changes are documented here. This project follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and uses semantic
version tags for fork releases.

## [Unreleased]

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

[Unreleased]: https://github.com/maclof/goja-perf/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/maclof/goja-perf/releases/tag/v0.1.0
