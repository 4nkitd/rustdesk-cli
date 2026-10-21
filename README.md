# rdcli: headless RustDesk client CLI

[![Go](https://img.shields.io/badge/Go-1.25-00ADD8?logo=go&logoColor=white)](https://go.dev)
[![Release](https://img.shields.io/github/v/release/4nkitd/rustdesk-cli)](https://github.com/4nkitd/rustdesk-cli/releases)
[![License](https://img.shields.io/badge/license-AGPL--3.0-blue)](LICENSE)
[![CI](https://img.shields.io/github/actions/workflow/status/4nkitd/rustdesk-cli/release.yml?label=release%20builds)](https://github.com/4nkitd/rustdesk-cli/actions)
[![Stars](https://img.shields.io/github/stars/4nkitd/rustdesk-cli?style=social)](https://github.com/4nkitd/rustdesk-cli)

A pure-Go CLI that speaks the RustDesk wire protocol directly, without the official GUI or binary. Connect to RustDesk peers by ID, name, or tag for file transfers, TCP tunnels, and remote shells. Listing commands offer tables and JSON; shell and tunnel commands carry stream output. Availability depends on the peer's version, permissions, and server configuration.

This README describes the published [v0.2.0 release](https://github.com/4nkitd/rustdesk-cli/releases/tag/v0.2.0), also available through Homebrew. The new commands pass local mock tests, but real-peer compatibility remains unverified; see [verification status](#verification-status).

![rdcli demo](docs/demo.svg)

```
rdcli -c home-pc ls C:/Users/jane
rdcli -c home-pc cp -d up --overwrite ./backup.tar.gz C:/Users/jane/Desktop/
rdcli -c linux-box sh "uname -a"
rdcli -c home-pc tunnel -L 3389:localhost:3389
```

## Why

RustDesk's public server now requires login, and the desktop GUI is the only official client. `rdcli` speaks the protocol itself: it reuses your existing RustDesk login (including the desktop app's remembered credentials), punches through NAT via hbbs, relays via hbbr, and authenticates to the controlled peer — all from a terminal.

## Use cases

- **Scripting & automation** — run remote commands, copy files, and set up tunnels from cron, CI, or deploy scripts; exit codes and JSON output make it pipe-friendly.
- **AI agents** can use JSON from commands such as `devices` and `ls` to list devices and inspect files. See [JSON output](#json-output) for the limits of `--json`.
- **Headless boxes** — manage machines that only have RustDesk installed (no display, no GUI session) straight from your terminal.
- **Homelab / self-hosted RustDesk** — point it at your own hbbs/hbbr with `--server`/`--key`, or use the Pro address-book API for device discovery.
- **RDP/SSH jump** — `tunnel -L` turns any RustDesk peer into a bastion: `rdcli -c home-pc tunnel -L 3389:localhost:3389` then `mstsc`.

## Install

### Homebrew

```sh
brew tap 4nkitd/tap
brew install 4nkitd/tap/rustdesk-cli
```

The tap is hosted at [4nkitd/homebrew-tap](https://github.com/4nkitd/homebrew-tap). Its formula installs v0.2.0 and bundled license notices. Upgrade, `brew test`, installed version/help and checksum checks passed on macOS arm64. Existing users can run `brew update` followed by `brew upgrade 4nkitd/tap/rustdesk-cli`.

### Prebuilt binaries

Grab the right `rdcli-<os>-<arch>` asset from [Releases](https://github.com/4nkitd/rustdesk-cli/releases), then:

```sh
chmod +x rdcli-linux-amd64
sudo mv rdcli-linux-amd64 /usr/local/bin/rdcli
```

### From source

v0.2.0 repairs the module path and includes the previously ignored CLI entrypoint. Public installation and startup checks passed with both `@v0.2.0` and `@latest` on macOS arm64. The older v0.1.0 tag cannot be installed using this path.

```sh
go install github.com/4nkitd/rustdesk-cli/cmd/rdcli@latest
```

## Quick start

```sh
# 1. Pull your machines + passwords from the desktop app (optional but handy)
rdcli import-gui

# 2. Check login status (the CLI can reuse your RustDesk GUI login)
rdcli auth status

# 3. Remember a machine's password (by id, name, or tag)
rdcli connect home-pc -p "your-password"

# 4. Go
rdcli -c home-pc ls C:/Users/jane
rdcli -c home-pc cp -d up --overwrite ./file.txt C:/Users/jane/Desktop/
rdcli -c home-pc cp -d down C:/Users/jane/Desktop/file.txt ./
rdcli -c linux-box sh "whoami"
rdcli -c home-pc tunnel -L 8080:localhost:80
```

Machine refs first match an exact configured ID, then a case-insensitive name substring or tag. Multiple matching peers are an error; use an exact ID. Otherwise, a valid raw RustDesk ID or IP address is accepted.

## Authentication

The public RustDesk server requires a login token. `rdcli` gets one from:

1. `--token` flag
2. `RDC_TOKEN` env var
3. `access_token` in your `config.toml`
4. **The desktop GUI's stored login** (auto-detected from your RustDesk install)

```sh
rdcli auth status        # show stored token/password presence metadata
rdcli auth import-gui    # persist the desktop app's token into rdcli config
rdcli auth set <token>   # set a token explicitly
rdcli auth logout        # clear the stored token
```

With a token present, `rdcli` performs the full hbbs `KeyExchange` handshake (signed server key + NaCl box session) and sends the token as `PunchHoleRequest.token` — the same flow as the official client.

Saved peer passwords work too: `rdcli` understands the desktop app's encrypted `00...` password blobs (decrypts them with the machine key like the GUI does) as well as plaintext passwords.

Stored tokens are scoped to the configured rendezvous server; automatic GUI-token fallback applies only to the public server. `auth status` reports stored config/GUI token presence, not a live login check or the effective `--token`/`RDC_TOKEN` override. Its JSON schema is version 2 and contains only `logged_in`, optional `source`, and `password_set` metadata, with no token or session handle.

For peer two-factor authentication, supply `--otp <code>` or `RDC_OTP`. The client sends one 6–8 digit code on the authenticated encrypted connection after a peer challenge. It does not save the code or enroll a trusted device. A rejected code requires a new connection with a fresh code. This is separate from the account login token and also applies to `info` and `doctor`.

## Commands

### `rdcli ls <ref> [remote_path]`

List a remote directory, defaulting to the current directory. Hidden entries are excluded unless `--all` or `-a` is supplied.

```sh
rdcli -c home-pc ls
rdcli -c home-pc ls C:/Users/jane --json
rdcli -c home-pc ls C:/Users/jane --all
```

### `rdcli cp [<ref>] <src> <dst> [-r]`

Copy files with an explicit direction: `-d up` uploads, and `-d down` downloads. Direction is required; paths are not used to guess which side is remote.

```sh
rdcli -c home-pc cp -d up --overwrite ./backup.zip C:/Users/jane/Desktop/
rdcli -c home-pc cp -d down C:/Users/jane/Desktop/backup.zip ./
rdcli -c home-pc cp -d up --overwrite -r ./project C:/Users/jane/project
```

- Downloads refuse existing destination files by default. `--overwrite` permits replacement; `--skip` skips existing files. These flags are mutually exclusive.
- Uploads require `--overwrite`, because the peer protocol cannot guarantee atomic no-clobber or skip.
- `--include-hidden` includes hidden files and directories. `-r` enables recursive uploads.
- `--resume` is unavailable for both directions and is rejected before dialing. The protocol cannot prove remote-prefix freshness. Checkpoint adoption and persisted transfer recovery are not implemented; retry a fresh transfer.

Fresh downloads create an exclusive `.rdcli-download` staging directory with mode `0700` and a `part.download` file with mode `0600`. The client checks ownership, file type, identity and single-link file status before publication. Pre-existing staging from a stale or concurrent job is never adopted or deleted; legacy `.download` and `.download.rdcli` files are left untouched. An abrupt process kill can leave staging behind. Inspect it manually, confirm no transfer is active, and remove only your abandoned staging before retrying.

Downloads fail closed where ownership checks are unavailable, including Windows clients. These checks do not inspect ACL grants or protect against hostile processes running as the same user or with elevated privileges. Local hashing checks staging integrity; it does not provide a remote checksum guarantee.

Progress and completion messages go to stderr. `cp` does not emit a JSON result.

### `rdcli mkdir` / `rdcli rm` / `rdcli rename`

```sh
rdcli -c home-pc mkdir C:/Users/jane/work/logs
rdcli -c home-pc rm C:/Users/jane/work/old.txt --confirm-delete
rdcli -c home-pc rm C:/Users/jane/work/empty --dir --confirm-delete
rdcli -c home-pc rename C:/Users/jane/work/old.txt new.txt --overwrite
```

`mkdir` includes missing parents. `rm` requires `--confirm-delete` or `--force`; `--dir` selects an empty directory, and deletion is never recursive. `--force` still reports remote errors. `rename` accepts a new filename within the same directory and requires `--overwrite` for every rename because the peer can replace an existing destination without a no-clobber handshake. Global `--yes` does not authorize deletion or replacement. These commands report peer operation results and support `--json`.

### `rdcli sh [<ref>] [command]`

Open an interactive PTY without a command. One-shot commands require a POSIX shell on a Linux or macOS peer; Windows one-shot execution is rejected.

```sh
rdcli -c home-pc sh
rdcli -c linux-box sh "uname -a"
rdcli -c linux-box sh --command-timeout 30s "exit 7"
```

One-shot execution waits for an explicit command-completion marker and returns the remote exit status. Output silence no longer ends a command. `--command-timeout` defaults to 5 minutes after opening the terminal; deprecated `--idle` is ignored. PTY output combines stdout and stderr. Disconnects, terminal errors, and unconfirmed completion return errors.

There are no attach/detach flags. Persistent reattachment is blocked because upstream can create or remap a terminal instead of attaching only to the requested old session. See [compatibility notes](docs/COMPATIBILITY.md#terminal-reattachment).

### `rdcli screenshot [<ref>] --output <file>`

```sh
rdcli -c home-pc screenshot --display 0 --output ./screen.png
rdcli -c home-pc screenshot --output ./screen.png --overwrite --json
```

Request one PNG screenshot. `--display` is zero-based and defaults to 0. `--output` or `-o` is required; stdout image output and `--output -` are unsupported. The parent directory must exist. Existing output files require `--overwrite`, and non-regular output files are rejected. `--json` returns metadata, not image bytes.

The implementation requires a peer reporting RustDesk 1.4.0 or later, an available display, and working capture permissions. It subscribes to the selected display and validates the returned PNG. This source-level gate is not a claim of live compatibility.

### `rdcli clipboard get` / `rdcli clipboard set`

```sh
rdcli -c home-pc clipboard get --timeout 15s
rdcli -c home-pc clipboard set "explicit text"
printf 'explicit text' | rdcli -c home-pc clipboard set --stdin
```

Text must be UTF-8 and at most 1 MiB. The commands never access the local OS clipboard. `get` waits for one remote text update, not a guaranteed snapshot. An unchanged or empty clipboard can time out; timeout does not mean the clipboard is empty. Reads require remote clipboard and keyboard permission and can be suppressed by one-way policy. Plain output preserves the text without adding a newline; `--json` returns a `text` field.

`set` accepts exactly one text argument or `--stdin`, including empty text. Older peers may ignore empty text. Success means the text was **submitted**, not that the remote clipboard changed. JSON reports `status: "submitted"` and `acknowledged: false`.

Clipboard commands use a default desktop session, which may briefly activate remote video capture before subscriptions are disabled. They do not provide background clipboard synchronization.

### `rdcli info [<ref>]` / `rdcli doctor [<ref>]`

```sh
rdcli -c home-pc info --json
rdcli -c home-pc doctor
```

Both commands connect to one peer, authenticate, report the connection and peer information, and close the session. They are networked probes, not local-only checks. Output includes transport, security state, advertised capabilities, observed permissions, and failure stage; missing observations remain unknown. Both support `--json` and peer `--otp`/`RDC_OTP`.

Success does not establish permission to transfer files, run terminals, or open tunnels. `--timeout` applies to each connection phase and separately to authentication. The probe makes no screenshot request and retains no screen content, but its default desktop session may briefly activate capture on the peer.

### `rdcli tunnel <ref> -L <lport>:<rhost>:<rport> [--once]`

TCP port forward through the peer.

```sh
rdcli -c home-pc tunnel -L 3389:localhost:3389      # RDP through the peer
rdcli -c home-pc tunnel --once -L 0:localhost:22    # single stream, pipes stdin/stdout
```

### `rdcli devices` / `rdcli tag`

Address book and local tags.

```sh
rdcli devices                     # table: ID NAME TAGS ONLINE PLATFORM LAST_SEEN
rdcli devices --online --tag prod
rdcli devices search home --json
rdcli tag add 1234567890 prod
rdcli tag rm 1234567890 prod
rdcli tag ls
```

### `rdcli connect` / `rdcli disconnect`

Save/forget peer passwords in config (supports id, name, or tag refs).

```sh
rdcli connect home-pc -p "your-password"
rdcli connect 1234567890          # uses RDC_PASSWORD without prompting
rdcli disconnect home-pc
```

### `rdcli ping <ref>`

Online check via the rendezvous server (exit 0 = online, 1 = offline).

## Common flags

| Flag | Env | Description |
|---|---|---|
| `-c, --connect <ref>` | | Machine ref: id, name, or tag |
| `--password <pw>` | `RDC_PASSWORD` | Peer password |
| `--otp <code>` | `RDC_OTP` | Peer 2FA code; never saved |
| `--token <tok>` | `RDC_TOKEN` | Login token (falls back to config / GUI) |
| `--server <host:port>` | `RDC_SERVER` | Rendezvous server (default: rs-ny.rustdesk.com:21116) |
| `--key <key>` | `RDC_KEY` | Licence key for self-hosted servers |
| `--relay` | | Force relay, skip hole punching |
| `--yes` | | Allow unsigned, unencrypted connections on a trusted network; not deletion/overwrite consent |
| `--json` | | JSON output for commands that implement it; see below |
| `--timeout <duration>` | | Connect timeout (default 10s) |

## JSON output

Commands such as `devices` and `ls` emit a single JSON object on stdout with `--json`. Screenshot metadata, clipboard operations, `info`, `doctor`, and file mutations also support JSON. `auth status` uses schema version 2 with credential-presence metadata only. Diagnostics and file operations can emit structured failure results on stdout as well as an error on stderr.

The flag is inherited by other commands, but that does not mean every command implements JSON output. `sh` emits terminal output, `tunnel --once` pipes bytes, and `cp` reports progress and completion on stderr rather than returning a JSON object.

Example `devices --json` output:

```json
{"version":1,"devices":[{"id":"1234567890","name":"home-pc","tags":["windows"],"online":true,"platform":"Windows","last_seen":"","source":"local"}]}
```

Example `ls --json` output:

```json
{"version":1,"path":"C:/Users/jane","entries":[{"name":"Desktop","type":"dir","size":0,"modified_time":1755317100,"is_hidden":false}]}
```

## Config

`~/.config/rdcli/config.toml` (override with `$RDC_CONFIG`):

```toml
server = ""               # empty = public rustdesk.com servers
key = ""                  # self-hosted licence key
access_token = ""         # public-server login token (see `rdcli auth`)

api_server = ""           # self-hosted Pro API base URL (address book)
api_username = ""
api_password = ""

[peers."1234567890"]
password = "00AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=="
name = "home-pc"

[tags]
"1234567890" = ["windows", "prod"]
```

`rdcli import-gui` populates peers from your desktop app automatically. The config file is written with `0600` permissions.

## Exit codes

| Code | Meaning |
|---|---|
| 0 | Success |
| 1 | Runtime / auth / network error |
| 2 | Usage error |

Confirmed remote shell exit statuses are returned as-is, including 1 or 2. A shell's nonzero exit can therefore overlap the CLI error codes above. Transport failure or unconfirmed command completion is an error, not successful shell completion.

## Limitations

- Full video/audio streaming remains deferred. Screenshot capture and explicit text clipboard commands do not provide continuous screen sharing or clipboard synchronization.
- Direct hole punching uses TCP. Some peers need relay fallback, which depends on relay availability, authentication, and network access. `--relay` forces a relay attempt, not a successful connection.
- Terminal sessions require advertised terminal support and permission. One-shot commands support Linux/macOS POSIX shells only. No persistent attach/detach is implemented.
- **Public server needs a login token** — same as the official client since RustDesk enabled mandatory login.
- **Not affiliated with RustDesk.** An independent, clean-room-ish port of the wire protocol (see License).

## FAQ

**Is this an official RustDesk tool?**
No. It's an independent CLI that interoperates with the RustDesk ecosystem (public servers, self-hosted hbbs/hbbr, and the official desktop client).

**Is rdcli the "Go Client" botnet?**
No. That's a separate ~2M-IP credential attack reported in [rustdesk/rustdesk#14167](https://github.com/rustdesk/rustdesk/discussions/14167). rdcli is fully auditable AGPL-3.0 source, connects only to IDs/names/tags you explicitly pass, and has no scanning or brute-force functionality. Contributions and security reviews are welcome so the code stays trustworthy.

**Can I use it with my self-hosted RustDesk server?**
Yes — pass `--server <host:port>` and `--key <licence-key>`, or set them in config. Self-hosted Pro address books work via `api_server`/`api_username`/`api_password`.

**Do I need the RustDesk GUI installed?**
No. The GUI is optional — `import-gui` is a convenience to reuse an existing login and saved passwords. Everything works standalone with `--token`/`RDC_TOKEN`.

**Is it safe to store passwords in config?**
The config file is written with `0600` permissions. Passwords supplied to `connect` are stored as plaintext; imported GUI credentials may be encrypted `00...` blobs that rdcli decrypts for authentication. Treat the config as a credential file, not an encrypted password vault.

**Why AGPL-3.0?**
The wire protocol details were ported from the AGPL-licensed RustDesk sources, so rdcli is AGPL-3.0 too. That's the correct and honest license for this project.

## Protocol notes

- **Framing**: variable-length little-endian header of 1–4 bytes; bottom 2 bits encode header length minus one, and the remaining bits encode payload length.
- **Crypto**: NaCl `box` for key exchange, `secretbox` for streams with sequential nonces; Ed25519 server-signed identity blobs (signature-first layout, like sodiumoxide `sign::sign`).
- **Rendezvous**: `PunchHoleRequest` over TCP, token-authenticated via hbbs `KeyExchange`. Direct connections and relay fallback depend on server and network conditions.
- **Login**: password hashed `sha256(sha256(pw+salt)+challenge)`; supports the GUI's encrypted `00`-prefixed password blobs.

## Unreleased

The current source registers screenshot, clipboard, info, doctor, mkdir, rm, and rename commands. It adds hidden listings, explicit transfer policies, peer OTP authentication, and command-confirmed terminal completion. The command reference above describes their implemented interfaces. See [CHANGELOG.md](CHANGELOG.md) for changes and remaining limits.

Security changes reject invalid signed peer identities even with `--yes`, confine download paths, and keep authentication status output to metadata. `--yes` can explicitly allow a missing-identity plaintext connection; it cannot bypass a broken signed identity chain or permit sending an OTP over an unencrypted connection.

The module is now `github.com/4nkitd/rustdesk-cli`, generated protobuf imports have been regenerated, and root-anchored ignore rules no longer hide `cmd/rdcli`. These source-install repairs ship in v0.2.0, which is also available through Homebrew.

### Verification status

The combined local race suite, vet, and clean committed-source install/startup checks pass on macOS arm64. GitHub Linux/macOS tests and all four native build/smoke jobs passed. All published asset checksums verified, and the downloaded Mac ARM64 binary passed version/help checks. See [release evidence](docs/VERIFICATION.md#published-v020-evidence-2026-10-20). The approved live peer was offline, so these changes have no live interoperability result. Source inspection, generated messages, and mock tests do not establish real-peer compatibility. Transfer resume and persisted recovery are unavailable, and terminal attach/detach remains blocked in [ROADMAP.md](ROADMAP.md).

- [Compatibility research](docs/COMPATIBILITY.md) records upstream version gates and protocol limits, not live test results.
- [Verification runbook and release evidence](docs/VERIFICATION.md) separates passing local/release checks from blocked live tests.
- [Release procedure](docs/RELEASING.md) covers CI gates, artifacts, and publication.
- [Protobuf maintenance](docs/PROTOBUF.md) records schema provenance limits and regeneration commands.

## Development

```sh
go build ./...
go test ./...
go vet ./...
```

The GitHub Actions release matrix targets Linux and macOS on amd64 and arm64 for `v*` tags. Windows is not in the current release matrix.

## License

AGPL-3.0 — this project ports wire-protocol details from the AGPL-licensed [RustDesk](https://github.com/rustdesk/rustdesk) sources.

---

Like it? [Star the repo](https://github.com/4nkitd/rustdesk-cli) and tell me what you'd use it for. Issues, PRs, and wild feature requests are welcome.
