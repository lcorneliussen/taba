## Status

- **Keep ordinary Bitbucket reads moving behind confirmations.** A confirmation
  queue at or above the request burst no longer blocks lists, checks, builds and
  PR details indefinitely while waiters keep asking; limits and cooldowns are
  unchanged. Keyed reads take turns in order; unkeyed page reads share one turn,
  and older clients can still delay them. Deferred PRs never observed show
  unknown checks with the actual refusal reason and retry time.
  ([#424](https://github.com/lcorneliussen/taba/issues/424),
  [#521](https://github.com/lcorneliussen/taba/pull/521)).
