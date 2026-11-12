Fix status refreshes that retained old OPEN/DRAFT pull request facts while a
complete open list omitted the PR and the shared forge allowance deferred its
confirmation. Unresolved confirmations now reserve bounded FIFO admission under
the existing allowance; competing panes cannot repeatedly spend their next token.
Reservations expire after five minutes without a retry, and older client
processes do not honor them. Request ceilings are unchanged.

JSON exposes additive `freshness`, `observed_at`, and `observation_source`
fields for PR state. A fresh unchanged list confirms state at its list-start
time without re-dating the older full-detail payload or its separate checks.
`settled` identifies complete historical merge evidence within the existing
24-hour cache policy; `recorded` identifies registry merge authority. Neither
requires a perpetual live read. Markdown and the live
view label stale/deferred facts; a new snapshot timestamp does not make them
current. Manual refresh (including `r` and `--forge`) uses the same cache and
allowance, so it can still report deferred observations. List absence never
proves a merge. Stale/deferred facts cannot authorize branch returns, settled
merge recording or next actions.
