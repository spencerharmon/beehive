# Release v__VERSION__

## Highlights
- 

## Binaries
`beehive`, `beehived`, `honeybee` — static (CGO_ENABLED=0), cross-compiled for:
linux/amd64, linux/arm64, darwin/amd64, darwin/arm64. Each target ships
`SHA256SUMS-<os>-<arch>` with a cosign signature (`.sig`) and, for keyless
signing, a certificate (`.pem`).

## Verify
Signatures are produced by the self-hosted **Gitea Actions** release pipeline
(`.gitea/workflows/release.yaml`; see docs/tasks/release-verify.md). Keyless
signatures need the certificate plus the signer identity + OIDC issuer **for
the pipeline that cut THIS release** — see that release's CI run for the exact
values (each instance/pipeline has its own issuer + identity, so neither is
hardcoded below). A key-signed release instead verifies against the published
public key (`COSIGN_KEY=cosign.pub`):
```
cosign verify-blob \
  --certificate SHA256SUMS-<os>-<arch>.pem \
  --signature   SHA256SUMS-<os>-<arch>.sig \
  --certificate-identity-regexp '<signer identity regexp — from this release's CI run>' \
  --certificate-oidc-issuer '<OIDC issuer — from this release's CI run>' \
  SHA256SUMS-<os>-<arch>
sha256sum -c SHA256SUMS-<os>-<arch>
```
Or verify a whole download directory in one step (static-binary check +
checksums + cosign — the same script CI runs before publishing):
```
COSIGN_IDENTITY_REGEXP='<signer identity regexp>' \
COSIGN_OIDC_ISSUER='<OIDC issuer>' \
  scripts/verify-release.sh .
```

## Install
See docs/install.md. Package install lays out /etc/beehive and generates a gpg
key at install time if none exists.

## Changes
- 

## Upgrade notes
- 
