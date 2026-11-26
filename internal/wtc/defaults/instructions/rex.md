# Driving collections with Rex

Rex is a terminal multiplexer with a native app; its server keeps sessions,
tabs and panes alive while no app is attached. `taba open` drives it the way
it drives herdr (`instructions/herdr.md`): the same panes, layouts, repair,
trim and retirement. This guide lists only what differs. Like herdr, it is
**ergonomics only**: closing a collection's session must cost nothing.

## When taba uses Rex

herdr stays the multiplexer wherever it is installed. Rex is used:

- with `WTC_MULTIPLEXER=rex`, in the environment or the machine settings
  file (`WTC_MULTIPLEXER=herdr` chooses herdr);
- in a Rex terminal (`REX_BLOCK` set) on a machine without herdr on `PATH`.

Any other value of `WTC_MULTIPLEXER` is an error. `taba host` prints `rex`
exactly when `taba open` would use Rex. `taba open`, `taba new` (which opens
the new collection once one of the project's collections is in Rex, as it
joins a running herdr session) and retirement from a collection's own pane
then use Rex. A Rex terminal taba did not open counts as a plain terminal,
so an ad-hoc collection retires in place there. `taba retire <other>` closes the collection's herdr workspace
and its Rex session, whichever exist.

taba never starts or stops the Rex server. A missing server is an error:
start Rex (the app, `rex server start`, or `rex server run` on a machine
without a desktop session) first.

## Shape

```text
Rex server                              one per user, shared by everything
└─ session "<project>/<collection>"     one per collection; cwd = collection root
   ├─ tab main    agent | status, and in wide browse, review, shell
   └─ tab tools   browse / review / shell (narrow only, once one is added)
```

A Rex session is what herdr calls a workspace. Rex has no level between the
server and a session, so the herdr session name (`<project>`, `--session`,
`$HARNESS_HERDR_SESSION`, the `[herdr] session` setting) becomes the label
prefix that groups a workspace root's collections. Other Rex sessions are
never touched. herdr names the first tab `main` only in narrow; Rex names it
`main` in both layouts, since Rex would call it `Window <id>`.

Wide, narrow, `auto`, `--add`, `--full`, `--trim`, `--list` and `--dry-run`
behave as in herdr. `auto` measures the Rex window taba runs in and picks
narrow below `WTC_LAYOUT_NARROW_AT`; run from anywhere else, it picks wide.

Every pane taba creates starts in the collection root with the collection's
environment, the same values a herdr workspace gets. A Rex terminal takes no
environment, so taba starts each pane as `env <settings> $SHELL -l`. A pane
you split yourself does not get it.

Moving panes between tabs keeps the tab an app shows.

## Agents and their state

`taba open` types the agent's command at the agent pane's prompt, so the
shell comes back when the agent exits and the next open restarts it. The
program per kind is `claude`, `codex`, `gemini`, `opencode`, `cursor-agent`
(kind `cursor`), `pi` or `agy`; other herdr kinds are refused. Claude starts
with `--dangerously-skip-permissions` and
`--remote-control "<session>--<collection>"`, as under herdr.

Rex cannot tell when an agent is ready for input. The first prompt of a
fresh collection is therefore passed as the start argument to `claude` and
`codex`, which open their session with it. Other agents start without it,
and the open reports that the first prompt was not sent.

An agent's state is what it reports itself with OSC 7501 (Claude Code
does), from the process in the pane's foreground. `taba open --list` shows
it as under herdr; an agent that reports nothing is `unknown`, never `idle`.

`taba status`'s next actions send a prompt only to an agent that reports
`idle` or `done`. `taba catch-up --idle-only` (and so `taba upgrade
--rollout`) leaves a collection to its agent while it is `working`,
`blocked` or `unknown`, since an agent that reports nothing may be mid-turn.

`taba catch-up`'s status pane reload and `taba status --procs` work as under
herdr; `--procs` lists the processes in the panes of the project's Rex
sessions.

## Retirement

`taba retire .` from a collection's own pane hands off to a
`<project>/--cleanup--` session, which waits for the agent to finish its
turn, closes the collection's session and removes the folder. An agent that
reports `blocked` is waiting for you and has not finished. One that reports
nothing counts as finished once its visible screen has not changed for a
minute; taba cannot tell it apart from one that waits for an answer without
saying so.

## Limits under Rex

- `taba open --focus` needs Remote Control on in the Rex app (Settings,
  General); without it the open reports the refusal.
- A "focused" pane, which `--trim` keeps, is the focused pane of the active
  tab of any session a Rex app has open, whether or not the app shows that
  session right now.

## Inspecting by hand

```bash
rex ls                                   # sessions; collections are <project>/<name>
rex session inspect <session> --json     # tabs, panes and their labels
rex block call -b <pane> program_status  # what the agent reports
rex capture -b <pane> --trim             # a pane's screen
```

Never kill running state to apply a change (see herdr.md, "Never kill
running state"); `rex kill`, `rex block close` and `rex window close` end
the programs in them.
