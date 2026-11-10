---
name: taba-customize
description: Customize worktree collection behavior through harness config and hooks or a repository's worktree lifecycle hooks. Use when adding tool pins, environment behavior, setup or teardown, or local agent guidance.
---

# Customize wtc behavior

Read `taba customize` for the installed CLI's current hook and config contract.
Inspect the target harness or repository before editing: its existing hooks,
`wtc.toml`, registry, and agent instructions determine where the change belongs.

- Put shared collection config and CLI lifecycle hooks in the harness. Check in
  an exact `.wtc-cli-version`; add other generated mise tools under
  `[mise.tools]` in `wtc.toml`.
- Put application setup and cleanup in that application's `harness:init` and
  `harness:teardown` mise tasks or `.harness/init.sh` and
  `.harness/teardown.sh`. Keep them safe to rerun.
- Use `taba env setup --dry-run` to inspect generated changes. Verify the hook in a
  disposable collection, then run the relevant harness or repository tests.
- For an embedded instruction or skill that needs local wording, run
  `taba eject <path>` and edit the copied file. Eject refuses overwrites.
- Run `taba skills render --dry-run` before applying skill or agent-hook
  changes. Check in skill overrides under `harness/skills/` or
  `harness/overlays/skills/`, or an H2 section patch under
  `harness/overlays/skills/<name>/sections/`. Run `taba skills diff --changes`
  to review the override and its base digest, then render and verify the
  generated links.

Keep credentials in the control root or `.env.collection.local`. Generated
collection-root files are regenerated, so put durable customization in git.
