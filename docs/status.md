# Reading local terminal status

Status combines independent observations: the Git checkout, its pull request,
reviews and checks, and builds of shared branches. Read them together. A green
result in one field does not answer the questions represented by the others.
Installed versions can differ; use `taba status --help` and `?` in the live view.

## Read from checkout to delivery

1. **Which checkout?** REPO identifies the worktree directory. BRANCH shows
   its branch, or `⌂ main` for a detached checkout at the development ref.
2. **What differs locally?** `±` describes working-tree changes or an operation.
   `↑` and `↓` count commits against comparison refs, which can differ.
3. **What does the PR report?** Read its identifier and draft/conflict state,
   then checks, mergeability and reviews in that fixed order.
4. **What has built?** TEST and PROD observe configured shared branches,
   independently of the PR's check run. After merge, STATE names the build
   tier the PR is still waiting on.
5. **How current is this?** The header, refresh log and JSON distinguish
   recent observations, cached facts and unavailable evidence.

The repository table answers “what is checked out here?” The separate PR table
also tracks enlisted work no longer checked out. Services, next actions and
retirement observations have their own sections when available.

## Git columns and counts

| Representation | Meaning |
| --- | --- |
| `±3` | Three entries from Git's porcelain status, not three commits or a line count. |
| `·` in the working-tree column | Clean working tree. |
| `⚠` in the working-tree column | An operation is in progress without the unmerged-path conflict flag. |
| `✗` in the working-tree column | Git has unmerged paths in this checkout. |
| `↑2` | Two commits ahead of the upstream, or the development-ref fallback. |
| `↓1` | One commit behind the development tip or an open PR's base. |
| `↓1*` / `·*` | The behind count uses a collection-local base override. `?` in the live view lists each override's ref and reason. |
| `✗3` / `✗` in the behind cell | A local or PR conflict, retaining a nonzero behind count when available. A PR conflict does not imply a dirty checkout. |
| `[gone]` after the branch | Git reports the configured upstream missing. It does not establish remote preservation of local work. |

JSON names `ahead_ref`, `ahead_basis` (`upstream` or `development-tip`) and
`upstream_state`. Ahead and behind need not use the same ref. Neither count
is a changed-file count or a general proof that work needs pushing. Raw counts
can remain after squash landing; see [squash status](squash-status.md).

## Read PR signals as a sentence

Wide PR tables label CHECKS / MERGE / REVIEWS; compact headings are C / M / R.
Each slot keeps its meaning even when another slot is unknown. Numbers use
the longest displayed identifier width; optional link space preserves alignment
when it fits, including rows without URLs.

| Slot | Representations |
| --- | --- |
| C — checks | `✓` success; `✗` failure/error; `●` pending/expected; `·` absent, unknown or unmapped. |
| M — mergeability | `✓` clean/mergeable; `✗` conflicting/dirty; `↓` behind; `⊘` blocked; `·` absent, unknown or another merge state. |
| R — reviews | `✓` approved; `✗` changes requested; `●` waiting; `✎` commented; `∅` no reviewers; `·` absent/unknown/none. |

`✓ ↓ ●` means “checks passed, behind base, review waiting.” `✗ ✓ ✓` means
“checks failed, clean mergeability, review approved.” Neither is a complete
merge decision. Checks can pass while review requests changes; an approval
can coexist with failing checks. A comment is not approval.

`◇` adds draft state. Wide repository cells put the conflict label and draft
marker after the three signal slots. A conflict warning remains visible even
with passing checks. Compact repository cells prioritize the draft/conflict markers and
identifier over optional signals where width permits. When signals fit,
draft state can use spare cells beside the PR number, keeping identifiers
and C/M/R aligned; the merge-slot `✗` carries conflict state without its long
label. If the complete representation cannot fit, prefix markers retain
state and identity before optional signals. In the PR table,
conflict takes priority over draft in STATE. A clean working tree can have a
conflicting proposed PR merge; see [mergeability](status-mergeability.md).

R is the **forge's review summary**, not the separate `taba review` gate.
Inspect current-head local review trust with `taba review status <repo> <pr>
--trusted-local`, and hosted reports/threads with `taba review activity <repo>
<pr>`. A completed bot check is not itself approval.

## PR checks versus TEST and PROD

PR C describes checks for the observed PR head. Shared-branch builds use a
separate tier mapping. The production branch comes from `production_ref`,
then `release_ref`. When neither is set, a development branch named `dev`
uses `master` for production; other development branches use themselves.
An empty `default_ref` falls back to `main` for this mapping.

When development and production resolve to different branches, TEST builds
the development branch and PROD builds the production branch. When they
resolve to the same branch, there is only a PROD tier and no TEST tier.
`origin/` prefixes are removed when matching these branch names.

| Configuration | Build tiers |
| --- | --- |
| `default_ref: origin/main`, no production/release ref | PROD: main only |
| `default_ref: origin/dev`, no production/release ref | TEST: dev; PROD: master |
| `default_ref: origin/main`, `release_ref: origin/release` | TEST: main; PROD: release |
| `default_ref: origin/main`, `production_ref: origin/main` | PROD: main only |

A project build hook can replace or refine the built-in provider. Columns
exist for reported observations and yield at narrower widths; a configured
tier with no reported build need not have a visible column.

| Build cell | Meaning |
| --- | --- |
| `T✓#105` | Successful development-tier build/run 105. |
| `P●#53` | Pending production-tier build/run 53. |
| `T✗#105` / `P✗#53` | Failed/error build observation. |
| `T·` / `P·` | No recognized result in an existing build observation. |
| `T○#104` / `P○#52` | The build commit is known not to contain this repository row's HEAD. The hollow circle replaces the ordinary result glyph. |

T/P identify tiers. The number identifies a provider build/run, not a PR.
A provider can have a link without a number. GitHub's aggregate may cover
multiple checks while its link names one available workflow run; inspect
the run or commit checks for detail. Unfetched commits make ancestry unknown,
rather than proving absence. JSON retains the result, SHA and optional
`head_in_build`. Passing builds do not independently verify deployment or health.
When containment is unavailable, the normal result glyph can still appear;
it does not establish that HEAD was included. Only `○` establishes known
exclusion. Inspect `head_in_build` in JSON for the distinction.

### After merge

In the PR table, merged entries no longer associated with their retained
branch use dots in C/M/R. STATE instead describes the first followed tier
that has not passed with the merge contained in its commit:

| STATE | Reading |
| --- | --- |
| `→ test` / `→ prod` | The observed build does not contain the merge yet. |
| `● test` / `● prod` | A containing build is pending. |
| `✗ test` / `✗ prod` | The tier failed or returned an error. |
| `? test` / `? prod` | Available facts cannot attribute the build to the merge. |
| `✓ built` | Followed, available tiers passed with the merge contained. |
| `merged` | Merged without a more specific established follow-up observation. |

Follow-up starts at the tier corresponding to the PR's base. An absent
recorded base falls back to the first tier; a recorded base no tier builds
is not followed through this mechanism. A missing recorded merge commit
leaves the entry plainly `merged`. Missing providers/tiers do not turn
`✓ built` into a universal release certificate.

A merged PR still associated with its retained branch instead shows
`⚠ catch-up`, `⚠ extra` for follow-up commits, or `unknown` landing evidence.
Such an on-branch PR row retains its recorded C/M/R observations; they are
not cleared merely because it merged, and are not a new post-merge gate.
Its title and JSON give the fuller explanation. Older merged entries move
behind the `a` archive toggle after the configured age; hiding does not delete.
Confirmed closed and declined PRs use the same archive age and `a` toggle as
merged entries. Unmerged closures retain a `closed`/`declined` label and, once
the closure is confirmed, show struck-through links and titles in supporting
terminals, both before and after they are archived. NO_COLOR
keeps the textual state and links. GitHub uses its closure date; Bitbucket uses
its last update conservatively, so later updates can delay archival. Missing,
malformed or future dates stay unarchived. A still-checked-out branch also stays
unarchived with the existing orphan warning; closure does not establish landing
or authorize discarding local work. Superseded entries retain their omission rule.
The archive lists the most recent terminal event first, across repositories:
the merge time of a merged PR, the provider's closure time of a closed one
(JSON `closed_at`). A Bitbucket decline is dated by the update that entered
its current declined state in the PR's activity log, from the same bounded
read that dates a Bitbucket merge; `updated_on` never stands in. Unlike a
merge, a decline can be reopened, so it is dated only when
`twg` reads with a known credential (a `TWG_BBC_TOKEN` environment token or a
host read credential; see [status-auth-cache.md](status-auth-cache.md)). That
read is cached under the PR's `updated_on` and serves later runs until a
reopen or re-decline changes `updated_on` (assumed from Bitbucket's
last-update field; not verified against a live provider) or the credential
changes. An incomplete or malformed reply is not cached: the next status run
retries it once, within the same request budget and cooldowns. A `twg`
profile has no local identity and `bb` does not report whether its activity
list was cut short, so under either a decline stays unknown and costs no
activity read. The time is also unknown when the log shows a later reopen,
has an undated state update, fills its 200-entry page or cannot be read.
Rows with unknown or malformed times follow the dated ones, and equal or
unknown times keep a stable repository and number order.
Observation and refresh times never reorder the archive, and sorting makes no
forge calls.

## What is detected and supported

| Observation | Source and limits |
| --- | --- |
| Local Git | Branch/detachment, upstream, raw counts, porcelain changes, operations and unmerged paths. No forge needed. |
| PR discovery | Built-in `github.com` and `bitbucket.org` adapters, verified repository/branch matching and local enlistment. Unsupported origins do not invent an association. |
| GitHub PR checks | Status-check rollup. Observed failure wins; unfinished/unrecognized conclusions remain pending. Neutral/skipped conclusions count as settled positive observations. This is not an audit of required branch-protection rules. |
| Bitbucket PR checks | Commit statuses and applicable pipelines for the reported head. Incomplete collection cannot claim clean absence or success. |
| GitHub reviews | Review decision, latest reviews, requests and draft state distinguish approval, requested changes, comments, waiting and no reviewers. |
| Bitbucket reviews | Participant approval/changes-requested summary. An observed approval can summarize as approved even with other review work remaining. |
| Mergeability | GitHub's merge state, with explicit conflict taking precedence. Bitbucket can use a local prediction for exact reported source/base objects already available. This prediction fetches nothing and starts no merge. |
| Branch builds | GitHub branch-tip checks, Bitbucket Pipelines or a project build hook. Available local objects control containment attribution. |
| Runtime | Opt-in task declarations and runner observations: service state, readiness, address/login problems, resource hooks and exposure. |

Authentication, registered refs and available objects affect coverage. This
guide describes local terminal status; the [OpenClaw panel](openclaw.md)
has a separate renderer and gateway-managed workflow.

## Runtime and other sections

LOCAL groups task marks by service: `w/T b` can mean web with a tunnel, then
a background task. Default marks come from task names; uppercase default
tunnel marks indicate public/unknown exposure. Marks can be configured.
Color supplies state; SERVICES names the task, its most urgent member state,
addresses and providers. Readiness begins with a startup probe; later address
or login trouble can still show `no answer` or `login`. Provisioned resources
are distinct from running services. Use `taba svc` and logs for detail.

`n` opens explicit next-action choices; the view rechecks status before acting.
Displaying status does not dispatch an agent prompt. Retirement observations
separate preservation blockers from unknown coverage; see
[retirement](retirement.md). `--procs` is a separate herdr process view.

## Freshness, missing facts and controls

The live header shows refresh timing and the running CLI version. The open-PR
list of a repository is the only routine forge read for its pull requests. It
is read once and shared by every pane for 90 seconds on GitHub and 180 seconds
on Bitbucket, whose hourly allowance is much smaller. `WTC_FORGE_CACHE_AGE`
sets one age for both. A PR on the list takes its state, draft flag, head and
title from it; on GitHub also its checks, reviews and mergeability.

A PR missing from a current, complete list is no longer open. Status reads it
by number once per list to learn how it ended, and shows a read made by another
pane since that list instead of repeating it. A PR whose last observation is
closed, declined, or merged with settled checks is not read again by background
refresh. A reopened PR shows open again when it is back on the list. A merge
whose checks are still running is read once per list until they settle.
Recorded merges and declines are reused without any read. A Bitbucket decline
is recorded in `.wtc-prs` (`# declined-pr repo number updated-on
[declined-at]`) the first time a pass observes it. A current open list that
carries the PR again, even a capped one, shows it open and removes the record.
A failed list, one served stale after a refused read, or a provider cooldown
keeps the recorded decline. GitHub closures are not recorded; their last
observation is kept in the forge cache.

A PR that was never observed and is missing from a complete list cached before
the pass may be newer than the list. Status then reads the list again and shows
the PR from it without a read of its own. A list that still lacks the PR is not
read again for it, so the PR's own read gets the next token.

A list cut short by its page limit confirms nothing about the PRs beyond it.
Those keep their last observation, `deferred` with reason `list-truncated`, and
are not read on their own in the background; `r` reads each of them once. When a list read is refused, every PR of that
repository keeps its last observation, `deferred` with the refusal as the
reason. When a list read fails, the PRs keep it as `unavailable`. A Bitbucket
host without a REST credential reads PRs through its CLI only and has no open
list. Status reads its PRs by number instead, at most once per 90-second cache
age.

In the collection view, `r` explicitly reads every open-PR list of the pane
again, even when the lists are still current. It reads a PR on its own only
when the PR is missing from its list and its last observation is not a merge:
a closed or declined PR can have been reopened and merged between two lists.
`r` also reads the checks of every open Bitbucket head again. It uses the same
shared request budget and provider cooldown as background readers. When the
budget is empty it waits for the next refill, at most one refill interval at a
time, without priority over background reads. Repeated keys coalesce, and a
request made during collection runs immediately after that collection. Manual
Refresh is not delayed by the background interval. A 30-second phase budget
returns completed live observations alongside deferred outcomes for unfinished
records. It does not date their old facts as new reads. A PR without a turn
keeps its last observed state, marked deferred.

Bitbucket checks are read for the head commit the list reports, charging each
page, head-resolution request and redirect separately. Passed or failed checks
of an unchanged head are kept and not read again by background refresh. Pending,
incomplete or empty checks are read again after the 90-second cache age, since
a run may not have started yet. A rerun on the same head shows after `r` or the
next push. Incomplete checks cannot claim success or absence; a known failure
for that exact head keeps its original observation time when fresh checks are
unavailable. Periodic collection still includes branch discovery, local refs,
runtime and build facts. A Bitbucket branch build comes from the shared recent
pipelines page, or from one read for that branch when the page does not cover
it.

Active PR state observations expire on screen using their original observation
time and the open-list age of their forge, even between background refreshes.
Expiry does not make a provider request or change saved evidence. Missing or
future observation times cannot establish currentness. Confirmed merges retain
their terminal authority; checks and reviews remain separate observations.

The PR STATE cell uses compact `s:age` (stale), `d:age` (deferred), and `u:age`
(unavailable) marks so titles remain visible. `never` means no known observation;
`?` means unknown age. The header shows a refresh in progress. `?` help and `l`
log provide the full last-known state, original observation time, actual admission
reason and retry time when known. Missing admission or budget reasons remain
unknown. Age can establish observation expiration, but never a provider rate limit. Existing landing warnings
remain visible. JSON adds `observation_reason` and `retry_at` without changing
the meaning of `observed_at`. Reading or rendering does not renew observations.
A read refused by the shared budget, a provider cooldown, a cache lock,
cancellation or a deadline names that reason; its older facts are `deferred`.
`observed_at` is the newest observation of the PR's state: a later open-list
confirmation of the same open state can supply it, without renewing freshness,
checks or reviews. When an enlisted PR's branch is checked out and its upstream
is gone, JSON reports `local_merge_evidence` (for example `upstream-gone; head
in origin/main`). That evidence is never a merge. It records nothing, schedules
no read and returns no worktree. A Bitbucket decline keeps archive treatment by
its closure time from any observation, even when the next read is deferred; a
GitHub close needs a current observation.

Interactive startup paints a loading frame before expensive collection or service
discovery. Saved rows appear only after receiving scope validates. Their ages
come from the saved observation timestamps, including when cache files were
copied; missing or future timestamps remain unknown. Help, resize and quit work
while discovery or watcher admission is pending, and cached actions stay blocked
until watcher admission succeeds. Explicit one-shot and cached output flags keep
their existing contracts.

Focus and input are bounded positive hints, not proof of an attached viewer.
Without a current hint, the default full refresh interval is five minutes and
service observations have a one-minute safety interval. A recent hint enables
the normal 30-second full refresh and three-second service cadence for up to one
minute. Local declaration/identity changes and service-change signals can wake
observations sooner. Unchanged service declarations reuse validated local
inventory instead of running Git discovery on every service read. Working-tree
changes without a local change signal are caught by the bounded full refresh.
`WTC_STATUS_WATCH` and `WTC_STATUS_WATCH_BG` tune full refresh intervals; manual
`r` keeps its bounded budget wait, coalescing and shared cooldown behavior.
Read-only identity requests share a process-local cache of at most 128 entries.
Each reuse rechecks current canonical paths, ownership, worktree and Git
configuration inputs; it does not reuse branch, working-tree, provider,
retirement or action facts. Registry and receiving scope validate at each
observation. When supported, trusted Git reports only its system/global config
paths for validation. Unknown provenance, includes, symlinks, unsupported Git or
environment overrides fall back to direct reads. No configuration values are
recorded by discovery. Resource coverage still requires measurement under the
actual configuration; isolated fixture settings do not establish that coverage.

No per-view host visibility command or unsupported attached-client API is used.
Painting, including key feedback and new observations, is capped at 30 frames
per second, so scrolling shows within a frame. An idle frame with an unchanged
view writes nothing. Input and collector work run independently, so the paint
cadence does not throttle manual refresh or provider admission.

If a shared cache or budget lock is unavailable, provider reads fail closed:
status reports deferred or unavailable observations instead of dispatching
without shared accounting. The same rule applies to background and one-shot
status reads; it does not increase provider limits.

Each forge has one request budget, shared by every status pane of the user: a
burst of 8 Bitbucket reads refilled at one every 8 seconds (450 an hour), and a
burst of 16 GitHub reads refilled at one every 2 seconds. Every provider read
(list pages, checks, builds, PR details, redirects) takes one token or is
deferred. Nothing is queued or held back for a later read, so no token stays
unspent while a read is deferred. A rate-limit response pauses that forge for
every pane for its `Retry-After`, or 15 minutes when it gives none.
`WTC_FORGE_MIN_INTERVAL` replaces the budget with one read per that many seconds;
`0` turns it off. Older releases share the same budget file and lock; the
confirmation and waiter files they still write are ignored. A deferred PR that
was never observed shows its checks as
`UNKNOWN` (`·` in the interactive view), not pending, and names the actual
refusal reason and the earliest retry time instead of only `never-observed`.

Quit cancels discovery and watcher waits, selected PR/build provider reads,
the read-only build hook, guarded retirement Git reads, and queued
cache/admission waits. Snapshot publication
checks cancellation before committing each file. Already committed observations
remain authoritative. Filesystem I/O already in progress and arbitrary hook
descendants are not universally cancellable; this is not a promise of complete
collector process-tree cancellation.

```sh
taba status                 # interactive live view in a terminal
taba status --no-watch      # one pass
taba status --json          # raw observations and provenance
taba status --cached --json # completed snapshot; no Git or forge calls
taba status --local --json  # local preview; no forge calls
```

`taba status --all` sweeps every collection in the workspace. A collection whose
context cannot be opened, for example because its generated environment names a
control root the receiving policy rejects, is listed under unreadable collections
with a fixed reason; the other collections are still read and their shared owners
refreshed. A local sweep also lists a collection whose checked-out repository has
no resolvable Git owner, naming that repository. Nothing in a rejected collection
is run, fetched or written. Such a partial sweep still prints its report, adds an
`unreadable` list to `--json`, and exits nonzero. A forge credential selection
that changes during the sweep ends it instead. Dirty worktrees are observed as
usual. `taba catch-up` keeps its own refusals and exit status.

`--forge` previews Git and forge facts without writing a completed snapshot. It
does not record merges, repair PR references or move any worktree; with
`--no-fetch` it changes no Git state at all. The status view's check before it
runs a selected next step uses the same preview.
`--cached` preserves the snapshot producer version and age; the live TUI shows
its own process version. See [version display](status-version.md).

Normal status refreshes can fetch eligible remote refs, record final merged-PR
facts, and return eligible clean merged worktrees to the development tip under
the existing return policy. Set `WTC_STATUS_RETURN_MERGED=off` (or
`[status] return_merged = false`) to retain branches for inspection. Use the
local/forge preview modes or cached reads when you want their stated write
boundaries. None of this submits a new PR, merges one on the forge, or dispatches
an agent prompt merely because its row is displayed.

Dots collapse multiple raw states. A hidden column, clipped `…`, missing
observation and known failure are distinct. Widen the terminal or inspect
logs/JSON for detail. Color supports the text and position; it does not replace them.

`?` explains the view; arrows, Page Up/Down and Home/End scroll; `a` toggles
archived PRs; `q` quits. Repository, branch, PR and build cells are terminal
hyperlinks that open with the terminal's modifier-click. `NO_COLOR` keeps
links; `TERM=dumb` uses plain text.

### Mouse, selection and copying

By default the live view does not capture the mouse. Dragging and
double-clicking select text with the terminal or multiplexer as in any other
program, and its normal copy shortcut copies. That selection covers the rows on
screen: the live view uses the alternate screen, which has no scrollback, and
a terminal may drop a selection when the cells under it are redrawn.

For the wheel, status asks the terminal whether it translates the wheel into
arrow keys on the alternate screen (DEC mode 1007, alternate scroll). If the
terminal reports the mode off, status turns it on, so each wheel step arrives as
↑/↓ and moves one row; status turns it off again when it exits or hands the
terminal to the selection view. A terminal that already has the mode on, or
that does not answer, is left unchanged; there the wheel follows the
terminal's own setting, and the keys always scroll.

`--mouse` captures the mouse instead, as before: the wheel scrolls in the
view (three rows per step), repository, branch, PR and build cells and the
refresh marker act on a left-button release on the cell where the button was
pressed (a drag never opens a link), and LOCAL marks show runtime text on
hover. Native selection then needs the terminal's selection modifier (for
example Shift- or Option-drag). `--no-click` disables those click targets,
not native links or wheel scrolling.

To copy rows outside the view, press `v`. The selection view leaves the live
screen and writes the complete current output, including rows outside the
viewport, to the terminal's normal screen with the time it shows. Select,
scroll and copy with the terminal or multiplexer; in a multiplexer pane its
own copy mode works too. That output stays as it is while you select. Enter,
`q` or `v` returns to the live view; Ctrl+C quits.

Refreshes already running continue meanwhile. Returning shows their results
with their original observation times; neither opening the selection view nor
copying reads the forge or starts another refresh. The written output remains
in the terminal's scrollback after you return, within the terminal's own
scrollback and clipboard limits.

Repository JSON facts expose `comparison_ref` and, for registered repositories,
`development_base`. These fields reflect a collection override when one is set.
Repository rows stay one line each. A `*` after the `↓` count marks a count
taken against the override; an open PR's base replaces the comparison ref and
then the count is unmarked. The ref and reason are details: `?` in the live view
lists them once per distinct ref and reason, with any PR comparison ref. Plain
terminal output adds a blank line and one note line: the number of
overridden repositories, the `repo base show` command and as many of their
names as fit the width. Markdown,
JSON and `repo base show` retain the full reason.
TEST follows the effective base; PROD keeps the registry mapping above. See
[repository bases](repository-bases.md).
