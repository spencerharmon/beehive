# ARTIFACTS — alpha

Build and deploy artifacts the alpha submodule produces. Keep this current so the
swarm and operators can see what ships.

- beehive: the swarm coordination CLI (static, CGO-free)
- beehived: the frontend daemon serving the web UI
- honeybee: the stateless worker binary
  - built with CGO_ENABLED=0 for a static ELF
- container image: ghcr.io/example/alpha published on release
- release notes
