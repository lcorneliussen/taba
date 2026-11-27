## Status

- **A status pane keeps the PR rows it saw.** With gh's default keychain
  storage, a background refresh that deferred a PR showed it as `never` again.
  The pane now keeps its earlier observation and age until a newer read
  replaces it. Kept rows grant no actions.
  ([#562](https://github.com/lcorneliussen/taba/issues/562)).
