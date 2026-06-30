# Orchestration

A honeybee runs one task and exits. There is no central controller — anything that can run a command on a schedule (cron, CI, a k8s CronJob, a loop) can drive the swarm. Run a pass as often as you want; passes coordinate only through git and are safe to overlap.

```sh
beehive honeybee start <beehive-repo>            # one pass, then exits
beehive honeybee start <beehive-repo> --debug    # same, streaming each turn
```

This page shows one production-friendly approach: **systemd user units** for both the frontend daemon and scheduled honeybee passes.

Assumptions in examples below:

- Beehive repo: `%h/beehive-infra`
- Binaries: `%h/.local/bin` or `/usr/local/bin`
- Config: `%h/.config/beehive` for user-local installs, or `/etc/beehive` for system installs
- Frontend port: `8955`

Replace paths for your host.

## Frontend daemon unit

`~/.config/systemd/user/beehived.service`:

```ini
[Unit]
Description=Beehive frontend daemon
Wants=network-online.target
After=network-online.target

[Service]
Type=simple
Environment=PATH=%h/.local/bin:/usr/local/bin:/usr/bin:/bin
Environment=BEEHIVE_CONFIG_DIR=%h/.config/beehive
WorkingDirectory=%h/beehive-infra
ExecStart=/usr/bin/env beehived -addr 0.0.0.0:8955 -repo %h/beehive-infra
Restart=on-failure
RestartSec=5s

[Install]
WantedBy=default.target
```

For a system install using `/etc/beehive`, remove the `BEEHIVE_CONFIG_DIR` line:

```ini
Environment=PATH=/usr/local/bin:/usr/bin:/bin
```

Enable frontend:

```sh
systemctl --user daemon-reload
systemctl --user enable --now beehived.service
journalctl --user -u beehived.service -f
```

Open `http://<host>:8955`.

## Honeybee scheduler units

Use two units:

- `beehive-honeybee.service` — fire-and-forget launcher.
- `beehive-honeybee.timer` — schedule.

Each timer activation spawns one honeybee pass as its own transient unit via `systemd-run` and returns immediately. Long-running passes do not block future timer fires.

`~/.config/systemd/user/beehive-honeybee.service`:

```ini
[Unit]
Description=Launch a beehive honeybee pass
Wants=network-online.target
After=network-online.target

[Service]
Type=oneshot
Environment=PATH=%h/.local/bin:/usr/local/bin:/usr/bin:/bin
Environment=BEEHIVE_CONFIG_DIR=%h/.config/beehive
ExecStart=/usr/bin/systemd-run --user --collect --quiet \
  -p SyslogIdentifier=honeybee \
  -p RuntimeMaxSec=21600 \
  /usr/bin/env PATH=%h/.local/bin:/usr/local/bin:/usr/bin:/bin BEEHIVE_CONFIG_DIR=%h/.config/beehive \
  beehive honeybee start %h/beehive-infra --debug
```

For a system install using `/etc/beehive`, omit `BEEHIVE_CONFIG_DIR`:

```ini
[Service]
Type=oneshot
Environment=PATH=/usr/local/bin:/usr/bin:/bin
ExecStart=/usr/bin/systemd-run --user --collect --quiet \
  -p SyslogIdentifier=honeybee \
  -p RuntimeMaxSec=21600 \
  /usr/bin/env PATH=/usr/local/bin:/usr/bin:/bin \
  beehive honeybee start %h/beehive-infra --debug
```

`~/.config/systemd/user/beehive-honeybee.timer`:

```ini
[Unit]
Description=Schedule beehive honeybee passes

[Timer]
OnActiveSec=2min
OnCalendar=*:0/7
Persistent=false

[Install]
WantedBy=timers.target
```

Enable scheduler:

```sh
systemctl --user daemon-reload
systemctl --user enable --now beehive-honeybee.timer
systemctl --user list-timers beehive-honeybee.timer
```

Trigger one pass now:

```sh
systemctl --user start beehive-honeybee.service
```

## Why a transient unit per pass

Spawning each pass with `systemd-run` instead of running it inside the scheduled service decouples honeybee lifetime from timer lifetime:

- **Overlap is free.** A pass that outlives the interval keeps running alongside the next pass. Honeybees are mutually safe through worktree isolation, claim skip, publish-to-main races, and process-unique session ids.
- **One log stream.** `SyslogIdentifier=honeybee` puts every pass under one journal tag; tell passes apart by PID.
- **Hard backstop.** `RuntimeMaxSec=21600` (6h) reaps a wedged process. A healthy honeybee self-caps its wall clock at `ttl_minutes` before this.

`--debug` streams turns and tool output to journal. Drop `--debug` for quieter logs.

## Operate

```sh
systemctl --user status beehived.service               # frontend status
journalctl --user -u beehived.service -f               # frontend logs

systemctl --user list-timers beehive-honeybee.timer    # next scheduled pass
systemctl --user start beehive-honeybee.service         # trigger one pass now
systemctl --user stop beehive-honeybee.timer            # pause scheduling
journalctl --user -t honeybee -f                        # all honeybee pass logs
systemctl --user list-units 'run-*.service'             # running transient passes
```

Stopping timer or editing/reloading units does not stop already-running passes. Each pass lives in its own transient `run-*.service` unit.

Avoid stopping `run-*.service` units unless you intentionally want to abort in-progress agent work.

## Headless hosts

The user manager and units stop at logout unless lingering is enabled:

```sh
loginctl enable-linger "$USER"
```
