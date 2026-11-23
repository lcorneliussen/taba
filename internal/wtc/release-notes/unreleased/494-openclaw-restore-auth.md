## OpenClaw

- **Restore evicted checkouts as the person who asked.** Collection **Restore**
  and **Restore all** now use the native restore under the requester's own
  gateway admin permission, instead of a gateway-host command that failed with
  `unauthorized` when that host held no credential of its own. Failures name
  the cause: missing capability, permission denied, connection authentication
  or unavailable snapshot. An unclear result is checked against the native
  record and never retried automatically. Install the matching CLI and plugin
  with `taba openclaw install` and reload open Control UI tabs.
  ([#494](https://github.com/lcorneliussen/taba/issues/494)).
