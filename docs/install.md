# Install Guide

Beehive ships three static Go binaries from one repo:

- `beehive` — deterministic CLI (no LLM calls)
- `beehived` — frontend daemon (only long-running process, defaults to `:8955`)
- `honeybee` — single-task agent runner

All three read shared config and gpg keyring from `/etc/beehive` by default. Override with `BEEHIVE_CONFIG_DIR`.

## Requirements

- Go 1.22+ when building from source.
- Git.
- `gpg` for secrets/key generation.
- Agent backend on PATH for honeybee runs. Default: `opencode`.

## Direct install from repo

System install to `/usr/local/bin` and `/etc/beehive`:

```sh
git clone https://github.com/spencerharmon/beehive.git
cd beehive
sudo ./scripts/install.sh
beehive version
```

User-local install without sudo:

```sh
git clone https://github.com/spencerharmon/beehive.git
cd beehive
PREFIX="$HOME/.local" \
BEEHIVE_CONFIG_DIR="$HOME/.config/beehive" \
./scripts/install.sh
export PATH="$HOME/.local/bin:$PATH"
beehive version
```

Staged install for packaging:

```sh
DESTDIR="$PWD/stage" PREFIX=/usr/local ./scripts/install.sh
```

`install.sh` behavior:

- Installs `beehive`, `beehived`, and `honeybee`.
- Prefers release artifacts named `dist/<binary>-<os>-<arch>` or `dist/<binary>`.
- Builds from source when matching artifacts are absent.
- Creates config directory and `config.yaml` only if missing.
- Creates gpg keyring and generates a key only when `gpg` exists and no secret key exists.
- Honors `PREFIX`, `BEEHIVE_CONFIG_DIR`, `DESTDIR`, `TMPDIR`, `CGO_ENABLED`, and `BEEHIVE_SKIP_KEYGEN=1`.

## Build without installing

```sh
git clone https://github.com/spencerharmon/beehive.git
cd beehive
mkdir -p bin
for bin in beehive beehived honeybee; do
  CGO_ENABLED=0 go build -trimpath -o "bin/$bin" "./cmd/$bin"
done
./bin/beehive version
```

## Run beehived

```sh
beehived -repo /path/to/beehive-repo
# http://localhost:8955
```

## Install systemd user units

For user-local config and services:

```sh
./scripts/install-systemd-user.sh --repo /path/to/beehive-repo --now
```

This creates `~/.config/beehive/config.yaml`, creates/generates a real gpg key under `~/.config/beehive/gnupg`, installs `beehived.service`, `beehive-honeybee.service`, and `beehive-honeybee.timer` under `~/.config/systemd/user`, reloads systemd user units, enables them, and starts them when `--now` is present.


Expose on all interfaces:

```sh
beehived -addr 0.0.0.0:8955 -repo /path/to/beehive-repo
```

## Package manager

```sh
sudo apt install beehive        # or rpm/dnf package
beehive version
```

The package places binaries in `/usr/local/bin` and scaffolds `/etc/beehive` with a `config.yaml` and `gnupg/` keyring. The postinstall scriptlet (`packaging/postinstall.sh`) generates a gpg key at install time only if none exists. Bring your own key by dropping it in `/etc/beehive/gnupg` before install. Set `BEEHIVE_SKIP_KEYGEN=1` to opt out when using `scripts/install.sh` directly.

Packages are built with [nfpm](https://nfpm.goreleaser.com) from `packaging/nfpm.yaml` after release cross-compile produces `dist/`.

## Release artifacts

CI cross-compiles `linux`/`darwin` × `amd64`/`arm64` on `v*` tags with
`CGO_ENABLED=0` (linux binaries are fully static ELF; darwin binaries are
CGO-free but link libSystem per macOS). Each `SHA256SUMS-<os>-<arch>` is signed
keyless with cosign — a signature (`.sig`) and Fulcio certificate (`.pem`) — and
a `verify-release` CI job re-verifies every published set from a clean runner.

Verify locally after downloading a set:

```sh
gh release download <tag> --dir dist \
  --pattern '*-linux-amd64' --pattern 'SHA256SUMS-linux-amd64*'
scripts/verify-release.sh linux amd64 dist
```

or run cosign directly:

```sh
cosign verify-blob \
  --certificate SHA256SUMS-linux-amd64.pem \
  --signature   SHA256SUMS-linux-amd64.sig \
  --certificate-identity-regexp '^https://github.com/spencerharmon/beehive/\.github/workflows/.+@refs/tags/' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  SHA256SUMS-linux-amd64
sha256sum -c SHA256SUMS-linux-amd64
```

## Config (`/etc/beehive/config.yaml`)

| key            | default              | meaning                         |
|----------------|----------------------|---------------------------------|
| `gpg_home`     | `/etc/beehive/gnupg` | secrets keyring dir             |
| `agent_cmd`    | `opencode`           | agent backend binary            |
| `ttl_minutes`  | `60`                 | heartbeat TTL before GC         |
| `max_turns`    | `15`                 | per-honeybee turn cap           |
| `reject_limit` | `3`                  | rejections before NEEDS-HUMAN   |

Missing file/fields fall back to defaults. Override config dir with `BEEHIVE_CONFIG_DIR`.
