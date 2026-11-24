## Review

- **Interrupting `review run` stops its reviewers.** Ctrl-C, `SIGTERM` or `SIGHUP`
  now stops every running reviewer with its process group before the command
  exits, removes a public run's temporary directory, and leaves no summary or
  verdict to post. A late write from a process that left its reviewer's group
  cannot become a result of that run or a later one
  ([#487](https://github.com/lcorneliussen/taba/issues/487), [#520](https://github.com/lcorneliussen/taba/pull/520)).
