# Skill: Bootstrap

> Use when: standing up a new target (or a whole new install) from scratch.

## Stand up a target

1. Add the submodule (`add-submodule` skill) and author its `ROI.md`.
2. A **bootstrap** pass (ROI present, PLAN absent) decomposes `ROI.md` into a
   weighted `PLAN.md`. It plans only — it does not implement. Subsequent `select`
   passes pick and execute tasks.

## Stand up a whole install

Follow `BOOTSTRAP.md` at the repo root. In short:

1. Install/build the binaries; place them per the install's deploy convention.
2. Create the beehive repo (`beehive init <path>`) — this lays down the managed
   instruction files (`AGENTS.md`, `HONEYBEE.md`, `BOOTSTRAP.md`).
3. **Author `LOCALS.md`** with this site's facts (paths, deploy, scheduler, ports,
   safety rules). It is site-specific and never managed by an update.
4. Add your first submodule and ROI, then let bootstrap build the plan.
5. Wire the scheduler/frontend per `LOCALS.md`.

## Rules

- Bootstrap plans; it must not implement tasks.
- `LOCALS.md` is the one file you must write by hand — there is no default for it.
