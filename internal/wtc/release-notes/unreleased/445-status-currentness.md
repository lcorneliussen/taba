## Status

- **Current PR facts for merged and declined work.** An enlisted PR whose
  checked-out branch has lost its upstream gets its own detail read instead of
  being served from an open list that may be stale; the gone upstream and a
  head already in the development tip are shown as local evidence only, never
  as a merge. A deferred read reports the newest observation of the PR's
  state, names its reason (shared budget, provider cooldown, cache lock,
  cancellation or deadline), and a deferred Bitbucket decline keeps archive
  treatment by its closure time. Bitbucket merges observed by an explicit
  refresh record their merge time again. `taba status --forge` no longer
  records merges, repairs PR references or returns merged worktrees; only a
  full collector does.
  ([#445](https://github.com/lcorneliussen/taba/issues/445)).
