## Runtime

- **Refuse a config change a newer runner would silently ignore.** When the
  pinned dekit has no `reload` command and the running runner is newer, a
  changed service definition used to be reported as applied while the runner
  kept the old one. Rendering now checks the runner first and, unless it
  reports dekit 0.10.0 running the pinned binary, changes nothing and asks for
  `down` then `up`; dekit 0.10.0 against its own runner still applies changes
  live.
  ([#456](https://github.com/lcorneliussen/taba/issues/456)).
