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
- **Bitbucket read refusals say what was observed.** A refused read names the HTTP
  status Bitbucket answered with possible explanations instead of asserting an expired
  token or a missing scope; TLS failures and Bitbucket server errors are reported as
  their own causes, and REST errors no longer echo a response body. Gateway pull
  request news announces a refusal once per read identity (a non-secret digest), so a
  rotated or switched credential is announced again. `docs/forges.md` now states that
  the host's git credential helper is a trusted-process mechanism: any command on the
  host that can reach it gets the token, and taba enforces no execution boundary.
  ([#471](https://github.com/lcorneliussen/taba/issues/471))
