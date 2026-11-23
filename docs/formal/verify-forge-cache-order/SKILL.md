---
name: verify-forge-cache-order
description: "Model-check the write ordering of the status forge cache (pr and build entries in internal/wtc) with TLA+ and TLC. Use when a change touches statusWriteForgeEntrySinceObservedContext, the since receipt, the cache entry lock, the retained-merge rules, or any writer of a pr or build cache entry, and before you add a new writer."
---

# Verify the forge cache write ordering

`tools/formal/forge-cache-order/ForgeCacheOrder.tla` is a TLA+ model of one
forge cache entry in `internal/wtc` and the writers that publish it. TLC checks
every order of their steps up to a bound. A model checker fits because the
bugs in this area are orders of steps that tests did not think to write.

This skill lives in `docs/formal/` because the repository has no directory for
contributor skills. `internal/wtc/defaults/skills/` ships to every harness.

## The property

A forge cache entry never replaces a response with one from a request that
started earlier, except that a confirmed merge replaces a response that is not
a merge. The model calls it `NoEarlierOverwrite`. The comment on
`statusWriteForgeEntrySince` in `status_cache.go` states the same rule.

If it breaks, `taba status` shows an older pull request or build state than
one it already recorded, and gates branch actions on it, until a later refresh
replaces it. Issue #486 lists eleven earlier fixes of this class, each at one
write site.

## Check whether a change touches the verified code

The sync commit is in the coverage record below. List the covered files that
differ from it, committed or not:

```sh
git diff --stat a1f9c35513c6b8a438d702ca8119685150de4c27 -- \
	internal/wtc/status_cache.go internal/wtc/status_forge_budget.go \
	internal/wtc/status_open_list.go internal/wtc/status_enlisted.go \
	internal/wtc/status_full_refresh.go internal/wtc/status_build_forge.go \
	internal/wtc/status_freshness.go internal/wtc/status_resource_provider.go \
	internal/wtc/pr_identity_schedule.go
```

List every production writer of a guarded entry, and of a pr, build or since
entry without the guard:

```sh
rg -n 'statusWriteForgeEntrySince(Observed)?(Context)?\(|statusResourceWriteSince\(|statusWriteForgeCache\(|statusWriteForgeEntry(Observed)?(Context)?\((ctx, )?"(pr|build|since)"|statusResourceWriteEntry\(ctx, "(pr|build|since)"' internal/wtc --glob '!*_test.go'
```

Every hit for a pr or build entry must map to a row of the coverage record.
At the sync commit, `statusWriteForgeCache` (`status_cache.go:156`) writes a pr
entry with no check, and only tests call it. A production caller of it, or of
`statusWriteForgeEntry` for a pr or build entry, repeats the 9274035 bug.

## Run the check

You need Java 11 or later. Run:

```sh
tools/formal/forge-cache-order/check.sh
```

The script downloads tla2tools.jar 1.7.4 into `tools/formal/.cache/` and
checks its sha256. It takes about 10 seconds. CI runs it in the `formal` job
of `.github/workflows/ci.yml`. The first line of each config states the result
TLC must report, and the script fails on any other result:

| Config | Design | Expected |
| --- | --- | --- |
| `main.cfg` | main | `NoEarlierOverwrite` holds |
| `replay-9274035-parent.cfg` | no receipt check, as before 9274035 | violated |
| `replay-4764f64-parent.cfg` | a writer whose lock failed still publishes, as before 4764f64 | violated |
| `finding-receipt-loss.cfg` | main, with the receipt lost after the data rename | violated: the open finding below |

`VERBOSE=1 tools/formal/forge-cache-order/check.sh main.cfg` prints TLC's own
output, including the trace of a violation.

## Read the result

Read every result with the pstack `/statechart` skill. Give it
`ForgeCacheOrder.tla` and the config. It tells each violation as its shortest
path in plain words. When the check passes, it draws how the entry moves
between states. These configs exceed its chart size. For a chart, use a copy of
a config with one list writer, one locked writer and `MaxRequests = 3`.

## Tell a real bug from a modeling error

Do these steps when `main.cfg` fails or a replay config holds.

1. Map each step of the path to code with the coverage record.
2. Replay the path against the real code as a Go test in `internal/wtc`, with
   one call per step. `TestStatusOlderListDoesNotReplaceANewerPullRequest` and
   `TestStatusOlderPipelinePageDoesNotReplaceANewerBuild` in
   `status_full_refresh_test.go` replay the 9274035 path. To lose a receipt in a
   test, replace the file at `statusForgeEntryPath("since", forge, slug, "pr:7")`
   with a directory before the write, so the receipt rename fails.
3. If the test shows the path, the code has a bug. If it does not, treat the
   model as wrong until you show otherwise, and fix the model.

Never weaken `NoEarlierOverwrite` or remove a writer to make `main.cfg` pass.
A replay config that starts to hold means the model lost the mechanism that
the replay tests. Fix the model.

## Update the model for an intended change

1. Change the action that the coverage record maps to the changed code. A new
   writer is a list writer when its request starts before it takes the entry
   lock, and a locked writer when the request starts after. Add it to the
   comment of that action.
2. When the change adds a design choice, add a constant for it and a config
   that sets it. Give the config an `expect` line.
3. When a fix closes the receipt-loss finding, `finding-receipt-loss.cfg` must
   hold. Change its `expect` line to `pass`, rename it, and remove the finding
   from this skill.
4. Run the check and read the result with `/statechart`.
5. Update the coverage record. Move the sync commit only after you confirm that
   the model describes the covered code at the new commit.

## Coverage record

Sync commit: `a1f9c35513c6b8a438d702ca8119685150de4c27` (2026-11-23). Line
numbers are at that commit.

| Model action | Code in `internal/wtc` |
| --- | --- |
| `ListFetch` | `statusFetchGHOpenListContext`, `statusFetchBBOpenListContext` (`status_open_list.go`, `item.Started`). The recent pipelines page in `statusBBBuildContext` (`status_build_forge.go`). |
| `Lock`, `Unlock` | `statusLockCacheContext` (`status_forge_budget.go`) on the entry path, with a deferred unlock, in every writer below. |
| `LockFails` | The `if lockErr == nil { defer unlock() }` pattern before 4764f64. On main a failed lock ends the write path. |
| `LockedFetch`, `Refused` | `statusFetchSharedAdmissionContext` (`status_forge_budget.go`; pr entries from `statusViewPRWithLiveContext`, build entries from `statusSingleFlightContext`), `statusViewPRWithLiveContext` with `bypassCache` (`status_enlisted.go`), `statusRefreshRecord`, `statusConfirmFullPRLockedContext` and `statusConfirmFullBuildContext` (`status_full_refresh.go`), `prIdentityOrdinary` (`pr_identity_schedule.go`). |
| `Check` | `statusWriteForgeEntrySinceObservedContext`, `status_cache.go:202-223`, with `statusRetainedMergeContext` (`status_freshness.go`) for a known merge. |
| `RetainMerge` | `statusRetainMergeObservationContext` (`status_cache.go:226-235`). |
| `Publish` | `statusWriteForgeEntryObservedContext` renames the data file (`status_cache.go:239`). List writers reach it through `statusDetailFromOpenItemContext` under `statusEnrichListedContext`, and through `statusSaveLivePipelinePageContext`. |
| `Receipt`, `LoseReceipt` | The receipt rename at `status_cache.go:245`. Its result is ignored. |

## Bounds and assumptions

- Configs use two list writers, three locked writers and seven requests on one
  entry. `main.cfg` reaches about 198,000 states.
- Entries do not interact. Each has its own lock and its own receipt.
- Request start stamps increase in real time. A wall clock that steps back is
  not modeled.
- Responses are any of open or merged. The model checks order, not content.
  "Earlier" means the request started earlier. The guard orders the responses
  of overlapping requests by start time, as the code does.
- A retained merge does not expire. `statusMergedCacheAge` is not modeled.
- A cancelled, refused or failed request before its publication only skips a
  write. The model includes a refused request only once the request bound is
  spent, so that every writer can come to rest.
- Not covered: credential selection changes during a write, the order inside
  the merge entry, the `listed`, `checks` and `checkhead` entries, and which
  entry a reader shows.

## Open finding: a lost receipt lets an older response back in

The data file and the receipt are two renames (`status_cache.go:239` and
`:245`). If the process dies between them, or the receipt write fails, the
entry holds a response newer than its receipt. `finding-receipt-loss.cfg` shows
the path:

1. A status pass reads the open list. That is request 1.
2. A refresh takes the entry lock and starts request 2.
3. The refresh passes the check and renames request 2's response into the entry.
4. The receipt is lost and the lock is released. The receipt still names
   request 0.
5. The status pass takes the lock in `statusEnrichListedContext`.
6. The check in `statusDetailFromOpenItemContext` passes, because request 0
   did not start after request 1.
7. The status pass writes request 1's list item over request 2's response.
