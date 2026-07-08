#!/usr/bin/env sh
# build-release-artifacts.sh — cross-compile beehive/beehived/honeybee, static
# (CGO_ENABLED=0), for every released os/arch, into DIST_DIR. Companion to
# verify-release.sh, which asserts staticness/checksums/signature over the
# result. Authored for the self-hosted Zuul release job
# (playbooks/beehive-release-cross-compile.yaml) but intentionally has no
# CI-specific dependency — run it directly to reproduce the release build
# locally:
#
#   scripts/build-release-artifacts.sh [DIST_DIR]     (default: dist)
#
# Then verify:
#
#   SKIP_COSIGN=1 scripts/verify-release.sh [DIST_DIR]
set -eu

DIST="${1:-dist}"
mkdir -p "$DIST"

# Stamp the build commit into every released binary (prompt-embed drift guard):
# a deployed binary can then warn when it predates the tracked-main tip. The
# release build always runs from a git checkout, so this is normally set; if git
# is unavailable the binaries fall back to an honest unstamped "dev" rather than
# a wrong SHA.
BUILD_SHA=""
if command -v git >/dev/null 2>&1; then
	BUILD_SHA=$(git rev-parse HEAD 2>/dev/null || true)
fi

bins="beehive beehived honeybee"
targets="linux/amd64 linux/arm64 darwin/amd64 darwin/arm64"

for target in $targets; do
	os=${target%/*}
	arch=${target#*/}
	for bin in $bins; do
		out="$DIST/$bin-$os-$arch"
		echo "build-release-artifacts: building $out (CGO_ENABLED=0 GOOS=$os GOARCH=$arch)"
		if [ -n "$BUILD_SHA" ]; then
			CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath -ldflags "-X github.com/spencerharmon/beehive/internal/version.SHA=$BUILD_SHA" -o "$out" "./cmd/$bin"
		else
			CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath -o "$out" "./cmd/$bin"
		fi
	done
	# hash only the component binaries (never the sums file itself)
	(cd "$DIST" && sha256sum "beehive-$os-$arch" "beehived-$os-$arch" "honeybee-$os-$arch" >"SHA256SUMS-$os-$arch")
	echo "build-release-artifacts: wrote $DIST/SHA256SUMS-$os-$arch"
done

echo "build-release-artifacts: OK ($DIST)"
