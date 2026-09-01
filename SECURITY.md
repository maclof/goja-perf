# Security policy

## Supported versions

Security fixes are provided for the latest tagged goja-perf release. During the
initial release line, that means the latest `v0.1.x` version. Older tags and
arbitrary historical commits are not supported; users should reproduce an issue
on the latest release before reporting it.

| Version | Supported |
|---|---|
| Latest `v0.1.x` | Yes |
| Older releases and commits | No |

## Reporting a vulnerability

Do not open a public issue for a suspected vulnerability. Report it privately
through [GitHub Security Advisories](https://github.com/maclof/goja-perf/security/advisories/new)
with affected versions, impact, reproduction steps, and any suggested fix.

The sandbox is defense in depth, not an operating-system security boundary. Its
documented limitations are not by themselves vulnerabilities, but escapes from
the stated capability policy, dynamic-code restrictions, or timeout cleanup are
in scope. See [SANDBOX.md](SANDBOX.md) for the threat model.
