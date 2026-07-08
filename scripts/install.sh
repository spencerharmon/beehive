#!/usr/bin/env sh
# Generic beehive installer. Builds or installs beehive, beehived, and honeybee;
# then scaffolds config/keyring. Safe to re-run: never clobbers existing
# config.yaml or gpg keys.
#
# DEFAULT is a rootless USER install — no root, no sudo:
#   binaries      -> $HOME/.local/bin
#   config/keyring -> $HOME/.config/beehive
# `beehive secret` then works out of the box: the binary resolves
# ~/.config/beehive on its own, so NO BEEHIVE_CONFIG_DIR export is needed
# anywhere (see internal/config resolveDir / docs/tasks/config-dir-user-first.md).
#
# Pass --system for the shared install (needs root/sudo):
#   binaries      -> /usr/local/bin
#   config/keyring -> /etc/beehive
#
# Usage:
#   scripts/install.sh [--user|--system] [DESTDIR]
#
# Environment (override the mode defaults):
#   PREFIX                install prefix (default: user $HOME/.local, system /usr/local)
#   BEEHIVE_CONFIG_DIR    config/keyring dir (default: user $HOME/.config/beehive, system /etc/beehive)
#   DESTDIR               staging root prefixed to install paths (packaging)
#   BEEHIVE_SKIP_KEYGEN=1 skip automatic gpg key generation
#   CGO_ENABLED=0         override Go build CGO setting
set -eu

usage() {
  cat <<'EOF'
Usage: scripts/install.sh [--user|--system] [DESTDIR]

  --user    (default) rootless install: $HOME/.local/bin + $HOME/.config/beehive (no sudo)
  --system            shared install:   /usr/local/bin + /etc/beehive (run with root/sudo)

Environment overrides: PREFIX, BEEHIVE_CONFIG_DIR, DESTDIR, TMPDIR, CGO_ENABLED, BEEHIVE_SKIP_KEYGEN=1
EOF
}

# Default user config/keyring dir, kept in lockstep with internal/config
# resolveDir: use $XDG_CONFIG_HOME when ABSOLUTE (a relative value is invalid per
# the XDG Base Directory spec and ignored), else ~/.config; always suffixed with
# /beehive. Matching resolveDir is what lets the binary find the installed dir with
# no BEEHIVE_CONFIG_DIR export.
user_config_dir() {
  base="$HOME/.config"
  case "${XDG_CONFIG_HOME:-}" in
    /*) base="$XDG_CONFIG_HOME" ;;
  esac
  printf '%s/beehive' "$base"
}

mode=user
DESTDIR="${DESTDIR:-}"
while [ "$#" -gt 0 ]; do
  case "$1" in
    --system) mode=system ;;
    --user) mode=user ;;
    -h|--help) usage; exit 0 ;;
    -*) echo "unknown option: $1" >&2; usage >&2; exit 2 ;;
    *) DESTDIR="$1" ;;
  esac
  shift
done

if [ "$mode" = system ]; then
  def_prefix=/usr/local
  def_confdir=/etc/beehive
else
  [ -n "${HOME:-}" ] || { echo "user install needs HOME set (or run with --system)" >&2; exit 2; }
  def_prefix="$HOME/.local"
  def_confdir="$(user_config_dir)"
fi
PREFIX="${PREFIX:-$def_prefix}"
CONFDIR="${BEEHIVE_CONFIG_DIR:-$def_confdir}"

case "$PREFIX" in
  /*) ;;
  *) echo "PREFIX must be absolute: $PREFIX" >&2; exit 2 ;;
esac
case "$CONFDIR" in
  /*) ;;
  *) echo "BEEHIVE_CONFIG_DIR must be absolute: $CONFDIR" >&2; exit 2 ;;
esac

# A non-staged --system install writes under /usr/local and /etc; hint sudo early
# rather than failing halfway through with a bare permission error.
if [ "$mode" = system ] && [ -z "$DESTDIR" ] && [ "$(id -u 2>/dev/null || echo 0)" != 0 ]; then
  echo "note: --system installs to $PREFIX and $CONFDIR; re-run with sudo if you hit permission errors" >&2
fi

SCRIPT_DIR=$(CDPATH= cd "$(dirname "$0")" && pwd)
ROOT=$(CDPATH= cd "$SCRIPT_DIR/.." && pwd)
BINDIR="$DESTDIR$PREFIX/bin"
ETCDIR="$DESTDIR$CONFDIR"
GPGHOME="$ETCDIR/gnupg"

# Stamp the build commit into any from-source binaries (prompt-embed drift
# guard): a running binary can then tell whether it predates the tracked-main tip
# and warn. Best-effort — a source tree with no .git (e.g. a release tarball)
# builds an honest unstamped "dev" binary rather than a wrong SHA.
BUILD_SHA=""
if command -v git >/dev/null 2>&1; then
  BUILD_SHA=$(git -C "$ROOT" rev-parse HEAD 2>/dev/null || true)
fi

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
  if [ -n "$BUILD_SHA" ]; then
    (cd "$ROOT" && CGO_ENABLED="${CGO_ENABLED:-0}" go build -trimpath -ldflags "-X github.com/spencerharmon/beehive/internal/version.SHA=$BUILD_SHA" -o "$tmpdir/$bin" "./cmd/$bin")
  else
    (cd "$ROOT" && CGO_ENABLED="${CGO_ENABLED:-0}" go build -trimpath -o "$tmpdir/$bin" "./cmd/$bin")
  fi
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
gpg_recipient: beehive@localhost
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

echo "installed beehive, beehived, honeybee to $BINDIR; config in $ETCDIR ($mode install)"

# For a real (non-staged) user install, $HOME/.local/bin is often not yet on
# PATH; point the operator at the one-line fix so `beehive` is runnable.
if [ "$mode" = user ] && [ -z "$DESTDIR" ]; then
  case ":${PATH:-}:" in
    *":$PREFIX/bin:"*) ;;
    *) echo "add $PREFIX/bin to PATH, e.g.: export PATH=\"$PREFIX/bin:\$PATH\"" >&2 ;;
  esac
fi
