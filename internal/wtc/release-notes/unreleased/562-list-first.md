## Status

- **The open-PR list is the only routine read.** Open PRs take their state
  from their repository's shared list and are no longer read by number every
  10 minutes. A PR that leaves the list is read once; a closed or merged PR is
  not read again. Passed or failed Bitbucket checks of an unchanged head are
  kept. `r` reads the pane's lists again instead of every PR.
  `WTC_FORGE_FULL_REFRESH` and `incremental-misses.log` are gone
  ([#562](https://github.com/lcorneliussen/taba/issues/562),
  [#566](https://github.com/lcorneliussen/taba/pull/566)).
