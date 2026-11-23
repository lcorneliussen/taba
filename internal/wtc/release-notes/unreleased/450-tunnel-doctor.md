## Services

- **`taba doctor` checks named tunnels.** For each configured tunnel it
  reports whether the service and its local target, the repository and the
  provider inputs resolve, with a fix for each problem, such as "declare
  `x-wtc.port_env` or `x-wtc.url` on `widget/web`". The check is local and
  advisory: no provider, account or network access, no env sourcing, and it
  never fails doctor. See `docs/tunnel-doctor.md`.
  ([#450](https://github.com/lcorneliussen/taba/issues/450)).
