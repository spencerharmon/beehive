#!/usr/bin/env sh
# Package postinstall: scaffold /etc/beehive keyring + generate a key if absent.
# Binaries and config.yaml are placed by the package payload; this only owns
# the gpg keyring so reinstalls never clobber existing keys/config.
set -eu
CONFDIR=/etc/beehive
GPGHOME="$CONFDIR/gnupg"
mkdir -p "$GPGHOME"; chmod 0700 "$GPGHOME"
if command -v gpg >/dev/null 2>&1; then
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
    echo "beehive: generated gpg key in $GPGHOME"
  fi
fi
