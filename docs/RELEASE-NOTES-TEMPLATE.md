# Release v__VERSION__

## Highlights
- 

## Binaries
`beehive`, `beehived`, `honeybee` — static (CGO_ENABLED=0), cross-compiled for:
linux/amd64, linux/arm64, darwin/amd64, darwin/arm64. Each target ships
`SHA256SUMS-<os>-<arch>` with a cosign keyless signature (`.sig`) and
certificate (`.pem`).

## Verify
Keyless signatures need the certificate plus the signer identity + OIDC issuer
(replace `<owner>/<repo>`):
```
cosign verify-blob \
  --certificate SHA256SUMS-<os>-<arch>.pem \
  --signature   SHA256SUMS-<os>-<arch>.sig \
  --certificate-identity-regexp '^https://github.com/<owner>/<repo>/[.]github/workflows/.+@refs/tags/v.+$' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  SHA256SUMS-<os>-<arch>
sha256sum -c SHA256SUMS-<os>-<arch>
```
Or verify a whole download directory in one step (static-binary check +
checksums + cosign — the same script CI runs before publishing):
```
COSIGN_IDENTITY_REGEXP='^https://github.com/<owner>/<repo>/[.]github/workflows/.+@refs/tags/v.+$' \
  scripts/verify-release.sh .
```

## Install
See docs/install.md. Package install lays out /etc/beehive and generates a gpg
key at install time if none exists.

## Changes
- 

## Upgrade notes
- 
