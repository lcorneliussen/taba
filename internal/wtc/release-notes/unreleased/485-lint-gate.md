## Services

- **A named tunnel host whose command is rejected now reports the error.**
  When the host command for a named Cloudflare or devtunnel host could not be
  built, for example because the `cloudflared` path was relative or the
  embedded devtunnel helper was missing, the host dropped the error and
  crashed with a nil-pointer panic. It now returns that error.
  ([#485](https://github.com/lcorneliussen/taba/issues/485),
  [#495](https://github.com/lcorneliussen/taba/pull/495)).
