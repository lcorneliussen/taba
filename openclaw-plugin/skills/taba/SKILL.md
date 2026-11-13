---
name: taba
description: "Taba (formerly wtc) projects and worktree collections under OpenClaw: work on one or more repositories, driven from one conversation. Use whenever the person wants to work on, look into, analyze or change a repository, a product or site built from one, or a Taba project; when a task names an issue; or when they mention Taba, wtc, a collection, the Collection panel or its card. Also before adding a repository, publishing, catching up or running a service."
metadata: { "openclaw": { "emoji": "🗂️", "skillKey": "wtc", "requires": { "bins": ["taba", "git"] } } }
---

# Taba

`taba` is on this gateway's PATH, and the gateway shows a conversation's
collection in the Control UI: a card above the message box and a Collection
panel. Its instructions come with the binary, so they always match it:

```bash
taba docs instructions/openclaw.md
```

Read that before the first `taba` command of a task and follow it.

Taba is the tool's name (formerly wtc). For "what Taba projects do you see?",
list all configured projects with `taba projects`. Treat an explicitly supplied
project or repository name separately.

In short:

- A **Taba project** is a named set of repositories (a harness the gateway is
  configured with); a **collection** is this conversation's worktrees of the
  repositories one task needs. An ad-hoc collection works without a project.
- **Work happens in this conversation's collection**, even when it is only
  reading or analyzing. When the person wants to work on something and the
  conversation has no collection yet, run `taba projects` to match what they
  named ("the site", "the CLI") to a project's repositories, then start one
  with `taba add-repo`. Never work in another conversation's collection
  folder, the workspace or a clone of your own, and don't answer from memory
  what the repositories can tell.
- `taba add-repo <repo> [...] --worktree <issue-id>-<slug>` creates nothing:
  it prints `sessions_spawn` intents. Call `sessions_spawn` once per intent,
  with its `args` unchanged. The Taba project whose repositories these are is
  picked, and a worktree of its harness comes first. Read the harness's
  `AGENTS.md` (`taba status` shows where it is) right away: it says which
  other repositories a task needs, so add them too.
- A conversation works in one Taba project. When `taba add-repo` asks which,
  ask the person, then pass `--project <name>`. When it says the project
  does not list a repository, put its choices to the person and run the one
  they pick. When another Taba project lists it, suggest working on that in a
  new conversation for that project. Never read or change another
  project's repositories from this collection unless the person added them
  here ad hoc.
- `taba status <name>` shows the collection folder; edit, build, test and
  commit in `<folder>/<repo>`.
- **Pushing is the person's.** When work is committed and ready for review,
  get its pull request ready with the **taba-pr** skill; the person pushes it
  with ↑ in the card. Never `git push`.
- **A "wtc: pull request news" event**: act on it with the **taba-follow**
  skill.
- **One focus per collection**: per repository one branch and one pull
  request. Parallel work, or a second pull request in a repository, is a
  collection of its own with its own conversation: propose it to the person
  with a brief, and they start it. Subagents only read your checkouts; don't
  give one a worktree to write in. A sub-session's worktree shows in the
  card under its repository; merge what you want of it
  (`taba catch-up --from <branch>`) or tell the person what is left.
- **Catching up** a repository, merging a sub-session's branch in, and resolving the
  conflicts either brings: the **taba-catch-up** skill. It never rebases and
  never pushes.
