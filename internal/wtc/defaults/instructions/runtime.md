# Opt-in service runtime with dekit

This integration is available from WTC **v0.1.40** and remains experimental.
Test it in a disposable collection before updating a harness's release pin.
Read `taba release-notes --since <old-pin>` using the target binary first.

WTC owns collection identity, repository association, generated configuration,
logs and retirement. [dekit 0.10](https://dekit.run/docs/start/runners) owns an
independent runner, process trees, dependency ordering and restart behavior.
Closing an agent or terminal does not stop the services. Mise remains the
repository's toolchain and task runner.

## Enable a downstream harness

Add to its committed `taba.toml` (`wtc.toml` in older harnesses):

```toml
[runtime]
backend = "dekit"

[mise.tools]
"github:pvolok/dekit" = "0.10.0"
```

Merge these tables with existing ones; TOML cannot repeat a table. Regenerate the
collection environment using `taba env setup`, then install
the pinned tool with mise. The adapter accepts dekit 0.10.x; test a newer release
before changing that constraint. A legacy mprocs binary named `dekit` is rejected.
An optional `[runtime] binary = "/absolute/path/to/dekit"` selects an isolated
trial binary. Relative binary paths resolve from the harness directory.

An ad-hoc collection (`taba init`) has no harness repository: put the same
`[runtime]` table in its `.wtc/config/wtc.toml`, where relative binary paths
resolve from `.wtc/config`. Services, tunnels, status and logs then work as in a
harness collection, and `taba retire .` stops the runner and removes the
collection's task files with it.

Use the v0.1.40 or newer target binary explicitly during the trial, before
changing the harness version pin (`.taba-version`, or `.wtc-cli-version` in
older harnesses). Keep the pin and compatibility range
in sync when adopting it.
Enabling the backend alone starts nothing. Neither `taba new`, env generation,
`taba status` nor repository initialization starts this runtime.

## Onboard an application repository

Keep the existing launch entry point: `bin/dev`, a Procfile runner, `mise run
dev`, `npx`, or a .NET/Mono host. The build/cache/watch cycle stays in that
command. Add an **opt-in** `.harness/dekit.tasks.yaml` alongside it:

```yaml
tasks:
  web/server:
    cmd: [mise, run, dev]
    autostart: true
    ready:
      cmd: [sh, -c, 'curl -fsS "http://127.0.0.1:${PORT:?}/health" >/dev/null']
      timeout: 60s
    x-wtc:
      url_env: WEB_LOCAL_URL
  web/tunnel:
    label: named development tunnel
    cmd: [mise, run, dev:tunnel]
    deps: [web/server]
    autostart: true
    tags: [endpoint]
    ready:
      cmd: [sh, -c, 'curl -fsS "${WEB_PUBLIC_URL:?}/health" >/dev/null']
      timeout: 60s
    x-wtc:
      url_env: WEB_PUBLIC_URL
```

`dev:tunnel` is the repo's chosen foreground tunnel command, for example an SSH,
Tailscale or named Cloudflare tunnel. Do not start it in the background or exit
while the tunnel remains alive. For a randomly assigned public URL, omit
`url_env` initially and read the assigned URL from the task log. For a known
endpoint, put its URL in the collection environment before `up`.

The repository might use `WIDGET_PORT` from its registry's `port_offset`. An
`env.post.sh` harness hook can supply aliases:

```sh
# After loading the generated environment, append these shell assignments.
printf 'PORT=%s\nWEB_LOCAL_URL=http://127.0.0.1:%s\n' "$WIDGET_PORT" "$WIDGET_PORT" >> .env.collection
```

Keep your chosen tunnel endpoint in `.env.collection.local`, or generate it
idempotently from collection identity. For example:

```sh
WEB_PUBLIC_URL='https://widget-demo.example.test'
```

Do not put credentials in endpoint URLs. WTC accepts HTTP(S) endpoint metadata
without user information, a query or a fragment, and stores that public address
in the manifest for read-only status. `x-wtc.url` can instead hold a literal
address. `x-wtc` also takes `port_env` (the variable holding a service's port:
status shows `http://127.0.0.1:<port>`, readiness defaults to that port
accepting connections, the resolved value is put in the env of the service and
its tunnels, and named tunnel targets resolve from it), `url_log` (a
pattern finding an address in the task's output), `tunnel`
(`tailscale|cloudflare|ngrok|devtunnel`; usually recognised from the command),
`public` (`true` or `false`, overriding what WTC infers), and `mark` (one
letter for status). WTC strips that metadata before passing the task to dekit;
dekit does not expand shell variables in argv or a literal `ready.http` string.

A collection can add its own services and tunnels without changing the
repository: `taba svc add` writes `svc/<repo>.yaml` in the
collection's runtime home (`.wtc`, or `.taba` where it already holds this
state), read together with the committed file and never
committed. `--shared` writes the committed file instead.

WTC prefixes paths with the worktree directory: `widget/web/server` and
`widget/web/tunnel`. The first local path component groups the service with its
endpoint in status. A task's cwd defaults to its repo root. Explicit relative
cwd and script paths are rebased from that root. Dependencies are local to the
repo; `/other-repo/db/server` explicitly names a task in another sibling.
Only checked-out opted-in repositories participate. A missing dependency is an
error; WTC never adds repositories or starts a host runner implicitly.

All other task fields follow the [native dekit task contract](https://dekit.run/docs/config/tasks).
The fragment has only a top-level `tasks` map. Task `add_path` entries should be
absolute; use mise for repo-relative toolchain resolution. Use argv arrays and
an explicit shell for variable expansion or shell operators. WTC supplies
persistent append logs and the collection's env. Do not embed credentials in
committed task definitions or command arguments.

For an existing Ruby stack runner, supervise its ordinary foreground `bin/dev`
as **one task**. Its child logs appear in that task's log; WTC does not invent
per-child status. Split tasks only when separate control and status are useful.
For Node or .NET, use the existing `npm run dev`, `npx` build-and-serve wrapper,
`dotnet run`, or Mono launcher in the same way. Disable self-daemonization.

## Preserve standalone development

The new fragment and hooks are consumed only by WTC. Do not replace the repo's
README quick start, make its default task call `taba up`, require dekit in its
ordinary dependency install, or assume `WTC_COLLECTION` is always set.

Accept a port/data-path override while retaining the current local default:

```sh
PORT="${PORT:-3000}" exec ./bin/server
```

A WTC-only hook may insist on a collection identity, because standalone users
never invoke it. Normal launch commands must still work with all WTC env unset.
A nested Procfile manager is fine if it stays in the foreground and stops its
children when its process group receives SIGTERM. Confirm this for the real
runner; a detached child is outside dekit's process-group guarantee.

## Resources, Docker and cloud services

Long-running accessories can be ordinary native tasks tagged `accessory`. For a
collection-owned Docker stack, supervise `docker compose up` in the foreground,
use a **collection-specific Compose project name**, and supply a native stop
command that stops only that project. Test container shutdown explicitly: a
runner being absent alone does not prove containers have stopped.

A shared Docker Postgres server or Azure service is external. Do not start a
second instance just to fit the process tree. Provision only the collection's
DB/schema/container/prefix through executable repo-owned hooks:

```text
.harness/runtime-provision.sh
.harness/runtime-teardown.sh
```

Declare what the hooks manage, so status names each resource and teardown says
what it removes:

```yaml
resources:
  database: {type: postgres}
  cache: {type: redis, mark: k}
```

Both run from the repo root with `.env.collection` then
`.env.collection.local` loaded. The hooks can call existing mise tasks and
provider CLIs. A provision hook requires a teardown hook. They must be
idempotent and return nonzero on failure. They run under the collection runtime
lock: call provider commands or mise tasks, rather than nested WTC runtime
commands. Avoid starting long-lived background
processes from them.

Before provisioning, record the owned resource's exact identifier and provider
scope in an ignored collection-local receipt. Use `WTC_COLLECTION` and repo
identity, with provider-safe sanitization and a collision-resistant suffix.
Check that ownership on teardown. Drop only that collection's database or
Azure resource, never the shared database server, account or resource group.
An external resource with no cleanup rights needs an explicit contract, rather
than a success-returning fake teardown. Back up/export valuable data before
retirement; collection-owned data is disposable.

WTC records **the owner before calling provision**, including failed attempts,
and retains it across config changes. Status shows `provision-attempted`,
`provision-hook-completed`, or `teardown-failed`. These are hook outcomes, not
live provider health. A native accessory task can expose a provider's log
stream or an explicit observer if ongoing visibility is needed. Keep receipts
and hook logs until cleanup succeeds; never store credentials in the receipt.

## Daily lifecycle and agent access

From the collection root:

```sh
taba svc                        # services, tunnels, resources and their state
taba svc add web --port-env WIDGET_PORT -- npm run dev   # enlist and start
taba svc up [web]               # provision, then start all (or one service)
taba svc restart web
taba svc logs web [--tail 100] [-f]
taba svc tunnel web --name preview --public  # approve/start configured definition
taba svc tunnel web --name preview --stop   # retain registration, record start-off
taba svc down [web]             # stop one service, or the runner (logs, data kept)
taba svc rm web                 # stop and remove, others keep running
taba svc render --dry-run       # ownership preview; no tool required or writes
taba svc provision              # provision only, no service startup
taba svc --json                 # machine-readable state; never starts a runner
taba svc teardown               # stop runner, discard saved tasks, strict cleanup
taba svc dekit -- why widget/web/server   # anything else, with the pinned dekit
```

`taba up`, `taba down`, `taba restart` and `taba logs` are shortcuts for the same
commands. A name without its repository works when only one repository has it.

Named tunnels use receiving-local desired policy at collection `.config/tunnels.toml`,
over project-root `.config/tunnels.toml` defaults. These paths do not follow a
credential-root override. Each identity is `<registered-repo>/<service>/<name>`;
registered repository identity resolves checkout aliases such as `ext.widget`.
A collection entry replaces the entire matching default. An entry containing
only `enabled = false` disables it; removing that override restores inheritance.
Unmatched definitions are displayed without provisioning. Several names can
expose the same service independently.

For example, save this one-entry definition as an absolute local TOML file:

```toml
schema = 1
[tunnels.widget.web.preview]
provider = "ngrok"
mode = "domain"
access = "public"
start_with_service = false
[tunnels.widget.web.preview.options]
domain = "web.example.test"
reserve = false
[tunnels.widget.web.preview.inputs]
hosting_file = "providers/ngrok-host.token"
```

```sh
taba svc tunnel widget/web --name preview --action configure --definition /absolute/entry.toml
taba svc tunnel widget/web --name preview --action register --public
taba svc tunnel widget/web --name preview --public
taba svc tunnel widget/web --name preview --stop
taba svc tunnel widget/web --name preview --action unregister
taba svc tunnel widget/web --name preview --action reconcile
```

Configure and disable write this collection's override. A native operator may
explicitly add `--project` to configure/disable receiving project defaults from
an actual member collection. A remote harness snapshot does not make the local
receiving project root immutable; the snapshot itself is never written. Actual
snapshot-only contexts remain read-only. Collection UI authority never grants
project writes or arbitrary definition-file selection. UI provider setup entries
show these receiving-local instructions; they do not install or start a client.
Existing explicitly declared temporary endpoints retain their own task commands;
provider shorthand never invents a global-login task for a new endpoint.

Credential fields are **file references**, relative to the approved receiving
credential root (absolute references must remain inside that scope), never token
literals. Built-in provider clients use only explicitly supplied inputs, with no
login, keychain/profile discovery or runtime package fetch. Management, hosting
and cleanup are distinct roles; permissions are enforced by the provider, not
inferred from the existence of a file. Missing/expired/insufficient capability
refuses the effect and retains cleanup information. A capability is pinned across
an effect. Credential rotation cannot turn another account's 404 into proof that
a recorded resource disappeared; a positive exact identity/fingerprint match is
required before rebinding cleanup to the new capability.

| Provider/mode | Explicit inputs and lifetime |
|---|---|
| `ngrok/domain` | `options.domain`, `inputs.hosting_file`; existing domain is borrowed. Only `options.reserve = true` plus management/cleanup files reserves and owns a new domain. Uses the bundled Go SDK; no ngrok CLI is needed. |
| `cloudflare/named` | Exact account/zone IDs, hostname, optional registration name; management/cleanup/hosting files and an absolute `inputs.executable` for cloudflared. Owns a dedicated tunnel, exact configuration and created DNS record; never adopts/replaces pre-existing DNS. Hosting token must bind the exact tunnel, or the explicit hosting API capability must retrieve it. |
| `cloudflare/quick` | Absolute cloudflared executable; public, temporary session, no persistent registration events. |
| `devtunnel/persistent` | Management/cleanup files, hosting token or explicit Bearer hosting API capability, optional registration name, absolute `inputs.node`. Retains exact tunnel/cluster/port identity. |
| `devtunnel/temporary` | Explicit management and cleanup files plus Node even though registration is temporary. Its owned provider resources survive in pending cleanup state on interruption. |
| `tailscale/serve` | Explicit authorized `inputs.socket`, expected `node_id`/`dns_name`, `options.listener_port` and optional `options.path`. Configuring this input must authorize I/O to that exact daemon socket. No socket/profile discovery, Funnel enabling, reset or shared listener widening. |

Dev Tunnel hosting requires an already installed Node22 or newer runtime and the
shipped pinned self-contained helper. Other providers, registration status and
pure reads need no Node. The helper never fetches packages at runtime. Dev Tunnel
backends require an explicit IPv4/localhost HTTP port without a backend path;
HTTPS/private access declarations alone do not establish enforced access.
Tailscale verifies the expected running node and uses conditional configuration
writes, preserving unrelated handlers. Its configuration ETag is not an atomic
profile-identity lock: an external profile switch cannot be excluded by that API.
Explicit credentials and minimal child environments are not an OS sandbox.

`--public` approves potential public exposure for the current effective definition,
including target, provider inputs, receiving scope, hook code and reload targets.
Provider names, HTTPS and declared `protected`/`account` access are not proof of
private enforcement. Tailscale can use fresh observed tailnet evidence; otherwise
explicit potential-public approval is required. UI start/register clicks name the
exact current desired-definition hash. Changing a definition requires renewed
approval and cleanup of retained old obligations before replacement.

`--stop` records start-off for this collection while retaining stable registration.
Ordinary service down stops sessions without erasing desired selection; up/restart
restore selected entries or `start_with_service` entries only with current approval.
Stable registration URL (`registration_url`) is separate from active URL (`url`).
Registered-but-stopped entries never appear in active host/origin publications.
`pending` and `lifecycle` identify interrupted operations; reconcile queries supported
exact evidence, never blindly repeats an unknown remote effect or arbitrary hook.
Some ambiguous effects require manual restoration/confirmation and remain pending.
A recorded active address is not perpetual host readiness. A crashed foreground
host is shown as pending; explicit stop/reconcile verifies the exact recorded
host and provider child instances before clearing their session receipt. A missing
observation or interruption between child launch and identity recording retains
the session for reconciliation. Process disposal does not prove arbitrary remote
or callback cleanup; those receipts remain independent. Normal runtime teardown
releases its retirement admission so an intact collection can be started again;
unknown ownership and interrupted destructive retirement still refuse allocation.

Optional `[tunnels.widget.web.preview.events.registered]` (and `started`, `stopped`,
`unregistered`) tables use `command = ["relative/executable", "argument"]`, resolved
inside the registered receiving checkout. Each receives typed JSON on stdin with
stable event/operation IDs, entry identity, previous/current URL/hostname/origin,
resource receipts and complete before/after active snapshots. Successful callbacks
may return `resources`/`released` identities (`kind`, `scope`, `id`, `fingerprint`);
only exact retained receipts may be released. Failures are strict and keep the
original event pending; arbitrary callback effects are not exactly once. Restore
changed cleanup code before retiring; output is not copied into public diagnostics.
Temporary modes emit started/stopped only. `reload = ["widget/app/server"]` names
only declared app services, in dependency order; tunnel targets/cycles are refused.
Reload occurs after events and active-data publication, never by restarting a tunnel.

Consumers read runtime `tunnel-publications/current.json` once, then both immutable
`hosts_file` and `origins_file` paths from that revision. Hostnames are deduplicated;
full origins retain scheme and port. A stale publisher cannot overwrite a newer
committed snapshot. Status/discovery only read desired and recorded tunnel state:
no provider/daemon/account queries, credential reads, hook runs, repair or allocation.

Managed obligations persist the existing runtime manifest as Schema2 before
external allocation. New clients accept Schema1/2 and preserve the fence through
stopped registrations, interruption and regeneration. Published 0.2.4 refuses
Schema2 before runtime teardown/worktree deletion, **but its earlier `retire.pre`
hook may already have run**. Corrupted/deleted old manifests cannot be protected
retroactively. New retirement consults the ownership journal even without a
manifest, cleans/validates tunnels before destructive legacy pre-hooks, and refuses
unknown/uncertain cleanup without a force bypass. Keep code/credentials available
until all obligations clear. Only exact verified tool-owned non-secret collection
policy can be removed; sole credentials, unknown/edited files and project control
data are preserved.

`up` and `restart` wait up to 30 seconds for startup readiness; `--wait 60s`
changes this, and `--wait 0` returns immediately. Untargeted `up` waits for
autostart tasks and intended tunnels it restores; explicitly name a group to
wait for all its selected tasks. `restart`
requires a running runner. A readiness failure remains visible in status and
logs, and the failed command's output includes the last lines of each failed
or unready task's log, so a build error is in the failure itself. **Ready
means a startup probe passed**, not continuous health checking. Ordinary local
application addresses can be checked as status is reported. Managed tunnel status
reports persisted registration/session facts without provider login or endpoint
queries; it does not claim that a remote account or access policy was verified.

The detailed terminal status table labels internet-reachable tunnels with
`[public]`. A healthy public tunnel uses the same health color as a healthy
protected tunnel; failures and login problems retain their error color.
Running tunnels with `[exposure unknown]` remain warning-colored until their
access policy is known.
Plain service output also names each endpoint's exposure, including when
terminal colors are disabled.

No `wtc start` runtime command is introduced; the session orientation skill
keeps its existing meaning.

Service logs are append files under `runtime/logs/<repo>/<task>.log` in the
runtime home, available after `down`. Hook logs live under
`runtime/hook-logs/<repo>/provision.log` and `teardown.log` there; agents can read
those files directly. They can contain application secrets, so keep them local.
For the native interactive console, run `taba svc dekit -- attach`; it uses
the pinned binary, this collection's runner and its environment. Native `why`
and `screen --json` work the same way. The screen is a bounded terminal buffer,
not the persistent log. Tasks dynamically added through native dekit commands
are controlled by the runner but are not yet represented in WTC's configured
task list.

Edit task files rather than generated `dekit.yaml`. A changed file applies on
the next `taba svc up` without stopping the runner: removed tasks are removed,
the runner reloads its config, and running tasks whose definition changed
restart; the others keep running. An existing runner keeps its launch
environment apart from `port_env` values; use a full `down`/`up` after
changing other variables or credentials.

`taba retire` verifies the target runner stopped, clears its saved definitions,
and runs strict resource teardown **before removing any worktree**. An
unconfirmed stop or failed teardown keeps the collection and ownership record,
even with `--force`. Fix the hook and retry. Removing the harness opt-in does not
bypass cleanup of an already recorded runtime. Ordinary repo init/teardown hooks
continue to serve their separate worktree lifecycle.

## Bootstrap acceptance test

Use a synthetic fixture first, then each real repo's existing launch command:

1. Run the normal repo command with WTC env unset. Check its existing default
   port/data location and build behavior.
2. Create two disposable collections; assign distinct ports and resource
   identities. `up` both, exit the launching shell/agent, and check both endpoints.
3. Inspect repo association, service/tunnel grouping, hook facts, JSON state and logs.
   A status query before `up` must create no runtime or resource.
4. Restart one group. Confirm the other collection is unaffected and builds use
   the existing cache. Break a readiness probe and confirm `up` fails visibly.
5. `down` one. Check its ports, child processes and any owned containers stop;
   the second keeps serving. Logs and collection data remain available.
6. Make teardown fail deliberately in the fixture. Retirement must retain its
   worktrees and receipt. Restore the hook, retry, and confirm owned resources
   disappear while a synthetic shared resource stays intact.
7. Check changes into the repo/harness. Adopt a published CLI pin only after
   these checks pass; leave standalone developers' dependency setup unchanged.
