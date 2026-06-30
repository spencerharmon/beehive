# Release v__VERSION__

## Highlights
- 

## Binaries
`beehive`, `beehived`, `honeybee` — CGO-free, cross-compiled for
linux/amd64, linux/arm64 (fully static ELF) and darwin/amd64, darwin/arm64
(CGO-free; macOS links libSystem by OS design). Each `SHA256SUMS-<os>-<arch>`
ships a keyless cosign signature (`.sig`) and Fulcio certificate (`.pem`).

## Verify
Keyless Sigstore signatures (Fulcio cert + Rekor log) — no shared key/secret.
Download a checksums set with its `.sig`/`.pem`, then run the bundled checker:

```
gh release download v__VERSION__ --dir dist \
  --pattern '*-<os>-<arch>' --pattern 'SHA256SUMS-<os>-<arch>*'
scripts/verify-release.sh <os> <arch> dist
```

or invoke cosign directly:

```
cosign verify-blob \
  --certificate SHA256SUMS-<os>-<arch>.pem \
  --signature   SHA256SUMS-<os>-<arch>.sig \
  --certificate-identity-regexp '^https://github.com/spencerharmon/beehive/\.github/workflows/.+@refs/tags/' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  SHA256SUMS-<os>-<arch>
sha256sum -c SHA256SUMS-<os>-<arch>
```

## Install
See docs/install.md. Package install lays out /etc/beehive and generates a gpg
key at install time if none exists.

## Changes
- 

## Upgrade notes
- 
