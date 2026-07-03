#!/usr/bin/env sh
# verify-release.sh — reproducible verification of beehive release artifacts.
#
# Confirms the release invariant the ROI requires: cross-compiled *static*
# (CGO_ENABLED=0) binaries whose SHA256SUMS is cosign-signed and actually
# verifies. Run it over a directory of downloaded (or freshly built) artifacts;
# CI runs the same script before publishing a release. Three checks:
#
#   1. static  — every component binary (beehive/beehived/honeybee) is CGO-free
#                (embedded build info records CGO_ENABLED=0); linux ELF binaries
#                are additionally fully statically linked. (Go darwin binaries
#                are CGO-free but always dynamically link libSystem — unavoidable
#                on macOS — so the ELF static-link assertion is linux-only.)
#   2. checksum — `sha256sum -c` over each SHA256SUMS-<os>-<arch>.
#   3. signature — `cosign verify-blob` of each SHA256SUMS against its published
#                keyless certificate (.pem) + signature (.sig), pinned to the
#                signing OIDC identity + issuer (or a documented public key).
#
# Usage:  scripts/verify-release.sh [DIST_DIR]        (default: dist)
#
# Keyless identity pin (default). The signer is this repo's release workflow on
# a v* tag. Override for a fork/mirror:
#   COSIGN_IDENTITY=<exact SAN>              exact certificate identity, or
#   COSIGN_IDENTITY_REGEXP=<RE2 regexp>     (default: any repo's workflow @ a v* tag)
#   COSIGN_OIDC_ISSUER=<issuer>             (default: GitHub Actions OIDC issuer)
# In GitHub Actions the exact identity is derived from
# $GITHUB_SERVER_URL/$GITHUB_WORKFLOW_REF automatically.
#
# Key-based alternative (offline-friendly; verify against a pinned public key):
#   COSIGN_KEY=cosign.pub scripts/verify-release.sh <dir>
#
# Escape hatch:
#   SKIP_COSIGN=1   run only the static + checksum checks (no cosign required).
set -eu

DIST="${1:-dist}"
ISSUER="${COSIGN_OIDC_ISSUER:-https://token.actions.githubusercontent.com}"

die() { echo "verify-release: FAIL: $*" >&2; exit 1; }

[ -d "$DIST" ] || die "artifact dir '$DIST' not found"

have_go=0;   command -v go   >/dev/null 2>&1 && have_go=1
have_file=0; command -v file >/dev/null 2>&1 && have_file=1

# ---- 1. static-binary check -------------------------------------------------
bins=0
for bin in "$DIST"/beehive-* "$DIST"/beehived-* "$DIST"/honeybee-*; do
	[ -f "$bin" ] || continue
	case "$bin" in *.sig | *.pem | *.sha256 | *SHA256SUMS*) continue ;; esac
	bins=$((bins + 1))
	if [ "$have_go" = 1 ]; then
		go version -m "$bin" | grep -q 'CGO_ENABLED=0' \
			|| die "$bin was not built with CGO_ENABLED=0 (not a static build)"
	fi
	case "$bin" in
	*-linux-*)
		if [ "$have_file" = 1 ]; then
			desc=$(file -b "$bin")
			case "$desc" in
			*"statically linked"*) : ;;
			*) die "$bin is not statically linked: $desc" ;;
			esac
			case "$desc" in
			*"dynamically linked"*) die "$bin is dynamically linked: $desc" ;;
			esac
		fi
		;;
	esac
	echo "verify-release: static OK   $bin"
done
[ "$bins" -gt 0 ] || die "no component binaries (beehive/beehived/honeybee) found in $DIST"
[ "$have_go" = 1 ] || echo "verify-release: WARN go not found — skipped CGO_ENABLED=0 assertion"
[ "$have_file" = 1 ] || echo "verify-release: WARN file not found — skipped linux static-link assertion"

# ---- 2. checksums -----------------------------------------------------------
sums_seen=0
for sums in "$DIST"/SHA256SUMS-*; do
	[ -f "$sums" ] || continue
	case "$sums" in *.sig | *.pem) continue ;; esac
	sums_seen=$((sums_seen + 1))
	(cd "$DIST" && sha256sum -c "$(basename "$sums")") \
		|| die "checksum verification failed for $sums"
	echo "verify-release: sha256 OK   $sums"
done
[ "$sums_seen" -gt 0 ] || die "no SHA256SUMS-<os>-<arch> files found in $DIST"

# ---- 3. cosign signature ----------------------------------------------------
if [ "${SKIP_COSIGN:-0}" = 1 ]; then
	echo "verify-release: SKIP_COSIGN=1 — skipped signature verification"
	echo "verify-release: OK (static + checksum)"
	exit 0
fi
command -v cosign >/dev/null 2>&1 \
	|| die "cosign not installed (set SKIP_COSIGN=1 for static+checksum only)"

# Resolve the keyless identity pin (unless a public key is supplied).
if [ -z "${COSIGN_KEY:-}" ]; then
	if [ -n "${COSIGN_IDENTITY:-}" ]; then
		id_flag=--certificate-identity
		id_val="$COSIGN_IDENTITY"
	elif [ -n "${GITHUB_SERVER_URL:-}" ] && [ -n "${GITHUB_WORKFLOW_REF:-}" ]; then
		id_flag=--certificate-identity
		id_val="$GITHUB_SERVER_URL/$GITHUB_WORKFLOW_REF"
	else
		id_flag=--certificate-identity-regexp
		id_val="${COSIGN_IDENTITY_REGEXP:-^https://github[.]com/[^/]+/[^/]+/[.]github/workflows/[^@]+@refs/tags/v.+$}"
	fi
fi

for sums in "$DIST"/SHA256SUMS-*; do
	[ -f "$sums" ] || continue
	case "$sums" in *.sig | *.pem) continue ;; esac
	sig="$sums.sig"
	[ -f "$sig" ] || die "missing signature $sig"
	if [ -n "${COSIGN_KEY:-}" ]; then
		cosign verify-blob --key "$COSIGN_KEY" --signature "$sig" "$sums" \
			|| die "cosign verify-blob (key) failed for $sums"
	else
		cert="$sums.pem"
		[ -f "$cert" ] || die "missing certificate $cert — CI must emit cosign --output-certificate"
		cosign verify-blob \
			--certificate "$cert" \
			--signature "$sig" \
			"$id_flag" "$id_val" \
			--certificate-oidc-issuer "$ISSUER" \
			"$sums" \
			|| die "cosign verify-blob failed for $sums"
	fi
	echo "verify-release: cosign OK   $sums"
done

echo "verify-release: OK (static + checksum + cosign)"
