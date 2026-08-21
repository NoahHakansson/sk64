# Releasing sk64

Releases are built by GitHub Actions from `v*` tags. The release workflow runs the complete CI workflow first, then GoReleaser builds and publishes the GitHub release directly, marked as latest. Verify its artifacts and signature as soon as the workflow finishes.

The commands below assume `gh` is authenticated and GoReleaser, golangci-lint, govulncheck, and Cosign are installed on `PATH`.

## Prepare the tag

Choose a semantic version and start from a clean, current `master`:

```sh
version=v0.1.1
git switch master
git pull --ff-only origin master
git status --short
goreleaser check
go test -race ./...
(cd e2e && go test -race ./...)
golangci-lint run
(cd e2e && golangci-lint run)
./hack/covergate.sh
govulncheck ./...
```

`git status --short` must print nothing. Confirm that the commit to release is already on `origin/master`, then create and push an annotated tag:

```sh
git tag -a "$version" -m "$version"
git push origin "$version"
```

Never move or reuse a published version tag. Fix the problem and issue a new version instead.

## Watch the release workflow

Pushing the tag starts `.github/workflows/release.yml`. Its `quality` job reuses the full CI workflow; GoReleaser runs only after that gate passes.

```sh
gh run list --workflow release.yml --limit 5
gh run watch RUN_ID
```

The GoReleaser configuration produces four static archives:

- Linux amd64 and arm64
- macOS amd64 and arm64

Each archive has an SPDX JSON SBOM. The release also contains `checksums.txt` and its keyless Cosign v3 signature in the Sigstore bundle `checksums.txt.cosign.bundle`. Release notes are generated from GitHub history; conventional `feat:` and `fix:` commits receive their own sections.

## Verify the release

Download the release assets into a temporary directory and verify every checksum:

```sh
release_dir=$(mktemp -d)
gh release download "$version" --dir "$release_dir"
(cd "$release_dir" && shasum -a 256 -c checksums.txt)
```

Verify that GitHub Actions signed the checksum file for this repository and tag:

```sh
cosign verify-blob \
  --bundle "$release_dir/checksums.txt.cosign.bundle" \
  --certificate-identity "https://github.com/NoahHakansson/sk64/.github/workflows/release.yml@refs/tags/$version" \
  --certificate-oidc-issuer "https://token.actions.githubusercontent.com" \
  "$release_dir/checksums.txt"
```

Inspect the generated release notes and confirm that all ten expected assets are present: four archives, four SBOMs, the checksum file, and its signature bundle:

```sh
gh release view "$version"
```

If verification fails, delete the release and its tag, fix the problem, and issue a new version under a fresh tag.
