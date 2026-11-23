---
id: launch.handoff
version: 1
inputs: [collection, goal, branch_note]
audience: private
---
# wtc: {{collection}} — launch note (EPHEMERAL)

**Goal:** {{goal}}

First agent on this wtc: read this, turn anything durable into issues /
commits / PRs, then **delete this file as your very first action**
(harness/AGENTS.md → "State lives in git").

{{branch_note}} Collection env: `.env.collection` (inherited via `mise.toml`).
Retire with `{{command}} retire .` from this collection's Herdr workspace, or `{{command}} retire {{collection}}` from another collection.
