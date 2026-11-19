## Services

- **Named tunnels resolve `port_env` and `url_env` targets.** Since 0.2.5,
  `taba svc tunnel --name` and `start_with_service` accepted only a literal
  `x-wtc.url` and failed with "needs an explicit local URL/port"; they now
  read the collection env like `svc up`, and the error names the missing
  variable.
  ([#450](https://github.com/lcorneliussen/taba/issues/450)).
