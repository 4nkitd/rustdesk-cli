# Third-party notices

rdcli's project license is in [LICENSE](LICENSE). This file maps dependency versions to their original license texts in [licenses/](licenses/). The texts retain their upstream wording. Identical Go license and patent-grant files are shared by the components listed below.

## Required Go modules

These are the eight modules required by `go.mod`. Source links identify the exact upstream tags used for the license texts. `mousetrap` is Cobra's Windows-specific dependency; its license is included with the other required modules.

| Module | Version | Included license text | Tagged upstream source |
| --- | --- | --- | --- |
| `github.com/klauspost/compress` | `v1.19.2` | [compress-LICENSE](licenses/compress-LICENSE) | [LICENSE](https://github.com/klauspost/compress/blob/v1.19.2/LICENSE) |
| `github.com/spf13/cobra` | `v1.10.2` | [cobra-LICENSE.txt](licenses/cobra-LICENSE.txt) | [LICENSE.txt](https://github.com/spf13/cobra/blob/v1.10.2/LICENSE.txt) |
| `golang.org/x/crypto` | `v0.55.0` | [go-LICENSE](licenses/go-LICENSE), [go-PATENTS](licenses/go-PATENTS) | [LICENSE](https://github.com/golang/crypto/blob/v0.55.0/LICENSE), [PATENTS](https://github.com/golang/crypto/blob/v0.55.0/PATENTS) |
| `golang.org/x/sys` | `v0.47.0` | [go-LICENSE](licenses/go-LICENSE), [go-PATENTS](licenses/go-PATENTS) | [LICENSE](https://github.com/golang/sys/blob/v0.47.0/LICENSE), [PATENTS](https://github.com/golang/sys/blob/v0.47.0/PATENTS) |
| `golang.org/x/term` | `v0.45.0` | [go-LICENSE](licenses/go-LICENSE), [go-PATENTS](licenses/go-PATENTS) | [LICENSE](https://github.com/golang/term/blob/v0.45.0/LICENSE), [PATENTS](https://github.com/golang/term/blob/v0.45.0/PATENTS) |
| `google.golang.org/protobuf` | `v1.36.12` | [protobuf-LICENSE](licenses/protobuf-LICENSE), [go-PATENTS](licenses/go-PATENTS) | [LICENSE](https://github.com/protocolbuffers/protobuf-go/blob/v1.36.12/LICENSE), [PATENTS](https://github.com/protocolbuffers/protobuf-go/blob/v1.36.12/PATENTS) |
| `github.com/inconshreveable/mousetrap` | `v1.1.0` | [mousetrap-LICENSE](licenses/mousetrap-LICENSE) | [LICENSE](https://github.com/inconshreveable/mousetrap/blob/v1.1.0/LICENSE) |
| `github.com/spf13/pflag` | `v1.0.9` | [pflag-LICENSE](licenses/pflag-LICENSE) | [LICENSE](https://github.com/spf13/pflag/blob/v1.0.9/LICENSE) |

Cobra's source files carry `Copyright 2013-2023 The Cobra Authors`; see [cobra.go at v1.10.2](https://github.com/spf13/cobra/blob/v1.10.2/cobra.go).

## Code included within dependencies

`github.com/klauspost/compress/zstd` imports `internal/snapref` and `zstd/internal/xxhash`. Their notices are included as follows:

| Component | Containing version | Included text | Tagged upstream source |
| --- | --- | --- | --- |
| Snappy-Go code in `internal/snapref` | `compress v1.19.2` | The `Files: internal/snapref/*` section of [compress-LICENSE](licenses/compress-LICENSE) | [LICENSE](https://github.com/klauspost/compress/blob/v1.19.2/LICENSE), [internal/snapref/LICENSE](https://github.com/klauspost/compress/blob/v1.19.2/internal/snapref/LICENSE) |
| Caleb Spare's xxHash implementation in `zstd/internal/xxhash` | `compress v1.19.2` | [compress-xxhash-LICENSE.txt](licenses/compress-xxhash-LICENSE.txt) | [LICENSE.txt](https://github.com/klauspost/compress/blob/v1.19.2/zstd/internal/xxhash/LICENSE.txt) |

The complete upstream `compress` license file also contains notices for other directories. It is reproduced without removing those sections. Its FSE and Huffman code credits `Copyright 2018 Klaus Post. All rights reserved.` and `Based on work Copyright (c) 2013, Yann Collet, released under BSD License.` See [fse/bitreader.go](https://github.com/klauspost/compress/blob/v1.19.2/fse/bitreader.go) and [huff0/bitreader.go](https://github.com/klauspost/compress/blob/v1.19.2/huff0/bitreader.go). Zstandard source files also credit Yann Collet; see [zstd/decoder.go](https://github.com/klauspost/compress/blob/v1.19.2/zstd/decoder.go).

## Go runtime and standard library

The Go runtime and standard library are included in Go-built executables. [go-LICENSE](licenses/go-LICENSE) and [go-PATENTS](licenses/go-PATENTS) reproduce [LICENSE](https://github.com/golang/go/blob/go1.27.3/LICENSE) and [PATENTS](https://github.com/golang/go/blob/go1.27.3/PATENTS) from `go1.27.3`, the release toolchain version. A binary's actual compiler version is recorded in its Go build information and can be read with `go version -m <binary>`.

Go's standard library vendors additional Go packages. In `go1.27.3`, [src/vendor/modules.txt](https://github.com/golang/go/blob/go1.27.3/src/vendor/modules.txt) records `golang.org/x/crypto v0.52.1-0.20260526024921-9beb694f9766`, `golang.org/x/net v0.55.1-0.20260731170536-c1d18010be90`, `golang.org/x/sys v0.45.0`, and `golang.org/x/text v0.37.0`. Their `LICENSE` and `PATENTS` files under `src/vendor/<module>/` have the same texts included here as [go-LICENSE](licenses/go-LICENSE) and [go-PATENTS](licenses/go-PATENTS). These toolchain-vendored versions are separate from the application's module versions above.

[go-SOURCE_NOTICES.txt](licenses/go-SOURCE_NOTICES.txt) preserves additional notices embedded in Go's math, cryptography, and runtime sources: Sun Microsystems, Stephen L. Moshier's Cephes code, fiat-crypto, the Rijndael reference implementation, and the Lucent Technologies, Vita Nuova, and Go Authors notice for the amd64 memory-move implementation. Each excerpt identifies its exact source tag and path.

## RustDesk-derived protocol code

The project's existing [README](README.md#license) attributes its protocol work to [RustDesk](https://github.com/rustdesk/rustdesk), and the project distributes the AGPL-3.0 text in [LICENSE](LICENSE). The vendored schemas do not record an exact upstream revision; this notice does not assign an invented version to them.
