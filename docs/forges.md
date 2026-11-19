# Forge operations — Bitbucket client contract

Contract version: **1** (issue [#127](https://github.com/lcorneliussen/taba/issues/127)).

This is the single list of every Bitbucket operation taba performs, which
client performs it, and the rules a client change must keep. Later migration
steps update this file in the same change that moves an operation.

## Clients

| Client | Role | Account it acts as |
|---|---|---|
| `bb` CLI | existing reads and writes; deprecated compatibility once an operation has a verified replacement | the `bb` CLI's own login |
| direct REST (`api.bitbucket.org/2.0`) | status lists, check evidence, summary/inline comment writes | credentials from the environment (and, for reads, the `bb` config file); see [status-auth-cache.md](status-auth-cache.md) |
| TWG CLI (`twg`) | target primary client, one operation at a time, only when selected | the user's own TWG profile and site selection |

The clients can act as **different accounts**. Taba therefore never treats
them as interchangeable:

- An operation uses exactly one selected client. A failure, an
  authentication or authorization denial, an incomplete answer or a timeout is
  reported as unavailable (`UNKNOWN`), never retried through another client.
- A read may only fall back to another client where the two are proven to act
  as the same actor with the same permissions, or the user explicitly chose
  that route. No such fallback exists today.
- Writes (not migrated yet) will pin provider, repository, PR, head, actor and
  client, persist known IDs before acknowledging, and keep an ambiguous result
  pending until reconciled; they are never retried through another client.
  The existing summary-comment create-intent and receipt guards stay in force.

## Selecting the client

`TABA_BITBUCKET_TRANSPORT` (legacy alias `WTC_BITBUCKET_TRANSPORT`) selects
the client for **migrated** operations only:

| Value | Effect |
|---|---|
| unset, empty, `bb` | the existing `bb` client — the default, unchanged |
| `twg` | TWG for migrated operations |
| anything else | the operation fails as unavailable with a fixed diagnostic; no client runs |

An installed `twg` alone changes nothing. The selection is read from the
process environment when the operation runs; it is not yet a machine default
in `taba.env`, and `taba doctor` does not report it. Operations that are not
migrated ignore it. GitHub operations never consult it.

## Migrated operations

### Catch-up: enlisted pull-request state

`taba catch-up` reads the state of each PR enlisted for a branch to decide
whether the branch landed, is still open against a merge target, or needs its
owner. With `twg` selected it runs, once per enlisted PR:

```text
twg bitbucket pull-requests get <number> --workspace <workspace> --repo <repo> \
    --strict --output json --output-summary none
```

with `TWG_AGENT_DEFAULTS=1` added to that child process only.

- Explicit target: workspace and repository come from the validated
  `bitbucket.org/<workspace>/<repo>` origin; the read never relies on TWG's
  local-remote inference or a `me`/global scope.
- No optional hydration (`--comments`, `--statuses`, `--diff`, `--full`).
- `--strict` and the batch fields are decoded strictly: a top-level
  `partial: true`, a non-empty `_failures` list, a batch that is not exactly one
  successful item for the requested input, or a non-zero exit status is
  unavailable, never empty or terminal.
- An `ok: false` failure envelope is a failure whatever the exit status. Only
  `error.code`, `error.kind` and `error.statusCode` are read, to choose between
  "authentication or authorization failed" and "TWG reported error `<code>`";
  messages, repair text and diagnostics are never echoed. A code that is not a
  plain identifier is shown as `unrecognized`.
- Identity pinning: the resource `id` must equal the requested number and
  `destination.repository.full_name` must equal the requested
  `<workspace>/<repo>` (case-insensitive), or the answer is refused.
- Decoded fields: `state`, `draft`, `destination.branch.name` (required for an
  open PR), `source.commit.hash`, `merge_commit.hash`. States other than
  `OPEN`, `MERGED`, `DECLINED` and `SUPERSEDED` are refused.
- Version: before its first read in a process, the reader runs
  `twg --version` once per resolved executable. TWG 1.x at or above 1.4.4 is
  accepted; older versions, another major version or unparseable output are
  refused before any read. A failed version check is not remembered.
- Deadline: each call is bounded by the catch-up forge timeout (30 s) and the
  owned process is killed when it expires. Processes that TWG itself starts are
  not tracked.
- Cost: one TWG process per enlisted PR, plus at most one version process per
  run — the same dispatch count as `bb`. Requests TWG makes internally
  (pagination, retries) are opaque to taba and are not counted per request.
  Catch-up reads use neither the shared status request budget nor a response
  cache, before or after this change.
- Diagnostics keep the existing public catch-up shape:
  `<forge>/<repo> PR #<n>: twg bitbucket pull-requests get failed: <cause>`,
  with `branch left untouched`.

Branch-only discovery (a branch with no enlisted PR) remains unsupported on
Bitbucket with either client.

## TWG contract evidence and gaps

Verified against TWG **1.4.4** command help and `help describe` output only —
no account, tenant or repository data was read:

- `bitbucket pull-requests get <id...>` (max 25 ids): `--workspace`, `--repo`,
  `--strict` ("exit with failure if any batched PR read fails or is incomplete;
  keep available results"), `--batch-concurrency` (max 5), optional hydration
  flags.
- Global output controls: `--output json`, `--output-summary none` (structured
  output directly on stdout), `--select`, `--agent-fields`.
- Advertised collections: `items`, `_failures` (with `source`, `operation`,
  `message`, `statusCode`), and the batch fields `items[].input`, `.ok`,
  `.error`, `.meta`, `.data`, plus top-level `summary` and `partial`.
- Failure envelope: `ok: false` with `error.code`, `error.kind`,
  `error.message`, `error.repair` and `error.retry`.

Gaps, handled conservatively rather than by guessing:

1. The advertised field presets do not list `draft`, `source.commit.hash` or
   `merge_commit.hash`. The reader requests the full resource (no `--select`)
   and treats them as optional: a missing `draft` reads as not draft (catch-up
   handles open and draft alike); a missing head or merge commit falls back to
   git-only landing checks, which can only leave a branch for its owner.
2. No documented catalogue of `error.code` values, including which ones mean
   authentication or authorization. Any failure is unavailable; the code only
   selects the diagnostic wording. Fixture codes are placeholders.
3. Help documents the batch (`items`) shape but not whether a single id is
   returned bare or as a one-item batch. Both are accepted and pinned.
4. TWG does not expose a stable, non-secret account identity for its selected
   profile without an extra call. Nothing read through TWG is cached, so no
   TWG observation can be shared across profiles, hosts or accounts.

A bounded upstream request would close gaps 1–3: advertise `draft`,
`source.commit.hash` and `merge_commit.hash` for `pull-requests get`, document
the single-id output shape, and publish stable `error.code` values for
authentication and authorization failures.

## Operation inventory

State of `main` at contract version 1. `bb` = `bb` CLI, REST = direct HTTP.
"Budget" is the shared status request allowance and cooldown.

| Area | Operation | R/W | Client | Bounds and guards |
|---|---|---|---|---|
| Audience | repository visibility for review audience | R | `bb repo view` | 15 s; any error = unknown; never cached |
| Status | open PR list | R | REST, paginated (`next`, ≤10 pages) | budget; auth-scoped cache; truncation marked |
| Status | PR by source branch (after a truncated list) | R | REST, one page | budget; auth-scoped cache |
| Status | PR detail, foreground and full refresh | R | `bb pr view` | 30 s; budget; auth-scoped cache |
| Status | merge time from activity | R | `bb pr activity` | 30 s; auth-scoped cache |
| Status | commit build statuses and pipelines for a head | R | REST, paginated (≤10 pages) | budget; incomplete evidence never passes |
| Status | abbreviated head resolution | R | REST | budget; cached |
| Status | branch pipelines | R | REST, one page | budget; auth-scoped cache |
| Review | PR head for checks and before posting | R | `bb pr view` | budget for checks; no deadline |
| Review | PR metadata for a bundle | R | `bb pr view` | no deadline; falls back to local refs |
| Review | prior review comments | R | `bb pr comments list --limit 1000` | unknown envelope = completeness unknown |
| Review | review status gate | R | `bb pr view`, `bb pr comments list --all` | no deadline |
| Open review | PR discovery by branch | R | `bb pr list --limit 100` / `bb pr view` | 15 s; reaching the limit is an error |
| **Catch-up** | **enlisted PR state** | R | **`bb pr view` (default) or TWG (selected)** | 30 s; no budget, no cache; strict decoding |
| Review | summary comment create/update | W | REST, else `bb pr comments add/edit` | create-intent file, saved id, receipt |
| Review | inline comment create | W | REST, else `bb pr comments add` | marker-key de-duplication; fail closed when prior comments are unreadable |
| Review | inline reply and resolve | W | REST reply / `bb pr comments reply`; `bb pr comments resolve` | no automatic retry |
| Review | mark ready | W | `bb pr ready` | gated by review status |
| OpenClaw | repository search, branch list, clone | R | plugin REST and git with the person's own plugin login | 15–60 s; one page |
| OpenClaw | publication | W | delegated to the gateway's Bitbucket plugin | receipt written first; only the local enlistment is retried |

Release differences: 0.2.7 predates the authenticated status-cache partition
([#441](https://github.com/lcorneliussen/taba/issues/441)); status caches in
0.2.7 are not scoped by credential context, and budget files stay compatible
with 0.2.7 consumers. Status currentness
([#445](https://github.com/lcorneliussen/taba/issues/445)) changed when status
refreshes these reads but added no Bitbucket operation. An open change to
Bitbucket review receipts
([#444](https://github.com/lcorneliussen/taba/issues/444)) alters the review
write rows above; they are updated when it lands, not before.

## Next steps

Further reads (status detail, review head and comment reads) move one at a
time, after the in-flight review and status changes land. A persistent
machine-default selection, `doctor` reporting, an auth-scoped cache identity
for TWG observations and any default switch need their own design and review
first. Writes move last, under the write rules above.
