## Status

- **New Bitbucket PRs are read promptly, and known facts stay put.** A PR no
  view has observed gets a bounded first read on the shared request budget,
  even behind panes from older releases, and shows `wait` with its reason
  rather than `never`; the view retries it without a keypress. Merged PRs keep
  their merge time and archive place when a later read is deferred. Where
  status cannot verify the credential context (such as a TWG profile kept in
  TWG's own store), earlier observations are still not reused, and the row
  says so.
  ([#548](https://github.com/lcorneliussen/taba/issues/548),
  [#560](https://github.com/lcorneliussen/taba/pull/560)).
