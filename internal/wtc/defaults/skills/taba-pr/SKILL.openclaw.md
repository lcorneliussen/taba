---
name: taba-pr
description: "Under OpenClaw: get a repository's work ready for its pull request. Check it with wtc publish, record the title and description, and hand the push to the person. Use when the work in a repository is committed and ready for review, when the person asks for a pull request, or when they ask you to publish."
metadata: { "openclaw": { "emoji": "↑", "requires": { "bins": ["wtc", "git"] } } }
---

# Get a pull request ready

Pushing is the person's. The collection card's ↑ has the gateway push the
repository with their own account, after they confirm, and open or update a
**draft** pull request. You prepare it; you never `git push`.

1. **Commit the work** in `<folder>/<repo>` (`wtc status` shows the folder).
   On Bitbucket only what is committed is pushed. On GitHub the gateway
   commits everything pending first, so leave nothing there that is not part
   of the work.
2. **Write the description** to a file: what changed and why, how it was
   checked, what is left. Follow the repository's pull request template when
   it has one. The harness's `AGENTS.md` may have rules for what a public
   pull request may say: follow them.
3. **Check and record it:**

   ```bash
   wtc publish --title "<what changed>" --body-file <file>
   ```

   It changes nothing. It refuses outside a managed worktree, after published
   history was rewritten, during an unfinished merge, or with files that look
   like credentials. Fix what it names and run it again. The title (at most
   256 characters) and the description (at most 8192) are recorded for the
   branch; the person's push opens the pull request with them. Bitbucket
   takes the title only.
4. **Read the plan's `pending` list.** Anything in it that is not part of the
   work gets removed or ignored first.
5. **Tell the person it is ready:** which repository, and that they push it
   with ↑ for that repository in the card.

Only when the person explicitly asks *you* to publish, carry out the plan in
the repository's session: on GitHub the `github_publish` tool, with the
intent's `args` unchanged. On another forge, hand the plan to that forge's
skill. Never force-push.

After that, pull request news arrives in this conversation: see wtc-follow.
