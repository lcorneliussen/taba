# Opt-in service runtime with dekit

This integration is available from WTC **v0.1.40** and remains experimental.
Test it in a disposable collection before updating a harness's release pin.
Read `wtc release-notes --since <old-pin>` using the target binary first.

WTC owns collection identity, repository association, generated configuration,
logs and retirement. [dekit 0.10](https://dekit.run/docs/start/runners) owns an
independent runner, process trees, dependency ordering and restart behavior.
Closing an agent or terminal does not stop the services. Mise remains the
repository's toolchain and task runner.

## Enable a downstream harness

Add to its committed `wtc.toml`:

```toml
[runtime]
backend = "dekit"

[mise.tools]
"github:pvolok/dekit" = "0.10.0"
```

Merge these tables with existing ones; TOML cannot repeat a table. Regenerate the
collection environment using `wtc env setup`, then install
the pinned tool with mise. The adapter accepts dekit 0.10.x; test a newer release
before changing that constraint. A legacy mprocs binary named `dekit` is rejected.
An optional `[runtime] binary = "/absolute/path/to/dekit"` selects an isolated
trial binary. Relative binary paths resolve from the harness directory.

An ad-hoc collection (`wtc init`) has no harness repository: put the same
`[runtime]` table in its `.wtc/config/wtc.toml`, where relative binary paths
resolve from `.wtc/config`. Services, tunnels, status and logs then work as in a
harness collection, and `wtc retire .` stops the runner and removes the
collection's task files with it.

Use the v0.1.40 or newer target binary explicitly during the trial, before
changing the harness's `.wtc-cli-version`. Keep the pin and compatibility range
in sync when adopting it.
Enabling the backend alone starts nothing. Neither `wtc new`, env generation,
`wtc status` nor repository initialization starts this runtime.

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
its tunnels, and `wtc svc tunnel` builds tunnels from it), `url_log` (a
pattern finding an address in the task's output), `tunnel`
(`tailscale|cloudflare|ngrok|devtunnel`; usually recognised from the command),
`public` (`true` or `false`, overriding what WTC infers), and `mark` (one
letter for status). WTC strips that metadata before passing the task to dekit;
dekit does not expand shell variables in argv or a literal `ready.http` string.

A collection can add its own services and tunnels without changing the
repository: `wtc svc add` and `wtc svc tunnel` write `svc/<repo>.yaml` in the
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
README quick start, make its default task call `wtc up`, require dekit in its
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
wtc svc                        # services, tunnels, resources and their state
wtc svc add web --port-env WIDGET_PORT -- npm run dev   # enlist and start
wtc svc up [web]               # provision, then start all (or one service)
wtc svc restart web
wtc svc logs web [--tail 100] [-f]
wtc svc tunnel web tailscale   # private tunnel; --public for one on the internet
wtc svc tunnel web --stop
wtc svc down [web]             # stop one service, or the runner (logs, data kept)
wtc svc rm web                 # stop and remove, others keep running
wtc svc render --dry-run       # ownership preview; no tool required or writes
wtc svc provision              # provision only, no service startup
wtc svc --json                 # machine-readable state; never starts a runner
wtc svc teardown               # stop runner, discard saved tasks, strict cleanup
wtc svc dekit -- why widget/web/server   # anything else, with the pinned dekit
```

`wtc up`, `wtc down`, `wtc restart` and `wtc logs` are shortcuts for the same
commands. A name without its repository works when only one repository has it.

`wtc svc tunnel` records an explicit on/off choice for that collection and
service endpoint. `--stop` records off, including when stopping fails; retry the
stop to confirm shutdown. Ordinary `down` keeps the choice. Service `up` and
`restart` restore enabled tunnels after checking the provider login, including
previously approved public tunnels. Untargeted `restart` restores them only for
services that were running. Untargeted `up` restores them for autostart or
already running services. Without an explicit choice, declaration defaults
keep their existing behavior.

Approval belongs to the current tunnel definition. Changing its command,
provider, destination or exposure, or removing and later reusing its task path,
requires a fresh `wtc svc tunnel` choice (and `--public` when applicable).
Changed or explicitly disabled tunnels do not autostart. Choices live in this
collection's runtime manifest and never change another collection's settings.
An intended tunnel that is not running reports a lifecycle problem separately
from provider login, current reachability and exposure; JSON includes
`tunnel_intent` and `lifecycle` alongside the observed runner `state`.

`up` and `restart` wait up to 30 seconds for startup readiness; `--wait 60s`
changes this, and `--wait 0` returns immediately. Untargeted `up` waits for
autostart tasks and intended tunnels it restores; explicitly name a group to
wait for all its selected tasks. `restart`
requires a running runner. A readiness failure remains visible in status and
logs, and the failed command's output includes the last lines of each failed
or unready task's log, so a build error is in the failure itself. **Ready
means a startup probe passed**, not continuous health checking. So `wtc svc`
and `wtc status` also look at each ready task's address as they report: one
that gives no answer, or a 5xx such as a tunnel's 502 while the service
behind it restarts, shows `not answering now`. A tunnel whose provider's
client is not signed in (Tailscale, devtunnel) shows how to sign it in, and
`wtc svc tunnel` refuses to open one until it is.

The detailed terminal status table labels internet-reachable tunnels with
`[public]`. A healthy public tunnel uses the same health color as a healthy
protected tunnel; failures and login problems retain their error color.
Running tunnels with `[exposure unknown]` remain warning-colored until their
access policy is known.
Plain service output also names each endpoint's exposure, including when
terminal colors are disabled.

No `wtc start` runtime command is introduced; the session orientation skill
keeps its existing meaning.

Service logs are append files under `.wtc/runtime/logs/<repo>/<task>.log`,
available after `down`. Hook logs live under
`.wtc/runtime/hook-logs/<repo>/provision.log` and `teardown.log`; agents can read
those files directly. They can contain application secrets, so keep them local.
For the native interactive console, run `wtc svc dekit -- attach`; it uses
the pinned binary, this collection's runner and its environment. Native `why`
and `screen --json` work the same way. The screen is a bounded terminal buffer,
not the persistent log. Tasks dynamically added through native dekit commands
are controlled by the runner but are not yet represented in WTC's configured
task list.

Edit task files rather than generated `dekit.yaml`. A changed file applies on
the next `wtc svc up` without stopping the runner: removed tasks are removed,
the runner reloads its config, and running tasks whose definition changed
restart; the others keep running. An existing runner keeps its launch
environment apart from `port_env` values; use a full `down`/`up` after
changing other variables or credentials.

`wtc retire` verifies the target runner stopped, clears its saved definitions,
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
