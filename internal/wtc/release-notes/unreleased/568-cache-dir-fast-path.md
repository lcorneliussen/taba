## Status

- **Less work per status cache access.** Status no longer tries to create its
  cache directory on every access; an existing directory is only checked
  ([#568](https://github.com/lcorneliussen/taba/issues/568)).
