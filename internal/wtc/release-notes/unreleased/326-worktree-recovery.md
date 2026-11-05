## OpenClaw

- OpenClaw Collection cards and sidebars distinguish evicted, recoverable checkouts
  and offer explicit per-repository and collection restore actions. Native admin
  recovery preserves recorded identities and saved changes; progress and failures
  appear in the Action log. Refreshing status never restores a checkout or extends
  its activity. Collection lifetime protection remains separate (#326).
- **Repository actions share one alignment rail.** Per-row `⋯` menus expose
  alternatives and blocked reasons; the header menu retains collection-wide
  catch-up, recovery and push preparation when another action is primary.
- **Action groups adapt to available width.** The primary stays right-aligned;
  additional eligible actions appear inline when space permits, with remaining
  and unavailable choices behind a left-side `⋯` menu.
