# wtc under OpenClaw

An [OpenClaw](https://docs.openclaw.ai) gateway runs agents for a team. `wtc`
works in an agent's shell there, with one difference from a terminal or herdr:
it cannot create a worktree the gateway will publish, and it cannot push. The
gateway owns both. So under OpenClaw `wtc` checks and plans, and the agent
carries the plan out with the gateway's tools.

`wtc host` prints `openclaw` when `OPENCLAW_SHELL=exec` is set, which the
gateway's exec tool does, or when `OPENCLAW_STATE_DIR` is: agents on another
runtime (Codex's bash tool) and the Control UI's terminal only inherit the
gateway's environment.

## One conversation, many repositories

One OpenClaw session owns at most one managed worktree, and the gateway
publishes a worktree only for its owner. wtc keeps that, but a task is still
driven from one conversation:

| wtc | OpenClaw |
|---|---|
| the collection (one task) | the conversation the task started in |
| a repository in it | a gateway-managed worktree, held by a **hidden owner** (a subagent session) |
| the collection folder | `$OPENCLAW_STATE_DIR/wtc/<harness>/<name>`, or `wtc/ad-hoc/<name>` without a harness (`wtc-collections/<name>` for one made before): a link per repository to its worktree, kept by `wtc status`; services run here |
| the branch | `openclaw/<name>`; `<name>` is the collection's `<issue-id>-<slug>` |

The conversation edits, builds and commits in the worktrees through the
folder. The owners only hold the worktrees for publication, which the
collection card's ↑ starts with the gateway's own `sessions.github.publish`,
as the chat's Publish PR button does for a session's single checkout. A
conversation per repository, for swarming, is an explicit choice:
`wtc add-repo --separate`.

The branch prefix is fixed by OpenClaw. The name must be lowercase letters,
digits and hyphens, at most 64 characters.

## Host compatibility

The plugin supports OpenClaw 2026.10.2 and later. Session reads await
`listSessionEntriesAsync` and `getSessionEntryAsync` when the host exposes them.
Older hosts use a legacy reader only when its corresponding async method is
absent. A failed async read never retries synchronously or authorizes an action
from stale data.

## What the agent runs

| To | The agent runs | And then |
|---|---|---|
| add a repository | `wtc add-repo <repo> --worktree <name>` | calls `sessions_spawn` with each intent's `args` |
| see the collection | `wtc status [<name>]` | reads it: folder, repositories, pull requests, services |
| check a publication | `wtc publish --title "…" --body-file <file>` in `<folder>/<repo>` | tells the person it is ready: they push it with ↑ in the card, which opens the pull request with that title and description. Only when they ask the agent to publish does the owner call `github_publish` |
| catch up | `wtc catch-up` in `<folder>/<repo>` | publishes again to send the merge |

`<repo>` names a project on the gateway, or a GitHub repository as
`owner/name` or URL. A GitHub repository the gateway has already cloned gets a
hidden owner by its project name. One it has not is added with **Add
repository** in the Collection panel (the gateway's `projects.add`, with the
person's authority); `--separate` instead has the gateway clone it for a
conversation of its own (`projectGitUrl`, which OpenClaw allows only for
visible sessions). Other forges: a gateway admin registers a checkout.

Each of the first two prints a plan and does nothing else:

```json
{
  "host": "openclaw",
  "version": 1,
  "intents": [
    {
      "host": "openclaw", "version": 1,
      "action": "publish", "actor": "repo-session",
      "repo": "api", "forge": "github",
      "tool": "github_publish",
      "args": { "title": "Add the thing" }
    }
  ],
  "pending": ["src/thing.go"],
  "warnings": ["The gateway will commit all 1 pending paths. …"]
}
```

`actor` says which session carries the intent out: `parent`, `repo-session`
or `current`. An intent with a `tool` is a call of that tool with `args`,
unchanged. The repository must be a project on the gateway under the name
given to `wtc add-repo`.

After `github_publish` the gateway commits what is pending as its GitHub
identity, pushes the session's branch and opens or updates a **draft** pull
request, once the turn that asked has ended. People merge.

## The pull request's title and description

`wtc publish` records `--title` and `--body` (or `--body-file`, `-` for
stdin) for the branch, in the worktree's git directory, so nothing is
committed. `wtc status --json` reports them per repository as `proposal`, and
the card's ↑ hands them to `sessions.github.publish` and shows the title
before the push. The gateway's limits apply: one line of at most 256
characters, a description of at most 8192. The gateway uses them only when it
opens the pull request. Without a proposal, it opens as "Publish
openclaw/<name>". Bitbucket plugin 0.3.4 or later takes the proposed title and
description when creating a PR and includes the description in its confirmation.
If an older publisher cannot confirm the description, the card requests an
upgrade instead of silently omitting it. Pushes to an existing PR preserve its
title and description.

## Pull request news in the conversation

The card's ↑ runs outside the agent's turn, and a merge or a review happens
on the forge. The plugin compares each look at a collection's pull requests
with the one before and tells the collection's conversation what changed. It
queues one system event (`wtc: pull request news for this collection`, a line
per change) and asks for a turn, so the agent updates its progress without
waiting for the person. It reports a pull request that was opened, became
ready or went back to draft, was merged, closed or reopened, failed or passed
its checks, was approved, got a request for changes, or conflicts with its
base.

The event also says what to do with the news: bring the task's progress up
to date, catch up after a merge, and tell the person only what they need to
act on. On the card, a merged pull request whose merge the checkout doesn't
have yet offers ↓: `wtc status` reports `mergedIn` for it, since a base that
was never fetched doesn't count as behind.

It looks whenever a card loads, and every `pullRequestPollMinutes` (default 5)
at collections with an open pull request. What it saw is kept in
`$OPENCLAW_STATE_DIR/plugins/wtc/pull-requests.json`, so a restart doesn't
repeat news. `pullRequestEvents` is `wake` (the default), `queue` (told on
the conversation's next turn) or `off`. Pull requests come from `gh`, so this
covers GitHub.

## What `wtc publish` refuses

The gateway commits every pending file in the worktree and never rewrites a
published branch. `wtc publish` reads the checkout and refuses before the
agent asks for something that would fail or publish the wrong thing:

- outside a gateway-managed worktree (the parent, or a checkout made by hand);
- when history that was published has been rewritten by a rebase, an amend or
  a reset. It knows from the marker the gateway leaves on the commits it
  publishes;
- during an unfinished merge or rebase, or with unresolved conflicts;
- when uncommitted files look like credentials (`.env`, `*.pem`, `id_rsa`, …).
  `--allow-sensitive` is for the file that is meant to be committed;
- when there is nothing to publish.

For a GitHub branch whose pull request has merged, `wtc catch-up` returns
the checkout to its base tip with HEAD detached. It also does this when an
earlier catch-up already advanced the completed branch. The old branch is
retained, and its PR moves from the repository row to the collection's pull
request list. Follow-up commits must move to a new branch before returning
to the tip; catch-up refuses to leave them behind.

The collection card and panel keep repository/branch, changes, pull request
and next step in stable positions without a visible header row. Small outlined
controls distinguish actions, with clear hover and keyboard-focus feedback.
The top bar keeps the next action available even when the card is collapsed.
At normal widths each repository uses one line with separate fixed columns for
its name, branch, changes, PR and next action. The top action aligns with the row
actions; progress occupies the same next-action slot. Narrow views fold facts
only when needed; long names and branches retain their full text on hover. The row action runs
that step after rechecking status. Catch-up runs on the first click, including
a clean merge into a live branch; active-author and conflict checks still apply.
Judgment work asks the conversation's named skill. Push keeps the personal
account and confirmation flow; merge opens the pull request for the person.
Refreshing the card alone does not execute actions.

Catch-up on the gateway merges locally, without rebasing. For an observed open
PR, the next **Update PR** / **Push** action prepares the branch update with the
personal-account confirmation. Catch-up alone does not publish it. In a local
collection, `wtc catch-up` already pushes merges to open or draft PR branches.

A baseline row consistently says **at main** (or its configured base). Hover
text distinguishes an untouched task branch from detached HEAD; it does not
imply that the checkout owns the main branch.

The repository row links its current PR. Additional PRs share a compact
**4 other PRs** control, whose accessible label separates open and archived counts.
When all additional PRs are archived, it keeps the **N archived PRs** wording,
including when a new current PR appears. **1 archived PR** opens the full
collection PR list in the taba sidebar. The conversation card omits that full
list, keeping its footprint small.

During a batch, the top bar shows the handled count and each row shows one phase.
Afterward, a **Caught up 2** or failure control opens the detailed **Action log**
in the sidebar. The chooser closes when dispatch finishes. The log is shared
between views of that collection for the current browser session; it is not a
durable job history and reloading does not resume work.

Uncommitted files offer **Commit changes**: the conversation reviews intended
changes and saves them as **local commits**. This does not push them or create
a pull request. GitHub's ↑ can also commit pending files as part of preparing
publication; Bitbucket's ↑ sends committed changes only, leaving uncommitted
files local. With only uncommitted files, Bitbucket offers Commit changes
instead of an empty push.

**Pushing** sends commits to the branch on the forge. Its existing PR includes
those commits automatically; a new draft PR is created if none exists. The
result distinguishes confirmed PR creation from updates to an existing PR.
When the host supplies only a URL, the card reports publication without
claiming that it created a PR.

A push to an existing PR keeps its title and description. If the scope changed,
use **Review PR text** in the publication result: the conversation compares
the text with the full current PR diff, preserves useful human edits, and
proposes revisions for you to review. This neither edits the remote PR text
nor requests a code review. PR text can then be edited on the forge or through
a separately authorized operation.

The expanded card shows the latest action result in full. A refused or failed
publication appears as an error even when the gateway supplies only a status
and message.

OpenClaw binds publication to the owner's managed branch. Returning to the
tip does not rebind that owner or permit publishing an arbitrary new branch:
another publication round needs a fresh managed worktree and owner. Support
for starting that round in the same collection is tracked in
[#207](https://github.com/lcorneliussen/taba/issues/207).

On a live branch, `wtc catch-up` merges and never rebases, and aborts a
conflicted merge so the worktree is as before.

## Other forges

OpenClaw publishes to GitHub. For a repository whose `origin` is elsewhere the
gateway still creates and owns the worktree, but brokers no publication. The
publish intent then carries a `forge` and no `tool`, and its `args` describe
the push and the pull request:

```json
{ "action": "publish", "actor": "repo-session", "repo": "docs", "forge": "bitbucket",
  "args": { "owner": "example-ws", "repo": "docs", "source": "openclaw/72-add-thing",
            "dest": "main", "title": "Add the thing", "draft": true } }
```

A skill for that forge carries it out, pushing from the session's worktree
with a login the gateway gives the session. Nothing commits for the agent
there: `wtc publish` refuses a worktree with uncommitted work.

## In a sandbox

When an agent's commands run in an OpenClaw sandbox, the shell sees a private
copy of the worktree: the session's branch, no `origin`, no network and no
GitHub token. There:

- `wtc publish` still refuses what is not safe, but cannot check the forge,
  the base branch or earlier publications. It plans `github_publish` and
  leaves the repository to the gateway, which takes it from the session. It
  ignores the skills OpenClaw mirrors into the copy (`.openclaw/sandbox-skills/`).
- The gateway offers `github_publish` in a sandbox only when it is allowed
  there: `tools.sandbox.tools.alsoAllow: ["github_publish"]`. The tool runs on
  the gateway, so this gives the sandbox no credential.
  `tools.github.allowInSandbox` is not needed and would put the token inside.
- Without the gateway's git service (below), `wtc catch-up` cannot fetch.
  Catch up from the repository's session outside the sandbox.
- A repository on another forge cannot be published from a sandbox.

### The gateway's git service

The private copy is one commit deep, and the commits made in it stay there:
only its files reach the managed worktree. The plugin can serve the gateway's
clones to the sandboxes, read-only, so that an agent there reads history and
catches up with Git's own merge state, and its commits reach the gateway:

```sh
openclaw config set plugins.entries.wtc.config.sandboxGit '{"port":18790}' --strict-json
openclaw config set agents.defaults.sandbox.docker.network wtc-sandboxes
openclaw config set agents.defaults.sandbox.docker.env \
  '{"WTC_GATEWAY_GIT":"http://<gateway>:18790"}' --strict-json
```

The gateway and its sandboxes share the Docker network (`wtc-sandboxes`
here; the gateway's container joins it too), and `<gateway>` is the gateway's
address or container name on it. A bridge network also routes out to the
internet: restricting that is the network's job, not wtc's. Sandboxes
already running keep their settings until `openclaw sandbox recreate`.

The service answers only sandbox containers of this gateway. It takes a
connection's address, finds the sandbox container that has it, the managed
worktree that container works on and the session that owns it, and serves
that session's collection: each repository's collection branches and what
origin has on the gateway. Other collections' branches stay hidden and
cannot be fetched, a push is refused, and no forge credential enters the
sandbox. Then, in the sandbox:

- The checkout gets a `gateway` remote: the collection's branches as
  `gateway/openclaw/…`, origin's as `origin/…`, with all their history.
  The first fetch deepens the copy. Other repositories of the collection
  clone from `$WTC_GATEWAY_GIT/<repo>`.
- `wtc catch-up` merges `origin/<base>` there, as the gateway last fetched it
  (at most five minutes old). A conflict stops the merge for the agent to
  resolve as in any checkout, with `git log --merge` and the stages; then
  `wtc catch-up --continue`. Uncommitted edits are set aside around the merge
  and put back after.
- Then the gateway takes the session's branch, the sandbox's commits and the
  merge included, if it extends the gateway's branch, merges only origin's
  commits and leaves no conflict markers. The commits keep the sandbox's
  author; one that carries a `Co-authored-by` trailer or the publication
  marker is refused, since [credit](#credit-for-the-people-who-asked) is the
  gateway's to add. Its branch and index move to it;
  the files arrive from the sandbox, as every edit does. Publishing stays the
  person's: ↑ in the card.
- If the gateway committed since (a publication), the sandbox's history
  starts over from the gateway's branch, with the files unchanged.
- `wtc doctor` reports `fetch: read-only from the gateway` and fails when a
  credential that could push is in the sandbox.

For a gateway that people with different rights share, sandbox the agents
that do development work. Each repository then gets its own conversation
(`wtc add-repo --separate`), working in its sandbox's private copy, which the
gateway reconciles into the managed worktree and publishes. The collection
folder, one conversation driving several repositories, needs the agent's
commands on the gateway itself, with the trust that implies. Do not bind the
managed worktrees or the gateway's clones into a sandbox to get it: that goes
around the private copy and its reconciliation
([#105](https://github.com/lcorneliussen/taba/issues/105)).

## Git the gateway runs

The card's status, catch-up and publish make the gateway run `wtc` and git in
agents' worktrees, as the gateway's user. A repository's own config can make
git run programs, so wtc does not let it:

- every git command runs with fsmonitor, hooks, signature checking and
  signing turned off;
- a checkout whose repository or worktree config sets anything else that can
  run a program (a filter or merge driver, `core.sshCommand`, an
  upload-pack, an include) is refused, with the key and how to remove it;
- the gateway's global and system config, the admin's, stay in effect, so a
  Git LFS filter configured there still works;
- the branch list for **Add repository** reads only a project's origin URL
  and runs `git ls-remote` outside the clone.

## Credit for the people who asked

The gateway commits as its own GitHub identity and adds a `Co-authored-by`
trailer for each person who prompted the session, provided their gateway
profile has a **verified** GitHub account. Only the sign-in can verify one:
Cloudflare Access with GitHub as the identity provider, or a Tailscale Serve
login made with GitHub. A token, a password, any other identity provider, and
a "My GitHub" connection made for publishing do not. A person who signs in
another way gets no credit unless an administrator links their profile to a
GitHub-verified one.

Repository sessions created with `sessions_spawn`, which is what
`wtc add-repo` asks for, take the parent session's contributors with them, so
people need only talk to the collection's session. This comes from OpenClaw's
documentation; no test here signs in with a verified GitHub account yet.

## Parallel work and sub-sessions

A collection has one focus: per repository one branch and one pull request.
Parallel work, or a second pull request in a repository, is a collection of
its own with its own conversation, which the agent proposes and the person
starts. Subagents that only read (review, research, a test run) need no
worktree.

A **sub-session** is a child session of the conversation with a worktree of
its own: one OpenClaw or the agent spawned with a worktree, or a wtc worker
(`wtc add-repo --worker`, deprecated). wtc does not manage them, but shows
them so that nothing is lost:

- The Collection panel and the card show a sub-session's checkout as a
  sub-row under its repository, compared with the collection's checkout of
  it: ± its uncommitted files (the tooltip says which the collection has the
  same, which differ and which only the sub-session has), **↑** merges its
  commits into the collection's checkout, **↓** catches it up with the
  collection's. It has no pull request. **✕** discards it after asking
  again: it deletes the sub-session's sessions, and OpenClaw removes their
  worktrees. A conflict goes to the conversation.
- `wtc catch-up --from <branch>`, in the collection's checkout of a
  repository, merges another branch of it by its name. It fetches nothing:
  the worktrees share the gateway's clone. `wtc status <name> --sub
  <worktree>` compares a worktree with the collection's (`subSessions` in
  `--json`).
- On a wtc worker's branch (`openclaw/<name>--<worker>`), `wtc publish`
  refuses, `wtc svc` won't add, start or provision services in its
  collection (stopping, listing and logs still work), and
  `wtc catch-up` merges the collection's branch.

## wtc projects

A **wtc project** is a named set of repositories that belong together, with
the instructions for working in them. On a gateway each one is a configured
harness: a repository whose `.harness-repos.yml` lists the project's
repositories and whose `AGENTS.md` holds its instructions. A collection is
one task's worktrees of a project's repositories. An **ad-hoc** collection
works without a project and takes any repository. The projects are
configuration, set by whoever maintains the gateway (the keys keep the
harness name):

```json
"plugins": { "entries": { "wtc": { "config": {
  "harnesses": [
    { "name": "platform", "remote": "https://github.com/example-org/platform-harness.git", "ref": "main" }
  ],
  "defaultHarness": "platform"
} } } }
```

`wtc projects` lists them with their repositories and roles, so that the
agent can match what the person asks to work on before `wtc add-repo` starts
the collection.

A conversation starts without a collection, wherever OpenClaw's New session
put it, and often only its first messages say where the work is. So the card
of a conversation without a collection suggests one: "Work in wtc project
**wtc** with wtc-site?". It suggests from what the person types, once they
pause, and from their latest message. **Start** asks the conversation to run
`wtc add-repo` for it, sending a draft along with that request. In the
agent's main session (Home) it asks for a new conversation for the task
instead. **Change…** opens the panel, and ✕ dismisses it.

A draft that has not been sent is matched by its words only, on the
gateway. Only a sent message goes to a decision model: the gateway's, when
one is selected (`agents.defaults.decisionModel`), as one choice among the
projects' repositories by name and role. No OpenClaw release has decision
models yet, so meanwhile the plugin asks OpenAI's Decisions API itself when
the gateway's environment has `WTC_DECISIONS_OPENAI_API_KEY` (the plugin's
`decisionModel` setting picks the model, `gpt-6-luna` by default). Give the
key that name only: OpenClaw's own OpenAI provider reads `OPENAI_API_KEY`,
and the chat model could then use it. With the key, the person's sent
message goes to OpenAI. Without either, the suggestion comes from matching
the message's words against the names and roles
([#159](https://github.com/lcorneliussen/taba/issues/159) tracks moving
to OpenClaw's decision models).

OpenAI's decision model (`openai/gpt-6-luna`, which needs an OpenAI Platform
API key; a ChatGPT or Codex subscription doesn't authorize it) suits this
best. In a small comparison
([#155](https://github.com/lcorneliussen/taba/pull/155)), on twelve test
messages over two projects, it chose right eleven times,
in about 200 ms and a few hundred tokens each. The local ONNX classifiers
chose right at most six times, mostly the same repository whatever was
asked, and the word match nine times.

Each harness must also be cloned on the gateway (an OpenClaw project), like
the repositories its registry names: **Add repository** in the Collection
panel, or the gateway's own project settings. `ref` defaults to `main`.

A collection works in at most one wtc project:

- **Which one.** The collection keeps the project it started with. A new
  collection takes `--project <name>` if given (`--harness` is the same
  flag under its earlier name); otherwise the registries decide: the
  project that lists the repositories asked for, by name, URL or
  `owner/name` (of several that list some, the ones that list all; of
  several still, `defaultHarness`). When none lists them, `defaultHarness`
  or the only configured project. Otherwise `wtc add-repo` asks the agent
  to ask the person; `--project none` starts an ad-hoc collection. Without
  projects configured, every collection is ad hoc, as before.
- **A repository the project does not list** is the person's choice, even
  with a default project. `wtc add-repo` stops and the agent asks: ad hoc
  beside the project's repositories in this collection (`--ad-hoc`),
  registered in the project first (an entry in `.harness-repos.yml` in the
  collection's worktree of the harness, published like any change), or a
  collection of its own in an ad-hoc project (`--project none` and another
  name). When another configured project lists it, a fourth choice suggests
  a new conversation for that project instead.
- **One project per conversation.** Every configured project can be listed,
  so that the right one is picked, but a collection never reads from or
  writes to another project's repositories unless the person added them ad
  hoc.
- **A worktree like the others.** The first `wtc add-repo` of a collection
  adds a managed worktree of the harness, on the collection's branch, so a
  task can change the harness and publish that like any repository.
- **Names resolve through it.** `wtc add-repo api` takes `api`'s remote and
  `default_ref` from the harness's `.harness-repos.yml`: from the
  collection's worktree of the harness once there is one, before that from
  the gateway's clone at `ref`. `--base` still wins. A URL or `owner/name`
  that is in the registry resolves to its entry; one that is not is the
  person's choice (above).
- **Instructions.** `wtc status` shows the project and the worktree of its
  harness; the agent reads its `AGENTS.md` from there, and it says which
  other repositories a task needs.
- **Every task starts one.** The skill has the agent start a collection
  whenever the person wants to work on a repository, also only to read or
  analyze it, rather than work from memory or another collection.

Not yet: hooks, secret links and mise pins from a harness do not run on a
gateway, and herdr layouts do not apply. Several projects in one collection,
stacked rather than merged, are tracked in
[#100](https://github.com/lcorneliussen/taba/issues/100).

### Repository renames

A gateway project's ID and its existing worktree paths stay stable when its
forge repository is renamed. A clone registered as `widget` can keep that ID
and directory while its origin becomes `example-org/gadget`. Lookup does not
require the directory to have the new forge name. Registry `aliases` retain old
logical names; the current remote's short name also resolves. Plans reuse the
existing project ID instead of requesting a second clone.

Before changing a clone's origin, an operator must verify that old and new URLs
resolve to the same permanent forge repository ID. Record that evidence in
`wtc-origin-aliases.json` inside its Git common directory, as reported by
`git rev-parse --path-format=absolute --git-common-dir`:

```json
{
  "version": 1,
  "origin": "https://github.com/example-org/gadget.git",
  "repositoryId": "12345",
  "aliases": ["https://github.com/example-org/widget.git"]
}
```

Keep this untracked Git metadata private and write it atomically, then update
`origin` to the verified new URL. Do not record aliases merely because names
look similar. A record applies only while its canonical origin matches the
clone; repointing the clone elsewhere invalidates the old aliases. Discovery
reads the record without changing Git state or making forge requests.

Old collections keep their older registry worktrees and conversation IDs.
Permitted sandbox metadata reports the verified current origin as its remote,
while retaining the registered remote for exact-match lookup. It carries declared
logical aliases separately from inferred origin short names and verified origin
aliases, together with the stable project ID,
without exposing the record or local paths. This grants no clone or publish
permission. Ambiguous short names require an explicit registered name or URL;
logical aliases never grant a forge URL. Exact registered remotes take precedence
over verified old URLs, so a reused repository name cannot shadow an exact match.

## The collection in the Control UI

OpenClaw shows a session's changes and pull request for its own checkout only,
so a conversation whose repositories are held by owner sessions shows none of
them. The plugin in [`openclaw-plugin/`](../openclaw-plugin) adds the view of
the task as a whole:

- a card above the message box, where OpenClaw shows a task's progress. It
  groups the repository, branch, changes, PR and next action in fixed columns
  when space permits, without visible repository headers. **±**, **↑** and **↓**
  mean changed files, commits to push and commits behind the base. The PR link
  includes its check, merge and review facts. **Service Repo
  State URL** is a row per service, then a column per way in (an OpenClaw
  portal or a tunnel provider) and a **+** that opens a new one. Collapsed, the
  card shows the same facts summed, in the same order: **± ↑ ↓ PR svc**.

  What can be done about a fact is done on it, so actions have their place
  too: the ↑ count pushes, opening a draft pull request when there is none,
  the ↓ count catches up (a conflict goes to the agent, which catches up in
  its own checkout, in a sandbox too, and resolves it there), a ✗ under **M**
  asks the conversation to resolve the conflict, and a service's ▶, ■ or ↻
  starts, stops or restarts it. The same marks in the
  header of the services act on all of them, and collapsed, ↑, ↓ and the
  services' mark act on the whole collection. Click a service name to read its
  recent logs without starting or restarting it. The log pane has Refresh and
  Close controls, opens at the latest of up to 100 lines, and stays in place
  while status refreshes. It appears below Services in the sidebar and clears
  on conversation changes. Services with several processes offer task buttons.
  Resource-only and unconfigured rows have no process logs to open. A click in the composer card opens that sidebar pane. Logs require read access; service
  lifecycle controls still require write access. An aligned **All repos** row
  offers **↑** (push) and **↓** (catch up) over their per-repository arrow tracks. Hover or focus
  exposes the eligible scope; unavailable actions stay disabled in their slots.
  When PR facts or history are present, they keep separate fixed tracks. Narrow
  rows omit empty fact bands. Action-log result markers also have a reserved glyph slot.
  Uncommitted-only rows offer **Commit changes** first.

  An open tunnel shows the first part of its address, links to it and closes
  with ✕; a closed one is a ▶ to open it again. **+** offers, in order, the
  gateway's own OpenClaw portal, a public Cloudflare or ngrok link for when a
  portal does not work, a dev tunnel for the person's account, and Tailscale
  when the plugin's `teamTailnet` setting says the gateway's tailnet is the
  team's rather than a person's; a Tailscale tunnel is never public, a public
  link is Cloudflare's or ngrok's. Without portal ingress on the gateway
  (`gateway.portals.ingress`, or Tailscale run by the gateway), a portal
  answers at the gateway's own address, so it is offered last, marked "needs
  ingress", and an open one is marked ⚠ when the browser cannot reach its
  address. A provider whose client is not on the gateway's PATH says "install
  first", and the conversation is asked to install it. A service's own address
  is on the gateway, often inside a container, so it shows as its port only. A
  tunnel started as a service of its own is marked, with **Consolidate** asking
  the conversation to open it again with `wtc svc tunnel`. The card appears
  only on sessions that have a collection. The plugin provides it as a
  composer that wraps the built-in one, selected when the plugin loads;
  **Customize UI → Built-in** switches back.

  Pushing is the person's, never the agent's. The gateway publishes the
  repository's worktree with the person's own GitHub connection
  (**Settings → Profile → GitHub connections → For me**), which only the
  gateway holds, and only once they confirm: the first click on ↑ prepares a
  separate preview with the personal account, repository, branch and captured
  change count. **Review push** reopens that preview; it does not publish.
  Only its distinct **Confirm push** control sends the captured request.
  **Cancel** discards the preparation. Bulk preparation retains these separate
  personal confirmations and never consumes an earlier one.
  The gateway's shared GitHub account, often the `gh` login its agents use
  too, is never used for it; without a personal connection ↑ says where to
  connect one. A Bitbucket repository is pushed the same way by the
  [openclaw-bitbucket](https://github.com/lcorneliussen/openclaw-bitbucket)
  plugin (0.3.0 or later), with the person's own Bitbucket login: it pushes
  what is committed, so uncommitted files stay behind, and opens a draft pull
  request into the repository's base. Its `execLogin: false` keeps the login
  out of the agents' commands, so that this is the only way to push. Without
  the plugin, ↑ shows what is waiting without a button; the card never asks
  the conversation to push. **Resolve** asks it to merge and commit, not to
  push.
- a **Collection** panel beside the conversation (⤢ on the card) with the
  collection's name, branch and folder, the same repository and service
  tables with the same actions, wtc status's pull request table (**PR Repo
  State C M R Title**), and **Add repository**.

When the fixed columns no longer fit, name and branch share the first line,
with the action alongside. Counts and any PR facts share one aligned second
line; facts-free rows omit it. Service layout has its own
520-pixel breakpoint. Long branches have a tap/keyboard expansion, and complete
feedback is available from the header in the sidebar rather than clipped into
an extra paragraph below the rows. Services precede the recent Action log.
Text on a row shares a baseline; glyphs center independently. Status and history
links use the host's quieter controls, while the separate personal confirmation
uses its primary style. Preparing a preview is labeled **Preparing…**; only a
personally confirmed push is labeled **Publishing…**. The collection-wide actions share the repository tracks
rather than forming a floating stack of buttons.

Both render what `wtc status <name> --json` prints and share a refresh every 30
seconds while visible. Manual refresh shows its busy state. Publication pauses
periodic refresh, discards responses begun before the operation, and waits for
a fresh status before clearing the row's busy state. A refresh failure is
distinct from a successful publication. `node --test openclaw-plugin/test/`
exercises rendering and interaction contracts against a minimal DOM.

## Install

The plugin's visible name is **taba**, formerly wtc, with the bundled SVG mark and selectable
**taba** wordmark linking to [taba.sh](https://taba.sh/). This branding does
not change its installed identity: the plugin ID is still `wtc`, its package
is `openclaw-plugin-wtc`, and its settings stay under `plugins.entries.wtc`.
Service start, stop and restart controls share the same icon rail as their names;
Start all and Stop all appear beside Services. The alignment check
`node openclaw-plugin/tools/check-service-alignment.mjs <playwright-module> <output-directory>`
uses synthetic data and saves screenshots with measured baseline and icon guides.

Use the existing install/reload commands below. When upgrading to a release
with this branding, install the matching CLI and plugin, then reload open
Control UI tabs. The technical ID/config migration remains a later step in
[the rename plan](https://taba.sh/wtc-is-now-taba/).

On the gateway's host, as the user the gateway runs as. For a gateway built
from an image by CI/CD, see [Gateway image and CI/CD](#gateway-image-and-cicd)
instead. An agent asked to set wtc up starts with `wtc docs setup.md`.

1. **`wtc`** on the `PATH` of the gateway and of the agents' shells: a
   [release](https://github.com/lcorneliussen/taba/releases)'s
   `taba_<version>_linux_<arch>.tar.gz` (`wtc_…` before the taba releases)
   unpacked into `/usr/local/bin`, or
   `mise use -g github:lcorneliussen/taba@<version>`.
2. **Git 2.42 or newer and the GitHub CLI.** OpenClaw 2026.10.2 checks Git
   attributes with `git var GIT_ATTR_GLOBAL` before it publishes; its image
   ships Git 2.39 and no `gh`, and publication fails there with "workspace
   attributes could not be verified".
3. **The plugin**, which brings the collection card, the Collection panel and
   the agent's skills: `wtc`, and the OpenClaw variants of `wtc-catch-up`,
   `wtc-pr` and `wtc-follow` (each `SKILL.openclaw.md` among wtc's skills):

   ```sh
   wtc openclaw install
   ```

   It is embedded in `wtc`, so it is always the plugin of that version. It
   turns on custom Control UI plugins
   (`gateway.controlUi.experimental.customPlugins`) and runs
   `openclaw plugins install … --force --accept-capabilities`, then
   `openclaw plugins reload wtc` so that a running gateway uses it without a
   restart. Running it consents to the capabilities the plugin declares: the
   Collection panel and its session actions. For a gateway on another host,
   `wtc openclaw install --dir ./wtc-plugin` writes the plugin for
   `openclaw plugins install ./wtc-plugin --force --accept-capabilities` there.
4. **Bitbucket**, if repositories live there:

   ```sh
   openclaw plugins install git:github.com/lcorneliussen/openclaw-bitbucket@v0.3.4 --accept-capabilities
   openclaw config set plugins.entries.bitbucket.config.execLogin false
   ```

   `execLogin false` keeps people's Bitbucket logins out of the agents'
   commands, so that pushing goes only through the card.
5. **Each person** connects their own accounts, which the gateway pushes
   with: GitHub under **Settings → Profile → GitHub connections → For me**,
   Bitbucket on `/plugins/bitbucket/connect` opened in its own tab. The card
   says so the first time it needs one.
6. **Repositories** the gateway clones itself: **Add repository** in the
   Collection panel, or the gateway's own project settings. With a harness,
   add it the same way and configure it (see [wtc projects](#wtc-projects)).
7. **Each agent's `AGENTS.md`** says that the gateway works in collections.
   The plugin's `wtc` skill only comes up when an agent already thinks of
   wtc; `AGENTS.md` is what it reads in every conversation. Append the
   section this `wtc` ships, to the `AGENTS.md` in each agent's workspace
   (`openclaw agents list` shows where):

   ```sh
   wtc docs openclaw-AGENTS.md >> <workspace>/AGENTS.md
   ```

To check: `wtc doctor --openclaw` on the gateway host checks Git, `gh`, the
plugin and its version, the Control UI setting and each agent's `AGENTS.md`.
From an agent's command, plain `wtc doctor` does the same. A
conversation asked to "start the collection 1-try-wtc with <owner/repo>" then
gets a card above its message box.

## Gateway image and CI/CD

A gateway that a pipeline builds keeps wtc in its image and its setup in
configuration, so that nothing is installed by hand and an upgrade is a
version bump.

In the image, with the versions pinned as build arguments:

```dockerfile
ARG WTC_VERSION=<version>
ARG OPENCLAW_BITBUCKET_TAG=<tag>
RUN curl -fsSLO "https://github.com/lcorneliussen/taba/releases/download/v${WTC_VERSION}/taba_${WTC_VERSION}_linux_$(dpkg --print-architecture).tar.gz" \
 && curl -fsSL "https://github.com/lcorneliussen/taba/releases/download/v${WTC_VERSION}/checksums.txt" | grep "linux_$(dpkg --print-architecture)" | sha256sum -c - \
 && tar -xzf taba_*.tar.gz -C /usr/local/bin taba wtc && rm taba_*.tar.gz \
 && wtc openclaw install --dir /opt/wtc-plugin
# Only with Bitbucket repositories:
RUN git clone --depth 1 --branch "${OPENCLAW_BITBUCKET_TAG}" https://github.com/lcorneliussen/openclaw-bitbucket.git /opt/bitbucket-plugin
```

The image also needs Git 2.42 or newer and `gh` (step 2 above). `wtc
openclaw install --dir` writes the plugin embedded in that `wtc`, so plugin
and binary cannot drift apart.

In the gateway's configuration, applied by the pipeline (or by
`openclaw config set` in a configure step):

```json
{
  "gateway": { "controlUi": { "experimental": { "customPlugins": true } } },
  "plugins": {
    "load": { "paths": ["/opt/wtc-plugin", "/opt/bitbucket-plugin"] },
    "entries": {
      "bitbucket": { "config": { "execLogin": false } },
      "wtc": { "config": { "harnesses": [{ "name": "platform", "remote": "https://github.com/example-org/platform-harness.git" }] } }
    }
  }
}
```

Plugins on a load path are loaded from the image as they are, so there is no
install step and nothing stale survives a new image. Keep the agents'
`AGENTS.md` in the same repository as the rest of the gateway's setup, with
the section from `wtc docs openclaw-AGENTS.md`.

To upgrade, bump `WTC_VERSION` (and the Bitbucket tag when its release notes
call for it) and let the pipeline build and roll out the image. People's
connected accounts live in the gateway's state, not in the image, and stay.
As a smoke test after the rollout, run `wtc doctor --openclaw --json` in the
gateway's container: it exits non-zero when a required check fails.

## Upgrade

Upgrade in place on the gateway's host. Keep the existing gateway,
configuration and people's connected accounts; the plugin normally reloads
without a gateway restart. A gateway built by CI/CD upgrades with a new
image instead (above).

1. **`wtc`**: replace the binary the way it was installed, with the new
   release.
2. **Its plugin**: `wtc openclaw install` again. It replaces the plugin with
   the one embedded in the new `wtc` and normally reloads it in the running
   gateway. If it reports that installation succeeded but the gateway did
   not reload the plugin, follow its instruction to restart the gateway.
   In either case, reload open Control UI tabs to get the new card. Until
   this step, the Collection panel says that the plugin and the gateway's
   `wtc` differ.
3. **openclaw-bitbucket**, when its release notes call for it: install the new
   tag over the old one and reload it.

   ```sh
   openclaw plugins install git:github.com/lcorneliussen/openclaw-bitbucket@<tag> --force --accept-capabilities
   openclaw plugins reload bitbucket
   ```

`openclaw plugins update` does not upgrade the wtc plugin: it skips plugins
installed from a path, which is how `wtc openclaw install` installs.

### Select next actions

Development builds put the recommendation beside each repository instead of
repeating the repository list in a checklist. Reasons are available on the
affordance. The top-bar shortcut remains visible when collapsed. With one
recommendation it runs that repository’s action directly; with several it captures
the visible recommendations. A quiet shortcut
such as **Catch up 2 repos →** or **Do next (2) →** captures those exact steps.
Catch-up and local steps start directly. Publication selections show a preview
and retain each repository's account/commit confirmation.

The header **⋯** menu, in both the card and sidebar, lists **Choose steps…**
for alternatives and mixed selection controls. Choose an
action per repository, check its selection, or use **Select suggested**. Existing
alternative choices are preserved. **Run selected** rechecks the exact targets.
A changed head, base, worktree state or PR observation makes an old selection
stale. New repositories do not join a captured selection. **Exit selection**
returns to the compact view; **Run chosen** retains your exact selection there.
Running, queued and pending results stay beside
the repository in either view.

Each batch captures the selected step per repository. Catch-up runs the command
first; a fetched merge conflict is rechecked and handed to the named catch-up
skill for resolution. Other follow-ups remain separate selections. Results stay per target;
a failure does not undo other targets. Selected author tasks are sent together
as one sequential prompt to the conversation. “Sent to conversation” means the
handoff was accepted; check the conversation for completion. **Cancel queued**
stops remaining dispatches and does not undo work already started. Publication
retains each repository's personal confirmation; selecting a batch does not
approve new commits or merge a PR. Open pull request remains a link for the
person to inspect the current merge requirements.

**Check PR progress** hands off checking the current reviews and requirements.
Forge approval does not prove a current independent review. Independent review
execution in the gateway remains unavailable until its shared review adapter is
installed; the disabled alternative explains this. Local status can hand off the
existing `wtc-local-review` skill, which checks existing review activity first.
Unchanged handoffs stay disabled in that view while completion is pending.
After checking the conversation, **Allow retry** enables explicit reselection.
There is no automatic continuation, durable job queue or cross-runner claim in
this phase. Reloading a view does not restart its unfinished batch.

The sidebar Action log shows the latest three results, with expandable detail
and **Earlier activity**. Direct catch-up, publication, service and manual-refresh
feedback use this history. Accepted author handoffs remain pending rather than
claiming completion. This history is browser-session feedback: reloading clears
it. It is not a durable gateway audit log or a trusted completion receipt.

The collection keeps its base layout in place while actions run. Bulk Push and
Catch up controls retain their slots and become disabled when no repository is
eligible. Temporary publication reviews, choices and connection help extend
above the composer card and below Services in the sidebar. The repository and
service rows remain visible in their original positions.

### Alignment and compact views

The brand, repository names and service names share an icon rail and a text rail.
Icons follow the adjacent text's cap-height middle; text of different sizes keeps
its baseline. Bulk arrows use the same arrow slot as their counts, with tabular
numbers aligned at the other edge. A captured push count stays visible but
disabled during publication, and archived-PR hints keep their wording and trailing
rail when a new current PR appears. Narrow rows combine name and branch and use
one fact line only when it has facts or a recorded result; catch-up rows do not
reserve an empty PR lane. Repeated row actions use quiet surfaces. Actionable header controls retain a
quiet outline at rest; progress stays muted. Completed results also open their
action log, so they retain that button affordance.

A publication started from the collapsed bar shows its personal preview above
the bar, without opening the repository/service details. Confirm and Cancel remain
separate controls. Local catch-up also retains the collapsed bar throughout.

The recording workflow can emit separate alignment guides, hierarchy measurements,
cropped action clips and a synchronized native-TUI renderer replay. The TUI already
offers keyboard Next actions with `n`; direct table mouse actions are separate.

The reusable recorder lives in the public
[site recording workflow](https://github.com/lcorneliussen/wtc-site/blob/setup/openclaw-interaction-recordings/docs/recording-openclaw-interactions.md).
Its recording-only adapter here accepts a JSON input with `columns`, `rows` and
`frames`. Each frame contains `at`, `phase`, a `StatusSnapshot` in `snapshot`,
one local-host `next.Facts` value per repository in `facts`, and optional
selection-only `keys`. For example, an empty initialized view is:

```json
{"columns":96,"rows":24,"frames":[{"at":0,"phase":"idle","snapshot":{"schema":1,"collection":"demo","repos":[],"prs":[],"runtime":[]},"facts":[],"keys":[]}]}
```

Save this as `/tmp/tui-input.json`, then run from the CLI source:

```sh
WTC_RECORDING_INPUT=/tmp/tui-input.json WTC_RECORDING_OUTPUT=/tmp/tui-frames.json go test -tags recording ./cmd/taba -run '^TestStatusRecordingFrames$' -count=1
```

Without those inputs the test skips; `go test -tags recording ./cmd/taba -run
'^TestStatusRecordingFrames$'` still compiles the adapter. This is an author-run
recording check, not current CI coverage. It renders native ANSI frames, refuses
unknown keys and any returned command, and never initializes the collector.
The site's workflow turns these frames into casts, lossless PNG/video and paired
replays; the adapter alone does not record a live terminal or gateway.

Preparation for direct table actions is tracked in
[the shared next-action issue](https://github.com/lcorneliussen/taba/issues/179).
Resolve the selected collection/repository/worktree/action key, re-read through
`statusNextCurrent`, and reuse ownership, busy and publication checks. Agent
steps must target the verified owner/pane, never the focused terminal or a typed
draft. Report handoff separately from completion. No direct mouse actions are
added to the status TUI by this recording work.

In the compact card, **Services** has a collection-level heading on the outer
content margin and a light separator above it. The service rows name their owning
repositories, but the section is not nested under the final repository row.
Browser table labels use normal capitalization; the terminal keeps its uppercase
headings. Visual review compares intended section regions with a separately
recorded pixels-only grouping, rather than inferring hierarchy from the DOM alone.

### Restore an evicted checkout

When OpenClaw evicts an idle managed worktree but retains its snapshot, the
Collection card and sidebar show **Evicted** and **Restore**. The header offers
**Restore all** for the recoverable repositories, including while the card is
collapsed. Restoration needs the person's gateway `operator.admin` permission.
It runs sequentially through OpenClaw's native restore command, rechecks the
recorded owner, destination, branch and snapshot, then refreshes status. Each
repository's success or error appears in the Action log. If the conversation
changes, queued repositories are left alone.

Restore keeps the existing native record and session binding, including saved
uncommitted changes. It does not fetch a newer branch, clone a replacement,
start services or overwrite an occupied destination. A stale record, missing
snapshot, changed branch or native permission failure is reported for inspection.
A directory that is merely missing, with no verified recovery record, does not
get a Restore action. Older hosts without recovery commands still show its
repository identity and **No checkout**.

Status refreshes never restore or extend activity. Recovery metadata uses a
coalesced native worktree listing only while checkout rows are unavailable; the
native service can reconcile its own registry during that listing. Restoration
does not change OpenClaw's idle cleanup or snapshot retention policy.

Repository actions use a shared fixed column: the main button and its `⋯` menu
keep the same positions for Catch up, Commit changes and Restore. The menu
shows available alternatives and explains blocked ones. If local changes keep
catch-up out of the current plan, the menu says to save or stash them first;
it does not override the plan or bypass the gateway's checks.

The header `⋯` menu keeps collection-wide Catch up, Restore and push preparation
available even when a different action occupies the main button. Counts include
only eligible repositories; catch-up excludes dirty and unavailable checkouts.
Pushes retain their personal confirmation. **Choose steps…** opens mixed
selection across repositories. Merely opening or navigating a menu runs nothing.

Action groups are right-aligned, with `⋯` on the left. The recommended action
stays at the right edge. Wider views show additional available actions inline;
narrower views move those choices into the menu. Unavailable alternatives stay
in the menu with their reasons. Each repository uses the same action-column
width, and space already exposed during the conversation is retained while
work is running so progress does not shift the repository facts. Changing the
viewport never runs or selects an action.
