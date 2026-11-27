## Status

- **Bitbucket declines are read once.** Status records a declined Bitbucket PR
  in `.wtc-prs` and stops reading it in background refreshes; it shows as open
  again, and the record is dropped, once the repository's open list carries it.
  A PR that left the open list is no longer read a second time by another pane.
  ([#562](https://github.com/lcorneliussen/taba/issues/562),
  [#548](https://github.com/lcorneliussen/taba/issues/548)).
