# Command reference

For complete status representations, column meanings, provider coverage and
examples of how Git, PR and build facts combine, see [Reading local terminal status](status.md).

Use `wtc <command> --help` for flags, or `wtc commands --json` for the machine-readable surface. `harness init`, `docs`, and `release-notes` require v0.1.39 or newer.

Status marks PR merge conflicts against the target branch with `✗ conflict` in
the repository's PR cell and the PR list. The `↓` cell keeps its count with a
conflict marker, such as `✗3`; compact views retain the warning when counts do
not fit. Passing checks or a clean local checkout do not clear a PR conflict.
Local unmerged files are shown separately in the working-tree (`±`) cell.

The terminal table shows **Next actions** from each repository's status; the
live view keeps a compact next-step hint until you press `n`. Choose with
`↑`/`↓`, then press Enter. A step needing no approval can run catch-up for that repository; status is read
again first, and newly dirty or busy checkouts stay with their owner. Steps
needing judgment go to the single author pane labelled `agent` in the owning
collection's herdr workspace, when it reports idle or done. The receipt names
the collection, agent and pane; dispatch is pending completion. Focused review,
status and shell panes are never substitutes. If the workspace or author pane
is missing or ambiguous, or its agent is busy, the view says **Not sent** and
shows the instruction to give it. Plain terminals show the same instruction.
An approved pull request opens in the browser for the person to merge. `Esc` returns to the status table. These are manual actions;
the view does not run them on every refresh.

- `wtc env list` shows variable names, source files, and whether a collection-local entry overrides an earlier one. It opens a scrollable, width-aware view in an interactive terminal; `--no-tui` prints a compact table and `--json` emits structured data. It never prints values. Bare `wtc env` shows help.
- `wtc env setup [--collection DIR] [--dry-run]` regenerates `.env.collection`, preserving its port base. It leaves `.env.collection.local` intact, writes the collection's mise environment file, and trusts the generated mise config when mise is installed. Existing flagged `wtc env` invocations remain supported.
- `wtc doctor` checks the collection registry and local tool availability. Under [OpenClaw](openclaw.md), or with `--openclaw`, it checks the gateway instead: Git 2.42+, `gh`, the wtc plugin and its version, custom Control UI plugins, and that each agent's `AGENTS.md` mentions wtc (`wtc docs openclaw-AGENTS.md` is the section to add). It fails when a required check does.
- `wtc commands --json` lists the command surface for agents.
- `wtc eject 'skills/wtc-*'` copies selected embedded defaults into the harness so they can be customized. Existing files are never overwritten.
- `wtc pr path|list|enlist|unlist` manages the collection-local `.wtc-prs` file used by status and catch-up.
- `wtc registry refresh` regenerates the local bare-owner map and reports registry mismatches.
- `wtc mcp render [--dry-run]` renders the harness MCP registry into Claude, Cursor, and Codex config files for this collection.
- `wtc secrets link [--repo <name>] [--dry-run] [--include-prod]` links gitignored control-root files into checked-out worktrees, preserving displaced local files in collection backups.
- `wtc secrets list [--repo <name>]` inventories control-root file paths and their state in this collection (linked, available, locally overridden, not ignored, or excluded as production-capable). In an interactive terminal it opens a scrollable, width-aware view with selected-row details; use `--no-tui` for a compact table or `--json` for complete metadata. It also identifies the collection-local secret-variable file. It never reads control-root file contents; use `wtc env list` to see variable names without values.
- `wtc agent-env` prints shell exports for sibling mise toolchains and configured collection-local bins; `--write` refreshes `.env.toolchain`, `--print-path` prints its bin list, and `--wrap` handles PreToolUse JSON from stdin.
- `wtc skills render [--seed-scope] [--dry-run]` exposes embedded skills and harness overrides to agent clients, wires the collection entry point and hooks, and refreshes agent shell files. `--all` explicitly renders every collection in the workspace.
- `wtc skills diff [--changes] [--json]` reports skill overrides, H2 section patches, and drift from a recorded base digest.
- `wtc customize` prints the versioned customization guide, including harness hooks and application repository init/teardown hooks.
- `wtc init [directory]` makes an empty folder an ad-hoc collection without a harness repository. Its local registry is `.wtc/config/repos.yml`; generic guidance comes from the embedded defaults. Nothing is cloned until `add-repo`.
- `wtc new [slug] [repo ...]` creates a collection from a slug, issue, tracker key, or GitHub PR head, then prepares its environment and runs repository init hooks. From an ad-hoc collection it creates a sibling ad-hoc folder; named repositories keep their registration, and `--issue` / `--pr` are unavailable. `--pr <repo>#<n> --review` creates a review-only collection for someone else's PR; see `wtc review start`.
- `wtc open [collection ...]` opens or repairs herdr workspaces with agent and status panes. `--add browse` or `--add shell` adds a tool pane on demand, and `--full` (or `WTC_PANES=full`) opens both every time. `--trim` closes idle browse and shell panes, never agent or status. Use `--list` or `--dry-run` to inspect workspaces, `--wide` or `--narrow` to set the layout, and `--all` only when intentionally opening or trimming every collection. `wtc open` and `wtc doctor` warn when the machine is close to its pseudo-terminal cap. `wtc new --open` uses this native command.
- `wtc status` opens the live TUI in an interactive terminal. `--no-watch` prints one pass, with `--silent` suppressing collector progress. `--json`, `--md`, `--ansi`, `--cached`, previews, and piped output remain one-shot. The TUI shows the current step while refreshing; click “refreshing” or press `l` to inspect its refresh log. Repository names, branches, PR numbers, and build references are colored terminal hyperlinks (modifier-click in terminals that use mouse reporting); ordinary clicks also open them unless `--no-click` is set. TEST and PROD columns follow the newest build of each repository's `default_ref` and `production_ref` from GitHub checks or Bitbucket Pipelines, and merged PRs are followed until both tiers pass with the merge in them; `harness/hooks/wtc/status.build.sh` can replace or refine that provider (`wtc customize`). Active PR details use a 90-second forge cache. On first seeing a merge, status records its merge time and final check result in `.wtc-prs` and skips that PR on later forge refreshes; old merged entries appear under the `a` archive toggle after the configured cutoff. `WTC_FORGE_CACHE_AGE` overrides the live cache age.
- `wtc dev install|status|uninstall` builds the collection's wtc-cli checkout into `.wtc/dev/wtc`. Any `wtc` started inside the collection hands over to that build while it is the same version or newer; see [development](development.md). `WTC_DEV_BUILD=off` skips the handover for one command.
- `wtc add-repo <repo> [repo ...]` adds detached worktrees to the current collection, prepares secrets and generated files, and runs each new repository's init hook. `--collection NAME` explicitly selects another collection; ad-hoc collections refuse it. In an ad-hoc collection `add-repo` also accepts a clone URL or a path to an existing checkout, registering the repository locally and per user; `--target <branch>` sets its development branch and PR target (asked interactively otherwise) and `--name` overrides the derived name.
- `wtc openclaw install` installs the OpenClaw plugin embedded in this `wtc` on the gateway: the collection card, the Collection panel and the agents' `wtc` skill ([install](openclaw.md#install)), and reloads it in the running gateway. Running it again after upgrading `wtc` [upgrades](openclaw.md#upgrade) the plugin. `--dir <path>` only writes it.
- `wtc host` prints what `wtc` is running under: `openclaw`, `herdr` or `terminal`. Under [OpenClaw](openclaw.md) `wtc add-repo <repo> --worktree <name>` creates nothing and prints `sessions_spawn` intents for the collection's session (`<repo>` is a gateway project, or a GitHub `owner/name` or URL the gateway clones), `wtc status [<name>]` lists the gateway's collections (its managed worktrees grouped by name), `wtc init` and `wtc new` refuse, and `wtc catch-up [--dest <branch>]` merges the destination branch into the current session's worktree without rebasing or pushing.
- `wtc publish [--title T] [--body B] [--dest BRANCH]` works under OpenClaw only. It checks that the session's managed worktree is safe to publish and prints the intent that does it: the `github_publish` tool on GitHub, a push and pull request through a forge skill elsewhere. It pushes nothing itself, and refuses rewritten published history, unfinished merges, uncommitted files that look like credentials (`--allow-sensitive` overrides), and checkouts the gateway does not manage.
- `wtc checkouts [path ...]` prints branch, commits ahead and behind, pending changes, publication state and pull request of each checkout as `wtc.collection/v1` JSON, for the OpenClaw collection plugin. `--no-pull-requests` skips the forge.
- `wtc svc` (alias `wtc services`) lists, enlists, starts and stops a collection's services, tunnels and resources on an opt-in dekit 0.10 runner independent of agents: `wtc svc add <name> --port-env VAR -- <cmd>`, `up|down|restart [name]`, `logs <name>`, `tunnel <name> <tailscale|cloudflare|ngrok|devtunnel> [--public|--stop]`, `rm <name>`, `dekit -- <args>`, and `render|status|provision|teardown`. `wtc up`, `wtc down`, `wtc restart` and `wtc logs` are shortcuts. A changed task file applies without `wtc down`, and a failed `up` shows the failing task's output. See the [experimental onboarding guide](../internal/wtc/defaults/instructions/runtime.md) and the `wtc-svc` skill; runtime commands require v0.1.40 or newer and remain experimental.
- `wtc retire <collection>` checks for dirty or unpushed work, runs repository teardown hooks, removes the collection's worktrees and generated files, and leaves remote branches intact. From the target's Herdr workspace, `wtc retire .` delegates cleanup to a visible `--cleanup--` workspace. An ad-hoc collection outside Herdr retires itself in place with `wtc retire .`. Use `--force` only after checking that local work is disposable.
- `wtc review start <repo>#<n> [repo ...]` is `wtc new --pr <repo>#<n> --review`: a collection for reviewing someone else's PR without writing to it. Pushing from the PR worktree is disabled in that worktree's own Git config (a dead push URL per remote, a rewrite for other URLs and a refusing `pre-push` hook; other worktrees of the repository are unaffected), catch-up leaves it alone, `review resolve` and `review ready` refuse, and the launch note, the first agent prompt and `wtc status` say review-only. Each `review bundle` there skips catch-up and moves the worktree to the PR's current head, fast-forwarding or following a rewritten branch, so running it again after the author pushes starts the next round; it stops instead of discarding a commit made in the collection.
- `wtc review activity <repo> [pr-number]` inspects current-head Copilot/Cursor reports, active requests/checks and all unresolved hosted threads on GitHub. It never requests review or treats a submitted report as approval.
- `wtc review status <repo> [pr-number] [--trusted-local]` reads the newest local review status comment and checks whether it covers the current PR head.
- `wtc review bundle <repo> [pr-number]` checks out the PR branch and catches up the current collection when a PR is known (a review-only collection instead skips catch-up and follows the PR head; see `wtc review start`), then builds a review bundle with an automatically selected public/private mode. Branch-only reviews use the current local refs. `--no-catch-up` skips the PR catch-up; `--public` forces target-only inputs; `--private` explicitly includes harness context. See [review audiences](review-audiences.md) for defaults, rule ownership, and publication refusal. Public rounds keep prior context from public bundles only. Inspect the exact bundle before giving it to an external reviewer or posting its summary.
- `wtc review run <bundle-dir>` launches separate concern agents, runs a lead agent, and writes a verdict and summary into the bundle. GitHub PR runs defer while Copilot/Cursor are active or have open threads; `--hosted-first=false` explicitly permits overlap. Hosted reports never replace the local pass. Failed or missing local coverage yields `error`; configured agent chains try the next provider on quota limits. `--post` posts a progress comment before the run and updates it with the result; inspect public review output before posting.
- `wtc review post <bundle-dir>` posts or updates the review comment, records a local receipt, and adds deduplicated inline findings. GitHub summaries show a verdict mark and exact-head source links, with secondary sections folded; open findings stay visible. Bitbucket receives plain Markdown.
- `wtc review resolve <bundle-dir>` replies to and resolves inline review threads; without filters, it resolves every unresolved thread in the bundle. It refuses in a review-only collection.
- `wtc review ready <pr-number> [--repo NAME]` promotes a draft only after a current, locally posted passing review (or an explicit user override). It refuses in a review-only collection, override included.
- `wtc browse [collection]` opens the bundled Neovim collection view in the current terminal. From an agent pane it refuses unless `--here` is given; `wtc open --add browse` opens the view in the workspace's browse pane. A harness can replace the view through `overlays/browse/wtc-browse.lua`.


- `wtc harness init <directory> --remote <git-remote> --cli-version <version>` scaffolds six project-owned files, without Git initialization, network access, installs, or hooks. Existing output files are never overwritten; use `--dry-run` to preview.
- `wtc docs [path]` lists or prints the guidance embedded in the installed binary, even outside a collection. Inside a collection, read `.wtc/instructions/` for the effective guidance including harness overrides.
- `wtc upgrade [--version X] [--dry-run]` moves a harness to a newer wtc release: it bumps `.wtc-cli-version` and the `wtc.toml` compatibility range together, refreshes the collection's mise config, runs `mise install`, and prints the new release's notes since the old pin. It commits nothing. `--dry-run` writes nothing and reads the published GitHub release notes. `wtc upgrade --rollout [--dry-run]`, after the harness change merged, is `wtc catch-up --all --harness-only --clean-only --idle-only --install-tools`: collections with a dirty harness, a conflicting branch or a working or blocked herdr agent are reported `needs-owner` and left alone. See [release notes and upgrades](release-notes.md#upgrade-a-harness).
- `wtc release-notes [version|unreleased]` reads embedded Markdown offline, defaulting to the latest numbered release. `--since <old-pin>` reads newer numbered releases in upgrade order; `--list` lists available versions. Use the target binary when preparing a harness upgrade. See [release notes and upgrades](release-notes.md).

In `wtc status --tui`, press `n` for next actions, Space to select a repository,
Right to choose an alternative, or `s` to select visible suggestions. Enter
runs selected catch-up and local steps directly. Publication selections show a
preview before dispatch. Each selected action is
revalidated; stale or blocked targets are reported separately. `c` cancels queued
dispatches. Unchanged handoffs cannot be sent twice in the current view; check
the conversation before using `x` to allow an explicit retry. Agent tasks in one collection are combined into one sequential
handoff; check that conversation for completion. Publication authority is not
broadened by a selection, and follow-up steps require a new selection.

## Retirement readiness

`wtc status` includes a retirement assessment for each selected collection: known
blockers, unverified checks and when the evidence was observed. JSON exposes it
in the `retirement` array; terminal and Markdown views show the summary.

`wtc retire [collection] --dry-run [--transcript <file>] [--json]` reads fresh
evidence and explains what needs verification or preservation. With `--dry-run`,
the collection defaults to the current one. Explicit Claude JSONL transcripts
are untrusted evidence; their availability does not establish archive state.

The dry run performs no status refresh, hooks, handoff or cleanup. Real `retire`
still requires an explicit collection name and retains its existing fresh
execution checks; this slice does not add a new approval or automation policy.
See [retirement readiness](retirement.md) for source coverage and limits.
