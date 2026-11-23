## Services

- **A repository's root `dekit.yaml` declares its services.** `taba svc` lists and
  runs its tasks, with the fragments it loads, the same file a bare `dekit up` runs;
  a task path also declared in `.harness/dekit.tasks.yaml` or the collection's own
  file is an error. `[runtime] declarations` and `health_path` (or
  `TABA_RUNTIME_DECLARATIONS` and `TABA_RUNTIME_HEALTH_PATH`) choose which files
  declare services and a URL path such as `/health` for readiness. See
  `docs/root-dekit-declarations.md`
  ([#510](https://github.com/lcorneliussen/taba/issues/510)).
