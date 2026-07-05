#!/usr/bin/env sh
# Install beehive systemd USER units and user-local config/keyring — the default,
# rootless way to run the services (no root, no sudo, no system units).
#
# Creates:
#   ~/.config/beehive/config.yaml
#   ~/.config/beehive/gnupg/       (with a real gpg key if no secret key exists)
#   ~/.config/systemd/user/opencode.service
#   ~/.config/systemd/user/beehived.service
#   ~/.config/systemd/user/beehive-honeybee.service
#   ~/.config/systemd/user/beehive-honeybee.timer
#
# When the config dir is the default ~/.config/beehive, the binary resolves it on
# its own (config-dir-user-first), so the generated units carry NO
# BEEHIVE_CONFIG_DIR export; a custom --config-dir keeps an explicit override.
#
# Safe to re-run: existing config.yaml and gpg keys are not clobbered. Unit files
# are regenerated from arguments so path/port/schedule changes take effect after
# daemon-reload.
set -eu

usage() {
  cat <<'EOF'
Usage:
  scripts/install-systemd-user.sh --repo PATH [options]

Required:
  --repo PATH              beehive orchestration repo served by beehived and honeybee

Options:
  --config-dir PATH        config/keyring dir (default: $HOME/.config/beehive)
  --addr ADDR              beehived listen address (default: 0.0.0.0:8955)
  --calendar SPEC          honeybee timer OnCalendar value (default: *:0/7)
  --on-active DURATION     honeybee timer OnActiveSec value (default: 2min)
  --runtime-max SEC        transient honeybee RuntimeMaxSec (default: 21600)
  --path PATH              PATH used by units (default: $HOME/.local/bin:/usr/local/bin:/usr/bin:/bin)
  --opencode-cmd CMD       opencode binary for the server unit (default: opencode)
  --opencode-hostname HOST opencode server bind host (default: 127.0.0.1)
  --opencode-port PORT     opencode server port (default: 4096)
  --no-opencode            do not install/enable the opencode server unit
  --no-debug               omit --debug from honeybee command
  --no-enable              write units and daemon-reload, but do not enable them
  --now                    start the opencode, beehived, and honeybee units after enabling
  --linger                 run loginctl enable-linger for this user
  -h, --help               show this help

Environment defaults:
  BEEHIVE_REPO             repo path if --repo is omitted
  BEEHIVE_CONFIG_DIR       config dir if --config-dir is omitted
EOF
}

# Default user config/keyring dir, kept in lockstep with internal/config
# resolveDir: use $XDG_CONFIG_HOME when ABSOLUTE (a relative value is invalid per
# the XDG Base Directory spec and ignored), else ~/.config; always suffixed with
# /beehive. Matching resolveDir is what lets the binary find the installed dir with
# no BEEHIVE_CONFIG_DIR export.
user_config_dir() {
  base="$HOME/.config"
  case "${XDG_CONFIG_HOME:-}" in
    /*) base="$XDG_CONFIG_HOME" ;;
  esac
  printf '%s/beehive' "$base"
}

repo="${BEEHIVE_REPO:-}"
config_dir="${BEEHIVE_CONFIG_DIR:-$(user_config_dir)}"
addr="0.0.0.0:8955"
calendar="*:0/7"
on_active="2min"
runtime_max="21600"
unit_path="$HOME/.local/bin:/usr/local/bin:/usr/bin:/bin"
opencode_enable=1
opencode_cmd="opencode"
opencode_hostname="127.0.0.1"
opencode_port="4096"
debug=1
enable_units=1
start_now=0
enable_linger=0

while [ "$#" -gt 0 ]; do
  case "$1" in
    --repo)
      [ "$#" -ge 2 ] || { echo "--repo requires PATH" >&2; exit 2; }
      repo="$2"; shift 2 ;;
    --config-dir)
      [ "$#" -ge 2 ] || { echo "--config-dir requires PATH" >&2; exit 2; }
      config_dir="$2"; shift 2 ;;
    --addr)
      [ "$#" -ge 2 ] || { echo "--addr requires ADDR" >&2; exit 2; }
      addr="$2"; shift 2 ;;
    --calendar)
      [ "$#" -ge 2 ] || { echo "--calendar requires SPEC" >&2; exit 2; }
      calendar="$2"; shift 2 ;;
    --on-active)
      [ "$#" -ge 2 ] || { echo "--on-active requires DURATION" >&2; exit 2; }
      on_active="$2"; shift 2 ;;
    --runtime-max)
      [ "$#" -ge 2 ] || { echo "--runtime-max requires seconds" >&2; exit 2; }
      runtime_max="$2"; shift 2 ;;
    --path)
      [ "$#" -ge 2 ] || { echo "--path requires PATH" >&2; exit 2; }
      unit_path="$2"; shift 2 ;;
    --opencode-cmd)
      [ "$#" -ge 2 ] || { echo "--opencode-cmd requires CMD" >&2; exit 2; }
      opencode_cmd="$2"; shift 2 ;;
    --opencode-hostname)
      [ "$#" -ge 2 ] || { echo "--opencode-hostname requires HOST" >&2; exit 2; }
      opencode_hostname="$2"; shift 2 ;;
    --opencode-port)
      [ "$#" -ge 2 ] || { echo "--opencode-port requires PORT" >&2; exit 2; }
      opencode_port="$2"; shift 2 ;;
    --no-opencode)
      opencode_enable=0; shift ;;
    --no-debug)
      debug=0; shift ;;
    --no-enable)
      enable_units=0; shift ;;
    --now)
      start_now=1; enable_units=1; shift ;;
    --linger)
      enable_linger=1; shift ;;
    -h|--help)
      usage; exit 0 ;;
    *)
      echo "unknown option: $1" >&2
      usage >&2
      exit 2 ;;
  esac
done

[ -n "$repo" ] || { echo "--repo PATH is required (or set BEEHIVE_REPO)" >&2; exit 2; }
[ -n "$HOME" ] || { echo "HOME is not set" >&2; exit 2; }

case "$config_dir" in
  /*) ;;
  *) echo "--config-dir must be absolute: $config_dir" >&2; exit 2 ;;
esac
case "$runtime_max" in
  *[!0-9]*|'') echo "--runtime-max must be integer seconds: $runtime_max" >&2; exit 2 ;;
esac
case "$opencode_port" in
  *[!0-9]*|'') echo "--opencode-port must be an integer: $opencode_port" >&2; exit 2 ;;
esac
[ -n "$opencode_hostname" ] || { echo "--opencode-hostname must not be empty" >&2; exit 2; }
[ -n "$opencode_cmd" ] || { echo "--opencode-cmd must not be empty" >&2; exit 2; }

if [ ! -d "$repo" ]; then
  echo "repo path does not exist: $repo" >&2
  exit 2
fi
repo=$(CDPATH= cd "$repo" && pwd)

unit_dir="$HOME/.config/systemd/user"
gpg_home="$config_dir/gnupg"

# systemd expands % specifiers in unit files. Escape literal percent signs from
# operator-provided paths/values before writing them.
escape_unit() {
  printf '%s' "$1" | sed 's/%/%%/g'
}

unit_repo=$(escape_unit "$repo")
unit_config_dir=$(escape_unit "$config_dir")
unit_addr=$(escape_unit "$addr")
unit_path_escaped=$(escape_unit "$unit_path")
unit_calendar=$(escape_unit "$calendar")
unit_on_active=$(escape_unit "$on_active")
unit_opencode_cmd=$(escape_unit "$opencode_cmd")
unit_opencode_hostname=$(escape_unit "$opencode_hostname")

# BEEHIVE_CONFIG_DIR in the units: with config-dir-user-first the binary
# auto-resolves the DEFAULT user config dir (~/.config/beehive) from HOME/XDG with
# no env, so the units need NO export in that case (unit_config_env / run_config_env
# stay empty). A CUSTOM --config-dir is not auto-resolved, so keep an explicit
# override so beehived and each honeybee pass open the right keyring.
if [ "$config_dir" = "$(user_config_dir)" ]; then
  unit_config_env=""
  run_config_env=""
else
  unit_config_env="Environment=\"BEEHIVE_CONFIG_DIR=$unit_config_dir\""
  run_config_env="\"BEEHIVE_CONFIG_DIR=$unit_config_dir\" "
fi

# add_opencode_dep inserts `Wants=/After=opencode.service` into a unit's [Unit]
# section (right after its `After=network-online.target` line) so the frontend and
# each honeybee pass order after a reachable opencode server. No-op when the
# opencode unit is not installed.
add_opencode_dep() {
  [ "$opencode_enable" -eq 1 ] || return 0
  file="$1"
  tmp="$file.tmp"
  awk '
    { print }
    $0 == "After=network-online.target" && !inserted {
      print "Wants=opencode.service"
      print "After=opencode.service"
      inserted = 1
    }
  ' "$file" > "$tmp" && mv "$tmp" "$file"
}

mkdir -p "$config_dir" "$gpg_home" "$unit_dir"
chmod 0700 "$gpg_home"

if [ ! -f "$config_dir/config.yaml" ]; then
  cat > "$config_dir/config.yaml" <<EOF
gpg_home: $gpg_home
gpg_recipient: beehive@localhost
agent_cmd: opencode
ttl_minutes: 60
max_turns: 15
reject_limit: 3
EOF
  echo "wrote $config_dir/config.yaml"
else
  echo "config exists; leaving untouched: $config_dir/config.yaml"
fi

if ! command -v gpg >/dev/null 2>&1; then
  echo "gpg is required to create/check $gpg_home" >&2
  exit 1
fi

if ! gpg --homedir "$gpg_home" --list-secret-keys 2>/dev/null | grep -q .; then
  gpg --homedir "$gpg_home" --batch --gen-key <<EOF
%no-protection
Key-Type: eddsa
Key-Curve: ed25519
Subkey-Type: ecdh
Subkey-Curve: cv25519
Name-Real: beehive
Name-Comment: user secrets keyring
Name-Email: beehive@localhost
Expire-Date: 0
%commit
EOF
  echo "generated beehive gpg key in $gpg_home"
else
  echo "gpg key already present in $gpg_home; leaving untouched"
fi

if [ "$opencode_enable" -eq 1 ]; then
  cat > "$unit_dir/opencode.service" <<EOF
[Unit]
Description=opencode server for beehive honeybees
Documentation=https://opencode.ai
Wants=network-online.target
After=network-online.target

[Service]
Type=simple
Environment="PATH=$unit_path_escaped"
# Provider credentials: prefer 'opencode auth login' (writes to ~/.config), or
# drop keys into this optional EnvironmentFile (leading '-' = missing is OK).
EnvironmentFile=-$unit_config_dir/opencode.env
ExecStart=/usr/bin/env $unit_opencode_cmd serve --hostname "$unit_opencode_hostname" --port "$opencode_port"
Restart=on-failure
RestartSec=2s
KillSignal=SIGINT
TimeoutStopSec=10

[Install]
WantedBy=default.target
EOF
else
  rm -f "$unit_dir/opencode.service"
fi

cat > "$unit_dir/beehived.service" <<EOF
[Unit]
Description=Beehive frontend daemon
Wants=network-online.target
After=network-online.target

[Service]
Type=simple
Environment="PATH=$unit_path_escaped"
$unit_config_env
ExecStart=/usr/bin/env beehived -addr "$unit_addr" -repo "$unit_repo"
Restart=on-failure
RestartSec=5s

[Install]
WantedBy=default.target
EOF
add_opencode_dep "$unit_dir/beehived.service"

honeybee_debug_arg=""
if [ "$debug" -eq 1 ]; then
  honeybee_debug_arg=" --debug"
fi

cat > "$unit_dir/beehive-honeybee.service" <<EOF
[Unit]
Description=Launch a beehive honeybee pass
Wants=network-online.target
After=network-online.target

[Service]
Type=oneshot
Environment="PATH=$unit_path_escaped"
$unit_config_env
ExecStart=/usr/bin/systemd-run --user --collect --quiet \\
  -p SyslogIdentifier=honeybee \\
  -p RuntimeMaxSec=$runtime_max \\
  /usr/bin/env "PATH=$unit_path_escaped" ${run_config_env}\\
  beehive honeybee start "$unit_repo"$honeybee_debug_arg
EOF
add_opencode_dep "$unit_dir/beehive-honeybee.service"

cat > "$unit_dir/beehive-honeybee.timer" <<EOF
[Unit]
Description=Schedule beehive honeybee passes

[Timer]
OnActiveSec=$unit_on_active
OnCalendar=$unit_calendar
Persistent=false

[Install]
WantedBy=timers.target
EOF

if ! command -v systemctl >/dev/null 2>&1; then
  echo "systemctl not found; unit files written but user manager not reloaded" >&2
  exit 1
fi

systemctl --user daemon-reload

enable_list="beehived.service beehive-honeybee.timer"
start_list="beehived.service beehive-honeybee.timer"
if [ "$opencode_enable" -eq 1 ]; then
  enable_list="opencode.service $enable_list"
  start_list="opencode.service $start_list"
fi

if [ "$enable_units" -eq 1 ]; then
  systemctl --user enable $enable_list
fi

if [ "$start_now" -eq 1 ]; then
  systemctl --user start $start_list
fi

if [ "$enable_linger" -eq 1 ]; then
  if ! command -v loginctl >/dev/null 2>&1; then
    echo "loginctl not found; cannot enable lingering" >&2
    exit 1
  fi
  loginctl enable-linger "$USER"
fi

echo "installed systemd user units in $unit_dir"
echo "config: $config_dir"
echo "gpg_home: $gpg_home"
if [ -n "$unit_config_env" ]; then
  echo "config resolution: units export BEEHIVE_CONFIG_DIR=$config_dir (custom dir)"
else
  echo "config resolution: auto (default ~/.config/beehive; no BEEHIVE_CONFIG_DIR export)"
fi
echo "repo: $repo"
if [ "$opencode_enable" -eq 1 ]; then
  echo "opencode server: $opencode_cmd serve --hostname $opencode_hostname --port $opencode_port"
else
  echo "opencode server unit: skipped (--no-opencode)"
fi
if [ "$start_now" -eq 0 ]; then
  if [ "$enable_units" -eq 1 ]; then
    echo "units enabled but not started; use --now or run: systemctl --user start $start_list"
  else
    echo "units written but not enabled; run: systemctl --user enable $enable_list"
  fi
fi
