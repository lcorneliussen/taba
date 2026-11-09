# You are in a worktree collection

This directory is one **wtc** — a worktree collection: several repos checked
out side by side for **one task**, plus `harness/`, which carries the tooling
and the instructions.

They are git **worktrees**, not clones. Each hangs off a shared bare owner in
the workspace root, so a fetch there serves every collection at once, and most
sit *detached at their repo's development tip* until your first commit creates
a branch.

```text
<workspace-root>/            every collection lives here; not a git repo
  .bare/<repo>.git           shared owners — the worktrees hang off these
  <other-collection>/        a COLLECTION SIBLING: someone else's task
  <this-collection>/         the COLLECTION ROOT — you are here
    AGENTS.md                this file (rendered from the pinned CLI or harness override)
    WTC-SCOPE.md             task context (or existing SCOPE.md) — read it next
    harness/                 project configuration and policy
    <repo>/                  REPO SIBLINGS: worktrees in scope for this task
    HANDOFF.md               only on a fresh collection — /wtc-start, then gone
```

Four words, because two of them get confused: the **workspace root** holds
everything; a **collection root** is one task; its **repo siblings** are the
worktrees inside it; its **collection siblings** are the neighbouring
collections — other tasks, not your working set.

## Public and private audiences

Before publishing any issue, PR, reply, commit or artifact, follow
`.wtc/instructions/publication-privacy.md`. Keep private project identities,
adoption relationships and delivery links out of public upstream records. This
also applies to delegated agents and to explanations of a privacy cleanup.

## Read in this order

1. **Task scope** — read and edit `WTC-SCOPE.md` when present, otherwise
   `SCOPE.md`. It states the task, the repos that belong to it, and what is
   deliberately out. If neither exists, run `/wtc-start`; fresh scope files
   use `WTC-SCOPE.md`. Do not create a legacy file beside existing `SCOPE.md`.
2. **`harness/AGENTS.md`** if this harness has one — the domain rules for your
   project. Then each sibling's own `AGENTS.md`: branch policy and commit
   restrictions are per repo.
3. **`.wtc/instructions/`** — geometry, branch policy, secrets, skills.

## Stay in this collection

Your working set is this collection root: `harness/` and the repo siblings
beside it. The collection siblings — every other directory under the workspace
root — are **other people's tasks**, even when they are your own from
yesterday. Crossing into one is a major barrier: read-only reference is fine,
acting there is not, unless the user **explicitly** names that collection and
asks you to act in it.

Do not "also fix" a collection sibling for convenience. The `.bare/` owners and
`$WTC_CONFIG_ROOT` are shared infrastructure, not another collection — reading
and fetching there is normal. Tools that span collections (`--all` sweeps,
`retire <other>`, a new branch-off) run only when the user asked for that
explicitly. Full rule: `.wtc/instructions/collection-context.md`.

## Launching anything: `wtc svc`

When services are enabled (`[runtime] backend = "dekit"` in `harness/wtc.toml`,
or in `.wtc/config/wtc.toml` of an ad-hoc collection), launch, run, serve,
preview or share an app, server or worker through `/wtc-svc` (`wtc svc up`,
`wtc svc add`, `wtc svc tunnel`) rather than in your own shell: such a server
dies with your session, may take another collection's port, and does not show
in `wtc status`. Without that setting there is no `/wtc-svc`; follow the
repository's own instructions.

## Skills, config, secrets — the short version

- **Skills** are the procedures for recurring collection actions —
  `/wtc-status`, `/wtc-open`, `/wtc-catch-up`, `/wtc-pr`, `/wtc-add-repo`,
  `/wtc-retire`, `/wtc-svc` to launch, share or stop an app where services
  are enabled, and `/wtc-start` on a fresh one. Generic procedures ship
  with the CLI; harness overrides live in `harness/skills/`. `wtc skills
  render` exposes them at this root. Prefer a skill when one exists.
- **MCP servers** are declared once in `harness/.mcp-servers.yml` and
  rendered into `.mcp.json`, `.cursor/mcp.json` and `.codex/config.toml` at
  this root by `wtc mcp render`. The rendered files name credentials and never
  hold one; the values come from the collection env.
- **Config** is `.env.collection` (generated: ports, `$WTC_CONFIG_ROOT`) plus
  `.env.collection.local` (yours, never regenerated, dies with the
  collection). herdr panes and mise load both, so a tool run from a pane
  already has them.
- **Toolchain PATH** is injected for agent shells that never ran `mise
  activate`. Do not spend turns on `mise trust`, `mise exec`, or prepending
  `mise where ruby` — a PreToolUse hook and `.envrc` prepend sibling bins;
  herdr passes the cached path for those surfaces so `/usr/bin/env ruby` cannot fall
  through to macOS system Ruby. If `ruby -v` still shows 2.6, once:
  `eval "$(wtc agent-env)"`. Grok project hooks need `/hooks-trust`
  the first time you open a collection.
- **Secrets** are never in git and never committed. One canonical copy lives
  in the control root at `$WTC_CONFIG_ROOT/<repo>/<repo-relative-path>` and is
  **symlinked** into the worktrees by `wtc secrets link`, so a
  rotated credential is current everywhere at once.

Details in `.wtc/instructions/` — secrets.md, hooks-and-env.md, skills.md.

## Follow the work this session owns

After opening or advancing a review-ready PR, use `/wtc-follow` on relevant
status/check/review updates and when resuming this task. Carry owned work
through review, main builds and required delivery steps, preparing human
checkpoints before actions that still need authorization. Reuse fresh status
snapshots instead of repeatedly querying every PR. `/wtc-catch-up` brings
outside changes in; `/wtc-follow` carries this session's work to its outcome.
A status-only request remains read-only. Procedure and resumption limits:
`.agents/skills/wtc-follow/SKILL.md`.

## Review a draft before marking it ready

Use `/wtc-local-review` to bundle the diff and run a separate headless review.
For a public PR, inspect the bundle and outgoing summary before posting them.
`wtc review ready` requires a passing review posted on the current head with a
local receipt. The rendered procedure is at
`.agents/skills/wtc-local-review/SKILL.md`.

## Widening the scope is a decision

Needing another repo, another system, another service is normal — doing it
silently is not. Bring the repo in with `wtc add-repo` and write
down in the selected scope file why it is here. A collection whose scope file
no longer describes it has stopped being one task.

## Private working hours

Use `wtc hours facts --json` at meaningful task/turn boundaries. When configured,
follow `.wtc/instructions/working-hours.md` for focus, wrap-up, quiet independent
work and explicit finite extensions. Offer setup when unset; never guess hours.
This is portable guidance, not a guarantee of automatic per-turn enforcement.

## Nothing durable lives in this folder

The selected scope file, `HANDOFF.md`, and `.env.collection.local` are disposable
— `wtc retire` deletes the whole directory when the work lands, and that must
cost nothing. Durable state goes to **git and the forge**: commits and branches
in the repo siblings, PRs, and the issue that the branch name points at.

---
This is the pinned CLI default, rendered into `.wtc/collection-AGENTS.md`.
Use `wtc eject collection-AGENTS.md` to own an override in the harness.
Project instructions override defaults in `.wtc/instructions/`; edit their
source under `harness/instructions/`, never the generated copy.
