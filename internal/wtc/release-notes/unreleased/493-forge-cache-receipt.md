## Status

- **A lost cache write no longer lets an older PR state back in.** When the
  forge cache's ordering receipt could not be written after the cached PR
  state, or the process stopped between the two, a PR list read before a
  refresh could later replace the refreshed state in `taba status`. The
  receipt is now written first, and a failed receipt leaves the cached state
  as it was.
  ([#493](https://github.com/lcorneliussen/taba/issues/493),
  [#501](https://github.com/lcorneliussen/taba/pull/501)).
