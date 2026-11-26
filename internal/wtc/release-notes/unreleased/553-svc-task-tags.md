## Services

- **`taba svc --json` lists each task's `tags`** as its declaration writes them,
  so a consumer no longer reads `dekit.yaml` and its fragments itself. A
  rendered task shows the tags it was rendered with; tasks rendered earlier
  show them after their next render. A `tags` value that is not a list of
  strings is now refused, as dekit refuses it
  ([#553](https://github.com/lcorneliussen/taba/issues/553)).
