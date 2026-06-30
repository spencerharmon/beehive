#!/usr/bin/env sh
# Generic beehive installer. Builds or installs beehive, beehived, and honeybee;
# then scaffolds shared config/keyring. Safe to re-run: never clobbers existing
# config.yaml or gpg keys.
#
# Usage:
#   scripts/install.sh [DESTDIR]
#
# Environment:
#   PREFIX=/usr/local                 install prefix
#   BEEHIVE_CONFIG_DIR=/etc/beehive   shared config/keyring dir
#   BEEHIVE_SKIP_KEYGEN=1             skip automatic gpg key generation
#   CGO_ENABLED=0                     override Go build CGO setting
set -eu

DESTDIR="${1:-${DESTDIR:-}}"
PREFIX="${PREFIX:-/usr/local}"
CONFDIR="${BEEHIVE_CONFIG_DIR:-/etc/beehive}"

case "$PREFIX" in
  /*) ;;
  *) echo "PREFIX must be absolute: $PREFIX" >&2; exit 2 ;;
esac
case "$CONFDIR" in
  /*) ;;
  *) echo "BEEHIVE_CONFIG_DIR must be absolute: $CONFDIR" >&2; exit 2 ;;
esac

SCRIPT_DIR=$(CDPATH= cd "$(dirname "$0")" && pwd)
ROOT=$(CDPATH= cd "$SCRIPT_DIR/.." && pwd)
BINDIR="$DESTDIR$PREFIX/bin"
ETCDIR="$DESTDIR$CONFDIR"
GPGHOME="$ETCDIR/gnupg"

if command -v go >/dev/null 2>&1; then
  GOOS_DETECTED=$(go env GOOS)
  GOARCH_DETECTED=$(go env GOARCH)
else
  case "$(uname -s)" in
    Linux) GOOS_DETECTED=linux ;;
    Darwin) GOOS_DETECTED=darwin ;;
    *) GOOS_DETECTED=$(uname -s | tr '[:upper:]' '[:lower:]') ;;
  esac
  case "$(uname -m)" in
    x86_64|amd64) GOARCH_DETECTED=amd64 ;;
    aarch64|arm64) GOARCH_DETECTED=arm64 ;;
    *) GOARCH_DETECTED=$(uname -m) ;;
  esac
fi

install_binary() {
  bin="$1"
  exact="$ROOT/dist/$bin"
  platform="$ROOT/dist/$bin-$GOOS_DETECTED-$GOARCH_DETECTED"

  if [ -f "$exact" ]; then
    install -m 0755 "$exact" "$BINDIR/$bin"
    return
  fi
  if [ -f "$platform" ]; then
    install -m 0755 "$platform" "$BINDIR/$bin"
    return
  fi
  if ! command -v go >/dev/null 2>&1; then
    echo "missing dist artifact for $bin and go not found; cannot build from source" >&2
    exit 1
  fi

  tmpbase="${TMPDIR:-/tmp}"
  mkdir -p "$tmpbase"
  tmpdir=$(mktemp -d "$tmpbase/beehive-install.XXXXXX")
  trap 'rm -rf "$tmpdir"' EXIT HUP INT TERM
  (cd "$ROOT" && CGO_ENABLED="${CGO_ENABLED:-0}" go build -trimpath -o "$tmpdir/$bin" "./cmd/$bin")
  install -m 0755 "$tmpdir/$bin" "$BINDIR/$bin"
  rm -rf "$tmpdir"
  trap - EXIT HUP INT TERM
}

# 1. Binaries: prefer release artifacts, else build from source.
mkdir -p "$BINDIR"
for bin in beehive beehived honeybee; do
  install_binary "$bin"
done

# 2. Shared config/keyring layout for CLI, frontend, and honeybee.
mkdir -p "$ETCDIR" "$GPGHOME"
chmod 0700 "$GPGHOME"
if [ ! -f "$ETCDIR/config.yaml" ]; then
  cat > "$ETCDIR/config.yaml" <<EOF
gpg_home: $CONFDIR/gnupg
agent_cmd: opencode
ttl_minutes: 60
max_turns: 15
reject_limit: 3
EOF
fi

# 3. Generate gpg key only when possible and absent. Users may bring their own.
if [ "${BEEHIVE_SKIP_KEYGEN:-}" != "1" ] && command -v gpg >/dev/null 2>&1; then
  if ! gpg --homedir "$GPGHOME" --list-secret-keys 2>/dev/null | grep -q .; then
    gpg --homedir "$GPGHOME" --batch --gen-key <<EOF
%no-protection
Key-Type: eddsa
Key-Curve: ed25519
Subkey-Type: ecdh
Subkey-Curve: cv25519
Name-Real: beehive
Name-Comment: secrets keyring
Name-Email: beehive@localhost
Expire-Date: 0
%commit
EOF
    echo "generated beehive gpg key in $GPGHOME"
  else
    echo "gpg key already present in $GPGHOME; leaving untouched"
  fi
else
  echo "skipping keygen (gpg missing or BEEHIVE_SKIP_KEYGEN=1); bring your own key in $GPGHOME"
fi

echo "installed beehive, beehived, honeybee to $BINDIR; config in $ETCDIR"
