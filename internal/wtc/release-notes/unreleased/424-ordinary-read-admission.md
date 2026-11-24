## Status

- **Keep ordinary Bitbucket reads moving behind confirmations.** A confirmation
  queue at or above the request burst no longer blocks lists, checks, builds and
  PR details indefinitely; limits and cooldowns are unchanged. Deferred PRs never
  observed show unknown checks with the actual refusal reason and retry time.
  ([#424](https://github.com/lcorneliussen/taba/issues/424)).
