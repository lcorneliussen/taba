---
name: taba-add-repo
description: Bring another repository into an existing worktree collection as a sibling worktree, cloning its bare owner from GitHub if needed and running its init hook. Use when a task turns out to need a repo that is not checked out in the current collection, when the user asks to add a repo, service, or frontend to the workspace, or when a cross-repo change needs a second sibling.
---

# Add a repo to a live wtc

**Under OpenClaw** (`taba host` prints `openclaw`) `taba add-repo <repo> --worktree <name>` creates nothing: it prints one `sessions_spawn` intent per repository for the collection's session to carry out, so that the gateway owns the worktrees — see [openclaw.md](../../instructions/openclaw.md).

Collections start scoped to what the task looked like at the beginning. When
it turns out to need another repo, add it — that is cheaper and more correct
than a second collection or a stray clone somewhere else on disk.

## 1. Check it isn't already there

Siblings are subdirectories of the collection root named after the repo. The
full inventory of what *could* be added is `harness/.harness-repos.yml`; the
name you pass is the registry `name`, not the GitHub slug.

If the repo is not in the registry at all, this is a different job — it has to
be registered first (remote, `default_ref`, a unique `port_offset` if it
serves, `issues_prefix` if it owns issues), then `taba registry refresh` re-run.

## 2. Add it

```bash
taba add-repo <repo> [<repo> …]
```

Run from this collection. An explicitly requested other collection can be
selected with `--collection <name>`, where the name is its directory under
the same workspace root.

Default: the new worktree is **detached at the repo's `default_ref`** — no
branch. A repo added purely for context (reading a sibling's code, checking an
API shape) then never grows a branch at all and needs no cleanup. If it does
turn out to need changes, `git switch -c <issue-id>-<slug>` at the first
commit.

- `-b <branch>` — check out a real branch instead. An existing branch of that
  name is used; otherwise it is created from `default_ref`. Use it when
  picking up a branch someone else pushed.
- `--tip` — accepted and ignored; detached at the tip is the default now.

The CLI fetches a missing bare owner, adds a detached worktree, links that
repository's gitignored control-root files, refreshes skills and MCP settings,
then runs its init hook. The collection env already carries every registry
repo's port, so nothing needs re-wiring.

## 3. Wire the rest

`taba add-repo` links secrets before init. A nonzero exit means a target is
not gitignored — fix the ignore rule in that repo first.

## 4. Before changing anything in it

Read the new sibling's own `AGENTS.md`. Branch policy, working branch, and
commit restrictions are per repo and this one's may differ from what the
collection has been following — `ops` especially stays
read-only-prod with its own rules.

Each repo's branch is created at its own first commit, so a sibling whose work
belongs to a different issue simply gets that issue's name — nothing inherits
the collection name any more. Each repo's commits map to the issue that owns
them.

## 5. Report

Which repos were added, on which branches, whether an init hook ran or was
absent, and whether secrets linked cleanly.

Then **record it in the selected scope file** (`WTC-SCOPE.md` when present,
otherwise existing `SCOPE.md`; a fresh file is seeded by `taba-start`) — one line
saying why that repo is now in this collection. Pulling a repo in widens the
scope, and a scope file that no
longer describes the collection is how one task quietly becomes two
(`instructions/collection-context.md`).

---
Canon: `.wtc/instructions/worktree-workspace.md`,
`.wtc/instructions/hooks-and-env.md`,
`.wtc/instructions/development-workflows.md`.
