# Recover local edits after catch-up

Use this procedure when native gateway catch-up reports `recoveryRequired`,
status shows `catchUpRecovery`, or the Collection card asks to recover local
edits. Catch-up has already saved staged, unstaged and untracked content in a
pinned Git copy. The base may have advanced; running catch-up again is not a
recovery operation.

## Find the operation

Work in this conversation's repository checkout from `wtc status`. Establish
whether your shell is on the gateway or in a sandbox (`WTC_GATEWAY_GIT` set).
The saved ref and marker belong to the **gateway worktree**, not a sandbox's
copy. If you cannot access that authorized environment, report the boundary
and ask for gateway-side help; do not substitute a similarly named checkout
or fetch private recovery refs into a sandbox. A sandbox merge instead uses
`wtc catch-up --continue` once its conflicts are resolved.

In the owning gateway checkout, inspect:

```sh
git status --short
git ls-files --unmerged
git rev-parse HEAD
git rev-parse --path-format=absolute --git-path wtc-catch-up-recovery
```

Read that per-worktree marker and compare its exact ref with the requested
operation. Stop if they differ, the marker is malformed, or another operation
is running. Do not remove a lock without establishing who owns it. A recovery
ref is repository-wide; `stash@{0}` may belong to another worktree.

## An aborted base merge with restored edits

A committed base conflict may abort the merge and restore the original local
edits successfully. There is then no active recovery marker, but the checkout
is dirty again. Do not start `git merge` over those edits or keep retrying the
same catch-up. In the owning environment, inspect the existing pinned backup
and preserve the current staged, unstaged and untracked edits in a separately
pinned Git stash. Identify it by its exact object/ref, never `stash@{0}`.
Verify that the intended content and staging are saved and the checkout is
clean before merging the base. Resolve and commit that merge, then reconcile
the saved edits and staging using the comparisons below. Keep both backups.
If restoring creates a conflict, inspect partial restoration before applying
anything else. Publication and committing the restored local work remain
separate actions. Only finish an active recovery marker if one exists.

## Reconcile the intended work

Inspect the saved ref rather than applying it blindly. For `<ref>`:

- `<ref>^1` is the old HEAD before catch-up;
- `<ref>^2` is the saved index (what was staged);
- `<ref>` is the saved tracked working tree;
- `<ref>^3`, when present, holds the saved untracked files.

Compare the old HEAD, saved index, saved working tree and current HEAD for
paths involved. Read the upstream changes as well as the local intent. An
unsuccessful `stash apply --index` may have partially restored files already.
Preserve unrelated current edits, new files and ignored data. Reconcile
collisions individually, including an untracked file now tracked upstream.
Avoid a wholesale checkout/reset or another stash application over restored
content. If intent is ambiguous, stop and explain the choice needed.

Keep staging meaningful. Restore the saved staged portion against current
HEAD, then preserve the unstaged portion separately; do not stage every file
just to clear a warning. Stage individually resolved conflict paths after
review. Inspect both `git diff --cached` and `git diff`, plus saved and current
untracked content. Run relevant build/tests and check for unresolved entries
and conflict markers. A clean Git index alone does not prove restoration.

## Finish the verified recovery

After verifying the content and staging, read the current full HEAD again.
From the same gateway checkout run:

```sh
wtc catch-up --finish-recovery <exact-ref> --expected-head <verified-full-HEAD> --json
```

This acknowledges your verification. It checks the operation identity, HEAD,
backup, Git operation state, unresolved entries and newly added tracked
conflict markers. It cannot verify semantic intent, untracked content or test
coverage for you. It archives only the active marker, writes a structured
completion receipt, and retains the pinned copy and stash. It does not fetch,
apply edits, commit, push or delete backups. If it refuses, inspect the stated
condition; do not manually clear the marker to make the card look successful.
A repeated finish for the same ref and HEAD returns its original receipt.

Refresh status to confirm the recovery marker is gone. The card can settle
its matching pending recovery from that receipt; an accepted chat request or
an absent behind count is not proof of completion. If files are already
restored but the marker remains, verify them with this procedure and finish
that exact operation rather than restoring again.

Tell the person briefly: whether the collection is caught up, whether local
edits and staging were preserved, what remains uncommitted, and any blocker.
Keep technical evidence in the Action log or requested details. Recovery
finishes this selected step; committing and publication remain separate.
