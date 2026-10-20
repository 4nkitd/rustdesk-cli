# Security policy

## Reporting a vulnerability

Report vulnerabilities privately through GitHub's [security advisories page](https://github.com/4nkitd/rustdesk-cli/security/advisories) if private reporting is enabled. Do not put exploit details or credentials in public issues.

Include the affected version or commit, reproduction steps using synthetic credentials, and impact. Redact tokens, passwords, OTPs, config contents, and private session data from attachments. Response times and fix dates are not guaranteed.

## Status and limitations

Security hardening is underway. The requirements below are acceptance criteria, not a claim that every released binary meets them. Changes in the working tree still need tests, parent review, and release approval. This policy is not an independent security audit.

Check the exact version and release notes before relying on a fix. Older releases may lack protections added during this work; this project does not promise backports or a support window.

An encrypted connection alone does not establish peer identity. An unsigned connection accepted through an insecure opt-in lacks verified identity. A trusted peer can still send malicious files or command output.

## What this project handles

rdcli handles login tokens, peer passwords, GUI credential imports, remote shells, tunnels, and file transfers. Its config defaults to `~/.config/rdcli/config.toml`, with an override through `RDC_CONFIG`. Treat it as a secret file. Owner-only permissions are not encryption or a keychain, and local compromise remains outside that protection.

## Required security boundaries

- Fail closed on invalid signatures, malformed identities, peer-ID mismatches, or missing identity messages in a signed handshake. Never silently downgrade after verification fails.
- Unsigned connections require an explicit insecure opt-in. That opt-in must never bypass a failed signed-identity check or imply verified identity.
- Never log tokens, passwords, OTPs, or credential-bearing payloads, including in debug output, JSON errors, and test artifacts. Error messages from servers and peers are untrusted and may echo secrets.
- Keep downloads inside the user-selected destination. Reject traversal, absolute or drive-qualified remote names, unsafe links, and conflicting names. Validate transfer IDs, file indexes, sizes, and completion before publishing a finished file; a partial transfer is not success.
- Do not capture screens or read, write, or synchronize clipboards implicitly. Any such feature needs an explicit request scoped to the intended peer and operation.
- Destructive operations need dedicated consent for their target and effect. A connection's insecure opt-in or a generic `--yes` must not authorize deletion, replacement, or other destructive actions.

## Verification and release gates

Security changes need regression tests for rejection paths as well as successful use. See [CONTRIBUTING.md](CONTRIBUTING.md) for required checks. The parent reviewer must inspect the combined changes and unresolved findings before merge or release. A passing unit test is not an interoperability test or an audit.

## Responsible disclosure

Coordinate disclosure and remediation with the maintainers through the private report. Agree timing there rather than assuming a fixed disclosure deadline.

## Scope

This policy covers rdcli. Report issues in upstream RustDesk implementations through that project's own security channels.
