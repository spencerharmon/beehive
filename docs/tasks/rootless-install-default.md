# install: make the rootless USER install the default

## Problem

The installers were ROOT-first. `scripts/install.sh` defaulted to
`PREFIX=/usr/local` + `BEEHIVE_CONFIG_DIR=/etc/beehive` and scaffolded the config
and gpg keyring there — so the documented "install" (`sudo ./scripts/install.sh`)
needed root to create the keyring and, with the old root-first config resolution,
root or a `BEEHIVE_CONFIG_DIR` export to read it. The rootless user path existed
only as an env-override incantation
(`PREFIX=$HOME/.local BEEHIVE_CONFIG_DIR=$HOME/.config/beehive ./scripts/install.sh`)
and `scripts/install-systemd-user.sh`, both presented as secondary. `packaging/`
is inherently the system path.

With `config-dir-user-first` now merged, the binary resolves `~/.config/beehive`
on its own (existence-probed, no env). This task flips the *installers and docs*
to match: the rootless user install is the default; the system `/etc` install is
an explicit opt-in.

## Fix

### `scripts/install.sh` — user by default, `--system` opt-in

- Parses `--user` (default) / `--system` and an optional positional/`$DESTDIR`.
- **Mode defaults**: user → `PREFIX=$HOME/.local`, `CONFDIR=$(user_config_dir)`;
  system → `PREFIX=/usr/local`, `CONFDIR=/etc/beehive`. Explicit `PREFIX` /
  `BEEHIVE_CONFIG_DIR` env still override the mode default (custom & staged
  installs unaffected).
- `user_config_dir()` mirrors `internal/config` `resolveDir`: `$XDG_CONFIG_HOME`
  when **absolute** (a relative value is invalid per the XDG spec and ignored),
  else `~/.config`, suffixed `/beehive`. Keeping it in lockstep is what lets the
  binary find the installed dir with **no `BEEHIVE_CONFIG_DIR` export**.
- Generated `config.yaml` now sets `gpg_recipient: beehive@localhost`, matching
  the key the installer generates, so `beehive secret` works with zero flags.
- User mode requires `HOME`; prints a one-line PATH hint when `$PREFIX/bin` is not
  on PATH. Non-staged `--system` as non-root prints a sudo hint up front.
- Final line names the mode (`… (user install)` / `… (system install)`).

### `scripts/install-systemd-user.sh` — default dir needs no env export

- Default config dir now comes from the same `user_config_dir()` helper.
- Generated `config.yaml` gains `gpg_recipient: beehive@localhost`.
- **Units drop `BEEHIVE_CONFIG_DIR` when the config dir is the resolvable default**
  (`$(user_config_dir)`): `beehived.service`, `beehive-honeybee.service`, and the
  transient `systemd-run … /usr/bin/env …` line all rely on the binary's
  auto-resolution. A **custom `--config-dir`** (not auto-resolved) keeps the
  explicit `Environment="BEEHIVE_CONFIG_DIR=…"` override so services still open the
  right keyring. Controlled by `unit_config_env` / `run_config_env`; the summary
  prints which resolution is in effect.

### `packaging/` — the explicit system path

`postinstall.sh` header clarifies it is the opt-in `/etc/beehive` system install
(the rootless user install is the default). `packaging/config.yaml` gains
`gpg_recipient: beehive@localhost` so a packaged system install's secrets also
work out of the box.

### Docs

`README.md` and `docs/install.md` present the **user install first as the
default** (`./scripts/install.sh`, no sudo, `beehive secret` out of the box) and
the system install as opt-in (`sudo ./scripts/install.sh --system`). The staged
example is `DESTDIR=… ./scripts/install.sh --system`. The Config section documents
the user-first resolution order and the `gpg_recipient` default.

## Why keep `BEEHIVE_CONFIG_DIR` only for a custom `--config-dir`

`resolveDir` auto-detects **only** `${XDG_CONFIG_HOME:-~/.config}/beehive`. A
custom `--config-dir` is never auto-detected, so a service started without the env
would silently open the wrong (default or empty) keyring. Hence the asymmetry:
omit the export when it is redundant (default dir), keep it when it is load-bearing
(custom dir).

## Tests (manual, fresh non-root run — see the change doc for full transcript)

- Default `./scripts/install.sh` as a non-root user with `BEEHIVE_CONFIG_DIR` and
  `XDG_CONFIG_HOME` unset: builds+installs to `$HOME/.local/bin`, writes
  `$HOME/.config/beehive/{config.yaml,gnupg + key}`, no sudo.
- `beehive secret add -f in.yaml` then `EDITOR=cat beehive secret edit` round-trips
  encrypt→decrypt against the installed user keyring with **no `BEEHIVE_CONFIG_DIR`
  and no sudo** — the binary resolved `$HOME/.config/beehive`.
- `DESTDIR=stage ./scripts/install.sh --system` stages `/usr/local/bin` +
  `/etc/beehive` (system install still succeeds as opt-in).
- `install-systemd-user.sh` default dir → no `BEEHIVE_CONFIG_DIR` in any unit;
  `--config-dir CUSTOM` → the override present in both services and the
  `systemd-run` line.
- `go test ./internal/config/...` passes (no Go changed; resolution semantics the
  installers mirror are unchanged).
