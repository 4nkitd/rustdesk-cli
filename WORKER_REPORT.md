# WORKER_REPORT.md — rdcli build report

Built: `rdcli` — a headless RustDesk client CLI in pure Go (no cgo), speaking the
RustDesk wire protocol directly. Module `rdcli`, binary `rdcli`.

Protocol implementation was ported from the current upstream sources
(`rustdesk/rustdesk` and `rustdesk/hbb_common` master), fetched during this
work: `bytes_codec.rs`, `tcp.rs`, `lib.rs` (AddrMangle), `common.rs`
(`create_symmetric_key_msg`/`decode_id_pk`/`secure_tcp`), `client.rs`
(`_start_inner`, `connect`, `secure_connection`, `create_relay`, `peer_online`),
`port_forward.rs`, `server/connection.rs`, `fs.rs`, and the current
`rendezvous.proto` / `message.proto`.

## Milestone completion

### M1 — Core protocol libraries + version + ping ✅

- `internal/hbb/proto/` — vendored `rendezvous.proto` + `message.proto`
  (current upstream, fs messages now live inside message.proto) with generated
  `.pb.go` (protoc-gen-go 1.36.12 / protoc 35.1).
- `internal/hbb/codec.go` + tests — BytesCodec framing: header bottom 2 bits =
  header_len−1 (1..4 bytes), remaining bits = little-endian payload length;
  raw mode. 9 tests incl. 1–4-byte headers and incremental decode.
- `internal/hbb/addr_mangle.go` + tests — hbb_common AddrMangle: IPv4
  timestamp-obfuscated u128 encode/decode, IPv6 16+2-byte form. Roundtrip
  tests for IPv4/IPv6/IPv6-mapped.
- `internal/hbb/crypt.go` + tests — X25519 via NaCl box, secretbox Encryptor
  with sequential nonces (counter increments BEFORE use, first message = nonce
  1, matching `tcp.rs`), Ed25519 SignedId verification, box-sealed symmetric
  key creation/decryption.
- `internal/hbb/secure.go` + tests — SecureConn with framed/encrypted/raw
  modes; encrypted mode skips ≤1-byte frames without advancing the nonce
  counter (fidelity to `Encrypt::dec`).
- `internal/hbb/rendezvous.go` + tests — punch request/response plumbing and
  online queries: UDP on the rendezvous port first (classic hbbs, raw
  protobuf), TCP fallback on port−1 (current hbbs OnlineRequest listener);
  state bits are MSB-first per peer (`states[i/8] & (0x01 << (7−i%8))`).
- `internal/cli/version.go` — `rdcli version` → `rdcli v0.1.0`.
- `internal/cli/ping.go` — `rdcli ping <id>`: online/offline + latency,
  exit 0/1, `--json` schema `{"version":1,"id":...,"online":...,"latency_ms":...}`.

### M2 — Punch hole + NAT + relay + login/auth + tunnel ✅

- `internal/hbb/nat.go` + tests — `TestNATType` (TCP probe to 21116 then 21115
  reusing the same local addr, same observed port ⇒ ASYMMETRIC else
  SYMMETRIC, with SO_REUSEADDR for the rebind), TCP simultaneous-open hole
  punch via raw syscall sockets on Linux (falls back to plain dial on macOS).
- `internal/hbb/conn.go` + tests — full Connect flow: punch with 3 retries +
  NAT type in the request, `PunchHoleResponse`/`RelayResponse` handling,
  relay fallback via `RequestRelay` (+ optional secured rendezvous TCP with
  token), `SignedId`→`PublicKey` secure handshake (hbbs-signed peer Ed25519
  key verified with RS_PUB_KEY, then peer-signed ephemeral X25519 pk verified
  with that), Hash challenge/response password login
  (`SHA256(SHA256(password‖salt)‖challenge)`), LoginRequest with
  file/tunnel/terminal unions, direct-IP connections (require `--yes`,
  plaintext).
- `internal/hbb/tunnel.go` — Tunnel: login as PORT_FORWARD, raw mode, pipe.
- `internal/cli/tunnel.go` — `rdcli tunnel <id> -L lport:rhost:rport`
  (listener, one session per accepted connection) and `--once` (stdin/stdout
  pipe).
- `internal/cli/connect.go` — dialOpts mapping CLI flags → DialOpts.

### M3 — File transfer (ls, cp) ✅

- `internal/hbb/file.go` + tests — upload (FileAction receive → wait server
  `FileResponse{Digest{is_upload}}` → reply `FileAction{SendConfirm}` with
  resume offset → stream 64 KiB blocks, zstd-compressed when smaller → one
  `FileResponse{Done}` per job), download (send → Dir list → digest
  confirm/skip → blocks → done, `.download`/`.digest` resume semantics),
  ReadDir/CreateDir/ReadAllFiles, progress callbacks.
- `internal/cli/ls.go` — `rdcli ls <id> [path]`: aligned table (NAME TYPE SIZE
  MODIFIED) + JSON `{"version":1,"path":...,"entries":[...]}`.
- `internal/cli/cp.go` — `rdcli cp <id> <src> <dst>`: direction auto-detect
  (remote-looking dst ⇒ upload), `-r` recursive, `-d up|down` override,
  progress bar on stderr.

### M4 — Terminal (sh) ✅

- `internal/hbb/terminal.go` + tests — TerminalSession: OpenTerminal (rows/cols,
  peer ≥ 1.4.1 check via PeerInfo features/version), data/resize/close,
  zstd decompression of compressed output (upstream `compress.rs` uses zstd),
  TestDelay pong.
- `internal/cli/sh.go` — interactive raw-mode PTY (golang.org/x/term,
  SIGWINCH resize forwarding, Ctrl+C/D sends close) and one-shot
  (`open → send "<cmd>\n" → print until idle window`: no output for
  `--idle` (default 300ms) after first output AND ≥1s elapsed).

### M5 — Devices, tags, config, JSON, README ✅

- `internal/config/config.go` + tests — hand-rolled TOML for
  `~/.config/rdcli/config.toml` (`$RDC_CONFIG` override): server/key/api_*,
  `[peers."id"]`, `[tags]`; deterministic save.
- `internal/hbb/devices.go` + tests — DeviceSource: local tag store and Pro
  address book (`POST /api/login` → `GET /api/ab`, Bearer token, tag push via
  `PUT /api/ab` with clear "not supported" failure); batch online status via
  the UDP+TCP online query.
- `internal/cli/devices.go` — `rdcli devices [--tag] [--online] [--json]` +
  `devices search <query>`; columns `ID NAME TAGS ONLINE PLATFORM LAST_SEEN`,
  sorted by ID; JSON `{"version":1,"devices":[...]}`.
- `internal/cli/tag.go` — `rdcli tag add|rm|ls [id]`, JSON output.
- `internal/cli/usage.go` — usage-error type; exit codes 0/1/2 enforced from
  `main.go` (usage errors ⇒ 2).
- `cmd/mockrv`, `cmd/mockapi` — mock rendezvous/API servers for tests.
- `README.md` — usage, JSON schemas, config reference, protocol notes, exit
  codes, direct-IP section.

## Fixes applied during verification

The initial tree contained a scaffold; a fresh protocol audit against the
fetched upstream sources found and fixed:

1. `CheckOnline` used the wrong online bit (`states[0] != 0` instead of
   `& 0x80`) and dialed TCP 21116 (punch port) instead of UDP 21116 / TCP
   21115; it also masked read errors as "offline". Rewritten as `QueryOnline`
   (UDP-first raw protobuf, TCP port−1 framed fallback, correct bit test,
   latency, error propagation).
2. Upload flow in `file.go` expected the server to send `FileAction{SendConfirm}`;
   upstream has the server send `FileResponse{Digest{is_upload}}` and the
   client reply with `FileAction{SendConfirm}`. Fixed, and `Done` is now sent
   once per job (not per file).
3. Terminal decompression used zlib; upstream `compress.rs` uses zstd. Fixed.
4. `secure.go` decrypted every encrypted frame; `tcp.rs` passes ≤1-byte frames
   through without advancing the nonce counter. Fixed.
5. Missing NAT type test — implemented (`nat.go`) and wired into the punch
   request.
6. Direct-IP connections were unimplemented — added (`--yes` required,
   plaintext handshake, no rendezvous).
7. Exit codes: usage errors returned 1; added `UsageError` classification
   (exit 2) and removed the non-spec `-c` flag (positional `<id>` only).
8. `rdcli ping` now reports latency and supports `--json` per the spec.
9. Test mocks updated to behave like real hbbs/peers (accept loops, port−1
   layout, TestNatRequest handling, MSB-first online bits).

## Test results

```
gofmt -l .        # clean (no output)
go build ./...    # ok
go vet ./...      # ok
go test ./... -count=1
ok  	rdcli/internal/cli
ok  	rdcli/internal/config
ok  	rdcli/internal/hbb
```
59 tests passing (3 packages). Includes codec boundary/roundtrip, addr
mangle roundtrip, crypto handshake vectors, secure stream, punch, relay E2E
(hbbr-style pairing), NAT type (asymmetric/symmetric), online queries (UDP +
TCP + batch bit decoding), file send/receive/resume, terminal, config,
devices/tags, CLI resolve/import.

Live verification against the public rendezvous server (rs-ny.rustdesk.com):
`rdcli ping <real-id>` returned `online (1.7s)`/`offline` with correct exit
codes and `--json` output; `rdcli devices --online` correctly filtered the
locally configured machines.

## Known gaps / risks

1. **No real-peer integration testing.** All protocol tests use mock peers,
   mock hbbs/hbbr. Hole punching, relay pairing, tunnel, file transfer and
   terminal have not been exercised against a real RustDesk controlled
   machine or hbbs/hbbr.
2. **Tunnel wire semantics.** Upstream `port_forward.rs` wraps the local
   sockets in `BytesCodec` (`forward.next()`/`forward.send()`), which implies
   framed local traffic; the spec's acceptance criterion is a raw pipe for
   `--once`. rdcli pipes raw bytes both ways (spec-first). If real peers
   frame their end, tunnel data may be garbled — needs a live peer test.
3. **Hole punch is TCP-only on Linux, plain-dial on macOS.** UDP hole
   punching and mapped-port detection are not implemented; macOS falls back
   to a plain dial (no simultaneous-open). `--relay` works around this.
4. **NAT probe latency.** Every non-forced-relay connect spends up to 1s on
   the NAT probe when the server is unreachable on port−1.
5. **`connect`/`disconnect`/`import` commands** exist beyond the spec surface
   (harmless extras; password storage is plaintext in config.toml).
6. **Address book API** uses the Pro API shape from the spec; HTTPS works via
   the configured URL, but there is no TLS-specific handling.
7. **Compression fidelity.** zstd default level is used (upstream uses
   `COMPRESS_LEVEL`); interoperable but not byte-identical.

## Verification commands

```sh
cd /Users/ankityadav/agent/localhost/rustdesk-cli

# Gates (run after each milestone)
gofmt -l .
go build ./...
go vet ./...
go test ./... -count=1

# Focused suites
go test ./internal/hbb/ -run "TestCodec|TestAddrMangle|TestGenerate|TestDerive|TestEncrypt|TestDecrypt|TestMakeNonce|TestVerifySignedID|TestCreateSymmetric|TestSecure" -v
go test ./internal/hbb/ -run "TestPunch|TestCheckOnline|TestNATType|TestConnect|TestRelay" -v
go test ./internal/hbb/ -run "TestFile|TestList|TestSend|TestReceive" -v
go test ./internal/hbb/ -run "TestTerminal" -v
go test ./internal/hbb/ -run "TestLocalSource|TestAPISource|TestCheckOnlineBatch" -v
go test ./internal/config/ -v
go test ./internal/cli/ -v

# Binary checks
go build -o /tmp/rdcli ./cmd/rdcli
/tmp/rdcli version
/tmp/rdcli ping <peer-id>          # exit 0 online / 1 offline
/tmp/rdcli ping <peer-id> --json
/tmp/rdcli devices --online --json
/tmp/rdcli ping                    # usage error, exit 2
/tmp/rdcli tunnel <id> -L 8080:localhost:80
/tmp/rdcli tunnel <id> --once -L 80:localhost:80   # raw stdin/stdout pipe
/tmp/rdcli ls <id> /tmp --json
/tmp/rdcli cp <id> ./f /remote/f
/tmp/rdcli sh <id> "uname -a"
/tmp/rdcli tag ls --json

# Mock servers (in-repo, for local integration)
go run ./cmd/mockrv        # mock rendezvous/relay/peer
go run ./cmd/mockapi       # mock Pro address book API
```
