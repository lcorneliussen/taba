---
name: wtc-svc
description: Launch, run, serve, preview, share, stop or debug a repository's app, server, worker or tunnel in a worktree collection with wtc svc. Use when the user asks to start or launch something, run the dev server, "show me" or open the app, give or share a link, expose it to another machine or to someone else, restart it, read its logs, find out why it does not start, or stop it. Do not start long-running servers in your own shell.
---

# Services in a collection

A collection runs its repositories' long-running processes — web servers,
workers, tunnels — under its own dekit runner. They keep running when your
session ends, use the collection's ports, show in `wtc status`, and stop when
the collection is retired. A server started in your own shell has none of
that: it dies with you, may take another collection's port, and nobody sees it.

```bash
wtc svc                        # what is enlisted, its state, URLs and tunnels
wtc svc up [web]               # start everything, or one service
wtc svc logs web [-f]          # recent output
wtc svc restart web
wtc svc down [web]
```

A name without its repository (`web`) works when only one repository has it;
otherwise use `<repo>/web`.

## "Launch it", "run the app", "show me"

1. `wtc svc`. If the service is listed, `wtc svc up <name>` and report its
   local URL (and any tunnel) from the output.
2. Not listed: find how the repository starts it — `bin/dev`, a mise task, a
   `package.json` script, a Procfile, its README. Find the variable that holds
   its port in `.env.collection` (`<REPO>_PORT`) or `.env.collection.local`;
   never hard-code a port, since every collection has its own.
3. Enlist and start it:

   ```bash
   wtc svc add web --port-env WIDGET_PORT -- npm run dev
   ```

   This writes `svc/<repo>.yaml` in this collection's runtime home, usually
   `.wtc`, which is never committed. Add `--shared` only when the user wants
   the service in the repository for everyone; that changes
   `.harness/dekit.tasks.yaml`, a file to commit and review like any other.
4. The command must read the port from that variable (or be given it); check
   the URL answers before reporting it.

## It fails to start

`wtc svc up` and `restart` print the last lines of each failed task's log —
usually the compiler or startup error. For more: `wtc svc logs <name>`, then
`wtc svc dekit -- why <repo>/<name>/main` and `wtc svc dekit -- screen …`.
Fix the cause in the repository; do not work around it by starting the
process by hand.

## "Share it", "give me a link", another machine

Open a tunnel, private first:

```bash
wtc svc tunnel web tailscale          # this tailnet only
wtc svc tunnel web devtunnel          # the owner's account only
wtc svc tunnel web --stop             # close it
```

A tunnel on the internet — `cloudflare` (quick tunnel), `ngrok`, an anonymous
`devtunnel` — opens only with `--public`. Pass it only when the user asked for
a public link in so many words, and say that anyone with the link can reach
the service. Status shows public tunnels in the warning colour.

Always open a tunnel with `wtc svc tunnel <service>`, never as a service of its
own (`wtc svc add …/tunnel -- cloudflared …`) or in your shell: only then is it
in the group of the service it exposes, starts after it, and shows with it on
one line. A tunnel that shows as a line of its own was started the other way.
Consolidate it: note its provider, `wtc svc rm <that line>`, then
`wtc svc tunnel <service> <provider>` (with `--public` only if it was a public
one the user asked for), and give the user the new address — a quick tunnel's
address changes.

## Stop, remove

`wtc svc down <name>` stops it; `wtc svc rm <name>` stops it with its tunnels
and removes it from its task file while the others keep running. A service
from the repository's committed file needs `rm --shared`, which is a repository
change.

## Beyond wtc svc

`wtc svc dekit -- <args>` runs the collection's own dekit with its runner and
environment: `why`, `screen`, `attach`, `ls`, or a one-off `spawn`. Tasks
spawned that way are not in a task file and do not show in `wtc status`;
prefer `wtc svc add` for anything the user will come back to.

Only this collection's services: never start, stop or tunnel another
collection's.
