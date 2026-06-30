# Orchestration

A honeybee runs one task and exits. There is no controller — anything that can
run a command on a schedule (cron, CI, a k8s CronJob, a loop) can drive the
swarm. Run a pass as often as you want; passes coordinate only through git and
are safe to overlap.

```
beehive honeybee start <beehive-repo>            # one pass, then exits
beehive honeybee start <beehive-repo> --debug    # same, streaming each turn
```

This page shows one approach: a **systemd user timer** that launches each pass as
an independent transient unit, so a long-running pass never blocks the next one.

## Two units

`~/.config/systemd/user/beehive-honeybee.service` — fire-and-forget launcher.
Each activation spawns one pass as its own transient unit via `systemd-run` and
returns immediately:

```ini
[Unit]
Description=Launch a beehive honeybee pass
Wants=network-online.target
After=network-online.target

[Service]
Type=oneshot
ExecStart=systemd-run --user --collect --quiet \
  -p SyslogIdentifier=honeybee \
  -p RuntimeMaxSec=21600 \
  beehive honeybee start %h/beehive-repo --debug
```

`~/.config/systemd/user/beehive-honeybee.timer` — the schedule:

```ini
[Unit]
Description=Schedule beehive honeybee passes

[Timer]
OnActiveSec=2min
OnCalendar=*:0/7

[Install]
WantedBy=timers.target
```

Enable it:

```
systemctl --user daemon-reload
systemctl --user enable --now beehive-honeybee.timer
```

## Why a transient per pass

Spawning each pass with `systemd-run` instead of running it inside the service
decouples the pass from the timer:

- **Overlap is free.** A pass that outlives the interval keeps running alongside
  the next one. Honeybees are mutually safe — worktree isolation, claim skip, a
  publish-to-main race, and process-unique session ids — so concurrency just
  means more throughput.
- **One log stream.** `SyslogIdentifier=honeybee` puts every pass under one
  journal tag; tell them apart by PID.
- **A hard backstop.** `RuntimeMaxSec=21600` (6h) reaps a wedged process. A
  healthy honeybee self-caps its wall clock at `ttl_minutes` well before this.

`--debug` streams each turn (reasoning + tool calls) to the journal; drop it for
quieter logs.

## Operate

```
systemctl --user list-timers beehive-honeybee.timer   # next fire
systemctl --user start beehive-honeybee.service        # trigger a pass now
systemctl --user stop beehive-honeybee.timer           # pause scheduling
journalctl --user -t honeybee -f                       # all passes, live
systemctl --user list-units 'run-*.service'            # passes running now
systemctl --user stop 'run-*.service'                  # stop all running passes
```

Editing or reloading these units never disturbs a running pass: each lives in its
own transient unit, unbound from the timer and the launcher.

## Headless hosts

The user manager (and its passes) stops at logout unless lingering is on:

```
loginctl enable-linger $USER
```
