## Status

- **Keep workspace sweeps usable when one collection is unreadable.**
  `status --all` now reports every readable collection and lists the ones whose
  context cannot be opened with a fixed, sanitized reason, then exits nonzero;
  receiving-policy guards stay fail-closed and catch-up keeps its own refusals.
  ([#459](https://github.com/lcorneliussen/taba/issues/459)).
