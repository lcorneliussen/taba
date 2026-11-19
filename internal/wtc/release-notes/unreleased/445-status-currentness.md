## Status

- **Current PR facts for merged and declined work.** An enlisted PR whose
  checked-out branch has lost its upstream gets its own detail read instead of
  being served from an open list that may be stale; the gone upstream and a
  head already in the development tip are shown as local evidence only, never
  as a merge. A deferred read reports the newest observation of the PR's
  state, names its reason (shared budget, provider cooldown, cache lock,
  cancellation or deadline), and a deferred Bitbucket decline keeps archive
  treatment by its closure time. Every status path that observes a Bitbucket
  merge without a merge time reads it from the activity log, under the shared
  allowance, so the merge is recorded again. `taba status --forge` and the
  status view's check before running a next step no longer record merges,
  repair PR references or return merged worktrees; only a full collector does.
  ([#445](https://github.com/lcorneliussen/taba/issues/445)).
