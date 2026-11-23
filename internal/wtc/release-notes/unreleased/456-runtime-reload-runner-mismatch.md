## Runtime

- **Refuse a config change a newer runner would silently ignore.** When the
  pinned dekit has no `reload` command and the running runner is newer, a
  changed service definition used to be reported as applied while the runner
  kept the old one. Rendering now checks the runner first and, unless it
  reports dekit 0.10.0 and its binary path resolves to the pinned file,
  refuses before writing the generated config and asks for `down` then `up`;
  dekit 0.10.0 against its own runner still applies changes live.
  ([#456](https://github.com/lcorneliussen/taba/issues/456),
  [#467](https://github.com/lcorneliussen/taba/pull/467)).
