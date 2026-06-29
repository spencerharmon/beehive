#!/usr/bin/env sh
# beehive package-manager install layout. Places the three binaries and
# scaffolds /etc/beehive, generating a gpg key at install time if one is absent.
# Idempotent: re-runnable; never clobbers an existing keyring or config.
# Usage: install.sh [DESTDIR]   (DESTDIR for staged/packaged builds; "" = live)
set -eu

DESTDIR="${1:-}"
PREFIX="${PREFIX:-/usr/local}"
CONFDIR="${BEEHIVE_CONFIG_DIR:-/etc/beehive}"
BINDIR="$DESTDIR$PREFIX/bin"
ETCDIR="$DESTDIR$CONFDIR"
GPGHOME="$ETCDIR/gnupg"

# 1. binaries: prefer dist/ (release artifacts), else build from source.
mkdir -p "$BINDIR"
for bin in beehive beehived honeybee; do
  if [ -f "dist/$bin" ]; then
    install -m 0755 "dist/$bin" "$BINDIR/$bin"
  else
    go build -o "$BINDIR/$bin" "./cmd/$bin"
    chmod 0755 "$BINDIR/$bin"
  fi
done

# 2. /etc/beehive layout, shared by cli/frontend/honeybee.
mkdir -p "$ETCDIR" "$GPGHOME"
chmod 0700 "$GPGHOME"
[ -f "$ETCDIR/config.yaml" ] || cat > "$ETCDIR/config.yaml" <<EOF
gpg_home: $CONFDIR/gnupg
agent_cmd: opencode
ttl_minutes: 60
max_turns: 15
reject_limit: 3
EOF

# 3. gpg keygen at install time if no key exists. User may bring their own.
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

echo "installed beehive,beehived,honeybee to $BINDIR; config in $ETCDIR"
