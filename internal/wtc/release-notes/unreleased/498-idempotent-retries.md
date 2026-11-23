## Workspace opening

- **An agent start with no confirmed outcome is not sent again.** When Herdr
  does not confirm an agent start, open leaves `.wtc-agent-start.pending` in
  the collection, and a later open refuses to start another agent there. Inspect
  the agent pane, then remove that file to allow a new start.
  ([#498](https://github.com/lcorneliussen/taba/issues/498),
  [#507](https://github.com/lcorneliussen/taba/pull/507)).

## Review

- **Overlapping review posts create one summary comment.** A post that started
  before another one saved its comment now finds that comment and asks you to
  post again, instead of creating a second one.
  ([#498](https://github.com/lcorneliussen/taba/issues/498),
  [#507](https://github.com/lcorneliussen/taba/pull/507)).
