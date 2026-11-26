## Status

- **New Bitbucket PRs are read promptly, and known facts stay put.** A PR no
  view has observed gets a bounded first read on the shared budget, shows
  `wait` instead of `never` and is retried without a keypress, while merged PRs
  keep their merge time and archive place across deferred reads. Panes from
  older releases keep their behaviour until upgraded, and status still does not
  reuse observations made under a credential it cannot verify, such as a TWG
  profile kept in TWG's own store.
  ([#548](https://github.com/lcorneliussen/taba/issues/548),
  [#560](https://github.com/lcorneliussen/taba/pull/560)).
