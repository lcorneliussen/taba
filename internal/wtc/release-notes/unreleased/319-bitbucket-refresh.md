## Status

- **Confirm omitted pull requests without hiding old state.** When a complete
  Bitbucket open list omits an enlisted PR, bounded FIFO admission reserves its
  by-number confirmation under the existing request ceiling; ordinary reads may
  spend unreserved tokens. Reservations expire after five minutes without a retry; older clients do not honor them. List absence never
  proves a merge.
- **Show when PR state was observed.** Additive JSON fields `freshness`,
  `observed_at`, and `observation_source` distinguish state observations from
  snapshot age and separate check coverage. Fresh unchanged lists keep list-start
  provenance without changing the full-detail cache file timestamp. Historical
  `settled` and registry `recorded` merges retain their existing authority. Markdown and the live view
  label stale, deferred, and unavailable observations. Such facts cannot authorize
  branch returns, settled merge recording, or next actions. A `pr_unconfirmed`
  branch flag blocks actions when its enlisted PR has no usable state.
- **Keep manual refresh bounded.** Manual refresh, including `r` and `--forge`,
  respects the shared cache and allowance and can report deferred observations.
  ([#319](https://github.com/lcorneliussen/taba/issues/319)).
