# Collection directory inputs for the taba rename

This [phase-0](https://github.com/lcorneliussen/taba/issues/201) increment
accepts `.taba/config` for an existing ad-hoc collection and `.taba/dev` for a
collection-local dev build. Check the target release's notes for availability.
There is no automatic migration or need to rename a working collection.

Each namespace is selected independently. An existing `.wtc/config` wins over
`.taba/config`; an existing `.wtc/dev` wins over `.taba/dev`. An unrelated
`.wtc/skills` directory does not hide `.taba/config`. The directories are not
combined. Broken legacy parents or namespaces are not bypassed, and an
incomplete configuration remains a discovery boundary instead of selecting a
parent collection. Discovery and input selection create no files.

Normal discovery, explicit collection selection, early dev-build handover and
the gateway collection helpers share this selection. Ad-hoc registry changes,
hooks, overrides and gateway settings use the selected configuration directory.
Existing `wtc.toml`/`taba.toml` selection still applies inside that directory.
Updating existing user configuration happens in place, as with other
[configuration inputs](rename-config-inputs.md); it does not create a legacy
directory that would hide the selected configuration.

Fresh `init` and `new` collections still use `.wtc/config`. A new ad-hoc sibling
copies the selected source configuration into its fresh legacy destination.
`dev install` still builds `.wtc/dev/wtc`, and generated skills, instructions
and the default collection entry still render into `.wtc`. Rendering recognizes
existing `.taba/skills` links when pruning stale managed links, preserves
hand-authored agent overrides, and uses configuration-owned overrides from
`.taba/config` when selected.

Dev-build lookup retains the executable ownership, mode and version checks;
linked generated parents are rejected. A broken or unsafe legacy dev slot
does not activate the alternate build. `dev uninstall` checks both slots
before changing either and removes both, so an old alternate build cannot
silently become active after removal.

Retirement recognizes generated instruction/skill trees, dev builds and managed
entry links under either spelling. It removes both local ad-hoc registry slots
so a hidden registry cannot reactivate after cleanup. Before removing worktrees,
it refuses linked registry parents or an unowned harness registry that would
keep the collection discoverable. Registry leaf links are removed locally;
their targets, hand-written configuration and other namespaces are retained.
Ad-hoc folders retain their existing reuse policy without a retirement marker.

Runtime, service, review, retirement and watcher state are covered in
[collection state](rename-collection-state.md). Default-name migration and
command packaging remain later phases.
