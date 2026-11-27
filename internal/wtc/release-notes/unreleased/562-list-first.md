## Status

- **The open-PR list is the only routine read.** A PR on its repository's list
  takes its state from the list, so open PRs are no longer read by number
  every 10 minutes. A PR that leaves the list is read once, and a closed or
  merged one is not read again; a merge with checks still running is read
  once per list until they settle. Passed or failed Bitbucket checks of an
  unchanged head are kept instead of read every 90 seconds. `r` reads the
  pane's lists again and rechecks closed PRs, Bitbucket checks included,
  instead of every PR. A PR beyond a capped list is shown deferred
  (`list-truncated`) rather than read by number. `WTC_FORGE_FULL_REFRESH` and
  `incremental-misses.log` are gone
  ([#562](https://github.com/lcorneliussen/taba/issues/562)).
