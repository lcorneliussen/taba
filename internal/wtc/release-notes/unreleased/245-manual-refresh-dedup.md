## OpenClaw

- **Reuse an active manual status refresh.** Repeated Refresh clicks across the collection card and panel share one read and one activity entry. Reloads after actions still request fresh status. Superseded or closed-view refresh observations no longer appear as unconfirmed actions or offer agent help. Missing or cancelled status responses, including background and post-action reads, report failure. This addresses part of [#245](https://github.com/lcorneliussen/taba/issues/245) ([#410](https://github.com/lcorneliussen/taba/pull/410)).
