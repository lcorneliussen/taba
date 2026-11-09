# Customize a worktree collection

Run `wtc customize` to read this guide from the installed CLI release. Keep
collection-specific choices in the harness repository and application setup in
each application repository. Generated collection-root files are disposable.

## Harness configuration

The harness checks in `.harness-repos.yml`, `.wtc-cli-version`, and `wtc.toml`.
The registry names repos, remotes, default refs, and optional port offsets.
`wtc.toml` declares CLI compatibility and extra collection-root mise tools.
For example, the tool entries can be:

```toml
[mise.tools]
bun = "1.2.3"
"npm:example-cli" = "4.5.6"
```

For collection-local commands outside mise, list existing directories relative
to the collection root. `wtc agent-env` puts these paths before sibling mise
bins, skips absent directories, and refreshes `.env.toolchain` when `wtc.toml`
changes:

```toml
[agent_env]
prepend_paths = ["tools/bin"]
```

The committed `.wtc-cli-version` supplies the exact `wtc` version. Do not add
`github:lcorneliussen/taba` to `[mise.tools]`. `wtc env setup --dry-run` previews
`.env.collection` and `mise.toml`; `wtc env setup` regenerates them. Put local values
and credentials in `.env.collection.local`, which the generator preserves.

## Harness hooks

Executable scripts in `harness/hooks/wtc/` run from the collection root. The
CLI sends one JSON object on stdin with `event`, `collection`, `harness`,
`workspace`, and `values`. It also sets `WTC_COLLECTION` and
`WTC_CONFIG_ROOT`, plus the same values as `TABA_COLLECTION` and
`TABA_CONFIG_ROOT`. `WTC_COLLECTION` is the collection name; the JSON
`collection` field is its absolute directory. Current events are:

| Action | Before | After |
|---|---|---|
| `wtc env setup` | `env.pre.sh` | `env.post.sh` |
| `wtc pr enlist` | `pr.enlist.pre.sh` | `pr.enlist.post.sh` |
| `wtc pr unlist` | `pr.unlist.pre.sh` | `pr.unlist.post.sh` |
| `wtc registry refresh` | `registry.refresh.pre.sh` | `registry.refresh.post.sh` |
| `wtc mcp render` | `mcp.render.pre.sh` | `mcp.render.post.sh` |
| `wtc secrets link` | `secrets.link.pre.sh` | `secrets.link.post.sh` |
| `wtc skills render` | `skills.render.pre.sh` | `skills.render.post.sh` |
| `wtc new` | `new.pre.sh` | `new.post.sh` |
| `wtc add-repo` | `add-repo.pre.sh` | `add-repo.post.sh` |
| `wtc retire` | `retire.pre.sh` | `retire.post.sh` |

`wtc new` and `wtc add-repo` also run `env.pre.sh` and `env.post.sh`
around their environment generation, before secrets linking and repository
initialization. Environment aliases added by a post-hook are therefore
available to each repository's init hook.

A pre-hook can stop the action by exiting nonzero. A post-hook failure is
reported as a warning; the completed action remains complete. Dry runs and
read-only commands do not run hooks. Hooks should be idempotent and avoid
writing to other collections. For example, a harness may use `env.post.sh`
to regenerate a local tool configuration after the collection env changes.

### Status build facts

`wtc status` follows builds for every registered repository out of the box.
Each repository has up to two tiers. The **test** tier (`tip`) is the
`default_ref` branch. The **prod** tier (`prod`) is `production_ref`, or
`release_ref` if that is what the registry sets. When `production_ref` names
the `default_ref` branch itself, production deploys from the development tip
and that branch is reported as `prod` only.

The built-in provider asks the forge about the newest build of each tier's
branch:

- **GitHub**: the check rollup of the branch's head commit, linked to its
  Actions run, through `gh api graphql`.
- **Bitbucket**: the newest branch pipeline, through the Bitbucket REST API.
  Credentials come from `BB_API_USER`/`BB_API_PASS`, `BB_USERNAME`/`BB_API_TOKEN`
  or `ATLASSIAN_ACCOUNT_EMAIL`/`ATLASSIAN_API_TOKEN`, and otherwise from the
  `bb` CLI login in `~/.config/bb/config.json`.

Every build carries the commit it ran on. Status uses that commit twice:

- A repository row shows a build's cell as `○` (dimmed) when the row's HEAD
  is not contained in that build, for example on a feature branch.
- A merged PR is **followed** until each tier has a passing build that
  contains its merge commit. The follow starts at the tier that builds the
  PR's base branch, so a hotfix merged into the production branch waits on
  prod only, and a PR merged into any other branch (a stacked PR, a release
  branch no tier builds) is not followed. The PR table's state column shows the tier it
  waits on: `→ test` (no build with the merge yet), `● test` (running),
  `✗ prod` (failed), `? test` (the build cannot be attributed), then
  `✓ built`. Merge commits are recorded in `.wtc-prs` with the final merge
  facts; a merge recorded by an older wtc without its commit shows as plainly
  `merged`. Archived PRs are not followed.

A provider failure marks only its own cell as an error (`✗`, with the message
in JSON, Markdown and the refresh log); the rest of the refresh is kept. The
TUI shows a TEST or PROD column only when some repository has a build in that
tier, so a repository without CI shows no build columns.

#### Customizing with `status.build.sh`

An executable `harness/hooks/wtc/status.build.sh` replaces the built-in
provider for the whole harness. It is a read-only data provider, not a
lifecycle hook, called once per repository and tier, with up to four calls
running at a time. It receives JSON on stdin:

```json
{"collection":"demo","repo":"widget","worktree":"/path/to/demo/widget","slug":"example/widget","forge":"github.com","branch":"main","tier":"tip"}
```

Return one JSON object with `checks` (`SUCCESS`, `FAILURE`, `PENDING`,
`NONE`, `ERROR` or `EXPECTED`), and optionally `build`, `url`, `sha` (the
commit the build ran on, 7 to 64 hex characters) and the same `branch`:

```json
{"checks":"SUCCESS","build":"123","url":"https://example.invalid/build/123","sha":"0123abc"}
```

- Without `sha`, rows and merged PRs cannot be attributed to the build: rows
  keep the plain cell, and a followed PR shows `?`.
- `{"use_default":true}` hands this one request to the built-in provider, so
  a hook can customize some repositories or tiers and keep the default for
  the rest.
- An empty response or `null` means there is no build to show.

Keep the provider fast and safe to call during watched refreshes. The CLI
stops a call after 15 seconds, rejects invalid JSON, a different branch, a
non-HTTP(S) URL or a malformed commit, and never infers a passing build when
the provider cannot answer. Clicking a build cell opens its URL unless
`--no-click` is set.

### Browse view

`wtc browse` ships its Neovim collection view inside the CLI release. To
replace that view for one harness, check in
`harness/overlays/browse/wtc-browse.lua`. The command loads the overlay in
place of the bundled view. Keep the file workspace-agnostic: the selected
collection path is available in `vim.g.wtc_browse_root`.

## Hooks in an application repository

The existing collection lifecycle runs an `init` hook when it creates or adds
a repository worktree, and a `teardown` hook before removing it. Each
application repository can check in either a `mise.toml` task named
`harness:init` / `harness:teardown`, or executable `.harness/init.sh` /
`.harness/teardown.sh` files. The mise task takes precedence when mise is
available. Hooks run with cwd set to that repository worktree. They can read
the collection env through mise, or source `../.env.collection` directly.

```toml
[tasks."harness:init"]
run = "./.harness/init.sh"

[tasks."harness:teardown"]
run = "./.harness/teardown.sh"
```

`wtc add-repo` links the new repository's gitignored control-root files before
running init, then refreshes skills and MCP configuration. The hook can use
those files immediately. Make init safe to repeat: a repo can be added after
collection creation, and catch-up may repeat setup. Check whether a sibling
exists before using it.
Keep teardown limited to resources owned by that worktree. The harness warns
and continues if a repository hook fails, so the hook should report incomplete
setup clearly on stderr. To link control-root files, call
`wtc secrets link --repo <name>` rather
than making links independently. The command refuses a target without a git
ignore rule and backs up an existing regular file before replacing it.

Production-capable files stay out of worktrees unless `--include-prod` is
explicit. Declare their paths relative to each repository in its registry
entry's `prod_paths`, or use `[secrets] prod_paths` in `wtc.toml` with
`repo/path` entries:

```toml
[secrets]
prod_paths = ["api/.env.production"]
```

## Skills and instructions

The CLI embeds generic `wtc-*` skills and instructions. `wtc skills render`
materializes embedded skills and instruction defaults in the disposable
collection, links skills into
`.claude/skills` and `.agents/skills`, and wires `AGENTS.md`, agent hooks,
`.envrc`, and `.env.toolchain`. `--seed-scope` creates `WTC-SCOPE.md` from the
harness template only when neither it nor `SCOPE.md` exists. Existing legacy
scope takes precedence; otherwise read and edit `SCOPE.md` without creating
a second file. `--dry-run` previews changes, and
`--all` explicitly applies them to every collection in the workspace.

Effective instruction files live under `.wtc/instructions/`. A matching file
under `harness/instructions/` is linked there instead of the embedded default.
Edit that tracked source, not the generated copy. The default collection entry
point uses these effective paths; authored harness entry points stay unchanged.
`wtc docs [path]` reads the installed binary's defaults from any directory,
without applying project overrides.

For a skill with local wording, check in `harness/skills/<name>/SKILL.md`;
it replaces the embedded default. A file at
`harness/overlays/skills/<name>/SKILL.md` takes precedence over both. New
skills in either directory are linked as well. A real directory already at an
agent client's skill destination is a local override and is left alone. The
renderer prunes only stale symlinks it owns.

To replace only one H2 section, put a complete section (starting with its
exact `## Heading` and ending with a newline) in
`harness/overlays/skills/<name>/sections/<short-name>.md`. The heading must
occur exactly once in the base skill, and the patch cannot introduce another
H1 or H2. The base is `harness/skills/<name>/SKILL.md` when present, otherwise
the CLI's embedded skill. A full overlay `SKILL.md` and section patches for
the same skill are mutually exclusive. `wtc skills render --dry-run` validates
patches without writing them; rendering materializes the assembled skill in
the collection's disposable `.wtc/skills/` directory.

Use `wtc skills diff --changes` to review local skill changes against the
installed CLI. It prints the current base SHA-256 for untracked or drifted
overrides. Record that digest in
`harness/overlays/skills/<name>/.wtc-base.sha256` after reviewing a section
patch. If the base later changes, render stops and diff reports `drifted`;
review the patch against the new base and update the digest. A full skill
override can use the same sidecar file for drift reporting. Without a sidecar,
diff reports `untracked`. `--json` provides the same findings for automation.

`wtc eject 'skills/wtc-customize'` or another selected path copies an
embedded default into the harness for editing; eject refuses to overwrite an
existing file. Keep repository-specific agent instructions in that
repository's `AGENTS.md` and hooks in its `.harness/` directory.
