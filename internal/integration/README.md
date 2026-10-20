# CLI subprocess tests

Run the bounded integration test from the repository root:

```sh
go test ./internal/integration -run TestCLIEncryptedLoopback -count=1 -timeout=120s
```

The test builds `cmd/rdcli` once into a temporary directory, then invokes that binary for every case. Go's existing build and module caches are reused.

Each CLI process receives a fresh temporary home and configuration path, with an environment allowlist that excludes inherited `RDC_*` values. All connection flags carry fixture credentials and loopback addresses. Forced relay routing avoids NAT probes to adjacent ports. The mock relay terminates the peer protocol itself; it does not connect to another machine.

Both rendezvous and peer sessions negotiate encryption using the public `internal/hbb` helpers. The fixture validates the token, peer identity, password proof, connection type, and disabled services for default logins. Its handshake follows the protocol exercised by `internal/hbb/conn_test.go` and `internal/hbb/conn.go`.

Coverage includes successful and rejected `info --json`, screenshot JSON and exact synthetic PNG bytes, rejection of invalid PNG data, usage exit code 2, and one-shot shell exit codes 0 and 27. Shell responses simulate PTY echo and split completion markers across encrypted messages. The mock never executes the submitted command. No GUI, live screenshot, or clipboard is used.

Six `cp` cases advertise peer versions 1.4.1 and 1.4.9. Each version covers a 208,953-byte upload with matching completion ACK, an upload whose peer withholds ACK while keeping the socket open, and a download checked by SHA-256. Uploads validate source size/mtime digest, explicit zero-offset confirmation, block identity, reconstructed content hash, and exactly one job-level Done. Missing ACK must produce exit 1 within five seconds without the success message or completed-file progress. Downloads use the upstream empty entry name for a single file and check that no staging artifacts remain. The same binary build serves all thirteen cases.

The fixture follows these upstream sources, checked on 2026-10-20:

- RustDesk [`1.4.1` connection handling](https://github.com/rustdesk/rustdesk/blob/1.4.1/src/server/connection.rs#L2738-L2750) and [`WriteDone` ACK/digest confirmation](https://github.com/rustdesk/rustdesk/blob/1.4.1/src/ui_cm_interface.rs#L823-L905).
- RustDesk [`1.4.9` connection handling](https://github.com/rustdesk/rustdesk/blob/1.4.9/src/server/connection.rs#L3418-L3431) and [`WriteDone` ACK/digest confirmation](https://github.com/rustdesk/rustdesk/blob/1.4.9/src/ui_cm_interface.rs#L1047-L1132).
- Their pinned `hbb_common` transfer implementations, [`f91459c4`](https://github.com/rustdesk/hbb_common/blob/f91459c4ab80fc3cfdef0882b2af51f984bc914c/src/fs.rs) and [`7e1c392c`](https://github.com/rustdesk/hbb_common/blob/7e1c392c62d39c364127307cd408421dd5f8cfb0/src/fs.rs).

These are protocol fixtures, not live interoperability tests against official RustDesk binaries. They exercise fresh transfers only.

Subprocesses, socket operations, and fixture shutdown all have deadlines. Temporary files, listeners, and accepted connections are cleaned up through the test lifecycle.
