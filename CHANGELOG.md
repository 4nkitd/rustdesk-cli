# Changelog

## v0.2.0

The combined local race-test suite and vet pass on macOS arm64 with Go 1.27.0, including encrypted CLI subprocess tests. Publication requires the separate GitHub test and native-build gates on Go 1.27.3. The approved live peer was offline, so no real-peer compatibility is claimed.

### Security

- Reject invalid server-signed and peer-signed identities, including with `--yes`. Unsigned plaintext connections require explicit `--yes`; a broken signed identity chain cannot downgrade.
- Confine downloads to their destination and reject unsafe remote names and symlink paths. Fresh downloads use exclusive `.rdcli-download` staging with directory mode `0700` and file mode `0600`, checking ownership, type, identity and single-link file status before publication. Pre-existing staging is never adopted or deleted, and legacy partials/checkpoints remain untouched.
- Reject ambiguous machine names/tags instead of selecting a peer or password arbitrarily.
- Scope stored login-token fallback to the configured rendezvous server, with GUI-token fallback only for the public server.
- Change `auth status --json` to schema version 2 with credential-presence metadata only: `logged_in`, optional `source`, and `password_set`. It does not emit credentials or session handles, verify a live login, or report effective flag/environment overrides.

### Added

- `screenshot --display <index> --output <file>` saves a validated PNG. Display defaults to zero; output is required and cannot be stdout. Existing regular files require `--overwrite`. JSON contains image metadata. The implementation checks peer version 1.4.0 or later and reported display availability.
- `clipboard get` waits for one text update with `--timeout`; it is not a reliable current-clipboard snapshot. `clipboard set` accepts one text argument or `--stdin`. Text is UTF-8, limited to 1 MiB, and never comes from the local OS clipboard. Writes report submission, with `acknowledged: false`, because the protocol has no application acknowledgement. Empty-text clearing is not portable across peers.
- `info` and `doctor` probe one live connection, authenticate, and report transport, security, peer metadata, advertised capabilities, observed permissions, and failure stage. Both support JSON and peer OTP authentication. Unknown observations remain unknown; successful probes do not establish file, terminal, or tunnel permission.
- `mkdir` creates remote directories including missing parents. `rm` requires `--confirm-delete` or `--force`; `--dir` selects an empty directory and deletion is never recursive. `rename` requires `--overwrite` and a new filename in the same directory because the peer cannot guarantee atomic no-clobber. Global `--yes` is not mutation consent.
- `ls --all` includes hidden entries; default listings exclude them.
- Peer `--otp` / `RDC_OTP` supplies one 6–8 digit code after a 2FA challenge on an authenticated encrypted connection. Codes are not saved or automatically retried, and trusted-device enrollment is not implemented.

### Changed

- `cp` requires `--direction up|down` or `-d up|down`. Uploads require `--overwrite`. Downloads refuse existing files by default, with mutually exclusive `--overwrite` and `--skip` policies. `--include-hidden` includes hidden entries.
- Reject `--resume` for both directions before dialing, with a second rejection in the download implementation. Remote-prefix freshness cannot be verified. Checkpoint adoption has been removed; retries require fresh transfers.
- One-shot `sh` uses explicit command-completion markers and returns the remote exit status instead of treating output silence as completion. It requires a POSIX shell on Linux/macOS; Windows one-shot execution is rejected. `--command-timeout` defaults to 5 minutes after terminal opening. Deprecated `--idle` is ignored.
- Terminal disconnects, decompression errors, timeouts, and unconfirmed command completion propagate as errors. PTY output combines stdout and stderr.
- Migrate the module and imports to `github.com/4nkitd/rustdesk-cli`, regenerate protobuf Go files, and anchor binary ignore rules so CLI source is not ignored. An isolated staged-source build and startup check pass; public tagged installation is a separate post-publication check.

### Limits and deferred work

- Clipboard and diagnostic commands use default desktop sessions that may briefly activate remote video capture. Diagnostics make no screenshot request and retain no screen content.
- Clipboard reads can time out without an update; writes are submitted, not acknowledged. These commands do not provide background synchronization.
- Transfer resume and persisted recovery are unavailable. An abrupt process kill can leave `.rdcli-download` staging that requires manual inspection and removal after confirming no transfer is active.
- Downloads fail closed on platforms/filesystems without supported ownership checks, including Windows clients. ACL grants are not inspected; hostile same-user or privileged processes are outside this protection. Local staging hashes do not provide a remote checksum guarantee.
- No terminal attach/detach flags ship. Upstream can create or remap a terminal for a stale identity, so safe attach-only semantics remain blocked. See [ROADMAP.md](ROADMAP.md).
- Full video/audio streaming remains deferred. Relay connectivity is not guaranteed.

### Distribution and documentation

- The public Homebrew formula `4nkitd/tap/rustdesk-cli` still references v0.1.0 assets. It has not been updated to these changes, and this documentation pass did not test installation end to end.
- Release targets remain Linux and macOS on amd64 and arm64. Windows is not a release target.
- Document JSON per command. Shell and tunnel output remain streams; `cp` emits progress/completion on stderr rather than a JSON result. Diagnostics and file operations can emit structured failure results on stdout.
- Add links to [upstream compatibility research](docs/COMPATIBILITY.md), the [verification runbook](docs/VERIFICATION.md), [release procedure](docs/RELEASING.md), and [protobuf maintenance](docs/PROTOBUF.md). These documents distinguish source research, local checks, and live interoperability evidence.

See [verification evidence](docs/VERIFICATION.md) and the GitHub Actions run attached to the tag for release-gate results.
