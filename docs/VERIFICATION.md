# Release verification checklist

This runbook separates source inspection, local tests, release artifacts and real-peer evidence. Record results against the exact release commit and binary. Recheck the inventory below when code changes.

An approved live connection attempt reached rendezvous, where the server reported the target offline/nonexistent. No peer operations ran. Real-peer feature verification remains `BLOCKED`; the private target identifier is deliberately omitted.

## Local integration evidence, 2026-10-20

- `PASS`: `go test -race ./... -count=1 -timeout=120s` and `go vet ./...`, using Go 1.27.0 on macOS arm64 after the final file-operation fixes.
- `PASS`: formatting and staged/unstaged `git diff --check`.
- `PASS`: isolated staged-source build with `CGO_ENABLED=0`, then `version` and `--help` in an empty environment and temporary home. This does not replace final committed-source verification.
- `PASS`: a clean export of indexed tree `50b27c3110055752df583b6839fec25b842a2dab` passed the complete race suite, vet, isolated `go install`, version and help. Subsequent changes before commit update documentation only.
- `PASS`: encrypted CLI subprocess cases for diagnostics, synthetic screenshots, POSIX command exit status, multi-block uploads, missing acknowledgements and download hashes. These use mock peers, not an official RustDesk process.
- `NOT RUN`: GitHub Go 1.27.3 test/native-build matrix and published-asset/source-install checks at the time of this pre-publication record. The tagged Actions run and release notes record their subsequent results.
- `BLOCKED`: real-peer interoperability. No screen or clipboard access was authorized or attempted.

Use `PASS`, `FAIL`, `BLOCKED`, `NOT RUN`, or `NOT APPLICABLE`, with evidence and a reason. A missing feature is not a passing test. Features scheduled in [ROADMAP.md](../ROADMAP.md) become release gates when that release advertises them.

## Approval and test boundaries

- [ ] Before any live check, obtain explicit approval naming the exact target, rendezvous/relay endpoints, allowed operations, paths, time window, and cleanup. Approval for file listing does not authorize a shell, screen capture, clipboard access, or a tunnel.
- [ ] Resolve an approved target to one exact peer ID. Do not select a group/tag that could match multiple peers. Do not reuse targets from personal history, configuration, or memory.
- [ ] Get separate approval for screenshot display selection, clipboard reads and writes, terminal persistence, interruption tests, and any permission changes. Use a dedicated test account/session with synthetic content.
- [ ] Have the operator supply any required authentication through the agreed private channel. Keep passwords, tokens, TOTP seeds/codes, trusted-device identifiers, and real config contents out of commands in reports, shell traces, fixtures, and this document. Do not import a personal GUI profile.
- [ ] Treat even `ping`, online status, address-book queries, and diagnostics that contact a server as live checks. A help/version command is local; an installed binary is not permission to connect.
- [ ] Do malformed-message, spoofed-identity, packet-corruption, and resource-limit tests against controlled local fixtures. Do not inject attacks into a shared service or repeatedly submit bad credentials to a live peer.
- [ ] Stop on an unexpected target, path, prompt, authentication source, or sensitive output. Record a blocker rather than broadening approval.

Approval record, stored privately with the run:

| Field | Value to record |
|---|---|
| Run ID and operator | Unique run ID, date/time, operator |
| Approval | Approver, approval reference, start/end time |
| Target | Exact approved peer, test owner, server and relay endpoints |
| Operations | Exact commands/actions, reads, writes, allowed interruption method |
| Scope | Remote fixture root, local fixture root, display, clipboard direction, terminal/session IDs |
| Authentication | Approved source description only; no secret values |
| Cleanup | Owned processes/files/session IDs; who restores test state |

## Evidence levels and existing fixtures

Keep these levels separate in release notes. Passing one does not imply passing another.

| Level | Existing evidence source | What it can establish |
|---|---|---|
| Source and help inspection | `internal/cli/commands.go`, command constructors, implementation files | Registered commands, intended behavior and declared limits; not runtime success |
| Unit and local filesystem | `internal/hbb/*_test.go`, `internal/config/*_test.go`, `internal/cli/*_test.go` | Specific assertions about framing, crypto, paths, config, parsing and command structure |
| Standalone plaintext mocks | `cmd/mockrv/main.go`, `cmd/mockapi/main.go` | CLI integration for online bitsets and address-book HTTP behavior only |
| Encrypted mock protocol | `internal/hbb/conn_test.go`, `file_test.go`, `download_resume_test.go`, `screenshot_protocol_test.go`, `auth2fa_relay_test.go`, `internal/cli/sh_pipeline_test.go` | Scripted peer messages; some fixtures perform generated-key handshakes, others preinstall symmetric keys on `net.Pipe`. Neither uses an official RustDesk process. |
| Installed binary, local | Exact executable path, checksum, `version`, `--help` | Artifact identity, startup and exposed commands on that OS/architecture |
| Real-peer E2E | Approved official RustDesk peer and approved infrastructure | Observed interoperability for the recorded versions, permissions and transport |

### Fixture inventory and limits

- `cmd/mockrv/main.go` binds `127.0.0.1:22116`. It returns an `OnlineResponse` bitset with synthetic IDs `1` and `9` online. It does not implement hbbs key exchange, punch, relay, peer login, or file/terminal sessions. It logs the supplied peer list.
- `cmd/mockapi/main.go` binds `127.0.0.1:18080`. It serves `/api/login` and `/api/ab` GET/PUT with in-memory synthetic records. It does not validate credentials or bearer tokens, implement TLS, or reproduce a production Pro API. PUT changes only its in-memory list and logs that list.
- `conn_test.go` provides an encrypted mock peer and rendezvous helper. `TestConnectAndLogin` includes raw tunnel echo. `secure_test.go` also contains plaintext/raw cases; do not label the entire suite encrypted.
- `file_test.go` uses generated Ed25519 identities, encrypted session setup, and scripted file handlers. Its mock checks that a login request exists, not a real peer's authorization policy. `TestFileSendRestartsUnverifiedUploadPrefix` checks that an unverified upload prefix is not reused. `transfer_policy_test.go` adds upload acknowledgements, multiple files, changed-source rejection, hidden files and symlinks.
- `download_test.go` covers path validation, symlink confinement, preservation of unowned partials, duplicate/reserved names, malformed responses, job-level completion, and the empty entry name used for a single file. `TestReceiveUpstreamJobDone` is still a mock, despite its name.
- `download_resume_test.go` contains encrypted fresh-download policy fixtures and local checks for exclusive staging, ownership, permissions, hard links, forged legacy checkpoint preservation and staging tampering. `TestDownloadResumeAlwaysRejected` checks rejection regardless of advertised resume capability. The filename does not imply resume or checkpoint-reconnect support.
- `terminal_test.go` remains an encrypted echo fixture. Its read-after-close branch has no failing assertion, and its peer does not validate resize dimensions. New `terminal_completion_test.go` checks advertised feature gating, terminal IDs, exit codes, disconnect/error handling, deadlines and bounded decoding. These are scripted terminal messages, not an official shell.
- `internal/cli/sh_completion_test.go` checks marker parsing across chunk boundaries, statuses, output failures, disconnects and deadlines. `TestShCommandScriptActualShell` executes a local POSIX shell wrapper when run. `sh_pipeline_test.go` uses encrypted generated-key mock sessions for delayed output after stdin EOF, peer error, nonzero exit and cancellation. Local shell and mock results remain separate from real-peer E2E.
- `TestConnectViaRelay` in `relay_test.go` pairs two mock streams by UUID and completes encrypted peer login. It does not transfer a file or run a terminal through a real hbbr. The fixture delays the peer handshake, so it does not establish correctness for coalesced relay/handshake frames.
- `identity_test.go` rejects bad server signatures/IDs and missing/bad peer identities even with `Yes`. `config_storage_test.go` covers private writes, symlinks, and preservation on save failure.
- `transport_hardening_test.go` and `network_regression_test.go` cover concurrent writes, short writes, frame limits, coalesced raw-transition bytes, rendezvous framing, deadlines, socket behavior and tunnel completion. They supplement the limited relay fixture rather than proving real hbbr behavior.
- `screenshot_protocol_test.go` uses encrypted pipes with preinstalled keys for login options, display activation, request correlation, PNG bounds, denial and deadlines. `internal/cli/screenshot_command_test.go` covers mock command output and no-clobber publication. Neither captures a real screen.
- `clipboard_protocol_test.go` uses plaintext framed pipes for subscription, permissions, payload bounds, timeouts and unconfirmed submission. `clipboard_auth_test.go` adds encrypted OTP login. `internal/cli/clipboard_cmd_test.go` covers explicit input/output and bounds; no test result implies an actual OS clipboard was accessed.
- `auth2fa_test.go` and `auth2fa_relay_test.go` cover encrypted OTP continuation, failure/deadline limits and relay fallback. Diagnostics has both plaintext and pre-keyed encrypted cases in `internal/hbb/diagnostics_test.go`, including OTP and redaction. Read each fixture's setup before labeling it encrypted.
- `file_ops_test.go` and `file_ops_adversarial_test.go` in `internal/hbb` cover encrypted mutation/listing responses, matching IDs, denial, dangerous paths and bounded waits. CLI file-operation and transfer-policy tests cover explicit consent and rejection before dialing.
- `internal/cli/command_contract_test.go` defines isolated-profile registration/help/flag checks for all seven added commands. Target-resolution, authentication-status and token-scope tests cover ambiguity and disclosure boundaries. Their presence is not a final-suite result.
- No checked-in real-peer E2E runner was found in this inventory. Local `rdcli`, `mockrv`, and `dist/` artifacts are not evidence that the current source or release binary passed anything.

## Upstream references and compatibility matrix

Source inspection used RustDesk commit [`53996e3c404ea5083df0775057e4149e49102084`](https://github.com/rustdesk/rustdesk/tree/53996e3c404ea5083df0775057e4149e49102084) and rustdesk-server commit [`873230e35a8805de7fd057e7eb763c46f7a174ef`](https://github.com/rustdesk/rustdesk-server/tree/873230e35a8805de7fd057e7eb763c46f7a174ef). These are reference snapshots, not tested release versions.

| Reference at the pinned RustDesk commit | Implication for verification |
|---|---|
| [`libs/base/protos/message.proto`](https://github.com/rustdesk/rustdesk/blob/53996e3c404ea5083df0775057e4149e49102084/libs/base/protos/message.proto) | Screenshot request/response correlation uses `sid`; clipboard has compressed content and format; terminal close has `exit_code`; terminal open response has service/session persistence fields. Generated Go message types alone do not establish CLI support. |
| [`src/common.rs`](https://github.com/rustdesk/rustdesk/blob/53996e3c404ea5083df0775057e4149e49102084/src/common.rs) | Upstream numeric gates are screenshot >= `1.4.0` and file-transfer resume >= `1.4.2`. rdcli rejects resume regardless of peer version; the upstream gate does not establish client support. |
| [`src/server/connection.rs`](https://github.com/rustdesk/rustdesk/blob/53996e3c404ea5083df0775057e4149e49102084/src/server/connection.rs) | Login may remain pending for 2FA; clipboard handling checks authorized remote-session scope and permissions; screenshot requests use the display and `sid`. File/terminal login is not general screen/clipboard authorization. |
| [`src/server/terminal_service.rs`](https://github.com/rustdesk/rustdesk/blob/53996e3c404ea5083df0775057e4149e49102084/src/server/terminal_service.rs) | Reattachment can replay bounded output and remap surviving terminal IDs. Closing a terminal and detaching from it are different operations. |
| [`src/client/io_loop.rs`](https://github.com/rustdesk/rustdesk/blob/53996e3c404ea5083df0775057e4149e49102084/src/client/io_loop.rs) | Resume tracks job state and peer-version support; screenshot responses and clipboard updates have separate handling. |
| [`src/relay_server.rs`](https://github.com/rustdesk/rustdesk-server/blob/873230e35a8805de7fd057e7eb763c46f7a174ef/src/relay_server.rs) | `make_pair_` checks a configured relay key, matches UUIDs, then switches paired TCP streams to raw forwarding. |

### Current implementation and help contract

`internal/cli/commands.go` registers all seven additions: `screenshot`, `clipboard`, `info`, `doctor`, `mkdir`, `rm`, and `rename`, alongside the existing commands.

| Command or feature | Source/help contract to verify |
|---|---|
| `sh [command]` | POSIX `sh` one-shot execution on Linux/macOS uses random start/completion markers and the command's exit status. `--command-timeout` defaults to five minutes after opening the terminal. `--idle` is deprecated and ignored. Windows one-shot execution is rejected; interactive terminal support is separate. |
| `cp` | Explicit `--direction up` or `--direction down` is required. Download defaults to no-clobber, with `--overwrite` or `--skip` as alternatives. Upload requires `--overwrite`; atomic remote no-clobber/skip is not promised. `--resume` is rejected for both directions before dialing; `--include-hidden` controls hidden entries. |
| Downloads and resume rejection | The download implementation also rejects resume unconditionally. Fresh transfers create exclusive `.rdcli-download/part.download` staging, with directory mode `0700`, file mode `0600`, and ownership, type, identity and single-link file checks. No checkpoint adoption or persisted recovery. Pre-existing staging is never adopted or deleted; legacy partials/checkpoints are untouched. Local hashing provides no remote checksum guarantee. |
| `screenshot` | RustDesk >= `1.4.0`, `--output <file>` required, PNG file only, zero-based `--display`, default no-clobber and explicit `--overwrite`. `--json` emits metadata, not image bytes. |
| `clipboard get/set` | Explicit UTF-8 text only, at most 1 MiB, no local OS clipboard access. `get` waits for an update and may time out on unchanged/empty content. `set` takes one text argument or `--stdin`; success means submitted, not acknowledged/applied. |
| `info` / `doctor` | Both make a live authenticated probe of one peer. Permissions are observed announcements, with missing values unknown. A default desktop session may briefly start peer capture, although diagnostics requests no screenshot and retains no screen content. |
| `mkdir`, `rm`, `rename`, `ls` | `mkdir` includes missing parents. `rm` requires `--confirm-delete` or `--force`; `--dir` selects an empty directory and there is no recursive delete. Same-directory `rename` always requires `--overwrite`. `--yes` does not authorize mutations. `ls -a` includes hidden entries. |
| Peer OTP | Shared `--otp` / `RDC_OTP` handling reaches ordinary file/terminal/tunnel login and the default-session screenshot, clipboard and diagnostics paths. A supplied 6-8 ASCII-digit code is sent once only after a challenge on an authenticated encrypted connection. Missing input fails without prompting; no trusted-device persistence is added. |
| Terminal attachment | Not implemented or advertised. Upstream may create/remap a missing service or terminal instead of failing an attach-only request. Safe attach-only semantics remain blocked. |

The module is now `github.com/4nkitd/rustdesk-cli`. `go.mod` declares Go `1.25.6`; the inspected CI/release workflows select Go `1.27.3`. Record the actual build toolchain. Version injection uses `-X github.com/4nkitd/rustdesk-cli/internal/cli.version=<version>`, not the old `rdcli/internal/cli.version` path. Source registration and help declarations do not prove an older installed binary contains these changes.

Fill one matrix row per scenario and transport. Use the candidate's help output for command syntax.

| Field | Required detail |
|---|---|
| Candidate | Commit/tag, dirty-tree status, build Go version, artifact SHA-256, full executable path |
| Client | OS/version, architecture, filesystem/case sensitivity, shell, TTY/non-TTY |
| Peer | Official RustDesk version/build, OS/version/architecture, service/user mode, advertised features |
| Server | hbbs/hbbr versions, public/self-hosted, pinned public-key fingerprint, API version if used |
| Route | Requested mode and observed direct/relay route, approved endpoints, local/LAN/NAT topology |
| Authorization | Password/2FA mode, permissions, terminal user scope, trusted/recent-session state without identifiers |
| Feature | Scenario ID, fixture sizes/hashes, display backend/scale if relevant, expected support |
| Outcome | Expected/actual result, stdout/stderr evidence, exit code, elapsed time, cleanup result |

Cover each advertised client artifact on its native OS/architecture. The inspected release workflow defines native Linux and macOS amd64/arm64 builds plus help/version smoke checks; these definitions are not completed CI evidence. No Windows artifact is in that matrix. Downloads fail closed on clients without supported ownership checks, including Windows; this is separate from Windows peer support. Use Windows, macOS and Linux peers where the feature claims support, with one-shot shell checks limited to Linux/macOS. Include peers below/at the screenshot `1.4.0` and terminal `1.4.1` boundaries, plus the current supported official release. Verify resume rejection locally regardless of peer capability. `internal/hbb/terminal.go` requires the advertised terminal feature; a version string alone is insufficient.

## Temporary setup and local checks

- [ ] Create a unique owner-only temporary root and a manifest of every fixture, output and process. Use `umask 077` on POSIX and an owner-only ACL on Windows. Never use a personal home/Desktop or an existing remote project as a fixture root.
- [ ] Isolate `RDC_CONFIG` and the platform home/profile directories. An empty config alone does not disable GUI token fallback for the matching public-server scope. Use a disposable profile with no RustDesk GUI data, clear inherited `RDC_*` variables including OTP/debug settings, explicitly select loopback mock endpoints, and pass `--token=` to disable token fallback where no synthetic token is needed. Verify server-scoped token selection with fixtures.
- [ ] Prepare empty, small UTF-8, spaces/Unicode-name, nested, hidden, compressible and incompressible multi-block files. Record sizes and SHA-256 hashes before transfers. Add a bounded larger file only for interruption tests. Keep all content synthetic.
- [ ] Put sentinel files outside each download root but inside the disposable test area. Hash them before/after confinement tests. Use fixture symlinks only within this area.
- [ ] Reserve fixed mock ports before starting standalone helpers. On a collision, stop this run or change the fixture under a separate change; never kill an unknown listener. Track only processes started for this run.
- [ ] Run the source checks in an isolated build/test environment and save their exit codes. These are commands to run, not results:

```sh
go test -race ./... -count=1 -timeout=120s
go vet ./...
go build -trimpath -o "$FIXTURE_ROOT/bin/rdcli" ./cmd/rdcli
```

- [ ] Build and run standalone `cmd/mockrv` and `cmd/mockapi` from the candidate source if exercising their CLI paths. Use only synthetic IDs and data. Assert online bit ordering, sorted/filterable devices, API response handling, and clean JSON separately. They must never become the default for a later live run.
- [ ] Record `command -v rdcli`, the resolved installed path, its hash, `rdcli version`, and `rdcli --help`. Check the candidate by its full path as well, including help for all seven additions, transfer policies, OTP and shell timeout. Use an isolated environment without reading the user's config. Do not replace an installed binary to perform this comparison. A matching version string without a matching artifact hash is insufficient.
- [ ] Test documented usage errors and JSON-producing commands using fixtures. Assert status 2 for usage errors, status 1 for runtime/auth/network errors, and status 0 only for confirmed success, except where a released command explicitly documents another mapping. Capture stdout and stderr separately. Never require all commands to emit JSON merely because the root accepts `--json`.

## Security and relay

- [ ] S1: With generated keys, accept a valid identity chain; reject invalid server key/signature, wrong server-signed peer ID, absent peer `SignedId`, invalid peer signature, and wrong peer ID. Repeat rejection with `--yes`; it must not override a broken authenticated chain.
- [ ] S2: Reject corrupted/truncated encrypted frames and invalid framing without success, panic or unbounded allocation. Test arbitrary frame splits and coalesced frames. Use bounded fixture payloads and a deadline.
- [ ] S3: Direct-IP/insecure fallback requires the documented explicit opt-in. A normal authenticated connection must not silently downgrade. Exercise plaintext fallback only on a local mock and record that it is not encrypted interoperability evidence.
- [ ] S4: Verify config creation/replacement permissions, symlink refusal, atomic failure preservation, and secret redaction with synthetic values. Check help/errors/debug logs and process arguments for accidental disclosure. Test GUI import using generated GUI-like files only.
- [ ] S5: In the approved lab, a permission-denied operation returns a clear error and nonzero status without performing the operation. Authenticate only within the agreed attempt budget. Use mocks for broad wrong-password and 2FA-negative cases.
- [ ] R1: Run the same small transfer over normal routing and `--relay`. Confirm the actual route through approved endpoint/session evidence, rather than assuming the flag or a successful login proves relay use. Compare byte hashes.
- [ ] R2: Force relay on an approved hbbr with its expected key. Test missing/wrong key and unmatched UUID locally. Failure must be bounded and distinguish rendezvous failure, relay pairing failure, and peer authentication failure.
- [ ] R3: Interrupt only the test connection or its dedicated proxy. Confirm cancellation/timeout, partial-file handling, and reconnection behavior. Do not disable a shared network interface or restart the peer/server.
- [ ] R4: If tunnel support is in scope, use an approved loopback-only echo service on the test peer and an unused local port. Verify exact bytes, EOF and cleanup for normal and `--once` modes. Do not tunnel to an unrelated service.

## Upload, download and resume rejection

`cp` requires explicit `-d up` or `-d down`; there is no direction inference in the command path. Verify omitted/invalid direction fails before connection. Upload additionally requires `--overwrite`, even for a fresh destination, because the protocol cannot guarantee atomic remote no-clobber. These templates require approval for their exact paths and overwrite scope:

```sh
"$CANDIDATE" --server "$APPROVED_SERVER" -c "$APPROVED_PEER" cp -d up --overwrite "$LOCAL_FIXTURE" "$REMOTE_FIXTURE_ROOT"
"$CANDIDATE" --server "$APPROVED_SERVER" -c "$APPROVED_PEER" cp -d down "$REMOTE_FIXTURE_FILE" "$LOCAL_DOWNLOAD_ROOT"
```

- [ ] F1: Upload each fixture, list only its approved remote directory, download to a fresh local directory, and compare names, lengths and SHA-256 hashes. Verify remote hashes independently if approved shell access is available. Record timestamp precision rather than assuming identical filesystem semantics.
- [ ] F2: Repeat `cp -r` trees, zero-byte files, trailing separators, single-file renaming, spaces/Unicode and empty directories. Compare default hidden filtering against `cp --include-hidden` and `ls -a`. Record unsupported cases. Do not claim preservation of links, ownership, ACLs or permissions unless explicitly tested and promised.
- [ ] F3: Downloads must preserve an existing destination by default, skip it with `--skip`, and replace only an approved fixture with `--overwrite`. Test identical and different bytes, and concurrent destination creation before publication. Reject `--skip --overwrite`, upload without `--overwrite`, upload `--skip`, and `--resume` in either direction before dialing. Preserve sentinels on refused operations; do not advertise upload no-clobber/skip.
- [ ] F4: Reject traversal, absolute/drive/UNC paths, duplicate/case-colliding names, reserved partial names, symlink escapes, wrong file/job numbers, oversized blocks and premature completion in mocks. No out-of-root writes and no incomplete final file may appear.
- [ ] F5: Verify compressed and uncompressed blocks, file-level and job-level completion, peer cancellation/error, local write failure, and abrupt EOF. Use a bounded disposable filesystem for disk-full tests. Progress belongs on stderr and must not report successful completion after failure.
- [ ] F6: Interrupt bounded upload/download fixtures, then retry fresh transfers without `--resume`. Upload must restart rather than trust an unverified remote prefix. Verify exclusive `.rdcli-download` staging, directory mode `0700`, regular-file mode `0600`, current-user ownership and a single file link. Normal cleanup must remove only the current job's staging. Stale/concurrent staging must cause refusal without adoption or deletion; legacy `.download` and `.download.rdcli` files must remain untouched. Verify final hashes and byte counts independently.
- [ ] F7: Verify CLI resume rejection before dialing in both directions and download-layer rejection regardless of peer capability. There is no checkpoint adoption, saved whole-job manifest or automatic recovery. In an isolated abrupt-kill fixture, verify leftover staging blocks a new transfer. Inspect it manually, establish ownership and confirm no transfer is active before removing only the abandoned fixture staging and retrying from zero. For a directory rerun, test the selected existing-file policy for already completed entries.
- [ ] F8: Test ownership/type/link-count checks, unsafe directory permissions, staging replacement and local data tampering in fixtures. Verify fail-closed behavior where ownership checks are unavailable, including Windows clients. ACL grants are not inspected; hostile same-user or privileged processes are outside this protection. Use stable sources and independent end-to-end hashes: local staging integrity does not establish a remote checksum or detect every remote source change.
- [ ] F9: With separate mutation approval, create a disposable nested directory with `mkdir`, rename a fixture within that directory using `rename --overwrite`, then remove only fixture files with `rm --confirm-delete` and empty directories with `rm --dir --confirm-delete`. Verify missing consent, `--yes` alone, recursive delete, invalid new names, unmatched response IDs and unsupported rename peers fail without mutation. Do not use these tests on pre-existing user files.

## Terminal completion, exit and reattachment

Use a disposable terminal and only bounded commands. Record the peer shell and its quoting/exit semantics. Do not run installation scripts or modify shell startup files.

- [ ] T1: Open an interactive terminal, print a synthetic marker, resize, and close it. Verify delivered dimensions, Unicode output, EOF/Ctrl-D, cancellation, and restoration of local terminal settings after both success and failure.
- [ ] T2: On Linux/macOS, a POSIX one-shot command prints a marker, waits beyond the former idle interval, then prints a final marker. Confirm the internal completion marker, not silence, determines completion. Also test silent delays, no-output commands, shell quoting and `exec`. Verify `--command-timeout`, rejection of nonpositive timeouts and the ignored/deprecated `--idle` behavior. Keep an external bounded deadline for the run.
- [ ] T3: Execute success and nonzero POSIX commands such as `exit 7`; assert local status 0 or the exact remote status 7 and final output drain. A disconnect, `TerminalError`, decompression failure or timeout must not become status 0. Distinguish command completion from shell/PTY closure. Verify Windows one-shot execution is rejected rather than claiming POSIX compatibility.
- [ ] T4: Test chunked/compressed output, prompt/command echo, marker-like user output, stdout/stderr behavior through a PTY, and a final output frame arriving next to `TerminalClosed`. Document whether a PTY merges stdout/stderr; do not promise separation without evidence.
- [ ] T5: Current-release check: help must not advertise attach/detach, persistence or saved terminal sessions. Mark live reattachment `BLOCKED`, not passed or implemented. Do not emulate attachment by opening terminal ID zero again.
- [ ] T6: Deferred attach-only gate: before any future release advertises attachment, prove stale/unknown service or terminal identity fails without creating or remapping a shell. Then test approved same-session/process continuity, bounded replay, multiple terminal IDs, cross-peer denial and resize after replay. These scenarios remain deferred, not current E2E instructions.
- [ ] T7: Close only the current run's terminal and verify its bounded task exits. Check pipeline stdin EOF still allows delayed output to drain, plus cancellation and nonzero terminal close. There is no current CLI persistent-session state to clean up. Do not terminate another user's terminal or restart the remote service.

## Screenshots

`screenshot --output <file> [--display <index>]` is registered. Screen access always needs its own approval, even if a remote-session login already succeeded.

- [ ] P1: Have the test owner display a synthetic chart with a run marker on the approved display. Request one screenshot. Decode the saved image independently; verify dimensions, format and marker instead of accepting any nonempty response.
- [ ] P2: Correlate response `sid` with the request. Test stale/wrong IDs, empty/corrupt data, peer error, permission denial, unsupported peer and timeout using mocks. Failure must not leave a file presented as a successful screenshot.
- [ ] P3: Test approved display selection, multiple monitors, scale/rotation and locked/headless/capture-permission states only where the release claims support. Never enable capture permission or unlock a session implicitly.
- [ ] P4: Reject absent output, `--output -`, negative display indices, symlink/directory output and existing files without `--overwrite`. Check private temporary-file handling, PNG limits and JSON metadata. The implementation temporarily subscribes to the selected display; verify the connection closes after the result and capture does not persist. No input events or clipboard subscription should occur. Retain only approved synthetic evidence, then remove image files.

## Explicit text clipboard

- [ ] C1: Use a dedicated desktop session whose clipboard the owner has populated with a synthetic marker. Approve remote read and remote write separately. A test on a personal clipboard requires separate permission to read, replace and restore its content; prefer the dedicated session.
- [ ] C2: Use `clipboard get` for one text update into stdout or JSON without changing the local OS clipboard. The peer may publish changes only; unchanged/empty content can time out. Use `clipboard set` with explicit text or `--stdin`, and verify success reports `submitted` with `acknowledged: false` in JSON. Confirm actual application separately through approved owner observation, never infer it from exit 0.
- [ ] C3: Check empty text, Unicode, newlines, UTF-8 validation and the 1 MiB bound for compressed/uncompressed content. Older peers may ignore empty writes; record the result. Reject simultaneous text argument and `--stdin`. Test unsupported formats and decompression failure in mocks. Do not enable file/image clipboard transfers as part of a text test.
- [ ] C4: Denied permission, unsupported session scope and timeout fail clearly. `get` needs clipboard and keyboard permissions for the peer's broadcast; `set` must not require keyboard permission. Verify default-session login starts with clipboard disabled, display capture is unsubscribed, and only explicit `get` enables receiving clipboard updates. A diagnostic command must not synchronize clipboard content. Text belongs only in the explicit get result, never debug logs or unrelated JSON metadata.
- [ ] C5: Stop subscriptions and restore/clear only the test session's clipboard as agreed. If its content changed independently during the test, ask the owner before replacing it.

## Peer 2FA

- [ ] A1: Distinguish server/API login from peer 2FA. The approved test peer should already be configured for 2FA. Do not enroll, disable, or reset a personal account for verification.
- [ ] A2: In encrypted fixtures, send `2FA Required` after password login. Exercise file, terminal, tunnel, screenshot, clipboard and both diagnostic command paths. A supplied `--otp`/`RDC_OTP` value is used only after challenge; missing input fails clearly without an interactive prompt. No operation runs before authorization, and sending the code alone must not count as login success.
- [ ] A3: Cover missing, malformed, wrong, expired and replayed codes, repeated challenges, cancellation, connection loss and timeout in mocks. Verify 6-8 ASCII-digit validation, at most one submission per connection, no resubmission on repeated challenge, and rejection of plaintext OTP even with `--yes`. A failed attempt requires an explicit fresh invocation; no automatic retry or trust enrollment.
- [ ] A4: With separate approval, complete one real valid challenge using operator-supplied `RDC_OTP` in an isolated process environment with tracing disabled. The supported `--otp` flag would expose its value in argv, so use only synthetic values there during fixture tests. Record success/failure and timing only; do not retain codes/seeds in logs, history, config, diagnostic bundles or evidence.
- [ ] A5: Use a fresh isolated client identity to avoid mistaking a trusted device or recent session for a tested challenge. Do not persist trusted-device state without explicit approval. Remove only test-created state during cleanup.

## Diagnostics and release decision

- [ ] D1: Verify `info` and `doctor` help describes a live authenticated probe and requires one approved target. Approval must cover the default desktop session's possible brief capture startup. Neither command requests a screenshot, retains screen content, reads clipboard content or mutates remote files. Keep local help/version inspection separate from these networked diagnostics.
- [ ] D2: On fixtures, verify distinct failures for configuration, DNS/connect, rendezvous authentication, identity verification, relay pairing, peer password/2FA, permission, unsupported feature, timeout and remote disconnect. Report the failing stage without secret values or payload dumps.
- [ ] D3: Verify human output and advertised JSON schemas separately. Validate version fields, exit codes, stderr separation and redaction using synthetic canary secrets. Preserve `unknown` versus `false` permission/security observations. Capabilities are peer advertisements, and successful probing does not establish terminal/tunnel/file permission. Match route/encryption fields to connection observations, not requested flags alone.
- [ ] D4: Review diagnostic bundles before sharing. Exclude credentials, personal peer IDs, filesystem listings outside fixtures, screenshot/clipboard content and persistent-session identifiers. Record evidence paths and hashes, not secret-bearing output inline.
- [ ] G1: Attach source-check results, encrypted-mock results, artifact checks and real-peer rows separately. Mark omitted live checks `NOT RUN` or `BLOCKED`, with their approval/fixture gap. Do not describe an unexecuted checklist as verification.
- [ ] G2: Block claims for a feature whose required scenario fails or lacks real-peer evidence. A security-fix release can explicitly defer future features without calling their cases passed. Record known limitations and the compatibility matrix in release notes.
- [ ] G3: Have the release owner reconcile the final commit, reviewed changes, CI artifact hashes and installed-binary evidence. A build/cross-compile proves compilation, not native runtime behavior.

## Cleanup and remaining fixture gaps

- [ ] Stop only recorded mock/proxy/echo processes and test terminals. Verify their listeners and sessions ended. Never use broad process-name kills.
- [ ] Inspect the local and remote fixture manifests. Confirm each cleanup path is the exact nonempty run root, is not a symlink, and contains the run's ownership marker. Refuse home, filesystem root, shared directories and paths outside the manifest. Remove only run-owned content; do not issue blind recursive deletes.
- [ ] Check sentinels and any pre-existing disposable destination copies. After an abrupt kill, inspect abandoned `.rdcli-download` staging and confirm no transfer is active before manually removing only the run-owned staging. Preserve pre-existing staging and legacy partial/checkpoint files. Remove only manifest-owned test state. Restore only explicitly approved test settings, with the owner's help if the state has changed.
- [ ] Keep redacted text evidence for the agreed retention period. Remove screenshot/clipboard payloads and authentication material from temporary storage. Record cleanup failures for the owner rather than broadening access.

Remaining evidence and fixture gaps before broad E2E claims:

1. Local combined-suite, vet, clean indexed-source and final code reviews pass. The final committed-checkout rerun, native CI build/smoke results and published artifact hashes remain separate checks; workflow definitions are not completed runs.
2. Existing command-contract, screenshot command, pipeline, completion and policy tests are more than structure-only tests. Full subprocess checks of the exact release artifact, isolated authentication environment, OS exit status and stdout/stderr still need release evidence.
3. Encrypted relay/OTP fixtures and framing regression tests exist. Real hbbs/hbbr routing, sustained file/terminal traffic and relay failure behavior still need approved E2E evidence; do not equate scripted route state with an observed real route.
4. Fresh-download policy, exclusive-staging, ownership, tampering and unconditional resume-rejection fixtures exist. Fresh-process artifact checks, abrupt-kill cleanup and native filesystem behavior still need evidence. Resume and persisted recovery are unavailable; legacy checkpoints are not adopted. Ownership checks fail closed on unsupported clients, including Windows. ACL grants and hostile same-user/privileged processes are outside the checked boundary, and local staging hashes provide no remote checksum guarantee.
5. Local shell-wrapper, parser and encrypted pipeline tests exist. Real-peer POSIX completion, output drain, exact exit status, interactive resize and terminal cleanup remain unverified here. Persistent reattachment is blocked on attach-only semantics, not merely a missing test.
6. Screenshot, clipboard, OTP, file-operation and diagnostic fixtures now exist. They use a mix of plaintext, pre-keyed encrypted and generated-key sessions. Record the specific test and transport setup with results; none establishes actual screen/clipboard behavior or official-peer permission handling.
7. The approved live attempt stopped at an offline/nonexistent rendezvous response. No feature operation ran. A reachable approved test peer is still needed for the compatibility matrix; retain that blocker without publishing its personal identifier.
