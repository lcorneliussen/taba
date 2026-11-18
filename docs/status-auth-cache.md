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
This does not add an authentication probe on a frame, cache hit or status pane.

Detail, lists, checks, builds, terminal observations, ordering receipts and
inflight record locks use the same partition. An observation operation pins its
context and revalidates local selection before publication. Shared request
budgets, cooldowns and confirmation queues remain conservative across contexts,
including consumers from 0.2.7; changing accounts does not grant a new burst.

Legacy unscoped response files are neither renamed nor promoted. Canonical
snapshot files remain available through the explicit offline `status --cached`
preview. The interactive view starts only from a separately scoped snapshot;
offline snapshots do not establish current permission or action authority.
The public snapshot JSON schema is unchanged.

This boundary prevents accidental reuse under the same OS user. It is not an
OS sandbox. A remote permission change without any local credential change is
not immediately detectable: existing observation age limits still apply.
