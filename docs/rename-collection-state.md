# Collection-state compatibility for the taba rename

This increment of [phase 0](https://github.com/lcorneliussen/taba/issues/201)
accepts alternate names for collection-local PR and status state. Check a
release's notes before assuming it includes these readers. No migration or
manual rename is required.

| State | Legacy name | Alternate name |
| --- | --- | --- |
| PR enlistments | `.wtc-prs` | `.taba-prs` |
| Canonical status snapshot | `.wtc-status.json` | `.taba-status.json` |
| Shell-compatible status fallback | `.last-wtc-status.yml` | `.last-taba-status.yml` |

An existing legacy file wins, including an empty file. Only an absent legacy
path permits the alternate name. Invalid or dangling legacy inputs are not
bypassed, and the files are not combined. Listing PRs, reading cached status
and checking cache age do not create or rename files.

Phase 0 continues to write legacy names. When an enlist, unlist or final-merge
recording operation starts with only `.taba-prs`, it reads that registry under
the existing `.wtc-prs.lock`, applies the change, and writes the complete result
to `.wtc-prs`. Existing rows, comments and merge facts carry forward. The
alternate source is left unchanged; subsequent reads select the legacy output.
This is a compatibility write, not a switch to new default names.

A completed status refresh still writes `.wtc-status.json`, `.wtc-status.md`
and `.last-wtc-status.yml`, making its new snapshot available to older readers.
Existing alternate snapshots are left unchanged. JSON snapshots take priority
over the YAML fallback, with legacy-name precedence within each format.

Catch-up has no cached-report reader: its default output remains
`.wtc-catch-up.json` plus `.wtc-catch-up.json.md`. An explicit
`--report .taba-catch-up.json` already works, as does any other report path.
Retirement recognizes both report names, `.taba-status.md`, and the alternate
PR/status files as disposable collection state.

## Review, retirement and watcher state

| State | Legacy name | Alternate name | Rule |
| --- | --- | --- | --- |
| Review-only marker | `.wtc-review-mode` | `.taba-review-mode` | Either makes the collection review-only |
| Retirement marker | `.wtc-retired` | `.taba-retired` | Either marks the folder retired |
| Review bundles | `.wtc-reviews/` | `.taba-reviews/` | Both are read; new bundles go to the legacy directory |
| Posted-review receipts | `.wtc-review-posted/` | `.taba-review-posted/` | Either proves a post; revoking clears both |
| Status watcher leases | `.wtc-status-watchers/` | `.taba-status-watchers/` | Retirement stops both; new leases use the legacy directory |

A marker under either name takes effect, and an unparseable marker still
counts. An unreadable legacy review marker falls through to the alternate.
With no readable marker at all, a collection is not review-only, as before.

Review rounds and earlier-round context continue across both bundle directories,
and the review pane picks the newest applicable bundle from either. Retired
folders also treat `.taba-review-mode`, `.taba/review-hooks`,
`.taba-status-watchers.lock` and `.taba-runtime.lock` as disposable. Nothing
writes those two lock names yet.

**Locks do not fork.** `.wtc-status-watchers.lock` stays the single lock every
client takes, so old and new binaries exclude each other. A later rename phase
must not move it while any supported client still uses it.

## Runtime and local services

A collection has one runtime home holding its manifest, logs and local service
declarations together. `.wtc/runtime` and `.wtc/svc` are selected when either
holds state; otherwise `.taba/runtime` and `.taba/svc` are, when either does.
A fresh collection uses `.wtc`. Empty directories do not count.

State under both refuses runtime commands and retirement, because the other side
may be a live runner, or services the selected side would hide. Retirement
refuses before hooks run. Keep one side before continuing.

`.wtc-runtime.lock` stays the one lock every client takes, wherever the state
lives. A later rename phase must not move it while any supported client uses it.
Retirement removes managed runtime and service state under either name.

[Collection directory inputs](rename-collection-directories.md) cover ad-hoc
configuration, dev-build lookup and generated skill/instruction links under
`.taba/`. Skill names, executable packaging and automatic migration belong to
later rename phases.
