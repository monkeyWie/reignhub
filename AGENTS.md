# Repository instructions

Read the relevant [engineering reference](docs/engineering/README.md) before changing a subsystem, then verify its linked source. Follow more specific directory instructions. Explicit task instructions take precedence.

## Global rules

- Maintain one public service contract across Go and Cloudflare Worker. Review both implementations when that contract changes; explain when one needs no edit. Self-hosting stays complete and account-free.
- Protect user ownership, identities and private data. Services forward encrypted traffic; never log secrets or private conversations, overwrite unrelated work, or replay a write whose outcome is unknown.
- Keep work bounded, cancellable and responsive. A slow operation or failed session must not block navigation, other sessions or independent Agent execution.
- Isolate every platform-dependent Go capability behind compile-time-selected files (`*_windows.go`, `*_darwin.go`, or `//go:build`), including platform tests and unsupported fallbacks; do not select OS implementations with runtime branches in shared code. On Windows, use Win32 APIs through `golang.org/x/sys/windows` / syscall for process discovery and control, and Microsoft's `go-winio` for named pipes. Do not invoke shells or system command-line utilities for OS inspection.
- Use the shared NSGo controller and existing design system. Every interaction needs clear feedback; preserve accessibility, responsive layouts and platform conventions.
- Keep rows, controls and ordering stable across background updates. Follow [refreshing lists](docs/engineering/frontend-design.md#refreshing-lists) when adding or changing a polled list.
- Avoid unnecessary secondary labels and descriptions. Let the layout do the explaining. Write product UI from the user's perspective; keep implementation details in diagnostics unless they help the user decide what to do.
- Verify observable behavior and recovery, update the owning reference when its contract changes, and report checks actually run and remaining limits. A document or successful build is not proof of a working integration.

## Task references

| Task                                             | Reference                                                                                             |
| ------------------------------------------------ | ----------------------------------------------------------------------------------------------------- |
| Boundaries, Providers, lifecycle and persistence | [Architecture](docs/engineering/architecture.md)                                                      |
| Public APIs, pairing, grants and deployment      | [Service parity](docs/engineering/service-parity.md) and [protocol](docs/platform/protocol.md)        |
| Components, themes, interaction and layout       | [Frontend design](docs/engineering/frontend-design.md)                                                |
| Implementation and review                        | [Code standards](docs/engineering/code-standards.md) and [validation](docs/engineering/validation.md) |
| Failures and recovery                            | [Troubleshooting](docs/engineering/troubleshooting.md)                                                |
| Cross-cutting choices                            | [Decisions](docs/engineering/decisions.md)                                                            |

## Working and finishing

- Preserve unrelated edits. Use isolated fixtures and temporary state; do not operate real Agent conversations or restart Codex App as tests.
- Root and `clients/nsgo` are separate Go modules. The shared build entry is root `scripts/build.sh`; default artifacts go to root `dist/`. See the validation reference for commands and platform requirements.
- After changing the locally running Mac application's runtime or UI, rebuild and restart ReignHub while preserving its data.
- Publish releases/RCs, push release tags or deploy only when explicitly requested.
- Write repository instructions and engineering references in English. Keep implementation details in their owning reference, not in this file. Public user documentation retains its supported translations.
