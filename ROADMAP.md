# Release roadmap

Each release requires regression tests, `go vet`, a fresh read-only code review,
release notes, and a successful GitHub release build. Mock-protocol tests do not
establish compatibility with a real RustDesk peer. Live tests need an explicitly
approved target and operations. Never capture a screen or clipboard implicitly.

The original small sequential milestones were combined after Boss requested
20 parallel agents. **v0.2.0 was published on 2026-10-20** from commit
`6d9fc5893cd92d9f2c0f3a93fef63d6df60438b1`.

- [x] Implement identity, config, resolver, framing and connection hardening.
- [x] Implement command completion and remote exit status for POSIX shells.
- [x] Implement screenshot, clipboard, information and diagnostic commands.
- [x] Implement mkdir, explicit deletion/rename and hidden listings.
- [x] Implement transfer acknowledgement, policies and exclusive fresh-download staging.
- [x] Implement peer 2FA and protected authentication output.
- [x] Migrate module/imports and restore source entrypoints to Git visibility.
- [x] Pass an integrated local race-test/vet run after module migration.
- [x] Resolve all final review findings and rerun the expanded test suite.
- [x] Validate an isolated clean tracked checkout.
- [x] Pass the GitHub Linux/macOS test and native artifact matrices.
- [x] Verify downloaded release checksums and the matching native binary.
- [x] Publish v0.2.0 with exact limitations and verification evidence.

Live verification remains blocked: the approved peer was reported offline before
any remote command or file operation ran. No screenshot/clipboard capture is
authorized. Transfer resume and persisted recovery are unavailable; safe terminal
reattachment is not shipped. Local and GitHub release gates pass; see
[verification evidence](docs/VERIFICATION.md#published-v020-evidence-2026-10-20).

Later candidates, not part of the first feature releases: remote restart with
confirmation, keyboard/mouse control, improved NAT traversal. Full video/audio
streaming remains deferred.

Do not include `tools/oc_setup.ps1` in releases. Do not replace the installed CLI
or change remote machine configuration as part of publishing a release.

## Parallel implementation

Boss approved 20 parallel agents. The parent owns command registration,
integration, final review, commits, tags and publication. Release numbers above
are planned boundaries, not promises that unfinished features will ship.

| Agent | Assignment |
| --- | --- |
| 1 | Terminal completion, exit status and reattachment |
| 2 | Screenshot commands |
| 3 | Explicit text clipboard commands |
| 4 | Peer information and diagnostics |
| 5 | File management and hidden listings |
| 6 | Transfer policies and safe resume |
| 7 | Peer 2FA and relay authentication |
| 8 | Atomic private configuration storage |
| 9 | Framed transport bounds and concurrent writes |
| 10 | Release workflows |
| 11 | Independent security patch review |
| 12 | Destructive-action and privacy safeguards audit |
| 13 | Upstream version/platform compatibility research |
| 14 | End-to-end verification checklist |
| 15 | NAT/relay correctness audit |
| 16 | README and changelog reconciliation draft |
| 17 | Dependency and distribution audit |
| 18 | Public issue and feature-feedback triage |
| 19 | Security and contributor guidance |
| 20 | CLI integration contract tests |

Workers have separate write ownership and may not launch nested agents. Audits
run read-only. No worker may publish or perform live-peer operations. Changes
remain unreleased until the combined tree passes tests and final review.

## Release blockers found during audits

- [x] Track CLI and mock entrypoints, which bare binary ignore patterns hid.
- [x] Align the module/import path with the documented GitHub install path.
- [x] Build with a patched supported Go toolchain and ship dependency notices.
- [x] Verify upload acknowledgements, including files larger than three blocks.
- [x] Separate connection/auth deadlines from established-session lifetime.
- [x] Reject unsolicited ID-zero listings as transfer completion evidence.
- [x] Remove token/session-handle disclosure from status and debug output.
- [x] Reject ambiguous machine names/tags before choosing a password or dialing.
- [x] Verify Linux socket semantics, rendezvous frame retention and tunnel exit in CI fixtures.
- [x] Test release artifacts from a clean tracked checkout, not this worktree.

## Known protocol blockers

`--resume` is rejected before dialing for both directions and again by the
download implementation. The protocol cannot prove remote-prefix freshness, and
checkpoint adoption has been removed. Fresh downloads use exclusive
`.rdcli-download` staging with directory mode `0700` and file mode `0600`, with
ownership, type, identity and single-link file checks. Stale or concurrent staging
is never adopted or deleted; legacy partials remain untouched. After an abrupt
kill, abandoned staging requires manual inspection and removal before retrying.
Confirm no transfer is active before removing it.

Downloads fail closed where ownership checks are unsupported, including Windows
clients. ACL grants are not inspected; hostile same-user or privileged processes
are outside this protection. Local staging hashes provide no remote checksum
guarantee.

Persistent terminal reattachment is not shipping in the current implementation.
RustDesk may create a missing service or terminal, or remap another terminal,
instead of rejecting stale attachment identity. There is no verified attach-only
request that meets this project's fail-closed requirement. No attach/detach flags
will be advertised until this can be implemented and tested without silently
creating or selecting a different shell. Persistence also does not guarantee
survival across a RustDesk restart or reboot.

Clipboard reads wait for a text update; the protocol does not guarantee an
immediate snapshot. Writes have no application acknowledgement and must report
submission rather than confirmed clipboard modification.
