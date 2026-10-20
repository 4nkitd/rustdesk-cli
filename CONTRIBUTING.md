# Contributing to rdcli

Keep PRs small and focused. State what changed, how you checked it, and what remains unverified.

## Getting started

```sh
go build ./...
go test ./...
go test -race ./...
go vet ./...
```

Run these checks on the combined tree before review. Report failures and unavailable checks rather than describing them as passed. Use synthetic credentials and temporary directories in tests.

## What we welcome

- Bug fixes with a failing test
- Protocol improvements with references to the RustDesk sources. See PROJECT_SPEC.md for the exact files.
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
2. Port faithfully. Do not invent field numbers or message shapes.
3. Keep the generated protobuf files committed

## Security requirements

Follow [SECURITY.md](SECURITY.md). In particular:

- Reject invalid signatures, malformed or mismatched identities, and missing messages in signed handshakes. Test that insecure opt-in cannot bypass these failures. Test unsigned connections separately, with and without explicit insecure consent.
- Keep tokens, passwords, OTPs, and credential payloads out of logs and errors. Use sentinel secrets to test stdout, stderr, JSON, debug output, and remote error handling.
- Contain downloads within the selected destination. Test traversal, Windows-style paths, symlink escapes, name collisions, invalid transfer IDs and indexes, oversized data, and interrupted transfers. Publish files only after validated completion.
- Keep screen capture and clipboard access opt-in. Test that ordinary connection, file, shell, and tunnel operations do not trigger either.
- Require dedicated destructive consent scoped to the target and action. Test refusal without it; insecure connection consent and generic `--yes` are insufficient.

Add regression tests that fail for the original bug. Tests must exercise observable behavior, including rejection before credentials are sent or files are changed where applicable.

## Parent review and release gates

1. Run the checks above after integrating all contributors' changes. Include targeted regression results and platform coverage.
2. Have the parent reviewer inspect the combined diff, security boundaries, and remaining findings. Contributors must not approve their own delegated work for release.
3. For protocol changes, record an end-to-end check against an authorized test peer, including versions and connection mode. If unavailable, mark interoperability unverified and let the parent reviewer decide whether it blocks release.
4. Obtain explicit parent approval before tagging, publishing binaries, or updating distribution packages. Automated builds do not substitute for that approval.

Distinguish implemented changes, passing tests, peer-verified behavior, and released fixes in the PR and release notes. Do not claim an audit or that all security issues are resolved. Document unresolved limitations before release.

## Reporting issues

Use the issue templates. Include the rdcli version or commit, command, peer version, server type, and relevant output. Redact credentials and private data from command arguments, JSON, logs, and screenshots before sharing them. Do not attach credential files.

For vulnerabilities, follow [SECURITY.md](SECURITY.md) instead of opening a public issue.
