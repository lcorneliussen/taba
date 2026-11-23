## Services

- **A repository's root `dekit.yaml` declares its services.** `taba svc` lists and
  runs the tasks of a plain `dekit.yaml`, with the fragments it loads, under the
  repository, in the collection's runner. A bare `dekit up` runs the same file.
  A task path declared in it and also in `.harness/dekit.tasks.yaml` or the
  collection's own file is an error. Symlinks, `!js` values and loads outside the
  repository are refused with a diagnostic, and nothing runs while the file is read.
  `svc status --json` names each task's declaring file in `declaration`. See
  `docs/root-dekit-declarations.md`.
  ([#510](https://github.com/lcorneliussen/taba/issues/510)).
