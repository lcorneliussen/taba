---
name: wtc-working-hours
description: Set or inspect private working hours, evaluate wind-down and next-window facts, and prepare a quiet authorized handoff when a working window ends.
---

Read [working-hours guidance](../../instructions/working-hours.md), then run
`wtc hours facts --json`. Use explicit setup only with a person-supplied
schedule. No personal hours are inferred or activated by loading this skill.

Re-evaluate at meaningful task/turn boundaries and the returned next transition.
This skill is portable guidance, not an automatic host hook or background timer.
