# Install Guide

Beehive ships three static binaries from one repo:

- `beehive` — deterministic CLI (no LLM)
- `beehived` — frontend daemon (only long-running process)
- `honeybee` — single-task agent runner

All three read shared config + gpg keyring from `/etc/beehive`. Works the same
single-host, config-managed, or container bind-mount.

## Package manager

```
sudo apt install beehive        # or: rpm/dnf
beehive version
```

The package places binaries in `/usr/local/bin` and scaffolds `/etc/beehive`
with a `config.yaml` and a `gnupg/` keyring. The postinstall scriptlet
(`packaging/postinstall.sh`) generates a gpg key at install time **only if none
exists** — bring your own key by dropping it in `/etc/beehive/gnupg` before
install. Set `BEEHIVE_SKIP_KEYGEN=1` to opt out.

Packages are built with [nfpm](https://nfpm.goreleaser.com) from
`packaging/nfpm.yaml` after the release cross-compile produces `dist/`.

## Manual / staged install

```
git clone https://github.com/spencerharmon/beehive && cd beehive
sudo scripts/install.sh                 # live install to /usr/local + /etc/beehive
scripts/install.sh "$PWD/stage"         # staged (DESTDIR) for packaging
```

`install.sh` prefers prebuilt `dist/` binaries, else builds from source. It is
idempotent: existing keyring and `config.yaml` are never clobbered.

## Release artifacts

CI cross-compiles `linux`/`darwin` × `amd64`/`arm64` on `v*` tags, emits
`SHA256SUMS-<os>-<arch>` and a cosign keyless signature. Verify:

```
cosign verify-blob --signature SHA256SUMS-linux-amd64.sig SHA256SUMS-linux-amd64
sha256sum -c SHA256SUMS-linux-amd64
```

## Config (`/etc/beehive/config.yaml`)

| key           | default              | meaning                       |
|---------------|----------------------|-------------------------------|
| `gpg_home`    | `/etc/beehive/gnupg` | secrets keyring dir           |
| `agent_cmd`   | `opencode`           | agent backend binary          |
| `ttl_minutes` | `60`                 | heartbeat TTL before GC       |
| `max_turns`   | `15`                 | per-honeybee turn cap         |
| `reject_limit`| `3`                  | rejections before NEEDS-HUMAN |

Missing file/fields fall back to defaults. Override dir with
`BEEHIVE_CONFIG_DIR`.
