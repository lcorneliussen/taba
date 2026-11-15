# Runtime status observations

Service status reads give native runner status and task listing one shared
five-second budget per collection. If either call exhausts that budget, tasks
whose state could not be observed show `unknown` with an error. A task-list call
does not receive a fresh five seconds after runner status returns.

When the runner reports `starting`, status continues to show `checking` with
`runner reports starting; task state is unavailable`. This does not establish
that any task is starting or healthy. A task that the runner actually observes
as starting retains that task state.

Workspace status uses the requesting collection's trusted runtime executable
and gives each target collection its own native observation budget. Saved
resource and tunnel facts and newly declared tasks remain visible. Ready
application address checks retain their separate two-second limit; the native
budget is not a five-second bound on the whole status command or workspace sweep.

Action and readiness polling retain their existing 30-second native command
limits and action deadlines. Post-action result snapshots also call
`RuntimeStatus`, so their task observations use the same five-second read budget.
An action can finish successfully while its returned snapshot reports an
unconfirmed task state as `unknown`. Status reads do not repair discovery, reload
task definitions, upgrade the runner, or change service state.
