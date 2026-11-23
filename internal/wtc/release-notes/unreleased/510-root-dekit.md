## Services

- **A repository's root `dekit.yaml` declares its services.** `taba svc` lists and
  runs its tasks, with the fragments it loads, the same file a bare `dekit up` runs;
  a task path also declared in `.harness/dekit.tasks.yaml` or the collection's own
  file is an error. See `docs/root-dekit-declarations.md`
  ([#510](https://github.com/lcorneliussen/taba/issues/510)).
