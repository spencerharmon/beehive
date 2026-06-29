# Release v__VERSION__

## Highlights
- 

## Binaries
`beehive`, `beehived`, `honeybee` — static, cross-compiled for:
linux/amd64, linux/arm64, darwin/amd64, darwin/arm64.

## Verify
```
cosign verify-blob --signature SHA256SUMS-<os>-<arch>.sig SHA256SUMS-<os>-<arch>
sha256sum -c SHA256SUMS-<os>-<arch>
```

## Install
See docs/install.md. Package install lays out /etc/beehive and generates a gpg
key at install time if none exists.

## Changes
- 

## Upgrade notes
- 
