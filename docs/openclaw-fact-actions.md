# Actions on repository facts

The Collection card and sidebar keep repository facts in shared columns.
Empty PR columns are omitted, and observed columns stay reserved during the
conversation so status refreshes do not repeatedly move the facts. Sparse
repositories can therefore stay on one line at widths that previously folded.
When folding is necessary, the facts and their column controls share the
repository text inset.

Click `±N` to ask the conversation to review and commit that repository's
intended changes. The `±` control above the column asks for the selected
eligible repositories in one bounded request. Clean, blocked and already
pending repositories are excluded. The conversation must recheck current
state and branch policy; this action does not publish anything.

Fact controls reserve the same width and height while resting, hovered,
focused, disabled or pending. Hover and keyboard focus paint the button in
place. Pending commit feedback appears in the changed-files cell and is
shared by card and sidebar. A handoff receipt remains pending. An increased
unpublished-commit count briefly shows a checkmark; remaining changed files
stay visible. A reduced dirty count without new commits settles the pending
indicator without claiming success. After 90 seconds without observable
progress, the spinner becomes an unconfirmed-result control that opens the
Action log. Failures offer retry in the same cell. Results remain in the
Action log after the brief feedback disappears.

The existing push and catch-up fact controls retain their publication
confirmations and state rechecks. Other recommended actions and alternatives
remain available in the action area and its menu. The collection toolbar also
remains available when the chat card is collapsed.
