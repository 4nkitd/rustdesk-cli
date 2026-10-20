# Vendored protocol schemas

The checked-in `.proto` files define the wire messages used by this client.
Changing the Go module path changes only `go_package`, not wire field numbers.
The original imported schemas did not record a precise upstream revision; do not
claim they are byte-identical to a particular RustDesk release.

For implementation and compatibility comparisons, use the pinned RustDesk
[1.5.0 message schema](https://github.com/rustdesk/rustdesk/blob/fada664df7a294d1d1a9ca3e7cd3637069122f17/libs/base/protos/message.proto)
and the sources in [COMPATIBILITY.md](COMPATIBILITY.md). Review schema upgrades
separately from regeneration. Do not fetch a moving branch during a build.

## Regeneration

The module-path migration regenerated the Go files using `libprotoc 36.2` and
`protoc-gen-go v1.36.12`:

```sh
protoc -I internal/hbb/proto \
  --go_out=paths=source_relative:internal/hbb/proto \
  internal/hbb/proto/message.proto internal/hbb/proto/rendezvous.proto
go test ./...
```

Generated files are committed so normal builds do not require either generator.
Protocol compatibility still requires encrypted mock tests and approved live
peer testing; successful generation is not interoperability evidence.
