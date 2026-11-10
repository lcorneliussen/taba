## Rename

- **Skills are named `taba-*`, and their examples use `taba`.** Each old `wtc-<name>` stays as a short shim that sends the agent to `taba-<name>`, until 0.3. A harness override under an old name (`harness/skills/wtc-pr`, `overlays/skills/wtc-pr`) still wins, and `taba-pr` then points to it. Section patches under an old name apply to the renamed skill. Rename the override folder to `taba-<name>` to finish the move. A patched skill reports base drift, as it does for any upstream skill change, so review and record the new base
  ([#351](https://github.com/lcorneliussen/taba/issues/351)).
- **`taba open` starts `taba status --tui` and `taba browse`** in collections pinned at 0.2.0 or later, and keeps `wtc` for older pins. A new collection's agent starts with `/taba-start` once its skills have been rendered
  ([#351](https://github.com/lcorneliussen/taba/issues/351)).
- **`taba harness init` writes `.taba-version` and `taba.toml`.** A harness that has `.taba-version` and no `.wtc-cli-version` seeds new collections with `SCOPE.md`. Other harnesses keep seeding `WTC-SCOPE.md`, because their own tooling may read that name. Existing scope files are never moved. To move an existing harness: `git mv .wtc-cli-version .taba-version && git mv wtc.toml taba.toml`
  ([#351](https://github.com/lcorneliussen/taba/issues/351)).
