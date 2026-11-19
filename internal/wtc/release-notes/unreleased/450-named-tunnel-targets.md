## Services

- **Named tunnels resolve `port_env` and `url_env` targets.** Since 0.2.5,
  `taba svc tunnel <service> --name <entry>` and `start_with_service` refused
  services whose local URL came from `x-wtc.port_env` or `x-wtc.url_env` with
  "needs an explicit local URL/port"; only a literal `x-wtc.url` worked. They
  now read the collection env the same way `svc up` does, and the error names
  the missing variable. Read-only status and discovery still leave the env
  unread.
  ([#450](https://github.com/lcorneliussen/taba/issues/450)).
