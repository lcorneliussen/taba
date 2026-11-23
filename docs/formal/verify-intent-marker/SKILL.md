---
name: verify-intent-marker
description: "Model-check the intent marker protocol that keeps a side effect from taking effect twice (internal/retry Intent.Do, the review comment create in internal/wtc, the agent start in cmd/taba) with TLA+ and TLC. Use when a change touches internal/retry, an Intent's Marker, Done or Record, an Effect's outcome classification, or the comment.create-pending or .wtc-agent-start.pending markers, and before a new caller sends a side effect that might be repeated."
---

# Verify the intent marker protocol

`tools/formal/intent-marker/IntentMarker.tla` is a TLA+ model of one side
effect and the command runs that try to achieve it. TLC checks every order of
their steps up to a bound: runs overlap, crash between any two steps, lose
replies and run again. A model checker fits because each earlier fix in this
area (#498 lists 128bbe8, 22001b7, 6d71799, 32851db and def4f96) was an order
of steps that no test had written down.

This skill lives in `docs/formal/` because the repository has no directory for
contributor skills. `internal/wtc/defaults/skills/` ships to every harness.

## The property

The receiver applies the effect at most once. The model calls it `AtMostOnce`.
The comment on `Intent` in `internal/retry/retry.go` states the same rule.

If it breaks, a review posts two summary comments on a pull request, or open
starts a second agent in a collection. Nothing removes the duplicate.

## Check whether a change touches the verified code

The sync commit is in the coverage record below. List the covered files that
differ from it, committed or not:

```sh
git diff --stat ec9a52bcea1824aac0a266593a53a2e1a2b4a37b -- \
	internal/retry/retry.go internal/wtc/review_post.go \
	cmd/taba/open_collection.go cmd/taba/open_herdr.go
```

List every side effect that goes through the protocol, and every marker:

```sh
git grep -n -E 'retry\.Intent\{|create-pending|\.pending"' -- cmd internal ':!*_test.go'
```

Every hit must map to a row of the coverage record, except
`reviewPosterFiles` in `internal/wtc/review_context.go`, which keeps review
agents away from the marker (see the assumptions). A new `retry.Intent` is a
new caller: add a row, and a config if it differs from both callers below. A
side effect repeated in a loop of its own, outside `Intent.Do`, repeats the
128bbe8 bug. The `forbidigo` rule in `.golangci.yml` flags a wait in such a
loop; it cannot see an immediate repeat.

## Run the check

You need Java 11 or later. Run:

```sh
tools/formal/intent-marker/check.sh
```

The script downloads tla2tools.jar 1.7.4 into `tools/formal/.cache/` and
checks its sha256. It takes about 15 seconds. CI runs it in the `formal` job
of `.github/workflows/ci.yml`. The first line of each config states the result
TLC must report, and the script fails on any other result:

| Config | Design | Expected |
| --- | --- | --- |
| `main-review-post.cfg` | review post on main | `AtMostOnce` holds |
| `main-agent-start.cfg` | the agent start on main | `AtMostOnce` holds |
| `replay-128bbe8-parent.cfg` | any failure attempted again, no marker, as before 128bbe8 | violated |
| `replay-22001b7-parent.cfg` | no marker, as before 22001b7 | violated |
| `replay-d396c59-review-post.cfg` | marker, but comment.id read only before it, as before #498 | violated |
| `replay-d396c59-agent-start.cfg` | no marker for the agent start, as before #498 | violated |
| `record-after-release.cfg` | marker removed before comment.id is saved | violated |

`VERBOSE=1 tools/formal/intent-marker/check.sh main-review-post.cfg` prints
TLC's own output, including the trace of a violation. Put a scratch config in
`tools/formal/intent-marker/`: TLC does not find a config in another
directory.

## Read the result

Read every result with the pstack `/statechart` skill. Give it
`IntentMarker.tla` and the config. It tells each violation as its shortest
path in plain words. When the check passes, it draws how the runs and the
marker move between states. For a chart, use a copy of a main config with two
runs and `MaxAttempts = 2`.

## Tell a real bug from a modeling error

Do these steps when a main config fails or a replay config holds.

1. Map each step of the path to code with the coverage record.
2. Replay the path against the real code as a Go test, with one call per step.
   `TestOpenAgentStartAfterNotReadyIsNotResent` and
   `TestOpenAgentStartWithAnUnknownOutcomeIsNotResentByALaterOpen`
   (`cmd/taba/open_agent_start_replay_test.go`) drive open through a herdr
   stand-in that records each start. `TestReviewPostDoesNotCreateACommentAnOverlappingPostCreated`
   (`internal/wtc/review_post_create_test.go`) saves comment.id from the
   stand-in gh in the middle of a post. `internal/retry/retry_test.go` drives
   `Intent.Do` directly.
3. If the test shows the path, the code has a bug. If it does not, treat the
   model as wrong until you show otherwise, and fix the model.

Never weaken `AtMostOnce` or remove a run or a crash to make a main config
pass. A replay config that starts to hold means the model lost the mechanism
that the replay tests. Fix the model.

## Update the model for an intended change

1. Change the action that the coverage record maps to the changed code.
2. When the change adds a design choice, add a constant for it and a config
   that sets it. Give the config an `expect` line.
3. Run the check and read the result with `/statechart`.
4. Update the coverage record. Move the sync commit only after you confirm
   that the model describes the covered code at the new commit.

## Coverage record

Sync commit: `ec9a52bcea1824aac0a266593a53a2e1a2b4a37b` (2026-11-23).
Line numbers are at that commit.

| Model action | Code |
| --- | --- |
| `Check` | Review post reads `comment.id` before anything else (`internal/wtc/review_post.go:201`); a saved ID takes the update path, which creates nothing. Open reads the pane and leaves a live one alone (`openStartPanes`, `cmd/taba/open_collection.go:317`). |
| `Claim` | `os.OpenFile(in.Marker, O_CREATE\|O_EXCL)` in `Intent.Do` (`internal/retry/retry.go:115`). An existing marker returns `ErrUnresolved`. Markers: `<bundle>/comment.create-pending` (`review_post.go:224`), `<collection>/.wtc-agent-start.pending` (`open_collection.go:444`). |
| `Recheck` | `in.Done()` (`retry.go:126`). Review post: `comment.id` is non-empty (`review_post.go:230`). Open: `occupied`, the pane's foreground is not a shell (`open_collection.go:445`). |
| `Send`, `Process` | `effect(ctx)` (`retry.go:136`). Review post: `postReviewComment(target, "", body)`, a GitHub or Bitbucket comment POST (`review_post.go:238`). Open: `herdr agent start` in `start` (`open_collection.go:422`). |
| `LoseReply` | Review post: any error from the POST. Open: `agent_not_ready` that `openReconcileStartedAgent` cannot verify, a reply that is not herdr's structured error, a timeout (`open_collection.go:422-442`). |
| `Settle` | The outcome switch in `Intent.Do` (`retry.go:140-153`). Open classifies `agent_pane_busy` on an idle shell as `Busy`, `agent_pane_busy`, `agent_name_taken` and `agent_start_failed` as `Refused`, and a verified `agent_not_ready` as `Applied` (`open_collection.go:430-441`). Review post classifies nothing but success as `Applied`. |
| `Record`, `Release` | `in.Record()`, then `os.Remove(in.Marker)` (`retry.go:142-146`). Review post saves `comment.id` (`review_post.go:234`). Open records nothing: the pane shows the agent. |
| `Crash` | The process dies. Files and a sent request survive. |
| `RetryUnknown = TRUE` | The loop in `openStartAgent` before 128bbe8, which started the agent again after any failure. |

## Bounds and assumptions

- Main configs use three runs. Open allows three attempts after busy replies;
  the code allows 15. Review post makes one attempt.
- The receiver applies every request it processes, or refuses it as busy. A
  receiver that drops duplicates (herdr refusing a taken agent name) only
  helps; the model does not count on it.
- A reply can be lost while its request is still processed later. Replies are
  never matched to the wrong attempt.
- Only the run itself writes the marker and the saved result. Review agents
  in a public review context cannot copy or change `comment.id` or
  `comment.create-pending` (`reviewPosterFiles`,
  `internal/wtc/review_context.go:254`).
- A person removes a marker only after confirming what the earlier attempt
  did and that its process has ended. Removing it sooner can repeat the
  effect, and the model does not include it.
- Retirement removes `.wtc-agent-start.pending` with the collection
  (`removeRetiredGeneratedFiles`, `internal/wtc/retire.go`), after it has
  closed the collection's herdr workspace. No later open of that collection
  follows.
- Open's `Done` reads the pane after the run that held the marker has
  settled; an applied start shows its agent in the pane by then. An agent
  that exits later is a new intent, not a repeat.
- Not covered: the update path of review post (a PATCH of the saved comment
  is idempotent), inline review comments, the first prompt after an agent
  start, catch-up's pane reloads, and tunnel provider effects, which keep
  their own journal.
