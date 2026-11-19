## Collections

- **Faster collection creation.** `taba new` now fetches the existing shared
  owners of a collection together instead of one after another, then checks
  the repositories out in the usual order; a fetch failure is still reported
  for the repository it belongs to. Opening a new workspace with tool panes
  settles all of its shells once instead of once more per added pane
  ([#449](https://github.com/lcorneliussen/taba/issues/449)).
