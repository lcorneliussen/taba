# Release notes and harness upgrades

`wtc release-notes` is available from v0.1.39.

Release notes ship inside the CLI, so people and agents can read them offline
from any directory. The archive includes published versions from 0.1.0 onward.
Historical notes describe the commands and limitations at the time of release;
read the complete upgrade range for later corrections.

```sh
wtc release-notes                  # latest embedded numbered release
wtc release-notes 0.1.38           # one release; v0.1.38 also works
wtc release-notes --since 0.1.35   # newer numbered releases, oldest first
wtc release-notes unreleased      # upcoming changes, explicitly separate
wtc release-notes --list           # available versions
```

`--json` returns the usual envelope with `data` as an array of documents
(`version`, `content`); `--list --json` returns an array of version strings.
An empty upgrade range returns an empty array. `--since` excludes the baseline
and unreleased changes. It cannot be combined with a version or `--list`.

## Upgrade a harness

`wtc upgrade`, run in a collection of the harness, does the mechanical part: it
resolves the latest release (or `--version`), bumps `.wtc-cli-version` and the
`wtc.toml` compatibility range together, refreshes the collection's mise
config, installs the release, and prints that release's notes since the old
pin. `--dry-run` writes nothing and reads the published GitHub release notes
instead. It commits nothing; the rest of this section is the review it leaves
to you. After the harness PR merges, `wtc upgrade --rollout` catches every
collection's harness up: clean worktrees and idle agents only, with `mise
install`, reporting the collections left for their owners.

The [rename configuration-input groundwork](rename-config-inputs.md) also
accepts `.taba-version` and `taba.toml`. When only those names exist, upgrade
updates them in place and applies dirty-file protection to those same files.
Existing legacy names take precedence when both are present.

Without the command, read the committed `.wtc-cli-version` and the target
release. Run the **target binary** with `release-notes --since <old-pin>`: an
old binary only contains its own archive and cannot fetch future notes.
Download the published target binary without changing the harness pin first.
For a binary that predates this command, read its [GitHub release
notes](https://github.com/lcorneliussen/taba/releases).

Use the notes to decide which features need local adoption and which contract
changes need tests. Compare local skills and instructions with the target's
defaults, including `wtc skills diff`. Update the CLI pin and compatibility
range together, regenerate the collection environment with `wtc env setup`,
run `mise install`, and re-render skills and MCP configuration. Preserve project
hooks, policy, and overlays; exercise changed behavior in a disposable collection.

The shipped `wtc-harness-upgrade` skill walks through the full procedure. Record
the version range, adoption decisions, and validation in the harness's upgrade
PR. Keep private adoption relationships in private records.

## Author a release

The source of truth is `internal/wtc/release-notes/<version>.md`. These files are
embedded directly; they are also passed to GoReleaser as the release body.

1. **Every change adds its own fragment.** A pull request that changes behaviour
   adds `internal/wtc/release-notes/unreleased/<issue>-<slug>.md` and never edits
   another change's fragment, so branches in flight do not conflict over
   release notes. A fragment holds `## <topic>` sections, each with at least
   one entry; the format is in that directory's `README.md`. `wtc release-notes
   unreleased` shows them assembled: sections with the same heading are merged,
   in issue order.
2. Before tagging, cut the release from the repository root:

   ```sh
   go run ./tools/release-notes cut <version>
   ```

   This writes `<version>.md`, headed `# taba/wtc <version>` (without `v`;
   `# taba <version>` from 0.3.0), from the fragments and deletes them.
   Commit both in one change.
3. Edit `<version>.md` into the release notes: order and merge the topics,
   add a summary, compatibility changes, harness adoption steps, and relevant
   limitations. Use generic public examples.
4. Run the Go suite and vet. Inspect the exact notes, diff, and release payload
   for public safety. Commit the numbered notes **before** creating the tag.
5. Follow the repository's normal review and release authorization procedure.

The tag workflow rejects missing or mismatched notes, and fragments left in
`unreleased/`, before publishing binaries.
It publishes the same file that is embedded in the tagged binary. Fix future
guidance in source rather than editing only the GitHub body: already downloaded
binaries keep their original notes. Historical files here were imported from
the published release bodies, with their top-level headings normalized.
