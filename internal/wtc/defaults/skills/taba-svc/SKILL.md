---
name: taba-svc
description: Launch, run, serve, preview, share, stop or debug a repository's app, server, worker or tunnel in a worktree collection with taba svc. Use when the user asks to start or launch something, run the dev server, "show me" or open the app, give or share a link, expose it to another machine or to someone else, restart it, read its logs, find out why it does not start, or stop it. Do not start long-running servers in your own shell.
---

# Services in a collection

A collection runs its repositories' long-running processes — web servers,
workers, tunnels — under its own dekit runner. They keep running when your
session ends, use the collection's ports, show in `taba status`, and stop when
the collection is retired. A server started in your own shell has none of
that: it dies with you, may take another collection's port, and nobody sees it.

```bash
taba svc                        # what is enlisted, its state, URLs and tunnels
taba svc up [web]               # start everything, or one service
taba svc logs web [-f]          # recent output
taba svc restart web
taba svc down [web]
```

A name without its repository (`web`) works when only one repository has it;
otherwise use `<repo>/web`.

## "Launch it", "run the app", "show me"

1. `taba svc`. If the service is listed, `taba svc up <name>` and report its
   local URL (and any tunnel) from the output.
2. Not listed: find how the repository starts it — `bin/dev`, a mise task, a
   `package.json` script, a Procfile, its README. Find the variable that holds
   its port in `.env.collection` (`<REPO>_PORT`) or `.env.collection.local`;
   never hard-code a port, since every collection has its own.
3. Enlist and start it:

   ```bash
   taba svc add web --port-env WIDGET_PORT -- npm run dev
   ```

   This writes `svc/<repo>.yaml` in this collection's runtime home, usually
   `.wtc`, which is never committed. Add `--shared` only when the user wants
   the service in the repository for everyone; that changes
   `.harness/dekit.tasks.yaml`, a file to commit and review like any other.
4. The command must read the port from that variable (or be given it); check
   the URL answers before reporting it.

## It fails to start

`taba svc up` and `restart` print the last lines of each failed task's log —
usually the compiler or startup error. For more: `taba svc logs <name>`, then
`taba svc dekit -- why <repo>/<name>/main` and `taba svc dekit -- screen …`.
Fix the cause in the repository; do not work around it by starting the
process by hand.

## "Share it", "give me a link", another machine

Read the receiving-local named policy and recorded status first. Configure an
explicit entry with `taba svc tunnel <service> --name <name> --action configure
--definition /absolute/entry.toml`; the canonical runtime instructions document
its schema and provider inputs. Default writes are collection overrides. Project
default changes require an explicit native `--project` request; collection UI
ownership never grants that authority. Do not put token values in desired policy.

```bash
taba svc tunnel web --name preview --public       # approve/start current definition
taba svc tunnel web --name preview --stop         # retain stable registration
taba svc tunnel web --name preview --action unregister
taba svc tunnel web --name preview --action reconcile
```

Pass `--public` only when the user authorizes potential public exposure. A
provider name, HTTPS or declared account protection is not enforcement evidence.
Fresh observed Tailscale tailnet evidence can authorize a private start. Built-in
providers require explicit scoped hosting/management/cleanup file references or
an authorized daemon socket; do not run login, inspect global stores, install
clients, create resources or widen sharing merely to make a refusal disappear.
Dev Tunnel hosting needs an already installed Node22+ runtime; other providers
and status do not. Never start provider hosts in your own shell.

Several named tunnels can share a service. Stop retains the stable registration;
service down preserves desired selection. Clean old definitions before replacing
providers/inputs/hooks. Unknown effects remain pending; reconcile uses exact
receipts and cannot replay arbitrary callbacks. Keep cleanup code and credentials
until unregister/retirement completes. Status does not probe provider accounts.
The UI setup menu gives receiving-local configuration guidance; configured named
controls use the same policy/journal and definition-bound approval as the CLI.
Existing explicit temporary task declarations remain supported; do not replace
those with an automatically generated global-login provider command.

## Stop, remove

`taba svc down <name>` stops it; `taba svc rm <name>` stops it with its tunnels
and removes it from its task file while the others keep running. A service
from the repository's committed file needs `rm --shared`, which is a repository
change.

## Beyond taba svc

`taba svc dekit -- <args>` runs the collection's own dekit with its runner and
environment: `why`, `screen`, `attach`, `ls`, or a one-off `spawn`. Tasks
spawned that way are not in a task file and do not show in `taba status`;
prefer `taba svc add` for anything the user will come back to.

Only this collection's services: never start, stop or tunnel another
collection's.
