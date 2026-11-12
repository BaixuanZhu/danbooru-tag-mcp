# Reproducible builds & verifying downloads

Release binaries are **not** Authenticode-signed. Integrity rests on
reproducible builds plus SHA-256, all free and verifiable by anyone.

[← back to the README](../README.md)

## What is guaranteed

- The exe and portable zips are **byte-reproducible**: the exact Go toolchain
  is pinned in `go.mod`, builds use `CGO_ENABLED=0 -trimpath -buildvcs=false`,
  and zips are written by `scripts/zipfile` with entry timestamps pinned to
  the commit time (`SOURCE_DATE_EPOCH`). Same source + toolchain ⇒ identical
  bytes on any machine, any timezone.
- Every release job runs a **verify-repro** check: it rebuilds the exe from
  the tagged source on a Linux runner and compares it byte-for-byte against
  the published Windows-built asset — a green check on the release proves the
  artifacts were built from the tag without tampering.
- All artifacts carry **SHA-256 checksums** (`checksums.txt`) and free keyless
  **build-provenance attestations** signed by the GitHub workflow.

## Verify a download

```bash
sha256sum -c --ignore-missing checksums.txt   # integrity vs the manifest
gh attestation verify --repo BaixuanZhu/danbooru-tag-mcp danbooru-tag-mcp.exe   # provenance
```

## Rebuild it yourself

Replace `vX.Y.Z` with the release tag you downloaded, and `amd64` with `arm64`
on ARM machines:

```bash
git checkout vX.Y.Z
make build-dist VERSION=X.Y.Z GOARCH=amd64
sha256sum dist/amd64/danbooru-tag-mcp.exe     # must equal the hash inside the published zip
```

## The one exception

The NSIS setup exe is the one non-reproducible artifact, because makensis
embeds build metadata. It is still covered by `checksums.txt` and the
attestations.
