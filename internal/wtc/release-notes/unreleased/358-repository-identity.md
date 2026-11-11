## Status

- Enlisted PRs recover automatically on new-version scoped status refreshes
  after a repository rename when the provider confirms the same immutable
  repository at both names and in the PR's base repository. Failed identity
  checks leave metadata unchanged and explain how to retry. URL repair
  preserves concurrent branch/title edits and recorded merge facts. Workspace
  status sweeps remain read-only across collection boundaries.
  ([#358](https://github.com/lcorneliussen/taba/issues/358)).
- A positive forge interval can advance rename proof across refreshes and fresh
  status invocations without spending extra tokens or waiting for refills inside
  status. Pending observations expire after five minutes, remain bound to the
  collection and credential context, and require a live completing old-name
  lookup. Failed shared endpoint reads are retried once per refresh. Active PR
  recovery and status detail take priority over settled archive backfill.

## Upgrades

- After the harness upgrade PR merges, run `taba upgrade --rollout` with the
  installed new version. Eligible collections now reload their verified status
  panes through the exact installed mise pin, with automatic installs and dev
  handover disabled for that process. A stale pane PATH cannot pick the old
  binary. Dirty worktrees,
  in-progress operations, busy owners and unrelated pane processes retain
  their existing safeguards. Failed environment or tool installation leaves
  the old status pane running. A pin proposal and release-note rendering do
  not restart panes or repair PR metadata.
