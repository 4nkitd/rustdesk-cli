# Security Policy

## Reporting a vulnerability

Please report security issues privately — do not open a public issue.

- GitHub: use the [private vulnerability reporting](https://github.com/4nkitd/rustdesk-cli/security/advisories) form
- Email: security@4nkitd.in (PGP key available on request)

Include: affected version, reproduction steps, and impact. You'll get an acknowledgement within 48 hours and a fix timeline.

## What this project handles

rdcli stores peer passwords and login tokens in `~/.config/rdcli/config.toml` (written with `0600` permissions). Treat that file like a keychain.

Relevant areas:

- Credential handling (config, env, `import-gui`)
- Crypto handshake (NaCl box/secretbox, Ed25519 identity verification)
- Remote code execution surfaces (`rdcli sh`, `rdcli tunnel`) — connections are end-to-end encrypted and authenticated like the official client
- File transfer paths (`rdcli cp`, `rdcli ls`)

## Supported versions

| Version | Supported |
|---|---|
| latest release | ✅ |
| older releases | ❌ |

## Responsible disclosure

We'll coordinate a fix before public disclosure. Please give us 90 days for critical issues.

## Scope

This policy covers the rdcli codebase. The RustDesk protocol and servers are maintained by the RustDesk project — report issues there via their own channels.
