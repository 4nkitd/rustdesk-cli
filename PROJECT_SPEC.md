# rdcli — headless RustDesk client CLI (Go)

A Go CLI that speaks the RustDesk wire protocol directly (no GUI, no official binary required). It connects to a RustDesk rendezvous/relay server (public `rustdesk.com` by default, or self-hosted hbbs/hbbr, or direct IP), authenticates to a peer, and provides TCP tunneling, file transfer, and remote terminal — all headless, with output designed for both humans and AI agents.

Repo root: this directory (`/Users/ankityadav/agent/localhost/rustdesk-cli`). Module name: `rdcli`. Binary name: `rdcli`. Pure Go, no cgo.

## CLI surface

```
rdcli tunnel <id> -L <local_port>:<remote_host>:<remote_port> [flags]   # TCP tunnel, listener mode
rdcli tunnel <id> --once -L <port>:<host>:<port> [flags]                # single stream, pipes stdin/stdout (ssh ProxyCommand style)
rdcli ls <id> <remote_path> [flags]                                     # remote directory listing
rdcli cp <id> <src> <dst> [flags]                                       # file transfer both directions
rdcli sh <id> [flags]                                                   # interactive remote terminal (PTY)
rdcli sh <id> "command" [flags]                                         # one-shot remote command
rdcli devices [--tag <tag>] [--online] [--json]                         # list machines, filter by tag
rdcli devices search <query> [--json]                                   # search by id/name/tag
rdcli tag add <id> <tag> | rdcli tag rm <id> <tag> | rdcli tag ls [id] # tag management
rdcli version
```

Common flags (all commands):

```
--password <pw>      password for the peer (also: env RDC_PASSWORD, or config file)
--server <host:port> self-hosted rendezvous server override (default: public rustdesk.com list)
--key <key>          licence key for self-hosted server (passed as punch request licence_key)
--relay              force relay (no hole punching)
--yes                auto-accept insecure connection (direct IP without encryption)
--json               machine-readable output
--timeout <s>        connect timeout
```

`rdcli cp` semantics: `<id> <src> <dst>` — if `dst` looks like a remote path (starts with `/` or contains `:` after a leading `/`), transfer local→remote, else remote→local; explicit `-d` direction flag overrides. `-r` for directories. Progress bar on stderr.

## Output contract (humans + AI agents)

- Human default: stable aligned table. Columns for devices: `ID NAME TAGS ONLINE PLATFORM LAST_SEEN`. Deterministic ordering (sort by ID).
- `--json`: single JSON object on stdout: `{"version":1,"devices":[{"id":"","name":"","tags":[],"online":false,"platform":"","last_seen":"","source":"api|local"}]}` (schema mirrors per-command data; document each command's schema in README).
- Exit codes: `0` success, `1` runtime/auth/network error, `2` usage error. Errors always on stderr; data always on stdout. Never print progress/colors to stdout.
- No interactive prompts ever — password comes from flag/env/config/stdin-pipe only. `--json` implies non-interactive.
- Env: `RDC_PASSWORD`, `RDC_SERVER`, `RDC_KEY`.

## Config

`~/.config/rdcli/config.toml` (or `$RDC_CONFIG`):

```toml
server = ""              # empty = public rustdesk.com servers
key = ""                 # self-hosted licence key
api_server = ""          # self-hosted Pro API base URL (e.g. https://hbbs.example.com) for address book
api_username = ""        # Pro login credentials for address book API
api_password = ""

[peers."123456789"]
password = "..."

[tags]
"123456789" = ["work", "prod"]   # local tag store, used when api_server is unset
```

Devices/tags data source priority: (1) `api_server` configured → address book via Pro API (`POST /api/login` then `GET /api/ab`; entries carry tags; treat API errors as failures with clear stderr message); (2) otherwise local tag store from config. Online status always refreshed via rendezvous `OnlineRequest` (batched, one request per `devices` invocation). `--tag` filters to devices having that tag (from whichever source). `tag add/rm` operate on the local store when no api_server; with api_server they also push to the AB API if it supports it, otherwise fail with a clear message.

## Architecture

```
cmd/rdcli/main.go            entry
internal/cli/                cobra commands, output formatting, JSON contract
internal/config/             config file load/save
internal/hbb/
  proto/                     generated .pb.go from rustdesk protos (commit generated files)
  codec.go                   BytesCodec frame encode/decode
  crypt.go                   NaCl key exchange + secretbox stream (port of hbb_common/src/crypt.rs)
  addr_mangle.go             port of hbb_common/src/addr_mangle.rs
  secure.go                  SecureStream: framed, encrypted connection
  rendezvous.go              hbbs client: punch hole request/response, online request, register
  nat.go                     TCP hole punch + UDP mapped-port detection
  relay.go                   hbbr client: RequestRelay + KeyExchange + secure channel
  conn.go                    post-connect: Login handshake, auth, PeerInfo, conn types
  tunnel.go                  port-forward session + raw piping (listener & --once modes)
  file.go                    file transfer: ReadDir/ReadAllFiles, send/receive jobs, digest/resume
  terminal.go                TerminalAction/TerminalResponse plumbing
  devices.go                 address book + local tags + online status
```

## Protocol reference (PORT EXACTLY, from these sources)

Fetch these files via webfetch/curl of `raw.githubusercontent.com` and port faithfully. Do not invent protocol details from memory.

| What | Source |
|---|---|
| Protobuf defs (rendezvous, message, fs) | `https://raw.githubusercontent.com/rustdesk/hbb_common/master/protos/rendezvous.proto`, `.../protos/message.proto`, `.../protos/fs.proto` |
| Framing (BytesCodec) | `https://raw.githubusercontent.com/rustdesk/rustdesk/master/libs/hbb_common/src/bytes_codec.rs` |
| Crypto (crypt.rs — sodiumoxide NaCl box/secretbox, nonce scheme) | `https://raw.githubusercontent.com/rustdesk/rustdesk/master/libs/hbb_common/src/crypt.rs` |
| AddrMangle (socket-addr obfuscation) | `https://raw.githubusercontent.com/rustdesk/rustdesk/master/libs/hbb_common/src/addr_mangle.rs` |
| Client rendezvous/punch/relay flow (client.rs — `_start_inner`, `connect`, `create_relay`, `secure_tcp`, `secure_connection`, `OnlineRequest`) | `https://raw.githubusercontent.com/rustdesk/rustdesk/master/src/client.rs` |
| Login/auth/terminal/file handling client-side | `https://raw.githubusercontent.com/rustdesk/rustdesk/master/src/client/io_loop.rs` |
| Server-side login/permission/port-forward/file/terminal handling | `https://raw.githubusercontent.com/rustdesk/rustdesk/master/src/server/connection.rs` |
| Terminal message shapes | `https://raw.githubusercontent.com/rustdesk/rustdesk/master/src/ui_session_interface.rs` (open_terminal / send_terminal_input / resize / close) |
| hbbs server behavior reference | `https://raw.githubusercontent.com/rustdesk/rustdesk-server/master/src/rendezvous_server.rs` |
| hbbr relay pairing reference | `https://raw.githubusercontent.com/rustdesk/rustdesk-server/master/src/relay_server.rs` |

Key facts (verify against sources, then implement):

1. **Framing**: length-prefixed frames. Header: bottom 2 bits = header_len-1 (1..4 bytes), remaining bits = payload length, little-endian. Payload = protobuf bytes (for messages) or raw bytes (for file blocks / tunnel data).
2. **Crypto**: X25519 keypair; shared secret via NaCl `box` (curve25519xsalsa20poly1305); data encryption via NaCl `secretbox` (xsalsa20poly1305) with a 24-byte nonce and sequential nonce increment scheme exactly as in `crypt.rs`. Handshake: `SecureRequest{public_key}` → `SecureResponse{public_key}` → shared secret → `KeyExchange` where needed. Server-signed peer keys: hbbs signs peer X25519 pubkeys with its Ed25519 key; the server's Ed25519 public key comes from config (`--key` server public key; for public servers the well-known one; direct IP connections skip encryption — flag `--yes`).
3. **Rendezvous**: client sends `PunchHoleRequest{id, nat_type, conn_type, token, version, udp_port, force_relay, licence_key}` over UDP (and TCP fallback); hbbs replies `PunchHole` (relay to peer) then `PunchHoleResponse{socket_addr, pk, relay_server, nat_type, failure}`. Handle `ID_NOT_EXIST`/`OFFLINE` failures as clear errors. `OnlineRequest{id, peers[]}` → `OnlineResponse` for online checks.
4. **NAT**: TCP simultaneous-open hole punch (bind same local address used for rendezvous connection, try peer addr, timeout by NAT types; asymmetric → generous timeout, symmetric → quick fail to relay). Fallback: `RequestRelay{id, uuid, socket_addr, relay_server, secure, conn_type, token}` → connect hbbr with uuid → `KeyExchange` → proceed as secure stream. `--relay` forces this path.
5. **Login**: after secure stream established, client sends `Message{login: Login{...}}` with the appropriate flags: `is_port_forward` + `port_forward="lport:rhost:rport"`, or `is_file_transfer`, or `is_terminal`, plus `password` when required. Server replies `PeerInfo` (authorized) or `LoginError`/auth-required flows (port exactly from `io_loop.rs`/`server/connection.rs`). Surface permission errors (file/tunnel/terminal disabled on remote) as clear stderr messages.
6. **Tunnel**: server side connects to `rhost:rport` on the CONTROLLED machine and pipes raw frames bidirectionally. Client side: listener mode binds `127.0.0.1:lport`; each accepted connection gets its own session (fresh Login with the same port_forward spec, own conn id) piped as raw frames; concurrent sessions supported. `--once`: no listener; single session; bidirectional pipe of stdin/stdout (raw, no line buffering, no TTY mangling).
7. **File transfer** (fs.proto): client→server `FileAction` (ReadDir, ReadAllFiles, Send/ReceiveFileList with job id + file list, Create/RemoveDir/RemoveFile, Cancel); server→client `FileResponse` (FileDirectory with entries {name, modified_time, size, is_directory}, SendConfirm, TransferBlock, Digest, Error). Data chunks via `FileTransferBlock{id, file_num, data, compressed}` over raw frames (64KB chunks). Implement: ls (ReadDir), recursive send (walk + job per dir), receive, overwrite via digest check when peer supports it (`overwrite_detection`), resume (`is_resume`, offset_blk) — port from `fs.rs`/`io_loop.rs`; on unsupported details, fall back to plain overwrite semantics. Progress events to stderr.
8. **Terminal** (message.proto): `TerminalAction{open{terminal_id, rows, cols}}`, `data{terminal_id, data}` (input), `resize`, `close`; server replies `TerminalResponse{terminal_id, output}` (bytes) plus open/close events. Interactive mode: raw-mode PTY (golang.org/x/term), open with current window size, forward keys, handle SIGWINCH resize, print output to stdout, exit on terminal close (Ctrl+C/Ctrl+D → send `close`). One-shot mode: open, send `<cmd>\n`, read output until the shell session closes or an idle window (configurable, default: no output for 300ms after first output AND at least 1s elapsed); print output; close; exit 0. Terminal requires peer ≥1.4.1 (PeerInfo version check; clear error otherwise).

## Protobuf generation

`protoc` 35.1 exists at `/opt/homebrew/bin/protoc`; `protoc-gen-go` is missing. Install it with `go install google.golang.org/protobuf/cmd/protoc-gen-go@latest` (add `$(go env GOPATH)/bin` to PATH) and generate `internal/hbb/proto/*.pb.go` from the three `.proto` files above (vendored into `internal/hbb/proto/*.proto`). If generation is impossible, hand-roll minimal wire-format marshal/unmarshal for exactly the messages used (no reflection, explicit field numbers from the .proto files). Commit generated/vendored files. `go.mod` may depend on: `google.golang.org/protobuf`, `golang.org/x/crypto`, `golang.org/x/term`, `github.com/spf13/cobra`, and anything else genuinely needed (YAGNI — no fancy table/color libs; hand-rolled padding for tables).

## Milestones (work in order; run `gofmt -l .`, `go build ./...`, `go vet ./...`, `go test ./...` after each)

- **M1** — module init, protos, `codec.go` + unit tests (frame roundtrip incl. 1–4 byte headers), `addr_mangle.go` + tests, `crypt.go` + `secure.go` + handshake tests. `rdcli version`, `rdcli ping <id>` (OnlineRequest via UDP+TCP) working against public server: `rdcli ping` returns online/offline + latency, exit 0/1 accordingly.
- **M2** — rendezvous punch + NAT + relay + Login/auth; `rdcli tunnel <id> --once -L ...` works end-to-end against a real controlled peer (test against `rdcli sh` mock later; acceptance: `tunnel --once` behaves as a pipe).
- **M3** — `rdcli ls`, `rdcli cp` (both directions, recursive, progress, digest/resume paths implemented).
- **M4** — `rdcli sh` interactive + one-shot.
- **M5** — `rdcli devices`, `devices search`, `tag add/rm/ls`, config file, JSON contract everywhere, README (usage, JSON schemas, config reference, protocol notes, exit codes).

## Constraints

- No comments in code unless genuinely non-obvious (keep them brief). Actually: no comments at all unless explaining a non-obvious protocol quirk.
- Deterministic ordering everywhere; no map iteration order leaks in output.
- All durations/retries as named constants; generous-but-bounded timeouts; every network call has a timeout.
- Errors: wrap with context (`fmt.Errorf("...: %w")`); user-facing messages clear and lowercase-start, no stack traces to stderr.
- Do NOT run git commands. Do not modify PROJECT_SPEC.md.
- Do not push to any remote. Do not install anything outside the repo (except the two `go install` tools needed: protoc-gen-go, and anything `go get` adds to go.mod).
- Report at the end: what was built per milestone, test results, known gaps/risks, and exact commands to verify.
