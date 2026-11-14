# wtc conventions and their taba names

wtc is now taba. A project still depends on names from the wtc days: harness and collection files, user directories, skill names, environment variables, and scripts that call `wtc`. This page lists each of these conventions, the taba name that replaces it, what the current release supports, and how to migrate.

> **Future taba releases drop the wtc names.** 0.2.x still supports them; later releases are planned to stop with upgrade instructions and then ignore wtc names. See [the stages of the rename](https://taba.sh/wtc-is-now-taba/#the-timeline).

Not every convention has a taba name yet. Those are marked "Old name only" below, and `taba doctor` reports them as *waiting* rather than asking you to migrate them.

## Check a project

```sh
taba doctor            # this collection, its harness, and your user directories
taba doctor --all      # every collection in the workspace; also from the workspace root
taba conventions       # this list, from the installed release
```

`doctor` prints each convention it finds once, with its migration step, followed by the places it was found. It checks the harness, the collection, your user directories, and the docs, scripts, configuration and CI tracked in each repository; source code, changelogs and release notes are left out. For old names in files it gives the line numbers. *migrate* means you can move it now. *waiting* means taba has no new name for it yet, or taba moves it itself. *review* marks a file that only mentions wtc in prose: usually that becomes taba too, but a sentence that explains the rename can stay. During 0.2.x, findings never fail `doctor`. `--json` returns the same findings.

## Migrate

Ask an agent in one of your collections:

> Run `taba doctor --all` and finish the migration to taba. Follow the step `doctor` gives for each finding, in the order below, and open a harness PR for the harness changes.

`doctor` tells the agent what to change and where. The order matters, so the agent works through it like this:

1. **Upgrade the harness to the latest 0.2.x** and roll it out, so every collection's pinned CLI reads both names.
2. **Harness files:** rename `wtc.toml` and `.wtc-cli-version` together, plus `wtc-*` skill folders and their base digests, in one harness change. Review, commit and merge it as usual, then roll it out.
3. **Harness references:** replace `WTC_*` with `TABA_*`, `wtc` with `taba`, and old skill names such as `/wtc-start` in hooks, scripts, skills and instructions.
4. **Each collection:** rename `WTC-SCOPE.md` and update `.env.collection.local`.
5. **Your machine:** rename `wtc.env` in the control root and the user directories once no wtc 0.1.x client on the machine uses them.
6. Run `taba doctor --all` again until only *waiting* items remain.

## The conventions

The tables below are generated from the CLI (`taba conventions`). The "In 0.2.x" column says which names the current release reads:
- *Both names; the old one wins if both exist:* taba reads either name, but never combines them. Keep only one.
- *Both names; the new one wins:* environment variables. Generated files set both prefixes.
- *Old name only:* there is no taba name yet.
- *New name written:* taba already writes the new name.
- *Both ship:* both commands are installed and do the same.

<!-- conventions:begin (replace with the output of `taba conventions`; edit internal/wtc/conventions.go) -->

### Harness

| Old | New | In 0.2.x | Written by | `doctor` | Migrate |
|---|---|---|---|---|---|
| `wtc.toml` | `taba.toml` | Both names; the old one wins if both exist | you | yes | `git mv wtc.toml taba.toml` in the harness, then commit and merge it as usual. `taba harness init` already writes `taba.toml`. |
| `.wtc-cli-version` | `.taba-version` | Both names; the old one wins if both exist | you | yes | `git mv .wtc-cli-version .taba-version` in the harness, together with `taba.toml`. A harness with only `.taba-version` seeds new collections with `SCOPE.md`. |
| `overlays/skills/<name>/.wtc-base.sha256` | `.taba-base.sha256` | Both names; the old one wins if both exist | you | yes | `git mv` each digest file to `.taba-base.sha256` in the same folder. |
| `skills/wtc-<name>`, `overlays/skills/wtc-<name>` | `taba-<name>` | Both names; the old one wins if both exist | you | yes | Rename each folder to `taba-<name>` and update its `name:`; section patches and their base digest move with the folder. Until then the old-name override still wins and `taba-<name>` points to it. |
| `hooks/wtc/` | `hooks/taba/` (planned) | Old name only | you | yes | No new name yet (#375). Keep `hooks/wtc/`. |
| `overlays/browse/wtc-browse.lua` | `taba-browse.lua` (planned) | Old name only | you | yes | No new name yet (#375). Keep the old file name. |
| `WTC_*` variables, `wtc …` commands and `wtc-*` skill names in harness files | `TABA_*`, `taba …` and `taba-*` | Variables: both, the new one wins. Commands: both ship. Skill names: the old ones are shims | you | yes | Replace `WTC_X` with `TABA_X`, `wtc` with `taba`, and old skill names such as `/wtc-start` with `/taba-start` in hooks, scripts, skills and instructions, at the lines listed. Other mentions of wtc, such as "a wtc collection" or a link to `wtc-cli`, usually become taba too; a file with only those is marked *review*, and a sentence that explains the rename can stay. Generated environments set both prefixes and ship both commands during 0.2.x. A few variables have no `TABA_` form yet: `WTC_GATEWAY_GIT`, `WTC_RETIRE_*`, `WTC_REVIEW_STATS_FILE`, `WTC_AGENT_ENV` and `WTC_DEV_*` (except `WTC_DEV_BUILD`). |

### Collection

| Old | New | In 0.2.x | Written by | `doctor` | Migrate |
|---|---|---|---|---|---|
| `WTC-SCOPE.md` | `SCOPE.md` | Both names; the old one wins if both exist | you | yes | `mv WTC-SCOPE.md SCOPE.md` at the collection root. The file is local, so nothing to commit. Older harness tooling that reads `WTC-SCOPE.md` needs the same change. |
| `WTC_*` in `.env.collection.local` | `TABA_*` | Both names; the new one wins | you | yes | Rename the variables in `.env.collection.local`. |
| `github:lcorneliussen/wtc-cli` in mise configuration | `github:lcorneliussen/taba` | New name written | you | yes | Run `taba env setup` to regenerate `mise.toml`. In `taba.toml` `[mise.tools]`, rename the key by hand. |
| `.wtc/`, `.wtc-prs`, `.wtc-status.*`, `.wtc-catch-up.*`, `.wtc-reviews/`, `.wtc-review-posted/`, `.wtc-status-watchers/`, `.wtc-review-mode`, `.wtc-dev-mode.json`, `.last-wtc-status.yml` | `.taba…` spellings | Both names; the old one wins if both exist | taba | yes | Nothing to do by hand: taba still writes these names and moves them itself once writes follow the harness (#375). |
| rendered `wtc-*` skills in `.claude/skills` and `.agents/skills` | `taba-*` | New name written | taba | no | Nothing to do by hand: `taba skills render` writes `taba-*`, and each old `wtc-*` name is a short shim that points to it. |

### User and control root

| Old | New | In 0.2.x | Written by | `doctor` | Migrate |
|---|---|---|---|---|---|
| `$WTC_CONFIG_ROOT/wtc.env` | `taba.env` | Both names; the old one wins if both exist | you | yes | `mv wtc.env taba.env` in the control root. Every 0.2.x release reads it. |
| `$XDG_CONFIG_HOME/wtc` | `$XDG_CONFIG_HOME/taba` | Both names; the old one wins if both exist | you | yes | `mv ~/.config/wtc ~/.config/taba` (or under `$XDG_CONFIG_HOME`) once no wtc 0.1.x client uses it. |
| `$XDG_DATA_HOME/wtc` | `$XDG_DATA_HOME/taba` | Both names; the old one wins if both exist | you | yes | `mv ~/.local/share/wtc ~/.local/share/taba` (or under `$XDG_DATA_HOME`) once no wtc 0.1.x client uses it. |

### Repositories

| Old | New | In 0.2.x | Written by | `doctor` | Migrate |
|---|---|---|---|---|---|
| `WTC_*` variables, `wtc …` commands, `wtc-*` skill names and other mentions of wtc in a repository's docs, scripts, configuration and CI | `TABA_*`, `taba …`, `taba-*` and taba | Variables: both, the new one wins. Commands: both ship. Skill names: the old ones are shims | you | yes | Replace them at the lines listed and merge the change through the repository's normal review. Source code is not checked, nor are changelogs and release notes. A file with only other mentions of wtc is marked *review*; a sentence that explains the rename can stay. |
| `x-wtc` in `.harness/dekit.tasks.yaml` | `x-taba` (planned) | Old name only | you | yes | No new name yet (#375). Keep `x-wtc`. |

### Command

| Old | New | In 0.2.x | Written by | `doctor` | Migrate |
|---|---|---|---|---|---|
| `wtc` command | `taba` | Both ship | taba | no | Both commands ship in 0.2.x. Call `taba` in scripts; harness files are covered by the harness-references check. |

### OpenClaw

| Old | New | In 0.2.x | Written by | `doctor` | Migrate |
|---|---|---|---|---|---|
| plugin id `wtc`, package `openclaw-plugin-wtc`, config `plugins.entries.wtc`, gateway state under `wtc/` | `taba` | Old name only | taba | no | Nothing to do yet; `taba doctor --openclaw` will move the gateway config when the plugin is renamed (#375). |

### Internal names

| Old | New | In 0.2.x | Written by | `doctor` | Migrate |
|---|---|---|---|---|---|
| git refs `refs/wtc-catch-up/…`, `wtc-pr-<n>` remotes, review markers `wtc-review v1`, locks `.wtc-runtime.lock`, `.wtc-status-watchers.lock`, `.wtc-prs.lock`, MCP and generated-file markers, temporary files | taba spellings | Old name only | taba | no | Nothing to do: taba reads and moves these itself. Locks keep their names while any supported client uses them. |

<!-- conventions:end -->
