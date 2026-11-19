## Status

- **Current PR facts for merged and declined work.** When the open list is
  served stale under a spent allowance, an enlisted PR whose checked-out
  branch has lost its upstream is confirmed by its own detail read (on
  Bitbucket in the existing confirmation queue) instead of staying listed as
  open; the gone upstream and a head already in the development tip are shown
  as local evidence only, never as a merge. A deferred read reports the newest observation of the PR's
  state, names its reason (shared budget, provider cooldown, cache lock,
  cancellation or deadline), and a deferred Bitbucket decline keeps archive
  treatment by its closure time. Every status path that observes a Bitbucket
  merge without a merge time reads it from the activity log, under the shared
  allowance, so the merge is recorded again. `taba status --forge` and the
  status view's check before running a next step no longer record merges,
  repair PR references or return merged worktrees; scoped one-shot, periodic
  and explicit refreshes still do.
  ([#445](https://github.com/lcorneliussen/taba/issues/445)).
