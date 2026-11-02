## Rename

- **Runtime and local service state are also read under `.taba/`.** A collection selects one home for `runtime` and `svc` together. `.wtc` wins when it holds state, a fresh collection still uses `.wtc`, and state under both is refused rather than risking a second runner or hidden services. `.wtc-runtime.lock` stays the shared lock; see [collection state](https://github.com/lcorneliussen/wtc-cli/blob/main/docs/rename-collection-state.md) ([#201](https://github.com/lcorneliussen/wtc-cli/issues/201)).
