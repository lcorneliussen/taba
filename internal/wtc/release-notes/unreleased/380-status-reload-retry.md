## Status

- **Retry status reload on transient foreground lookup errors.** Reload keeps its bounded waits and requires a verified shell before sending a command and an observed status process before reporting success. Exhausted waits suggest inspecting the pane before retrying.
  ([#380](https://github.com/lcorneliussen/taba/issues/380), [#398](https://github.com/lcorneliussen/taba/pull/398)).
