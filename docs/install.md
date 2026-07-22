# Install Guide

Beehive ships three static Go binaries from one repo:

- `beehive` — deterministic CLI (no LLM calls)
- `beehived` — frontend daemon (only long-running process, defaults to `:8955`)
- `honeybee` — single-task agent runner

All three read shared config and a gpg keyring from one directory, resolved **user-first**: `$BEEHIVE_CONFIG_DIR` if set, else `~/.config/beehive` when it exists (the default user install), else `/etc/beehive` (the system install). So a rootless user install needs no `BEEHIVE_CONFIG_DIR` export — see [Config](#config-configyaml).

## Requirements

- Go 1.22+ when building from source.
- Git.
- `gpg` for secrets/key generation.
- Agent backend on PATH for honeybee runs. Default: `opencode`.

## Direct install from repo

### User install (default, no root)

`scripts/install.sh` defaults to a rootless **user** install: binaries to
`$HOME/.local/bin`, config and gpg keyring to `$HOME/.config/beehive`.

```sh
git clone https://github.com/spencerharmon/beehive.git
cd beehive
./scripts/install.sh
export PATH="$HOME/.local/bin:$PATH"   # if not already on PATH
beehive version
```

No sudo is needed to create or read the keyring, and `beehive secret` works out
of the box: the binary resolves `$HOME/.config/beehive` on its own, so you do
**not** export `BEEHIVE_CONFIG_DIR` anywhere (see [Config](#config-configyaml)).

### System install (opt-in, needs root)

Pass `--system` to install for all users under `/usr/local/bin` and `/etc/beehive`:

```sh
git clone https://github.com/spencerharmon/beehive.git
cd beehive
sudo ./scripts/install.sh --system
beehive version
```

### Staged install for packaging

```sh
DESTDIR="$PWD/stage" ./scripts/install.sh --system
```

`install.sh` behavior:

- Defaults to the **user** install; `--system` selects `/usr/local` + `/etc/beehive`.
- `PREFIX` and `BEEHIVE_CONFIG_DIR` override the per-mode defaults (custom or staged installs).
- Installs `beehive`, `beehived`, and `honeybee`.
- Prefers release artifacts named `dist/<binary>-<os>-<arch>` or `dist/<binary>`.
- Builds from source when matching artifacts are absent.
- Creates config directory and `config.yaml` (with `gpg_recipient: beehive@localhost`) only if missing.
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

The default, rootless way to run the services (no root, no system units):

```sh
./scripts/install-systemd-user.sh --repo /path/to/beehive-repo --now
```

This creates `~/.config/beehive/config.yaml`, creates/generates a real gpg key under `~/.config/beehive/gnupg`, installs `opencode.service`, `beehived.service`, `beehive-honeybee.service`, and `beehive-honeybee.timer` under `~/.config/systemd/user`, reloads systemd user units, enables them, and starts them when `--now` is present. The opencode server unit runs `opencode serve` (default `127.0.0.1:4096`); pass `--no-opencode` to skip it.

With the default config dir (`~/.config/beehive`) the binary resolves it on its own, so the generated units carry **no** `BEEHIVE_CONFIG_DIR` export; a custom `--config-dir` keeps an explicit override in the units.


Expose on all interfaces:

```sh
beehived -addr 0.0.0.0:8955 -repo /path/to/beehive-repo
```

## Package manager

A distro package is the explicit **system** install (the opt-in `/etc/beehive` path; the rootless user install above is the default).

```sh
sudo apt install beehive        # or rpm/dnf package
beehive version
```

The package places binaries in `/usr/local/bin` and scaffolds `/etc/beehive` with a `config.yaml` and `gnupg/` keyring. The postinstall scriptlet (`packaging/postinstall.sh`) generates a gpg key at install time only if none exists. Bring your own key by dropping it in `/etc/beehive/gnupg` before install. Set `BEEHIVE_SKIP_KEYGEN=1` to opt out when using `scripts/install.sh --system` directly.

Packages are built with [nfpm](https://nfpm.goreleaser.com) from `packaging/nfpm.yaml` after release cross-compile produces `dist/`.

## Release artifacts

CI cross-compiles `linux`/`darwin` × `amd64`/`arm64` on `v*` tags, emits `SHA256SUMS-<os>-<arch>` plus a cosign **keyless** signature (`.sig`) and its certificate (`.pem`), and re-verifies every artifact before publishing. Verify a download yourself with [cosign](https://docs.sigstore.dev) (replace `<owner>/<repo>`):

```sh
cosign verify-blob \
  --certificate SHA256SUMS-linux-amd64.pem \
  --signature   SHA256SUMS-linux-amd64.sig \
  --certificate-identity-regexp '^https://github.com/<owner>/<repo>/[.]github/workflows/.+@refs/tags/v.+$' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  SHA256SUMS-linux-amd64
sha256sum -c SHA256SUMS-linux-amd64
```

Or run `scripts/verify-release.sh <dir>` over the download directory to do the static-binary check, checksums, and cosign verification in one step (the same script CI runs).

## Config (config.yaml)

The config/keyring directory is resolved **user-first** by every binary
(`beehive`, `beehived`, `honeybee`), in order:

1. `$BEEHIVE_CONFIG_DIR` — explicit override, used verbatim even if absent.
2. `${XDG_CONFIG_HOME:-~/.config}/beehive` — the default **user** install, chosen when it exists (so no env export is needed).
3. `/etc/beehive` — the **system** install fallback.

So a user install (`~/.config/beehive`) is picked up automatically; a system
install (`/etc/beehive`) works with nothing set. Paths in the table below are the
system install's; a user install substitutes `~/.config/beehive`.

| key             | default (system)     | meaning                         |
|-----------------|----------------------|---------------------------------|
| `gpg_home`      | `/etc/beehive/gnupg` | secrets keyring dir             |
| `gpg_recipient` | `beehive@localhost`  | recipient for `SECRETS.yaml.gpg` (matches the installer-generated key) |
| `agent_cmd`     | `opencode`           | agent backend binary            |
| `ttl_minutes`   | `60`                 | heartbeat TTL before GC         |
| `max_turns`     | `15`                 | per-honeybee turn cap           |
| `reject_limit`  | `3`                  | rejections before NEEDS-HUMAN   |
| `abort_on_remote_failure` | `true`     | unreachable remote at preflight: `true`=abort the pass, `false`=degrade to local-only (outage escape hatch; see docs/sharing-modes.md) |

Missing file/fields fall back to defaults. The installers write `gpg_recipient:
beehive@localhost` to match the key they generate, so `beehive secret` works out
of the box; bring your own key and set `gpg_recipient` to its identity.

See [docs/sharing-modes.md](sharing-modes.md) for how a component detects
local-sharing vs. remote-sharing from the repo's git remotes, and
[docs/main-convergence-protocol.md](main-convergence-protocol.md) for the write
protocol every remote-sharing writer to `main` must follow to avoid a
silent-loss fork.
