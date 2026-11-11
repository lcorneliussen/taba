## Status

- Enlisted PRs recover automatically on the first new-version status refresh
  after a repository rename when the provider confirms the same immutable
  repository at both names and in the PR's base repository. Failed identity
  checks leave metadata unchanged and explain how to retry. URL repair
  preserves concurrent branch/title edits and recorded merge facts. Workspace
  status sweeps remain read-only across collection boundaries.
  ([#358](https://github.com/lcorneliussen/taba/issues/358)).

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
