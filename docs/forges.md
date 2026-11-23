# Forge operations — Bitbucket client contract

Contract version: **3** (issues [#127](https://github.com/lcorneliussen/taba/issues/127),
[#471](https://github.com/lcorneliussen/taba/issues/471)).

This is the single list of every Bitbucket operation taba performs, which
client performs it, and the rules a client change must keep. Later migration
steps update this file in the same change that moves an operation.

## Clients

| Client | Role | Account it acts as |
|---|---|---|
| TWG CLI (`twg`) | default client for every Bitbucket read | the user's own TWG profile, or the host read credential for one repository (see below) |
| `bb` CLI | deprecated: reads only when selected explicitly; review writes until they migrate | the `bb` CLI's own login |
| direct REST (`api.bitbucket.org/2.0`) | status lists, check evidence, summary/inline comment writes | the host read credential for one repository, else credentials from the environment or the `bb` config file (read as a file; `bb` is not run); see [status-auth-cache.md](status-auth-cache.md) |

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
the client for **migrated** operations only. It is resolved when each
operation runs, with the same precedence as the TWG site default:

1. the process environment, when set — even set empty;
2. the machine defaults file in the control root (`taba.env`, or legacy
   `wtc.env`), which must be a regular file inside the control root;
3. otherwise the default, `twg`.

A defaults file that cannot be read safely leaves the selection unknown: the
operation fails as unavailable instead of falling back to the default.

| Value | Effect |
|---|---|
| unset, empty, `twg` | TWG — the default since #471 |
| `bb` | the deprecated `bb` client, for one more release |
| anything else | the operation fails as unavailable with a fixed diagnostic; no client runs |

The default changed from `bb` to `twg` once, at the release that carries
#471, and is reported by doctor as `twg (default)`; it never changes at run
time. A failure through the selected client is never retried through the
other. The selection is resolved once per operation (one status refresh, one
review command, one catch-up) and frozen for it, so a changed selection
applies to the next operation and nothing observed through the previous
client is reused: the selection and the read credential are part of the
status authenticated context, and because neither a TWG profile nor a host
token can be fingerprinted locally, observations made through TWG are kept
only in that operation's memory (see
[status-auth-cache.md](status-auth-cache.md)). GitHub operations never
consult the selection.

## Host read credential

A host whose Bitbucket reads must not use a personal login — a gateway with
one repository access token per repository — opts in with
`TABA_BITBUCKET_READ_CREDENTIAL=git-credential` (legacy alias
`WTC_BITBUCKET_READ_CREDENTIAL`; same precedence as the client selection).
Unset, empty or `personal` keeps the client's own login. Any other value, or
`git-credential` with `bb` selected, makes every Bitbucket read unavailable
with a fixed diagnostic; nothing runs.

With `git-credential`, the first read of a repository in an operation runs

```text
git -c credential.interactive=false -c credential.useHttpPath=true credential fill
```

with `protocol=https`, `host=bitbucket.org`, `path=<workspace>/<repo>.git`,
`GIT_TERMINAL_PROMPT=0` and empty askpass programs, bounded to 10 s. Only the
`password` is used; the helper's `username` is ignored rather than read as an
authentication mode. No helper is ever asked to store or erase. The answer is
kept in that operation's memory for that repository and then:

- added to that repository's TWG read child only, as `TWG_BBC_TOKEN` with an
  empty `TWG_BBC_USER` (bearer authentication, as a repository access token
  needs; `x-token-auth` as user is rejected by Bitbucket for this);
- sent as `Authorization: Bearer` on that repository's same-origin REST reads.

It is never set in taba's own environment, an argument list, a file, a cache
key or path, a diagnostic, a write, or an agent command; TWG and helper
stderr are discarded. No answer, a failed or timed-out helper, or a denied
read is reported as unavailable for that repository and never falls back to
a personal login. A new operation asks again, so a rotated or removed token
applies to the next refresh.

`taba doctor` in a collection shows a `bitbucket-client` row with the
selected client and where it came from (environment, `taba.env`/`wtc.env` or
default). When TWG is selected it also runs `twg --version` once and reports
whether that version is supported. The row is advisory: it never fails doctor,
and it never contacts a forge, reads credentials or probes an account. A
configured value that is not understood is reported without echoing it.

## Migrated operations

Every Bitbucket read below goes through one adapter
(`internal/wtc/bitbucket_read.go`) with explicit `--workspace`/`--repo`,
`--output json --output-summary none`, the version check described for
catch-up, a 30 s bounded child, completeness decoded at every level, and a
failure envelope classified without echoing messages:

| Read | TWG operation | Pinning and completeness |
|---|---|---|
| PR detail (status, review gate, review head, bundle metadata, open review) | `pull-requests get <n> --strict` | id and destination repository must match |
| merge time | `pull-requests activity <n> --strict --limit 200` | entries naming another PR are refused; 200 entries = incomplete |
| review comments (gate, bundle) | `pull-requests comment query <n> --limit <cap+1>` | more than the cap = incomplete; entries naming another PR are refused |
| repository visibility | `repo get` | `full_name` must match; anything else is `unknown` |
| PR discovery by branch | `pull-requests query --scope repo --state OPEN --source <branch>` | a `next` page or more than the limit = error |

Shapes beyond `pull-requests get` follow TWG help and synthetic fixtures
only, not a live capture.

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
- `--strict` and completeness metadata are decoded strictly at every level
  that can carry them — the envelope, the batch item and the resource. Only
  `partial` absent or `false`, `_failures` absent, `null` or empty, `ok` absent
  or `true` (required `true` on a batch item) and `error` absent or `null`
  count as complete. A `partial: true`, a non-empty `_failures` list, a wrong
  type, an `error` beside a success, a batch that is not exactly one successful
  item for the requested input, or a non-zero exit status is unavailable, never
  empty or terminal.
- An `ok: false` failure envelope is a failure whatever the exit status. Only
  `error.code`, `error.kind` and `error.statusCode` are read, to choose between
  "authentication or authorization failed" and "TWG reported error `<code>`";
  messages, repair text and diagnostics are never echoed. A code that is not a
  plain identifier is shown as `unrecognized`.
- Identity pinning: the resource `id` must equal the requested number and
  `destination.repository.full_name` must equal the requested
  `<workspace>/<repo>` (case-insensitive), or the answer is refused.
- Decoded fields: `state`, `draft`, `destination.branch.name` (required for an
  open PR), `source.commit.hash`, `merge_commit.hash`. The accepted states are
  those catch-up already accepts from `bb` (`OPEN`, `MERGED`, `DECLINED`,
  `SUPERSEDED`, and `CLOSED`, which Bitbucket does not use); anything else is
  refused.
- Version: before its first read in a process, the reader runs
  `twg --version` once per resolved executable. TWG 1.x at or above 1.4.4 is
  accepted (a pre-release of 1.4.4 is not); older versions, another major
  version or unparseable output are refused before any read. An unsupported
  answer is remembered per executable (path, size and modification time), so
  an upgrade in place is asked again; a version check that fails to run is not
  remembered and is retried by the next read.
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
| Audience | repository visibility for review audience | R | adapter (`twg repo get`) | 15 s; any error = unknown; never cached |
| Status | open PR list | R | REST, paginated (`next`, ≤10 pages) | budget; auth-scoped cache; truncation marked |
| Status | PR by source branch (after a truncated list) | R | REST, one page | budget; auth-scoped cache |
| Status | PR detail, foreground and full refresh | R | adapter (`twg … get`) | 30 s; budget; auth-scoped cache, operation-private through TWG |
| Status | merge time from activity | R | adapter (`twg … activity`) | 30 s; auth-scoped cache, operation-private through TWG |
| Status | commit build statuses and pipelines for a head | R | REST, paginated (≤10 pages) | budget; incomplete evidence never passes |
| Status | abbreviated head resolution | R | REST | budget; cached |
| Status | branch pipelines | R | REST, one page | budget; auth-scoped cache |
| Review | PR head for checks and before posting | R | adapter | 30 s |
| Review | PR metadata for a bundle | R | adapter | 30 s; falls back to local refs |
| Review | prior review comments | R | adapter (cap 1000) | reaching the cap = completeness unknown |
| Review | review status gate | R | adapter (PR, comments) | 30 s each; trusts only exact comment ids and `#comment-<n>` links |
| Open review | PR discovery by branch | R | adapter | 15 s; reaching the limit is an error |
| Catch-up | enlisted PR state | R | `twg` (default) or `bb pr view` (selected) | 30 s; no budget, no cache; strict decoding and identity pinning with TWG only |
| Review | summary comment create/update | W | REST, else `bb pr comments add/edit` | create-intent file, saved id, receipt; a missing, conflicting or malformed id stays pending |
| Review | inline comment create | W | REST, else `bb pr comments add` | marker-key de-duplication; fail closed when prior comments are unreadable |
| Review | inline reply and resolve | W | REST reply / `bb pr comments reply`; `bb pr comments resolve` | no automatic retry |
| Review | mark ready | W | `bb pr ready` | gated by review status |
| OpenClaw | repository search, branch list, clone | R | plugin REST and git with the person's own plugin login | 15–60 s; one page |
| OpenClaw | publication | W | delegated to the gateway's Bitbucket plugin | receipt written first; only the local enlistment is retried |

Release differences: 0.2.7 predates the authenticated status-cache partition
([#441](https://github.com/lcorneliussen/taba/issues/441)); status caches in
0.2.7 are not scoped by credential context, and budget files stay compatible
with 0.2.7 consumers. After 0.2.7, status currentness
([#445](https://github.com/lcorneliussen/taba/issues/445)) changed when status
refreshes these reads, and Bitbucket review receipts
([#444](https://github.com/lcorneliussen/taba/issues/444)) changed how the
summary comment id is read back; neither added a Bitbucket operation.

## Next steps

Writes (review post, reply, resolve, ready, PR title/description) move next,
under the write rules above: locally through the person's own TWG login, on
a gateway only through brokered methods of the Bitbucket plugin, and never
through the host read credential. A per-repository doctor read row follows.
