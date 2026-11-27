# Authenticated status observations

Status response caches belong to a selected local credential context. The
partition includes the forge host, credential-store selection and credential
material, compared through an HMAC using a random private key in the disposable
OS-user response cache. Usernames alone do not establish equality. Rotating a
token, changing stores or logging out prevents reuse of the previous context's
observations. Credential values and store paths do not appear in cache names.
Losing the cache key invalidates cached observations.

GitHub environment tokens and locally stored selected tokens can establish a
context without an identity API call. A keychain-only login is unknown to this
local reader. Bitbucket includes the local candidates used by direct HTTP reads
and the external CLI; OAuth refresh credentials are not treated as an established
stable context. Unknown contexts cannot reuse known or legacy observations.
Unknown credentials can still supply live status. Their observations and
inflight work live only in the current collection operation's memory, allowing
one live list to enrich several PRs without refetching the list. They are never
saved as active fallback or shared with a later operation or another pane.
A status pane keeps the PR rows it has shown. All passes of one pane share its
credential inputs, so a pass that defers a PR leaves its row with the earlier
observation and age. A newer observation replaces the row, changed credential
inputs remove it, and a kept row grants no actions.
Because unknown observations are not kept, a pass in an unknown context reads
its own open lists. It also reads a PR that left its list in every pass, unless
a recorded merge or decline covers the PR. The same aggregate request limits apply; a deferred
refresh stays visibly deferred.
This does not add an authentication probe on a frame, cache hit or status pane.

Detail, lists, checks, builds, terminal observations, ordering receipts and
inflight record locks use the same partition. An observation operation pins its
context and revalidates local selection before publication. Shared request
budgets and cooldowns remain conservative across contexts,
including consumers from 0.2.7; changing accounts does not grant a new burst.

Legacy unscoped response files are neither renamed nor promoted. Canonical
snapshot files remain available through the explicit offline `status --cached`
preview. The interactive view starts only from a separately scoped snapshot;
offline snapshots do not establish current permission or action authority.
The public snapshot JSON schema is unchanged.

This boundary prevents accidental reuse under the same OS user. It is not an
OS sandbox. A remote permission change without any local credential change is
not immediately detectable: existing observation age limits still apply.
