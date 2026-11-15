## Status

- **Bound native service status reads.** Runner status and task listing share a
  five-second budget per collection, including returned action snapshots; an
  unavailable task observation reports `unknown` with an error. Action and
  readiness command limits are unchanged.
  This is a partial diagnostic improvement for
  [#284](https://github.com/lcorneliussen/taba/issues/284)
  ([#412](https://github.com/lcorneliussen/taba/pull/412)).
