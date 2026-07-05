# config: resolve the host config/keyring dir USER-FIRST

## Problem

`resolveDir` (`internal/config/config.go`) — the single source of the host config
and gpg-keyring directory, feeding `Load`, `Resolve`, and `LoadRegistry` — resolved
ROOT-first: it honored `$BEEHIVE_CONFIG_DIR`, otherwise hardcoded
`DefaultDir = /etc/beehive`. There was no per-user scope.

So a plain (non-root) user install silently read `/etc/beehive` unless
`BEEHIVE_CONFIG_DIR` was exported into **every** process that touches config: the
`beehived.service` unit, each transient honeybee pass (`systemd-run --setenv`), and
every interactive shell that runs `beehive`. Miss one and `beehive secret` opens the
wrong (or empty) keyring. This install already paid that tax — see `LOCALS.md`: the
keyring lives at `~/.config/beehive/` and the units carry an explicit
`BEEHIVE_CONFIG_DIR=%h/.config/beehive` solely to route around the root-first default.

## Fix

Make resolution USER-FIRST — the first location that is *usable*, in order:

1. **`$BEEHIVE_CONFIG_DIR`** — an explicit override, honored **verbatim even when it
   does not yet exist**, so a fresh scaffold can create the dir at exactly that path.
   (Unchanged from before; still the top precedence.)
2. **`${XDG_CONFIG_HOME:-~/.config}/beehive`** — the per-user config dir, chosen only
   when it **already exists** (existence-probed with `os.Stat` + `IsDir`). This makes a
   user install auto-detected with no env export anywhere. A **relative**
   `XDG_CONFIG_HOME` is invalid per the XDG Base Directory spec and is ignored (fall
   back to `~/.config`); with neither an absolute `XDG_CONFIG_HOME` nor `HOME` set, the
   scope is skipped. Factored into a small `userConfigDir()` helper that returns `""`
   when no usable base exists.
3. **`DefaultDir` (`/etc/beehive`)** — the final, **unconditional** fallback. It is
   **never stat-probed**: when nothing above resolves, `resolveDir` returns it directly,
   so a bare system install (no `BEEHIVE_CONFIG_DIR`, no `~/.config/beehive`) is
   byte-identical to before.

Only the private middle scope is new; `Load`/`Resolve`/`LoadRegistry` and every
secret/GPG caller are untouched and keep working (they consume `resolveDir()`'s
string). `config.go` was rewritten+merged by `config-layered` (pointer `c08cd15e`);
this change anchors to the current `resolveDir`/`DefaultDir`.

### Why existence-probe the user scope but not `/etc`

Probing `~/.config/beehive` is what lets a user dir win *automatically* while leaving
a bare host on `/etc/beehive` — without it, a user scope would either never win or
would shadow `/etc` even when empty. `/etc/beehive` is the terminal default: there is
nothing lower to fall to, so probing it would only add a pointless stat (and could
wrongly reject a legitimately-absent-but-creatable system dir). Hence the asymmetry.

### Why a relative `XDG_CONFIG_HOME` is ignored

The XDG Base Directory spec requires these variables to be absolute and says a relative
value must be treated as invalid and ignored. `userConfigDir` enforces this with
`filepath.IsAbs`: a non-absolute (including empty) `XDG_CONFIG_HOME` falls back to
`~/.config`, matching the `${XDG_CONFIG_HOME:-~/.config}` shell idiom while staying
spec-correct.

## Tests (`internal/config/config_test.go`)

`TestResolveDir` is table-driven (`t.Setenv`/`t.TempDir`, all three env vars set in
every case so the real environment can't leak in) and proves order + existence-probing:

- override wins verbatim even when the path is absent — and even when a usable
  `~/.config/beehive` is also present;
- an existing `$XDG_CONFIG_HOME/beehive` (absolute) wins over the `~/.config` fallback;
- a relative `XDG_CONFIG_HOME` is ignored, falling back to an existing `~/.config/beehive`;
- with `XDG_CONFIG_HOME` unset, an existing `~/.config/beehive` is picked;
- a set-but-absent user dir (HOME present, no `.config/beehive`) falls through to
  `/etc/beehive` (existence-probing);
- an absolute `$XDG_CONFIG_HOME` whose `beehive` subdir is absent falls through to
  `/etc/beehive` (existence-probing on the XDG branch);
- `HOME` + `XDG_CONFIG_HOME` unset → `/etc/beehive`.

The existing layering/registry tests all pin `BEEHIVE_CONFIG_DIR` to a `t.TempDir()`,
so they hit the top branch unchanged and continue to pass.
