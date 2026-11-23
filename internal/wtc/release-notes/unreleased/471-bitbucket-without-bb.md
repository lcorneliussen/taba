## Bitbucket

- **Bitbucket without `bb`: TWG is the default client.** Status, review, audience and
  open-review reads go through `twg`; `TABA_BITBUCKET_TRANSPORT=bb` keeps the
  deprecated `bb` client for one more release, and a failure is never retried
  through the other client. Comment, reply, resolve and ready writes use your own
  TWG login, and on a gateway that keeps logins away from commands they are not
  written directly. A host can opt in to `TABA_BITBUCKET_READ_CREDENTIAL=git-credential`:
  each repository's read token then comes from the host's git credential helper and
  reaches only that repository's reads. See `docs/forges.md`.
  ([#471](https://github.com/lcorneliussen/taba/issues/471),
  [#483](https://github.com/lcorneliussen/taba/pull/483)).
