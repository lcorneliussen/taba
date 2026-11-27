# Forge operations — Bitbucket client contract

Contract version: **3** (issues [#127](https://github.com/lcorneliussen/taba/issues/127),
[#471](https://github.com/lcorneliussen/taba/issues/471)).

This is the single list of every Bitbucket operation taba performs, which
client performs it, and the rules a client change must keep. Later migration
steps update this file in the same change that moves an operation.

## Clients

| Client | Role | Account it acts as |
|---|---|---|
| TWG CLI (`twg`) | default client for every Bitbucket read, and the default review write route (see [Writes](#writes)) | reads: the user's own TWG profile, or the host read credential for one repository (see below); writes: the user's own TWG profile only |
| `bb` CLI | deprecated: reads and review writes only when selected explicitly | the `bb` CLI's own login |
| direct REST (`api.bitbucket.org/2.0`) | status lists, check evidence; summary, inline and reply comment writes when explicit REST credentials are set | reads: the host read credential for one repository, else credentials from the environment, else the deprecated `bb` config-file fallback (read as a file; `bb` is not run); see [status-auth-cache.md](status-auth-cache.md). Writes: only the explicit environment credentials named under [Writes](#writes) |

The clients can act as **different accounts**. Taba therefore never treats
them as interchangeable:

- An operation uses exactly one selected client. A failure, an
  authentication or authorization denial, an incomplete answer or a timeout is
  reported as unavailable (`UNKNOWN`), never retried through another client.
- A read may only fall back to another client where the two are proven to act
  as the same actor with the same permissions, or the user explicitly chose
  that route. No such fallback exists today.
- A write takes one route, chosen before anything is sent (see
  [Writes](#writes)). A failed or uncertain write is never retried, through
  the same route or another; an ambiguous result stays pending until
  reconciled. The summary-comment create-intent, saved-id and receipt guards
  apply to every route.

## Selecting the client

`TABA_BITBUCKET_TRANSPORT` (legacy alias `WTC_BITBUCKET_TRANSPORT`) selects
the client for adapter reads, catch-up, and review writes that neither a
gateway nor explicit REST credentials route first (see [Writes](#writes)).
It is resolved when each operation runs, with the same precedence as the TWG
site default:

1. the process environment, when set — even set empty;
2. the machine defaults file in the control root (`taba.env`, or legacy
   `wtc.env`), which must be a regular file inside the control root;
3. otherwise the default, `twg`.

A defaults file that cannot be read safely leaves the selection unknown: the
operation fails as unavailable instead of falling back to the default.

| Value | Effect |
|---|---|
| unset, empty, `twg` | TWG — the default since #471 |
| `bb` | the deprecated `bb` client, kept for compatibility until its removal ([#127](https://github.com/lcorneliussen/taba/issues/127)) |
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

A host whose Bitbucket reads must not use a personal login opts in with
`TABA_BITBUCKET_READ_CREDENTIAL` (legacy alias `WTC_BITBUCKET_READ_CREDENTIAL`;
same precedence as the client selection). The value names the credential
**type**; taba never guesses it from what a helper answers, because the types
authenticate differently and are not interchangeable:

| Value | Credential | Sent as |
|---|---|---|
| unset, empty, `personal` | the client's own login | — |
| `git-credential` | a repository access token per repository | bearer: `TWG_BBC_TOKEN` with an empty `TWG_BBC_USER`; REST `Authorization: Bearer` |
| `git-credential-api-token` | a scoped API token (a person's or a service account's, such as the gateway login proposed in openclaw-bitbucket#32) | its user and token: `TWG_BBC_USER`/`TWG_BBC_TOKEN`; REST Basic |

Any other value, or a host credential with `bb` selected, makes every
Bitbucket read unavailable with a fixed diagnostic; nothing runs. The
`git-credential-api-token` type is covered by synthetic fixtures only; no
gateway login helper exists yet, so it is not claimed to work with one.

With `git-credential`, the first read of a repository in an operation runs

```text
git -c credential.interactive=false -c credential.useHttpPath=true credential fill
```

with `protocol=https`, `host=bitbucket.org`, `path=<workspace>/<repo>.git`,
`GIT_TERMINAL_PROMPT=0` and empty askpass programs, bounded to 10 s. git
itself needs the helper to answer a `username` as well (any value); without
one, the fill fails and the repository has no host credential. For a
repository access token only the `password` is used and the `username` is
ignored; for an API token the `username` is required (one line, no `:`) and
is the user the token authenticates as. The credential type, user and token
together are the observation's identity (a keyed digest), so the two types
never share cached observations. No helper is ever asked to store or erase. The answer is
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

### Trust boundary

The host's git credential helper is a **trusted-process mechanism**, not a
secret store. Any process that runs as the same host user and can run `git
credential fill` (or read the helper's storage) gets the same token, and that
includes an agent command run on the host. taba keeps the token out of the
places it controls (listed above), but it does not enforce an execution
boundary and does not hide the token from arbitrary commands on the host.
Configure a host read credential only where every process that can reach the
helper is trusted with read access to those repositories: a read-only,
per-repository token limits what such a process can do. Keeping the token
from agent commands needs a boundary outside taba, such as a gateway that
runs agent commands where the helper is not reachable; taba does not provide
or check one.

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

State of `main` at contract version 3. `bb` = `bb` CLI, REST = direct HTTP.
"Budget" is the shared status request allowance and cooldown. Write rows list
the routes in the order of [Writes](#writes); a gateway route is a confirmed
plugin write and is left out.

| Area | Operation | R/W | Client | Bounds and guards |
|---|---|---|---|---|
| Audience | repository visibility for review audience | R | adapter (`twg repo get`) | 15 s; any error = unknown; never cached |
| Status | open PR list | R | REST, paginated (`next`, ≤10 pages) | budget; auth-scoped cache; truncation marked |
| Status | PR by source branch (after a truncated list) | R | REST, one page | budget; auth-scoped cache |
| Status | PR detail, once after the PR leaves the open list | R | adapter (`twg … get`) | 30 s; budget; auth-scoped cache, operation-private through TWG |
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
| Review | summary comment create/update | W | REST (explicit credentials), else `twg … comment create/update` (default) or `bb pr comments add/edit` (selected) | create-intent file, saved id, receipt; a missing, conflicting or malformed id stays pending |
| Review | inline comment create | W | REST (explicit credentials), else `twg … comment create --path --line` (default) or `bb pr comments add --file --line-to` (selected) | marker-key de-duplication; fail closed when prior comments are unreadable |
| Review | inline reply | W | REST (explicit credentials), else `twg … comment create --reply-to` (default) or `bb pr comments reply` (selected) | no automatic retry |
| Review | inline resolve | W | `twg … comment resolve` (default) or `bb pr comments resolve` (selected); REST credentials are not used | no automatic retry |
| Review | mark ready | W | `twg … update --ready` (default) or `bb pr ready` (selected); REST credentials are not used | gated by review status |
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

## Writes

Review writes have migrated: TWG is the default route, and `bb` writes only
when selected explicitly. A write takes one route, chosen before anything is
sent from the first row that applies, and is never retried, through the same
route or another:

| Where | Route |
|---|---|
| A gateway that keeps logins away from commands (`BITBUCKET_PUBLISH=gateway`) | a request for the Bitbucket plugin's confirmed pull request writes; the person confirms on the Collection card |
| Explicit `BB_API_USER`/`BB_API_PASS` or `BB_USERNAME`/`BB_API_TOKEN` in the environment | REST comment writes: summary create/update, inline create and reply. Resolve and mark ready fall through to the next rows |
| `TABA_BITBUCKET_TRANSPORT=bb` | the deprecated `bb` client |
| otherwise | the person's own TWG login: `pull-requests comment create/update/resolve`, `pull-requests update --ready` |

Only those two explicit pairs select the REST write route. Taba never uses
its other read credentials for a write: not `ATLASSIAN_ACCOUNT_EMAIL`/
`ATLASSIAN_API_TOKEN`, not the token it reads from the `bb` config file, and
not the host read credential. The `bb` route writes with whatever login the
`bb` CLI itself has. Existing create-intent, saved-id and receipt guards apply
to every route.

### Gateway writes

On a gateway, `review post` (summary and inline comments), `review resolve`
and `review ready` write `bitbucket-pr-writes.json` into the worktree's git
directory: a closed set of operations (`comment.create` with optional
`inline`/`parent`, `comment.update`, `comment.resolve`, `pr.ready`,
`pr.update`) for one pull request and head, under an intent id derived from the review. The command
reports that the writes await confirmation. The Collection card shows
**Confirm Bitbucket writes…** for that checkout; the Bitbucket plugin
(`pr-writes`, then `pr-writes-confirm`, openclaw-bitbucket with #33) shows
exactly what it would write and with whose login, writes as that person, and
leaves `bitbucket-pr-writes/<intent>.receipt.json`. Running the command again
applies the receipt: the summary id, inline records and — only for a complete
post — the review receipt. A summary create stays `comment.create-pending`
until its id is known; an unknown outcome stays pending for reconciliation; a
run that stopped asks for the rest under a new intent, because the plugin
carries an intent out once. Without a plugin that has `pr-writes`, the card
says so and nothing is written.

## Gateway card and pull request news

The gateway's checkout status reads a Bitbucket branch's pull request — the
open one, else the latest merged, declined or superseded one — through the
read adapter, with head checks from the budgeted status reader. Declined and
superseded show as closed. Checks that could not be read keep the previous
value, so a later read is not announced again.

### Refused reads

A read that fails is never shown as "no pull request". It is classified:

| Cause | Meaning |
|---|---|
| `no-credential` | the host's credential helper has none for the repository |
| `unsupported-credential` | the configured credential type cannot be used (for example an API token without a user) |
| `unauthorized` (401) | Bitbucket did not accept the credential; it may be invalid, revoked or expired |
| `forbidden` (403) | Bitbucket refused access; a repository permission or token scope may be missing |
| `not-visible` (404) | the repository or pull request was not found or is not visible to the credential |
| `client-unavailable` | the selected client is missing or unsupported |
| `tls` | TLS verification with Bitbucket failed (certificate or handshake) |
| `provider-error` (5xx) | Bitbucket answered with a server error |
| `rate-limited`, `timeout`, `network`, `incomplete`, `other` | transient; retried later |

Messages state the HTTP status Bitbucket answered and offer explanations; they
never assert an expiry or a missing scope, which an HTTP status alone does not
show. `tls`, `provider-error` and the transient causes are retried later and
are not access causes. A TWG failure is classified from its structured status,
code and kind only, never its message text; a REST failure without an HTTP
answer keeps only its class (TLS, timeout, network), never the URL or a
response body.

The first six are access causes. The gateway checkout status carries
`pullRequestRefusal {cause, status, message, access, readIdentity,
observedAt}`; the card shows **⚠ PR read refused** with the message (or **PR
read failed** for a transient cause). `readIdentity` is a non-secret digest of
the read context and `observedAt` is when the read completed. A context whose
credential taba sees (a host token, or `TWG_BBC_TOKEN` in the environment) is
`k:` plus an HMAC of the credential type, repository, user and token under a
**read identity key**: 32 random bytes, mode 0600, created on first use as
`.bitbucket-read-identity-key-v1` in the control root, or in the user's
configuration directory (`taba/`) without one. That key is not the
disposable status cache's key, so clearing the status cache keeps every
identity; a rotated token, another user, repository or control root gives
another one. The key is never needed to read anything; deleting it, or an
invalid key file (never overwritten), only makes each refusal news once more.
Any other context (a TWG profile login taba cannot see, an unavailable
credential or key) is `u:` plus a digest of the client selection only, which
never equals a `k:` identity. PR news tells the conversation once
per read identity and access cause — not per poll, not again after a restart
or a transient failure in between — again when another read identity is
refused (a rotated or switched credential), and once when reads work again. Every
completed Bitbucket read (a pull request, a refusal, or none found) also sets
the checkout's `pullRequestRead {readIdentity, observedAt}`. A result that
completed before one already seen for the checkout is ignored, and only a
read known to be newer clears a refusal: a late, older read or one without its
completion time never announces a recovery.
Only the digest and the cause are kept. `taba status` records `observation_reason:
bitbucket-<cause>` for an access refusal. Messages are fixed text naming the
credential type, never a value, response or stderr.

## Doctor

`taba doctor` shows a `bitbucket-read <repo>` row per Bitbucket repository
(collection doctor: the checked-out repositories; gateway doctor: the managed
checkouts): the client, the read credential and its source. It reads local
configuration only — no client, credential helper or network. `taba doctor
--bitbucket-read-probe` additionally runs, per repository and within 30 s, one
repository read and one one-item pull request list through the selected client
and credential, and reports the classified cause and HTTP status (see
Refused reads) for the credential type it names. The rows are advisory.

## Next steps

Live provider acceptance of the TWG write and list shapes and of the plugin's
un-draft and resolve endpoints; removal of the deprecated `bb` client and of
the `bb` config-file credential fallback, tracked in
[#127](https://github.com/lcorneliussen/taba/issues/127). Until then both stay
in place.
