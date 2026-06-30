#!/usr/bin/env bash
# verify-release.sh — verify a published beehive release artifact set for one
# os/arch, end to end, from a clean environment:
#
#   1. cosign verify-blob the SHA256SUMS manifest against its keyless Fulcio
#      certificate + Rekor transparency log (no private key, no shared secret).
#   2. sha256sum -c the manifest, tying every shipped binary to the signed list.
#   3. assert the linux binaries are statically linked (CGO_ENABLED=0 ELF).
#
# It verifies the ACTUAL published artifacts, so download them first, e.g.:
#
#   gh release download v1.2.3 --dir dist \
#     --pattern '*-linux-amd64' --pattern 'SHA256SUMS-linux-amd64*'
#   scripts/verify-release.sh linux amd64 dist
#
# Usage: scripts/verify-release.sh <os> <arch> [dist-dir]
#   dist-dir defaults to ./dist and must hold SHA256SUMS-<os>-<arch>, its .sig
#   and .pem (the signing certificate), and the named binaries.
#
# Keyless identity (override via env for a fork or a renamed workflow):
#   COSIGN_REPO             owner/repo (default: $GITHUB_REPOSITORY or
#                           spencerharmon/beehive)
#   COSIGN_IDENTITY_REGEXP  cert SAN regexp (default: this repo's workflows on a tag)
#   COSIGN_OIDC_ISSUER      OIDC issuer (default: GitHub Actions)
set -euo pipefail

if [ "${1:-}" = "-h" ] || [ "${1:-}" = "--help" ]; then
  # Print the contiguous comment header (skip the shebang, stop at first code line).
  awk 'NR==1{next} /^#/{sub(/^# ?/,"");print;next} {exit}' "$0"
  exit 0
fi

os="${1:?usage: verify-release.sh <os> <arch> [dist-dir]}"
arch="${2:?usage: verify-release.sh <os> <arch> [dist-dir]}"
dist="${3:-dist}"

repo="${COSIGN_REPO:-${GITHUB_REPOSITORY:-spencerharmon/beehive}}"
issuer="${COSIGN_OIDC_ISSUER:-https://token.actions.githubusercontent.com}"
identity_regexp="${COSIGN_IDENTITY_REGEXP:-^https://github.com/${repo}/\.github/workflows/.+@refs/tags/}"

sums="${dist}/SHA256SUMS-${os}-${arch}"
sig="${sums}.sig"
cert="${sums}.pem"

for f in "$sums" "$sig" "$cert"; do
  [ -f "$f" ] || { echo "missing required artifact: $f" >&2; exit 1; }
done

echo "==> cosign verify-blob ${sums}"
cosign verify-blob \
  --certificate "$cert" \
  --signature "$sig" \
  --certificate-identity-regexp "$identity_regexp" \
  --certificate-oidc-issuer "$issuer" \
  "$sums"

echo "==> sha256sum -c $(basename "$sums")"
( cd "$dist" && sha256sum -c "$(basename "$sums")" )

if [ "$os" = "linux" ]; then
  echo "==> static-linkage check (linux ELF)"
  # Check exactly the binaries named in the signed manifest.
  while read -r _hash name; do
    [ -n "$name" ] || continue
    f="${dist}/${name}"
    desc="$(file -b "$f")"
    echo "    ${name}: ${desc}"
    case "$desc" in
      *"statically linked"*) ;;
      *) echo "NOT statically linked: $f" >&2; exit 1 ;;
    esac
    case "$desc" in
      *"dynamically linked"*) echo "has dynamic deps: $f" >&2; exit 1 ;;
    esac
  done < "$sums"
else
  echo "==> static-linkage check skipped for ${os} (macOS Mach-O links libSystem by OS design; binaries are CGO-free)"
fi

echo "OK: ${os}/${arch} release artifacts verified"
