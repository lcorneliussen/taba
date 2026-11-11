# Rehearsing a harness upgrade

`tools/upgrade-rehearsal/rehearse.sh` shows how one harness behaves when its collections move to another CLI release ([#332](https://github.com/lcorneliussen/taba/issues/332)). It is meant for the taba rename: run it against each harness on a `v0.2.0-rc.N` pre-release after the repository rename, before the final tag.

```sh
tools/upgrade-rehearsal/rehearse.sh \
  --harness git@github.com:example-org/agent-harness.git \
  --to 0.2.0-rc.1 --cli-checkout --registry-followup
```

## What it does

1. Creates a throwaway workspace (`--work`, default a new temporary directory). Its config root and mise data, cache, state and config live inside it; `WTC_*`, `TABA_*` and `MISE_*` variables from the calling shell are cleared.
2. Clones the harness bare from its remote and creates one collection, `rehearsal`, on the harness's current pin (`.wtc-cli-version`). `--from` pins another release first, committed on a local `rehearsal-pin` branch of the throwaway clone so `upgrade` accepts it; nothing is pushed. `--repos a,b` adds registry repositories.
3. With `--cli-checkout`, adds an unmanaged `ext.wtc-cli` sibling whose owner is a clone outside `.bare/`, cloned from the old CLI URL. This is how some collections carry the CLI next to their own repositories.
4. Records the collection: versions, environment variable names (never values), the generated `mise.toml` and MCP files, rendered skills (files and links into the harness), sibling origins, `status --json` with volatile fields masked, and `doctor`.
5. Runs `upgrade --dry-run` and `upgrade --version <to>` with the pinned binary. A pin older than the `upgrade` command is moved by the new release instead.
6. Puts the new release on `PATH`, checks that `wtc` and `taba` both run (from 0.2.0), catches the collection up, and records it again.
7. With `--registry-followup`, renames the harness's `wtc-cli` registry entry to `taba` with `aliases: [wtc-cli]` and points that entry's remote at `taba`; other entries are left alone. Then it catches up again and records a third time.
8. Lists hard-coded references to the CLI repository and to `wtc_<version>` release archives in the harness, and counts its `WTC_*` variables.

Releases are installed from, and `ext.wtc-cli` is cloned from, `--cli-repo` (default `lcorneliussen/wtc-cli`, the name 0.1.x pins use); the pin file holds only a version. `--work DIR` sets the work directory, which must not exist or be empty. `--help` lists every option.

`report.md` in the work directory has the step results, the diffs between the recorded states, and the harness references. Logs for every step are under `logs/`. The exit status is 1 when a step failed or the rehearsal stopped early, for example on a repository without `.harness-repos.yml`; the report then lists the steps that ran.

## What to look for

- **A failed step.** Its log has the command output.
- **Release archive names.** From 0.2.0 archives are `taba_<version>_<os>_<arch>.tar.gz`. A workflow or hook that downloads `wtc_<version>_…` breaks, even though the repository path is redirected.
- **`ext.wtc-cli`.** Its origin should be unchanged and still fetch through the rename redirect; catch-up never rewrites an unmanaged checkout's remote.
- **The follow-up.** Renaming the registry entry should not clone a second owner, move a folder or drop ports.
- **`WTC_*` variables** keep working in 0.2, since both prefixes are generated. They need changing only before the old names are dropped.

## Boundaries

Nothing outside the work directory is written: no pushes or forge writes, and the only commit is the local `--from` pin. Forge reads (clone, fetch, release lookups) use the caller's git and `gh` authentication. The harness's own hooks run, as they would in a real collection, so rehearse only harnesses whose code you would run anyway. Secrets are not linked: the throwaway config root is empty, so a step that needs a credential fails without it instead of using the real one.

Reports name the harness and its files. Keep reports for private harnesses out of public issues; file what they find as generic cases.
