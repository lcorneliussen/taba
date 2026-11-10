---
name: taba-catch-up
description: "Under OpenClaw: bring a repository of this conversation's collection up to date with its base branch, or merge another branch of it (a sub-session's) into it, and resolve the conflicts when the merge stops on them. Use when the person or the collection card asks to catch up a repository, when a merge conflicts, when a pull request conflicts with its base, or after a pull request merged."
metadata: { "openclaw": { "emoji": "↓", "requires": { "bins": ["wtc", "git"] } } }
---

# Catch up a repository

The input names the repository, and sometimes what the card already saw:
"wtc-cli: catching up with origin/main conflicts in README.md". Catching up
merges; it never rebases and never pushes. The gateway never resolves a
conflict: you do, in your own checkout of the repository.

1. **Find the checkout.** `wtc status` shows the collection folder; work in
   `<folder>/<repo>`. In a sandbox, that is your copy of it.
2. **Commit your work first.** Outside a sandbox, wtc refuses to merge over
   uncommitted changes to tracked files. They are yours: commit them, never
   discard them. (In a sandbox it sets them aside around the merge and puts
   them back.)
3. **Merge.** In `<folder>/<repo>`:
   - the base branch: `wtc catch-up`;
   - another branch of the repository (a sub-session's) into yours:
     `wtc catch-up --from <branch>`, by its name. A sandbox's catch-up
     merges only the base: merge another branch from the repository's
     session outside the sandbox;
   - in a sub-session's checkout made by `--worker`, `wtc catch-up` merges
     the collection's branch, its parent.
4. **Read what it says.**
   - Merged, fast-forwarded, moved to the base's tip (a detached worktree
     with no work yet), or already up to date: done.
   - **Detached with commits of its own:** put them on a branch first
     (`git switch -c openclaw/<collection>`), then catch up again.
   - **A conflict in a sandbox** (`WTC_GATEWAY_GIT` set): the merge stops
     with git's merge state in place. Resolve each file (below), `git add`
     it, then `wtc catch-up --continue`. That commits the merge and hands
     the branch to the gateway.
   - **A conflict outside a sandbox:** wtc aborted its merge and names the
     ref and the files. Run `git merge --no-edit <ref>` yourself, resolve each
     file (below), `git add` it, then `git commit --no-edit`.
5. **Check it.** Build and run the tests that cover what the conflicts
   touched before you call it done.
6. **Say what happened:** what came in, which files conflicted, and how you
   resolved them. When the repository has an open pull request, the person
   sends the merge with ↑ in the card.

## Resolving a conflict

Keep what each side meant, not one side's text. `git log --merge -p <file>`
shows the commits on both sides that touched the file; `git show :2:<file>`
is yours and `git show :3:<file>` theirs. When the two sides want different
behavior and the task doesn't say which wins, ask the person before you
commit. Never leave a conflict marker behind: the gateway refuses a branch
with one.

Never rebase, never `git commit --amend` a published commit, and never push.
