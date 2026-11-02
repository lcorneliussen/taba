## Rename

- **Review, retirement and status-watcher state are also read under taba names.** `.taba-review-mode` and `.taba-retired` take effect like their legacy markers, `.taba-reviews/` and `.taba-review-posted/` are read alongside the legacy directories, and retirement stops leases in `.taba-status-watchers/`. Writes and locks keep their legacy names; see [collection state](https://github.com/lcorneliussen/wtc-cli/blob/main/docs/rename-collection-state.md) ([#201](https://github.com/lcorneliussen/wtc-cli/issues/201)).
