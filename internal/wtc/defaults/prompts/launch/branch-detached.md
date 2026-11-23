---
id: launch.branch-detached
version: 1
inputs: [branch]
audience: private
---
Worktrees start **detached at the development tip** — no branch exists yet.
Create one at your first commit:

    git switch -c {{branch}}

(that is the expected name; adjust it if the work turns out to be something else).
