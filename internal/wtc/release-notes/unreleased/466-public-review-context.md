## Review

- **Public reviews run outside the authoring collection.** Concern and lead
  processes of a public bundle now start in a fresh temporary directory with a
  copy of the bundle and a read-only snapshot of the reviewed head's files,
  written from raw blob contents (export attributes and filters do not apply;
  symlinks with absolute or parent-step targets are omitted and submodule
  contents are not included), instead of
  beneath the collection with the worktree beside them. Concern rules still
  come from the target's base through the bundle. Reviewers receive an
  allowlisted environment (locale, proxy, certificate and client
  authentication variables; add names with `TABA_REVIEW_ENV`) rather than the
  whole parent environment, and no PATH entries inside the collection.
  Claude starts with `--safe-mode --restricted --strict-mcp-config
  --no-session-persistence`, so project, local and user instructions, skills,
  hooks, plugins and MCP servers are not loaded; administrator-managed
  settings and managed-plugin hooks still apply. Reviewers have file tools
  only, confined by the client to the bundle copy and the snapshot: they read
  code but run no commands, builds or tests, and check results remain separate
  evidence. These are client restrictions, not an OS sandbox: after each phase
  the runner compares the snapshot and the bundle with their earlier state and
  fails the run on any write outside the expected findings, summary and
  verdict, without copying the run's files over the bundle. Posting state
  stays out of the reviewers' copy. Each run records `execution-context.json`
  in the bundle and one provenance line in the summary, without local paths or
  values. Public review refuses codex and grok, for which taba has no
  supported way to exclude user-level instructions and skills; use a
  `claude:` spec, or a `TABA_REVIEW_AGENT_CMD` launcher, whose client-level
  isolation is then recorded as the operator's responsibility. Such a launcher
  receives the bundle as its fourth argument; for public runs the prompt file
  is no longer inside the bundle.
- **Private review keeps the worktree read-only for claude.** The rule that
  denies claude edits in the worktree is now anchored at the filesystem root;
  in the previous form it did not match and writes were allowed. Private
  reviews otherwise keep their collection context.
- **Timed-out reviewers stop promptly.** A timeout now stops the reviewer's
  whole process group instead of waiting for descendants that keep its output
  open.
  ([#466](https://github.com/lcorneliussen/taba/issues/466)).
