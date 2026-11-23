## Upgrades

- **Upgrades run the new release's doctor.** After `taba upgrade` writes and
  installs a new pin, and after `taba upgrade --rollout` updates and installs
  a collection's harness, the installed target release runs `doctor --json` in
  that collection, with its own environment and dev handover off. Its findings
  are printed as advice (`doctor` in the JSON result, a `doctor` row per
  collection in the rollout report) and never undo or fail the upgrade. A dry
  run only plans it; a refused upgrade, failed install, skipped or current
  collection runs nothing; a target that is not installed, reports another
  version or has no doctor is reported `unavailable`, not as a pass. An
  ordinary catch-up or a hand-merged pin does not run it: run `taba doctor`
  with the new release yourself
  ([#450](https://github.com/lcorneliussen/taba/issues/450)).
