## Collections

- **Git refs are always fully qualified.** Catch-up, status, worktree
  checkout and the gateway's catch-up give Git `refs/heads/<branch>` and
  `refs/remotes/origin/<branch>`, so a tag or branch named like the development
  branch no longer stands in for it. A development ref must be
  `origin/<branch>` or fully qualified: a bare `default_ref: main` now fails
  with its value named, instead of following a local branch that fetches never
  move. A sandbox's catch-up merge commit names `refs/remotes/origin/<base>`.
  ([#497](https://github.com/lcorneliussen/taba/issues/497),
  [#506](https://github.com/lcorneliussen/taba/pull/506)).
