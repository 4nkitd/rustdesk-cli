# Releasing rdcli

## Release gates

`.github/workflows/test.yml` runs `go test -race ./... -count=1 -timeout=120s`
and `go vet ./...` on Linux and macOS for pull requests and pushes to `main`.
The tag workflow calls the same test workflow at the tagged commit. Every test
job must pass before any release build starts.

CI also requires Git to track `cmd/rdcli/main.go`, explicitly builds
`./cmd/rdcli`, and runs its `version` and `--help` commands. `go test ./...` alone
can pass when the CLI entrypoint is missing from the checkout.

All Go jobs explicitly use Go 1.27.3, verified available as a stable release in
the [official Go downloads feed](https://go.dev/dl/?mode=json). The minimum Go
version in `go.mod` does not select the release compiler. Update the pinned
version in both workflows together when adopting security patches.
`GOTOOLCHAIN=local` prevents a dependency from silently selecting another
toolchain, and `GOFLAGS=-mod=readonly` prevents CI from rewriting module
requirements. Update `go.mod` and `go.sum` together if a dependency needs a newer
Go version.

Tests use temporary configuration and mock peers on loopback. Keep real peers,
saved credentials, and production connection tests out of this workflow. No
repository secrets are passed to the test workflow. Build smoke tests run only
`version` and `--help`.

## Artifacts

Each binary is built with `CGO_ENABLED=0` on a matching native GitHub-hosted runner.
The build checks the runner OS and architecture, then runs the binary to verify
its embedded tag version and help output.
The linker sets `github.com/4nkitd/rustdesk-cli/internal/cli.version`, matching the
module path `github.com/4nkitd/rustdesk-cli`.

| Asset | Runner |
| --- | --- |
| `rdcli-linux-amd64` | `ubuntu-24.04` |
| `rdcli-linux-arm64` | `ubuntu-24.04-arm` |
| `rdcli-darwin-amd64` | `macos-15-intel` |
| `rdcli-darwin-arm64` | `macos-15` |

The Linux amd64 build also creates `rdcli-licenses.tar.gz` from tracked root
`LICENSE`, optional `LICENSE.*`, `THIRD_PARTY_NOTICES` or `THIRD_PARTY_NOTICES.*`,
and files under `licenses/` and `third_party/licenses/`. The root license and at
least one third-party notices file are required. Every included file must be
nonempty. Commit dependency license texts under either supported license
directory, or include their full required text in the notices file.

The publication job requires all four nonempty binaries and the license bundle,
then creates `checksums.txt` covering all five files. It uploads six assets to a
draft, then publishes that release. Tags containing a hyphen become GitHub prereleases.
Publication requires an exact `## vMAJOR.MINOR.PATCH` changelog heading matching
the tag, including any prerelease suffix. Its section supplies the release notes
and limitations; a missing or empty section stops publication.
Release assets are raw binaries. After downloading, run `chmod +x` before use.
Distribute `rdcli-licenses.tar.gz` alongside any redistributed binaries.
macOS binaries are unsigned and are not notarized.

Windows is not a supported release target. Shared NAT entrypoints and Windows
socket options are present, but native build/runtime verification is pending.
One-shot shell commands and `tunnel --once` are explicitly unsupported there.
A Windows release requires native tests before adding artifacts to the matrix.

## Publish a release

These commands publish through GitHub Actions. Run them only after release
approval, with a clean checkout of the reviewed commit on `main`.

1. Commit the release changes, including both workflows and these instructions.
   Include the project license, third-party notices, and required dependency
   license texts. Confirm the module declaration and imports use
   `github.com/4nkitd/rustdesk-cli`, matching the release linker path.
   Confirm `git cat-file -e HEAD:cmd/rdcli/main.go` succeeds. Check
   `git status --short --untracked-files=all` and `git ls-files --others --ignored
   --exclude-standard cmd internal` for source missing from the commit. Bare
   ignore patterns such as `rdcli` also ignore a directory named `cmd/rdcli`;
   root binary patterns should be anchored, such as `/rdcli`.
   Push `main`, then wait for its `test` workflow to pass on Linux and macOS.
   Confirm Actions is enabled and all four runner labels are available to the
   repository. The workflow requires the repository's `GITHUB_TOKEN` to have
   `contents: write` in the publication job. No personal access token is needed.
2. Choose an unused version, such as `v0.2.0` or `v0.2.0-rc.1`. The accepted tag
   format is `vMAJOR.MINOR.PATCH` with an optional hyphenated prerelease suffix.
   Replace the example value below with the approved version.

   ```sh
   git switch main
   git pull --ff-only origin main
   test -z "$(git status --porcelain)"
   VERSION=v0.2.0
   git log -1 --oneline
   git ls-remote --tags origin "refs/tags/$VERSION"
   ```

   Confirm the commit is the reviewed, passing commit. The remote tag query must
   return no matching tag.
3. Create and push only that tag.

   ```sh
   git tag -a "$VERSION" -m "Release $VERSION"
   git push origin "refs/tags/$VERSION"
   gh run list --workflow release.yml --branch "$VERSION" --limit 5
   gh run watch RUN_ID --exit-status
   ```

   Replace `RUN_ID` with the run for the tag and expected commit. Pushing the tag
   starts tests, native builds, and automatic publication.
4. Verify the published release and download its assets into a fresh directory.

   ```sh
   gh release view "$VERSION"
   mkdir "verify-$VERSION"
   gh release download "$VERSION" --dir "verify-$VERSION"
   cd "verify-$VERSION"
   sha256sum --check checksums.txt
   ```

   On macOS, use `shasum -a 256 --check checksums.txt`. Select the binary matching
   the current machine, make it executable, and run its `version` and `--help`
   commands. The version must match the tag. This verifies startup, not remote
   peer interoperability.

## Failures and reruns

Test or build failures block publication. Fix source failures on `main`, run
the tests again, and use a new tag. Do not move a published tag.

For a transient infrastructure failure, rerun the failed jobs on the same tagged
commit. Artifacts are retained for seven days. After that, rerun all jobs to
regenerate them.

If publication fails after draft creation, inspect that draft and the workflow
logs before retrying. The create step deliberately refuses to overwrite an
existing release. Remove only the failed, unpublished draft after confirming
its tag, then rerun the failed publication job. A published release must use a
new version for changed assets.

The test and build jobs have read-only repository access. Only publication has
write access, checkout does not persist credentials, and actions are pinned to
commit SHAs. Review action updates before changing those pins.
