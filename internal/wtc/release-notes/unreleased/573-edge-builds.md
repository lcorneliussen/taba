## Upgrade

- **Prepare opt-in edge builds of green `main`.** An edge build is selected in
  one collection through its local `mise.local.toml`. Agent shells now refresh
  their cached toolchain when that file appears, changes or disappears, and
  `upgrade` refuses edge versions so they never become a pin. Publishing edge
  builds stays off until it is enabled separately; see
  [Edge builds](https://github.com/lcorneliussen/taba/blob/main/docs/edge-builds.md)
  ([#573](https://github.com/lcorneliussen/taba/issues/573)).
