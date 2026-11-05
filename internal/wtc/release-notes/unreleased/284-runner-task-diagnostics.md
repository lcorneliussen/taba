## Fixed

- Runtime status shows task state as checking, with an explicit diagnostic, when
  the runner reports starting before task discovery is available. Observed task
  startup, internal runtime controls and service processes are unchanged.
  ([#284](https://github.com/lcorneliussen/wtc-cli/issues/284)).
