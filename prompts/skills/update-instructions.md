# Skill: Update instructions

> Use when: the binary advanced and the on-disk managed instruction files
> (`AGENTS.md`, `HONEYBEE.md`, `BOOTSTRAP.md`) and the `skills/` directory should be
> refreshed to the new defaults.

The managed set = the three root docs plus every file under `skills/`. `LOCALS.md`
and all per-repo content are never managed.

## Procedure

- `beehive instruction list` — show each managed file's status (`clean`, `modified`,
  or `missing`) without changing anything.
- `beehive instruction update` — bring the managed files to the binary's current
  defaults:
  - A **missing** or **clean** file is written in place.
  - A file you have **modified** is, without `--clobber`, offered for confirmation
    (overwrite y/N); with `--clobber` it is backed up to `<name>.<epoch>.bak` and
    replaced. The backup and the refreshed file are both committed.
- `--yes` answers every confirmation yes; `--repo <path>` targets a repo other than
  the cwd.

## Rules

- `LOCALS.md` is never touched by an update — it is site-authored.
- A new skill shipped by the binary appears as a `missing` file and is installed; an
  update does not delete a skill you have added locally.
- Customizing a managed file is fine — the update always backs your version up before
  replacing it, so nothing is lost.
