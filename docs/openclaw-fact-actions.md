# Actions on repository facts

The Collection card and sidebar keep repository facts in shared columns.
Unused PR and push/catch-up columns are omitted until observed, and those
columns stay reserved during the
conversation so status refreshes do not repeatedly move the facts. Sparse
repositories can therefore stay on one line at widths that previously folded.
When folding is necessary, the facts and their column controls share the
repository text inset.

Click `±N` to ask the conversation to review and commit that repository's
intended changes. The `±` control above the column asks for all
eligible repositories in one bounded request. Clean, blocked and already
pending repositories are excluded. The conversation must recheck current
state and branch policy; this action does not publish anything.

Fact controls reserve the same width and height while resting, hovered,
focused, disabled or pending. Hover and keyboard focus paint the button in
place. Pending commit feedback appears in the changed-files cell and is
shared by card and sidebar. A handoff receipt remains pending. An increased
unpublished-commit count with a clean working tree briefly shows a checkmark.
Partial commits settle the pending indicator and keep remaining changes visible. A reduced dirty count without new commits settles the pending
indicator without claiming success. After 90 seconds without observable
progress, the spinner becomes an unconfirmed-result control that opens the
Action log. Pending cells open those details too. **Ask agent for help** sends
one diagnostic request to the owning conversation, without retrying the action.
Failures offer retry in the same cell. Results remain in the
Action log after the brief feedback disappears.

The existing push and catch-up fact controls retain their publication
confirmations and state rechecks. Other recommended actions and alternatives
remain available in the action area and its menu. The collection toolbar also
remains available when the chat card is collapsed.

## Check the layout

Run the committed synthetic Chromium checker with an existing Playwright and
Chromium installation:

```sh
node openclaw-plugin/tools/check-fact-cells.mjs <playwright-module> <output-directory>
```

It checks card/sidebar widths, sparse and dense facts, header alignment, fixed
cell bounds across hover/focus/availability/pending/completion, feedback expiry
and viewport/menu clipping. Screenshots and a JSON measurement report go to the
chosen directory. It contacts no gateway or forge and dispatches only into its
synthetic host. This optional browser check is separate from the Node tests.

Group Catch up controls highlight their eligible rows on hover or keyboard
focus. This changes paint only. Gateway catch-up can retain and restore local
edits and staging; an unresolved operation or unavailable checkout stays out of
the group. See [gateway recovery](openclaw.md#gateway-catch-up-with-local-changes)
for recovery-copy retention and the gateway/sandbox boundary.

If a local commit request becomes unconfirmed, open its Action log details and
check the owning conversation. The diagnostic help action does not retry work.
After confirming that the earlier request has finished, **I checked: request
has finished** releases the browser reservation. It does not cancel work or
verify a commit; subsequent actions still require explicit selection.
