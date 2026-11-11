## Status

- Enlisted GitHub PRs recover incrementally on new-version scoped status
  refreshes after a repository rename. One live GraphQL operation must confirm
  equal opaque repository IDs at the old name, current name and PR base, with
  matching canonical names, PR number and URL. Partial/error responses leave
  metadata unchanged. Bitbucket rename mismatches remain fail-closed; automatic
  recovery for that forge is not included.
  ([#358](https://github.com/lcorneliussen/taba/issues/358)).
- Recovery and due live PR detail receive rotating request priority under the
  existing forge allowance. A failed job yields to the next eligible row;
  cache hits, local preflight failure and token/cooldown denial do not advance
  the cursor. There is at most one issued repair operation per forge per scoped
  refresh. No provider identity or completed proof is cached. Active repair rows
  start ahead of history; stored row order, concurrent edits and settled merge
  facts are preserved. Workspace status sweeps remain read-only across
  collection boundaries.

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
