---
id: launch.branch-review
version: 1
inputs: [primary_dir]
audience: private
---
**Review-only collection.** You review someone else's PR; the result is a verdict and nothing else.
Produce and post the review only: `{{command}} review bundle`, `{{command}} review run`, `{{command}} review post`.
No commits, no pushes, no thread resolution, no `{{command}} review ready`; those belong to the author.
Pushing from `{{primary_dir}}` is disabled. When the author pushes again, `{{command}} review bundle` moves to the new head for the next round.

Other siblings start detached at the tip.
