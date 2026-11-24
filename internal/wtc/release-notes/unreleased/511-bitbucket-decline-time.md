## Status

- **Declined Bitbucket PRs can take their place in the archive.** With a known
  `twg` credential, a decline is dated by its event in the PR's activity log,
  read once per change, instead of sorting after every dated row. Under a `twg`
  profile or `bb` a decline stays undated; `updated_on` never counts as a
  decline time.
  ([#511](https://github.com/lcorneliussen/taba/issues/511)).
