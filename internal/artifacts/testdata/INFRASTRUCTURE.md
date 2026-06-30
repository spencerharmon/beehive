# INFRASTRUCTURE — alpha

Topology and deployment state for the alpha submodule. Document every rig here so
the next bee can reuse, repair, or tear it down.

## Environments

Active: green
Environments: blue, green, canary

## Topology

- web tier behind the load balancer
- a single postgres primary with a hot standby
- object storage for build artifacts
