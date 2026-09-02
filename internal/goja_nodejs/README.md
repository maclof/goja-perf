# Embedded goja_nodejs compatibility code

The command-line interpreter uses a small compatibility subset copied from
`github.com/dop251/goja_nodejs` at commit `8dd9abb0616d`: the `console`,
`require`, and `util` packages. It is kept internal so the command can use the
goja-perf runtime's canonical Go type identity after the v0.2.0 module rename.
The copied code remains under the MIT license in [LICENSE](LICENSE).
