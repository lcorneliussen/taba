---
name: taba-follow
description: "Under OpenClaw: act on what happened to the collection's pull requests. Use for a \"wtc: pull request news\" event (opened, checks failed or passed, approved, changes requested, conflicting, merged or closed), or when the person asks how a pull request is doing."
metadata: { "openclaw": { "emoji": "🔭", "requires": { "bins": ["wtc", "gh"] } } }
---

# Follow the pull requests

The wtc plugin tells this conversation when a pull request of the collection
changes, in a system event starting with `wtc: pull request news`. Each line
is one change. Act on it, then bring the task's progress up to date.

| The news | Do |
|---|---|
| checks failed | `gh pr checks <n>` and the failing job's log. Fix it in `<folder>/<repo>`, commit, and tell the person to push again with ↑. |
| changes requested, or new comments | `gh pr view <n> --comments`. Say what you will change, change it and commit. Don't answer review comments on the forge yourself. |
| conflicts with its base | Catch the repository up: the taba-catch-up skill. |
| merged | Catch the repository up (taba-catch-up), so the checkout continues from the merge. Then say what is done and what is left of the task. |
| approved, checks passed, opened | Nothing to fix. Say so when it changes what is next, for example "approved: ready to merge". |
| closed without merging | Ask the person whether the work goes on, and in which pull request. |

Tell the person only what they need to act on. People merge pull requests;
you never merge, and never push.
