---
name: taba-local-review
description: Review a draft PR with separate headless concern agents before marking it ready.
---

# Local review gate

Use `taba review` from a worktree collection. A draft PR becomes ready only
after a separate reviewer process posts a passing summary for its current head.
The review is independent of the authoring agent; do not write its findings,
summary, or verdict yourself.

In a review-only collection (`taba review start`, `taba status` shows
`review-only`), you review someone else's PR: build, run and post the review
only (steps 2 and 3). Make no commits, pushes or thread resolutions, and do not
mark the PR ready; those steps belong to the author, and the tools refuse them.
After the author pushes, `taba review bundle` again starts the next round on
the new head.

1. Inspect `taba review activity <repo> <pr>` on GitHub before spending local
   review time. Let an active Copilot/Cursor pass finish, then address or answer
   and resolve its open threads. Inspect review bodies as well as inline
   findings; `reported` is not approval. Do not start all providers together.
   If a hosted reviewer reports a quota, budget or provider failure, record that
   unavailable coverage and continue with local review instead of repeatedly
   summoning it. If no hosted run exists (often on drafts), proceed locally;
   do not trigger one just to satisfy this step. A clean hosted review still
   requires a separate local pass. Recheck activity after every new head.
2. Build a bundle with `taba review bundle <repo> <pr>`. Public or unknown
   visibility, a different forge/owner, or an unknown harness namespace defaults
   to public mode. It uses only target base-commit concerns (or CLI defaults),
   embedded prompts, and public prior rounds. `--public` forces this isolation;
   `--private` explicitly includes harness overlays and other repository context.
   Inspect the selected mode and reason in the output and manifest. Private
   bundles cannot be posted unless fresh forge evidence confirms a private
   destination, even with `--force`. Normally an upstream project runs its own
   gate rather than a consuming harness judging it. A known PR checks out its
   branch and catches up the collection before the diff; `--no-catch-up` skips that refresh. Branch-only
   reviews use current local refs. Inspect the bundle before giving it to an
   external reviewer.
3. Run `taba review run <bundle-dir>` without `--post`. Inspect `summary.md`
   and planned inline comments against the destination's audience, then run
   `taba review post <bundle-dir>`.
4. Read `summary.md`, `verdict`, and any error output in `run.log`. Address or
   answer every open finding. Use `taba review resolve <bundle-dir> --reply
   "..."` for inline threads after answering them. A later round retains the
   original inline thread; resolve it using the bundle that first posted it.
   When a finding names an invariant, trace all affected writers/readers and fix
   the shared class rather than only its first instance. Use deterministic
   ordering evidence for races. Answer scope/design checkpoints before another
   round of point fixes; keep new behavior in an explicitly scoped follow-up
   when appropriate. These checkpoints do not authorize expanding the task.
   An intentional behavior can still need a truthful explanation or diagnostic;
   answer that clarification separately from a request to change the behavior.
   Optional style notes alone do not hold the gate. Supply current head-specific
   evidence when correcting a stale pending-test claim.
5. Push fixes and build a new bundle on the new head. Continue until the
   verdict is `pass`, or `pass-with-notes` with remaining notes answered.
6. Only when the user has asked to mark the draft ready, use
   `taba review ready <pr> --repo <repo>`. It requires a current posted review
   with a local receipt. `pending`, `error`, and stale reviews keep the gate
   closed.

The runner defers GitHub PR reviews while a known hosted review is active or
has open threads, and saves current reports plus all unresolved hosted threads
in `prior/hosted-reviews.md`. It does not trigger providers, wait in a background
loop, or lock out a hosted run that starts later. Use the host's completion
notifications where available; otherwise take bounded snapshots while doing
independent work. After an agreed wait expires or hosted evidence cannot be
read, document the limitation before intentionally using `--hosted-first=false`;
this changes scheduling only and never bypasses the local gate. Do not describe
missing or failed hosted coverage as a clean review.

A failed or missing local concern yields `error`, even if the lead or a hosted
reviewer recommends approval. Rerun failed concerns with `--only <ids>` and an
available agent chain, retaining completed concern results for this same head.
Do not silently drop concerns when limits are exhausted. The comments concern
produces a Delete / Refactor first / Keep plan for changed code; preserve useful
rationale, contracts, directives and API documentation.

The default model tiers come from `HARNESS_REVIEW_STRONG`,
`HARNESS_REVIEW_STANDARD`, `HARNESS_REVIEW_FAST`, and `HARNESS_REVIEW_LEAD`.
`--strong`, `--standard`, `--fast`, and `--lead` override them per run. Each
spec is an `agent:model` chain. Supported agents are Claude, Codex, and Grok.

Before every external write, inspect the exact destination and outgoing text.
Public PRs must not include private project names, paths, or adoption links.
