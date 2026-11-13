# Lifecycle hooks and collection env

Collections have a lifecycle git knows nothing about: worktrees get created
(`taba new`, `taba add-repo`) and torn down (`taba retire`). Repos can hook
into that, and every collection carries a small shared environment (ports
etc.) that all siblings inherit.

Design principle: **no tool is load-bearing.** [mise](https://mise.jdx.dev)
is the recommended runner (it also pins toolchains and layers env), but
everything works with bare git + bash; mise only makes it nicer.

## Hook contract

When a repo's worktree is created, the harness tools run its **init** hook;
before it is removed, its **teardown** hook. Resolution order per repo:

1. The repo's checked-in `mise.toml` defines a task named `harness:init` /
   `harness:teardown` **and** mise is installed → `mise run harness:<hook>`.
2. An executable `.harness/init.sh` / `.harness/teardown.sh` exists in the
   repo → run it (cwd = the worktree).
3. Neither → no-op.

Because hooks are checked into each repo, a repo brought into a collection
later (via `taba add-repo`) carries its own setup with it. Hooks must be
idempotent and must not assume which siblings exist — check
`../<repo>`/`$<REPO>_PORT` and degrade gracefully.

**A hook must not fail its caller.** Report what went wrong on stderr and
exit 0; a nonzero exit from work that merely did not complete is not worth
aborting a collection over. The harness enforces this from its side too —
`run_hook` warns and continues rather than propagating the status — but hooks
should not lean on that: an older harness will still abort, and a hook that
exits nonzero because a *sub-tool* it called failed (a linker refusing a bad
path, say) is reporting an operator-fixable condition, not a reason to leave
a half-built collection on disk.

Typical init-hook work: restore dependencies, template a local `.env` from
the collection env, link secrets from the machine's control root
(`$WTC_CONFIG_ROOT` — see `secrets.md`). Typical teardown: stop
containers, deregister local services. Keep hooks fast.

For secrets, hooks **call `taba secrets link --repo <name>`** rather than
writing their own `ln` lines — one implementation, so the ignore-check and
prod-path rules cannot drift per repo. The hook locates the harness worktree
as a sibling and degrades to a printed note when there is none (a repo cloned
outside a collection has no control root to link from).

Note the lifecycle gap this leaves on its own: hooks run at worktree
**creation**, so a control-root file added later never reaches collections
that already exist. Catch-up closes it by re-running the same tool
(`AGENTS.md` → Catch-up rules).

`.env.collection` has the same gap for the same reason — it is generated at
collection creation and never revisited, so a variable the generator learned
afterwards reaches new collections only. `taba env setup` is that file's
version of the same "run it again" tool, and catch-up runs it too.

## Collection env and ports

`taba new` allocates each collection a **port block** — the lowest free
`42000 + 100·n` across existing collections — and writes two generated,
uncommitted files at the collection root:

- `.env.collection` — each `WTC_` variable also appears as `TABA_` with
  the same value. `WTC_COLLECTION`, `WTC_AGENT_NAME`
  (`<herdr-session>--<collection>`, what `taba-open` passes to
  `herdr agent start`), `WTC_CONFIG_ROOT`, `COLLECTION_PORT_BASE`, and one
  `<REPO>_PORT` per registry repo with a `port_offset` (e.g. `api` offset 1 →
  `API_PORT=42001`). Ports are emitted for *all* such repos, present or not,
  so a frontend can always resolve the API port — point absent services at a
  shared dev instance or start them on demand. It also carries the optional
  tool-identity variables (`GH_CONFIG_DIR` and friends) when the workspace has
  opted into them — `secrets.md` → Tool identity.
- `mise.toml` — loads `.env.collection` via `[env] _.file` and, when the
  harness commits a version pin (`.taba-version` or `.wtc-cli-version`), pins that exact taba GitHub release
  through `[tools]`. mise treats the
  collection root as a parent config, so **every sibling repo worktree
  inherits these variables automatically** in any mise-activated shell or
  `mise run` task. The harness tools run `mise trust` on generated
  collection configs and on each created worktree's checked-in `mise.toml`
  (worktrees come from <org> remotes, so their configs are as trusted as
  the code itself); manual `mise trust` is only needed for files you create
  by hand.

Without mise: `set -a; . ../.env.collection; set +a` from inside a repo, or
source it in the shell/IDE run configuration.

Port offsets live in `.harness-repos.yml` (`port_offset:`); keep them unique
and stable — they are the contract between repos.

## Agent shells and PATH

Agent CLIs spawn non-login shells. They do not run `mise activate`, so
`/usr/bin/env ruby` on a shebang (`bin/rails`, `bin/rake`) falls through to
macOS system Ruby 2.6, and `mise exec` from the **collection root** uses the
global pin rather than a sibling's. That is not an untrusted `mise.toml` and
not a reason to prepend `mise where ruby` by hand.

The harness injects the union of each sibling's `mise bin-paths` so every
agent shell command sees repo-pinned tools on PATH, independent of cwd:

| Surface | What it does |
|---|---|
| `taba agent-env` | trusts sibling `mise.toml` files, caches bins as `.env.toolchain`, prints `export PATH=…` |
| `hooks/agent-env.json` | SessionStart refreshes the cache; PreToolUse wraps `Bash` / `run_terminal_command` with inline exports from `taba agent-env --wrap` |
| collection-root `.envrc` | Grok `load_envrc` (and direnv) prepend PATH without needing project hook trust |
| `taba open` | new herdr workspaces get `WTC_TOOLCHAIN_PATH` and `TABA_TOOLCHAIN_PATH` at create time |

### Why the toolchain is applied after shell startup

Anything that sets `PATH` *before* a login shell starts loses on macOS.
`/etc/zprofile` and `/etc/profile` run `path_helper`, which rebuilds `PATH`
from `/etc/paths` and `/etc/paths.d` and appends whatever was already there —
so an injected prefix comes out **below** `/usr/bin`, and `ruby` resolves to
system 2.6. Measured: a prefix passed at position 1 lands at position 18, with
`/usr/bin` at 6. An injection done that way achieves the exact opposite of its
purpose, silently.

So the harness passes `WTC_TOOLCHAIN_PATH` — an ordinary variable `path_helper`
does not touch — and the agent hook and `.envrc` apply it after the profile:

* the PreToolUse hook — adds the cached toolchain path to each agent shell
  command, independent of how its stdin is connected.
* `.envrc` — direnv and Grok `load_envrc`.

Interactive human panes never needed this: a login shell that runs `mise
activate` from `.zshrc` already resolves per-directory pins correctly. The
problem was only ever the non-login shells agents spawn.

`taba skills render` installs the hook JSON at `.grok/hooks/`, `.claude/settings.json`,
and `.cursor/hooks.json` (symlinks; a real file there is left as a local
override) and regenerates `.envrc` / `.env.toolchain`. Catch-up re-runs it.
Grok skips project hooks until `/hooks-trust`; `.envrc` still prepends PATH,
while herdr carries `WTC_TOOLCHAIN_PATH` for the hook.

`mise exec` remains correct **inside a sibling**. It is not required to dodge
system interpreters once PATH is injected.

## Optional mise extras (never required)

- Repos may pin toolchains in their `mise.toml` (`[tools]` — node, python,
  gradle…) so collections don't drift per machine.
- Humans with `mise activate` in their shell can add `enter` hooks (e.g.
  print catch-up status when cd-ing into a collection). Convenience only —
  nothing in the harness may depend on shell activation.

## What runs what

| Event | Tool | Hooks run |
|---|---|---|
| Collection created | `taba new` | `init` for every included repo (after env is written and skills are linked) |
| Repo added later | `taba add-repo` | `init` for the new repos only |
| Collection retired | `taba retire` | `teardown` for every repo, then worktrees removed (pre-flight refuses on dirty/unpushed work unless `--force`; branches are never deleted) |
| Generator or registry changed | `taba env setup` | none — regenerates `.env.collection`, preserving the port base |

## Receiving project and collection boundaries

Read-only project discovery uses `.taba/project.toml`; an explicit member
association uses `.taba/collection.toml`. New project metadata uses these canonical
names only. Existing `.wtc` collection/runtime formats retain their compatibility
rules. A local project's `.taba/config` is project configuration, not a standalone
ad-hoc collection. Discovery never initializes files or selects the first task.

Project descriptors use `schema_version = 1` and `mode = "local"` or `"harness"`.
Local project config/registry lives in `.taba/config`. Harness mode declares one
project-relative `[harness].path`, or `[harness].owner` and `ref` naming a local
bare-owner snapshot. Snapshot reads do not fetch; an unavailable source is an
error. Config selection matches filesystem rules: `wtc.toml` remains authoritative
when present, otherwise `taba.toml`; invalid sources do not silently fall back.
Descriptors cannot contain credential roots, auth grants, shell commands, a task
selection or a version pin. Receiving secrets policy stays in the selected config.

A collection descriptor contains `schema_version = 1` and a `project` path
relative to the collection. It must point to an enclosing explicit project;
malformed or intervening boundaries prevent parent fallback. Commands requiring
an actual collection do not accept a project root as an implicit task. Project
initialization and later adoption are separate explicit operations.

Lifecycle and runtime process boundaries validate receiving roots and replace
both root aliases together. When shell env layers are used, roots are checked
again after evaluation and before the child program starts. Local shell files
remain trusted executable configuration; this is not a shell sandbox. Provider
credential isolation is separate from receiving-root validation. See
[secrets.md](secrets.md) for scope, opt-in and migration.
