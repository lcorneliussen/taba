## Status

- **A known closure survives a deferred refresh.** A closed or declined PR no
  longer looks open, showing only its refresh age (such as `d:7m`), when a later
  refresh is deferred, stale or failed: it keeps its `closed`/`declined` label
  and strikethrough, with the age before the title, until a read sees it open.
  ([#545](https://github.com/lcorneliussen/taba/issues/545)).
