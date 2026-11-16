# Enlisted repository identity

PR enlistment, status enrichment and enlisted branch matching share repository
identity rules. A directory prefix alone does not establish an identity.

For a registered repository named `widget`, a checked-out `ext.widget` is an
alias only when its origin identifies the same forge, owner/workspace and
repository as the registry remote. SSH and HTTPS spellings of that identity
are equivalent. New enlistments using either spelling use the registry name.
GitHub owner and repository spelling [ignores case](https://docs.github.com/en/rest/repos/repos#get-a-repository);
local repository names are not folded. Other forge identities retain their
existing exact comparison.

For an unmanaged `ext.widget`, the bare spelling `widget` is accepted when
the origin is a supported forge and its repository basename is `widget`.
New enlistments use the actual directory name, `ext.widget`. An unrelated
`ext.notes` pointing at a repository named `widget` does not gain a `notes`
alias. Prefixes are not stripped repeatedly.

Alias inference uses Git roots inside the current collection. It does not
borrow an enclosing repository's origin or use a directory symlink outside
the collection as alias evidence. Exact-name linked checkouts keep their
existing branch association and origin fallback when the registry supplies no
forge identity. An unknown name stays unresolved. If a spelling identifies
different repositories or more than one worktree, enlistment reports ambiguity
and status does not guess a forge or attach that spelling to a branch. Use
distinct directory names to remove the collision. This is PR bookkeeping;
it does not rename worktrees or change registry and catch-up selector syntax.

A registry entry without a forge remote and only an `ext.<name>` checkout has
no verified link between those identities. The bare spelling reports ambiguity
until the registry remote is configured. The exact `ext.<name>` spelling still
identifies its checkout. This refusal avoids silently selecting an unrelated
repository from its basename.

Existing enlistment rows retain their spelling, merge markers and merge commit
metadata when updated through a verified alias. Re-enlisting the same PR does
not add another alias row. Conflicting duplicate rows require reconciliation;
enlistment does not silently choose one. Unlisting can remove verified alias
duplicates; an ambiguous spelling removes only its exact-key rows. Status reads
existing rows without requiring a migration. Forge caches continue to use the
full forge/repository identity and PR number.

Re-enlisting a single existing PR preserves its title when the incoming title is
empty and its URL when `--url` is omitted. A supplied nonempty title or URL
replaces that value. With no existing or supplied URL, enlistment still derives
a link from a verified supported repository remote; it does not fetch a title.
An omitted `--branch` still defaults to the checked-out branch, including an
empty branch for a detached checkout. Native publication retries retain an
existing title while applying the receipt's captured branch and URL.

When an enlisted PR is absent from the open list, status already looks it up
by number. A missing branch or merge marker does not prevent that lookup once
the repository resolves. Scoped status records final merged facts only after
the merge time and settled checks are known. Forge errors, unknown states and
unsettled checks retain their existing behavior; this alias fix adds no new
landing or branch-deletion policy.

Retained merged PRs use the verified directory association when computing the
existing next action, including `new-branch` for follow-up work. This internal
association adds no JSON fields. Cached snapshots retain their computed next
actions; older observations use exact-name matching without guessing aliases.

An enlisted PR can also be a read-only reference without a registry entry or
checkout. Supply its HTTPS GitHub `/owner/repo/pull/<number>` or Bitbucket
`/workspace/repo/pull-requests/<number>` URL with `wtc pr enlist --url`. The
number must match the row; credentials, unsupported hosts and conflicting or
ambiguous local identities are refused. Terminal progress explains lookup
refusals; JSON, Markdown and silent output retain UNKNOWN without that
progress message. Status uses the normal authenticated forge/cache path and
displays a canonical URL without query or fragment text. This does not
register a repository, attach a branch or grant catch-up, checkout, landing or
retirement authority. Closed references follow the existing omission rules;
settled merged facts can still be recorded.

A verified local repository can also retain its matching browser-view URL
(such as a PR’s `/files` view or an HTTP link): status still reads its
canonical forge and displays an HTTPS PR link. Conflicting identities remain
refused. Already recorded merge facts stay available even if a URL can no
longer be resolved; that does not restore a refused worktree association.
