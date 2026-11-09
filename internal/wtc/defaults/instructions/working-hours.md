# Working hours: help the person clock off

No schedule is assumed. Offer `wtc hours setup --help` once when useful; never
invent hours or activate a sample. The private local-person record lives under
the OS user's configuration root, shared across projects and collections.
`wtc hours view` shows the effective schedule. `setup` / `edit --file` explicitly
replace personal defaults; `--project` replaces this project's entire schedule.
`clear --project` resumes inheritance. `clear` removes personal defaults while
leaving explicit project schedules in place.

For a synthetic setup example (not a recommendation), supply this JSON through
`wtc hours setup --file -`, adding `--project` only for an explicit override:

```json
{"timezone":"Etc/UTC","windows":[{"days":[1,3],"start":"10:00","end":"16:00"}]}
```

Run `wtc hours facts --json` at each meaningful task or turn boundary, before
asking the person for a decision or sending a nonurgent notification, and at
`next_transition` while genuinely running. Check `evaluated_at`; re-evaluate
when time passes. TUI phases update live; portable guidance is instruction-only.
Loading this file or a skill once does not enforce future turns. There is no
new daemon, automatic per-turn host hook, timer wake-up or delivery mechanism.

- `working`: keep the next finishable priority clear.
- `wind-down` (last hour): narrow today's priorities, defer new scope, identify
  what can finish without human input and what needs the next working window.
- `wrap-up` (last 15 minutes): preserve work and prepare already-authorized
  commits, pushes or draft PRs. Record saved versus local-only work, PR/check
  state, unresolved decisions and a next-window plan. Draft team updates if
  useful; sending still needs explicit communication authority.
- `outside`: call `wtc hours claim-reminder --json` only when ready to give the
  nudge. Speak only when `reminder_claimed` is true. This claim is shared by all
  collections in this project for this outside interval. Offer a finite
  extension once. Respect explicit insistence without repeated debate; an
  agent may record `wtc hours extend --for 30m` only when the person explicitly
  chose that duration. Never infer extension authority from continued activity.
- `extended`: respect the person's choice. An extension applies to this person
  and project, expires automatically, and never rewrites recurring hours.
  `end-extension` ends it early. At expiry, refresh facts; a previously claimed
  nudge stays claimed for the same outside interval.

Be an eager coworker: “I've got this covered. I can finish the checks and prepare
the review; let's pick up the remaining decision at the next working window.”
Give the actual next local window and timezone from fresh facts. Continue only
already-authorized independent work quietly; batch nonurgent questions and
notifications. Say what can actually run without the person. Promise background
continuation or future notification only with a real available execution or
delivery mechanism. Never grant yourself extra execution, publication, merge,
cleanup or messaging authority. Do not cancel work, stop processes or withhold
essential status because a window ended. Keep an explicit bypass easy.

The record is private; facts and views contain personal times. Do not copy them,
project keys, local paths or identities into commits, PRs, team messages or
public artifacts. Use synthetic examples in documentation and tests.

Local identity means the OS account, not a configurable agent name. A managed
OpenClaw host remains unconfigured until an adapter proves the authenticated
person and compatible lifecycle hooks. Never read the gateway OS owner's hours
for every session, or infer identity from agentId, display name or USER.md.
