# Contributing to rdcli

Thanks for wanting to help. Small, focused PRs are the fastest way to get merged.

## Getting started

```sh
go build ./...
go test ./...
go vet ./...
```

Keep the tree green before opening a PR.

## What we welcome

- Bug fixes with a failing test
- Protocol improvements (with references to the RustDesk sources — see PROJECT_SPEC.md for the exact files)
- Documentation and README fixes
- Shell completions, CI improvements, packaging (brew, etc.)

## What to avoid

- Refactors without tests
- New dependencies for things stdlib does fine (the project is deliberately dependency-light)
- Unrelated changes bundled into one PR

## Style

- `gofmt` clean, deterministic output ordering (no map iteration leaks)
- No comments unless the code is genuinely non-obvious (protocol quirks)
- Errors wrapped with context; user-facing messages lowercase, no stack traces
- Network calls always have timeouts

## Protocol work

rdcli ports wire details from the AGPL-licensed RustDesk sources. When touching protocol code:

1. Cite the source file in the PR description
2. Port faithfully — no invented field numbers or message shapes
3. Keep the generated protobuf files committed

## Reporting issues

Use the issue templates. Bug reports need: command used, peer version, server (public/self-hosted), and the output of `rdcli` with `--json` if applicable.

Security issues: don't open a public issue — use SECURITY.md.
