---
name: taba-open
description: Open or reshape a worktree collection in herdr — ensure the agent and status panes, add browse, shell or review panes on demand, trim idle ones, switch wide ↔ narrow, and start an agent only when the agent pane is empty. Use when the user says taba-open, open the collection in herdr, switch to narrow/wide layout, add a browse, shell or review pane, trim panes, or fill missing panes after starting an agent first.
---

# Open a collection in herdr

Mechanism: `taba open`. Canon: `.wtc/instructions/herdr.md`.

A workspace is ergonomics only — worktrees already exist. Opening is
idempotent: reuse the workspace, keep a live agent, fill idle panes, and
only reshape when asked (or when the layout is still partial). A workspace
starts with agent and status only; browse, shell and review come on demand, because
every pane holds one of the machine's capped pseudo-terminals.

## 1. Run it

From the collection root (or name the collection):

```bash
taba open                 # this collection; agent + status, auto layout
taba open --add browse    # add the browse pane (or shell); repeatable
taba open --add review    # pinned bundle or current PR diff (Hunk, or Git/less)
taba open --full          # agent, status, browse and shell
taba open --narrow        # stacked tabs; switch if needed
taba open --wide          # stacked columns; switch if needed
taba open --list          # pane-by-pane report; change nothing
taba open --dry-run       # plan only
taba open --trim          # close idle browse/shell/review panes (--all: every collection)
```

`WTC_PANES=full` in the control root's `wtc.env` makes `--full` the default.

Bare args after flags are collection names under the workspace root. `--all`
is always something you typed.

### Narrow vs wide

| | Wide | Narrow |
|---|---|---|
| Shape | `[ agent \| browse ]` / `[ shell \| status ]`; agent beside status without tools | tab `main`: agent/status; tab `tools`: browse/shell, once added |
| Heights | shell 20%, status 35% | same ratios, stacked full-width |
| When | default when the session is wide enough | `--narrow`, or `WTC_LAYOUT=narrow` / auto under `WTC_LAYOUT_NARROW_AT` |

`--narrow` / `--wide` **switch** an existing workspace. The agent pane is
preserved; the status pane moves with its running process. Auto never flips a complete wide ↔
narrow on its own.

## 2. Agent-first is normal

Often the human (or you) starts the agent in a bare workspace, then opens:

1. `taba open` detects a **partial** layout and builds wide or narrow around
   the existing agent pane.
2. If the agent pane already has a live agent, it is left alone.
3. If that pane is empty (or the agent exited), `taba-open` starts one —
   unless `--no-agent`.

Do not restart a working agent to "apply" a layout. Do not close panes to
force a reshape — pass `--narrow` or `--wide` and let the command move panes.

## 3. Panes on demand

When a step needs a human to look at files, `taba open --add browse`; for a
spare prompt, `--add shell`. For the pinned review range, `--add review`
uses the latest applicable bundle or exact current PR commits. It sits in the
wide right column or narrow tools tab and coexists with browse and shell.
It requires Hunk or less; no tool is installed automatically.
`--trim` closes a shell at its prompt, a browse Neovim without unsaved buffers,
and a review viewer that exited to its shell prompt.
It preserves agent and status panes, focused panes, and running commands. Trim when the user asks, or when
`taba open` warns that the machine is close to its pty cap.

## 4. What to report

The command prints one line per collection (`layout switched (narrow)`,
`agent live`, `browse started`, …). Prefer that over re-probing. `--list`
is the read-only check.

## 5. Stay out of the way

- Do not `herdr session stop` or close the workspace to change layout.
- Do not type browse/status commands into a pane that is already running them.
- Attach is the human's job: `herdr --session <project>`.

---
Canon: `.wtc/instructions/herdr.md`.
