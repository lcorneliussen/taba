## Secrets

- **Resolve control roots from receiving policy.** Project members and standalone
  collections use their own `.config` defaults; external sharing requires an exact
  receiving declaration. Remove stale root assignments or explicitly opt in
  before regenerating env. Discovery and launch checks enforce project boundaries,
  and retirement preserves nonempty private control data. Provider credential
  isolation and global-store locking remain separate.
  ([#376](https://github.com/lcorneliussen/taba/issues/376),
  [#395](https://github.com/lcorneliussen/taba/pull/395)).
