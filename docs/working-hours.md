# Private working hours

`wtc hours` helps a person clock off while agents retain already-authorized
independent work. It starts **unconfigured**. No timezone or schedule is guessed,
no sample is activated, and installing or rendering guidance does not set hours.

## Setup, view and edit

Supply a JSON schedule explicitly with `wtc hours setup --file schedule.json`
or `--file -` for stdin. This synthetic example illustrates the format:

```json
{"timezone":"Etc/UTC","windows":[{"days":[1,3],"start":"10:00","end":"16:00"}]}
```

`days` uses Sunday=0 through Saturday=6. Times are zero-padded `HH:MM` in the
explicit IANA timezone. Start is inclusive and end exclusive; an end before its
start crosses midnight and belongs to its starting weekday. Split windows are
separate entries. Overlapping and adjacent entries are merged for phases.
Equal start/end is invalid; represent continuous coverage with adjacent windows.
Nonexistent DST minutes do not occur; repeated minutes occur twice. A window
wholly inside a repeated hour may therefore have two intervals. The timezone
database is embedded for hosts without system zoneinfo.

`setup --project --file …` replaces this project's complete schedule for this
person. `edit` is an alias for `setup`, with the same explicit input and scope.
`view` shows the effective private schedule and whether it comes from personal
defaults or a project override. `clear --project` restores personal inheritance;
`clear` removes personal defaults and leaves explicit project overrides intact.

## One private authority

The OS account identifies the local person. The authoritative record is
`working-hours/profile.json` under the per-user configuration root selected by
`UserConfigRoot` (normally `XDG_CONFIG_HOME/wtc` or `~/.config/wtc`, retaining
existing `wtc`/`taba` input precedence). The directory is private (0700); the
record and lock are private regular files (0600). Writes lock and replace the
record atomically. Schedule data is never generated into collections or public
project templates. This is a new optional record; existing configuration needs
no migration and no hours are enabled on upgrade.

Project overrides, extensions and reminder claims are keyed by a digest of the
canonical shared `Context.ConfigRoot` (`WTC_CONFIG_ROOT` / `TABA_CONFIG_ROOT`).
Collections using that same root share policy and reminder state. Different
control roots are separate projects, even if they check out the same repository.
Moving the root changes its key; re-enter the project override explicitly.
Separate OS accounts/configuration roots have separate personal records. Do not
point several people's processes at the same per-user configuration directory.
The digest is a storage key, not anonymization for publication.

## Fresh facts and temporary continuation

`wtc hours facts --json` returns an envelope whose `data` has schema `version: 1`,
`evaluated_at`, `configured`, `phase`, `timezone`, `source`, optional
`current_window`, `next_window`, `next_transition`, `extension`, and
`reminder_claimed`. Windows use RFC3339 instants with local offsets. Unknown
identity or absent schedules return `unconfigured`. An unsupported host also
returns `unavailable_reason` and explains the missing adapter. Facts do not
claim reminders or write state. `view` and facts are private output: do not paste personal times
or project identities into public records.

Phases are `working`, `wind-down` (last hour), `wrap-up` (last 15 minutes),
`outside`, and `extended`. For short windows, the appropriate later phase starts
immediately. Continuous weekly coverage returns `continuous: true` with no
bounded current window or end-of-day transition.
`next_window` means the next interval after now, including the next split window;
`next_transition` identifies when to evaluate again. Always use a fresh result,
not the model's remembered clock. TUI status evaluates fresh local facts on its
normal tick, independently of forge refresh. The hint shares the existing
progress row and yields while a refresh is in progress; click targets keep their
positions. One-shot terminal-table status adds a hint; Markdown and default
nonterminal output do not include private hours.
Cached snapshots and status JSON retain their existing schema; adapters use
`hours facts --json` for current hours.

After an explicit human choice, `wtc hours extend --for 30m` creates a temporary
extension for this person/project across its collections. Durations must be
positive and at most four hours, start now, and expire without changing recurring
hours. `end-extension` ends one early. Never infer permission from activity.

`wtc hours claim-reminder --json` atomically claims a single outside-window nudge
for this person/project and the current outside interval. Speak only when
`reminder_claimed` is true, and claim only when ready to deliver. Reading status
never consumes it. An extension suppresses nudges while active; a prior claim
remains claimed after expiry in the same outside interval. There is no automatic
message sender, scheduler or process cancellation.

## Agent integration and limits

`wtc hours guidance` prints the version-matched portable coworker guidance.
`wtc-working-hours` and the default collection instructions point agents to it.
Wind down by narrowing finishable priorities; wrap up by saving work, preparing
already-authorized publication, and recording saved versus local-only work,
PR/check state, decisions and the next-window plan. Continue authorized
independent work quietly and batch nonurgent questions. Team updates may be
drafted; sending still requires explicit communication authority. An extension
or explicit insistence receives no repeated debate. Essential status stays
readable and continuation is an explicit bypass.

The TUI refresh is automatic while running. Agent behavior is **guidance-only**:
no new daemon, per-turn host hook, timer wake-up, or delivery mechanism is
installed. Agents must re-evaluate at meaningful task/turn boundaries, before
nonurgent questions/notifications, and at transitions while actually running.
Loading a skill once cannot guarantee all-turn enforcement. Promise independent
background continuation or future notification only when a real mechanism is
available. Hours confer no extra execution, publication, merge or messaging
authority and never stop processes automatically.

OpenClaw is intentionally unconfigured and refuses mutations in this slice.
The current plugin has a credential-specific session-owner resolver for
Bitbucket. That contract is insufficient evidence of a compatible general
working-hours person mapping and prompt/transition lifecycle. A later adapter
must verify authenticated human identity and installed-version hooks, bind a
separate per-person record, and test cross-person/session isolation. It must
never apply a gateway OS owner's schedule to all sessions, or infer a person
from agent IDs, display names, or shared USER.md. No plugin upgrade is assumed.
The official [multi-user contract](https://docs.openclaw.ai/concepts/multi-user)
distinguishes creator, assignable owner, and participants; ownership alone does
not prove the current requesting person. Official [prompt/session hooks](https://docs.openclaw.ai/plugins/hooks/prompt-and-session)
describe prompt enrichment, but this slice has not established installed-host
compatibility and does not register a hook.
