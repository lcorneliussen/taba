---
id: launch.handoff-adhoc
version: 1
inputs: [collection, goal, branch_note]
audience: private
---
# wtc: {{collection}} — launch note (EPHEMERAL)

**Goal:** {{goal}}

An ad-hoc collection: no harness, registry in `.wtc/config/repos.yml`.
First agent on this wtc: read this, turn anything durable into issues /
commits / PRs, then **delete this file as your very first action**.

{{branch_note}}

Add repositories with `{{command}} add-repo <name|clone-url|path>`. Retire with `{{command}} retire .` from inside it.
