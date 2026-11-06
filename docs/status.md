# Reading local terminal status

Status combines independent observations: the Git checkout, its pull request,
reviews and checks, and builds of shared branches. Read them together. A green
result in one field does not answer the questions represented by the others.
Installed versions can differ; use `wtc status --help` and `?` in the live view.

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

R is the **forge's review summary**, not the separate `wtc review` gate.
Inspect current-head local review trust with `wtc review status <repo> <pr>
--trusted-local`, and hosted reports/threads with `wtc review activity <repo>
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
Closed, declined or superseded enlisted PRs leave the active PR rows. If their
branch is still checked out, an orphan warning identifies it below the table;
closure alone does not establish landing or authorize discarding local work.

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
are distinct from running services. Use `wtc svc` and logs for detail.

`n` opens explicit next-action choices; the view rechecks status before acting.
Displaying status does not dispatch an agent prompt. Retirement observations
separate preservation blockers from unknown coverage; see
[retirement](retirement.md). `--procs` is a separate herdr process view.

## Freshness, missing facts and controls

The live header shows refresh timing and the running CLI version. Active PR
facts normally use a 90-second forge cache. `r` requests refresh, but a fresh
screen can still contain cached provider facts. `l` opens the refresh log.

```sh
wtc status                 # interactive live view in a terminal
wtc status --no-watch      # one pass
wtc status --json          # raw observations and provenance
wtc status --cached --json # completed snapshot; no Git or forge calls
wtc status --local --json  # local preview; no forge calls
```

`--forge` previews Git and forge facts without writing a completed snapshot.
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

`?` explains the view; arrows, Page Up/Down and the wheel scroll; `a` toggles
archived PRs; `q` quits. Available repository, branch, PR and build targets
use terminal hyperlinks. Modifier-click follows the terminal's convention;
`--no-click` disables ordinary TUI targets, not native links. `NO_COLOR` keeps
links; `TERM=dumb` uses plain text.
