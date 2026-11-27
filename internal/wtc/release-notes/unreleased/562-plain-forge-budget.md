## Status

- **One plain request budget per forge.** No token stays unspent while a
  Bitbucket read waits, and the Bitbucket open-PR list is reused for 180
  seconds (GitHub keeps 90). Restart status panes still on an older release
  ([#562](https://github.com/lcorneliussen/taba/issues/562),
  [#564](https://github.com/lcorneliussen/taba/pull/564)).
