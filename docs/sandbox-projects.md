# Project discovery in a sandbox

A sandbox can use `wtc projects --json` and resolve repository names for
`wtc add-repo` through the gateway's sandbox Git service. Set `WTC_GATEWAY_GIT`
as described in [OpenClaw sandbox setup](openclaw.md#in-a-sandbox).
The gateway and sandbox must both have a version of wtc that supports project
discovery.

Discovery is **denied by default**. On the gateway, the operator can configure
`plugins.entries.wtc.config.sandboxGit.projectsByAgent`:

```json
{
  "port": 18790,
  "projectsByAgent": {
    "developer": ["platform"],
    "operations": ["operations"]
  }
}
```

These are exact agent IDs and configured wtc project names. There is no wildcard
or implicit default. An absent, empty or invalid entry denies discovery for that
agent. Choose this policy deliberately: allowing a project exposes its name,
harness remote and ref, default status, and its entire repository registry
(names, remotes, roles and default branches) to every sandbox session of that
agent. Use separate agents when their project metadata visibility must differ.

The service identifies the agent from the gateway's running Docker container,
rechecking the container for every discovery request. A sandbox that projects a
managed worktree must also have that worktree's single owning session. A
sandbox without one, such as a new conversation's before it has a collection,
is identified by its container's agent alone, and its result names no
collection. Request parameters and headers cannot select another identity or
project. Unknown or ambiguous ownership is refused. Only allowlisted registries
are read. Remotes are returned as credential-free GitHub or Bitbucket HTTPS
URLs; unsupported remotes or unreadable registries produce a generic project
error. Raw configuration, command errors and gateway filesystem paths are not
returned.

Inside the sandbox:

```sh
wtc projects --json
wtc add-repo api --worktree 42-fix --project platform --separate
wtc doctor
```

Repository names and default branches resolve from one permitted snapshot per
plan. If the authenticated collection has a worktree of its harness, that
worktree's registry (including unpublished edits) is used and the harness is not
added again. A registered repository uses its gateway project ID. An existing
collection's project is kept; a unique existing harness worktree establishes the
project when no separate record exists yet.

`add-repo` emits intents only and writes no gateway or sandbox project records.
It permits a chosen base only when the gateway already measures and publishes
that collection's repository to that destination. If a registry default or
`--base` needs a new gateway base record, the plan is refused before any intents
are emitted. Prepare that plan in the gateway collection session instead.
Worker plans and plans for another collection are also refused in a sandbox:
the read-only interface cannot record worker base inheritance or inspect another
collection's unpublished registry state. A separate GitHub clone intent with no
explicit starting branch retains the existing gateway authorization step.

`doctor` reports the gateway discovery source or that discovery is unavailable.
A denied, malformed or unreachable discovery response fails `projects` and
`add-repo`; it does not silently fall back to an empty local configuration.
An empty permitted project list is distinct from a discovery failure. Keep
gateway configuration and state out of sandbox mounts. The hidden gateway
adapter rejects calls configured for sandbox transport; HTTP discovery is the
authenticated interface.

**Listing grants no clone, worktree, publication or Git read permission.**
The gateway still authorizes session-spawn intents. The Git service continues
to serve only repositories already in the session's collection; knowing another
repository's metadata does not make it fetchable. Discovery supplies no forge
credential. Local gateway and terminal behavior is unchanged when
`WTC_GATEWAY_GIT` is unset.
