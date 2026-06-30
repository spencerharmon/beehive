# INFRASTRUCTURE — beehive

Test rigs and live infrastructure that runs the swarm against this submodule.
This representative document carries NO blue/green markers, so the resolved
Deployment must fall back to the defaults (blue active, {blue, green} available).

## Host capabilities

Agents have user-level (non-root) access to a Linux host with podman, systemd
user units, and a static Go toolchain (CGO_ENABLED=0).

## Conventions

- Rootless and user-scoped only — never assume root or sudo.
- Name rigs after their purpose so they can be found and pruned.
