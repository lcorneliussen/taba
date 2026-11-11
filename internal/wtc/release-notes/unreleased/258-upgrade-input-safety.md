## Upgrade

- **Protect edits through symlinked inputs.** Upgrade checks every selected
  version/config alias and its tracked target for uncommitted changes before
  writing inputs or refreshing the environment. Clean aliases within the harness
  remain supported; broken, escaping, untracked and non-file targets are refused.
  Existing selector precedence and independent duplicate files are preserved
  ([#258](https://github.com/lcorneliussen/taba/issues/258)).
- **Refuse upgrades during Git operations.** Finish or abort an in-progress
  harness merge, rebase, cherry-pick, revert or sequencer operation before
  upgrading.
