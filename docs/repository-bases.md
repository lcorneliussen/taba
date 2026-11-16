# Collection-local repository bases

A collection can explicitly use a different development branch for one repository
without changing its harness registry or other collections:

```sh
taba repo base set widget origin/beta --reason 'Feature work for the staging line'
taba repo base show widget
taba repo base clear widget
```

Use `--collection <directory>` to name the receiving collection. Names and aliases
must identify exactly one registered repository. `set` accepts a branch name or
`origin/<branch>` and requires a nonempty reason of at most 1024 UTF-8 bytes,
without control characters. It records intent only: it does not fetch, switch,
reset, merge, push or modify existing dirty or unpublished work. The branch must
be available when a consumer needs it; an unavailable override is an error.
`set` rejects an empty ref; clearing requires the explicit `clear` command.
`clear` restores the registry default, or `origin/main` when no default is set.

For a new collection, select bases before its worktrees are created:

```sh
taba new staging widget --repo-base widget=origin/beta --base-reason 'Staging work'
```

Repeat `--repo-base` for multiple selected repositories. Each receives the
explicit reason. A new collection never inherits the invoking collection's
local overrides. `add-repo` uses the receiving collection's setting, including
when invoked from another collection. An explicit existing branch or exact PR
head retains its existing checkout semantics. An existing local or origin branch
does not need the unused development base to be available, but receiving state
and identity validation still apply. Detached checkout and a new explicit branch
require the effective development base to be available.

The single local file is `.wtc/repo-bases.toml` in the receiving collection:

```toml
schema_version = 1
[repos.widget]
base = "origin/beta"
reason = "Feature work for the staging line"
```

This file is disposable local state, outside repository worktrees. There is no
parallel `.taba` file or environment override. Commands use canonical repository
names, reject ambiguous aliases, malformed state and symlinked state paths, and
serialize updates under a lock. Do not commit the file or copy it into a shared
registry. Malformed data must be corrected locally before it can be read or changed.
A multi-repository inventory reads one consistent validated collection snapshot;
the next operation rereads receiving edits. Updates always validate current state
again under the lock.
A valid stored key orphaned by a registry removal or rename is retained and does
not block unrelated repositories. `show` explains how to clear that exact old key;
`clear` removes it without inventing a new effective base. Unrelated updates
preserve orphaned entries. Stored keys are exact canonical identities: a later
repository that reuses an old name as an alias does not inherit or migrate its
intent. Explicit command aliases select the current repository and persist its
canonical name. If `clear` names an exact stored orphan key, that cleanup takes
priority over a newer alias; use the current canonical name to clear the newer
repository's setting.

If a selected repository has no canonical override but a retained key matches
one of its current aliases, implicit resolution refuses with an exact recovery
instruction. Explicitly set the current canonical repository or clear the old
key; the resolver never guesses that a rename or matching remote transfers
ownership. Explicit new intent can coexist with the retained old key. Clearing
that canonical override refuses before mutation until the related old key is
cleared, so a successful clear always has a real resulting base.

Browse's explicit unmanaged compatibility mode treats an unregistered folder
literally and uses `origin/HEAD` for an existing Git worktree when no current
canonical override applies. A retained orphan does not apply to that folder.
The entire local store is still validated first: corruption, ambiguous identities
and unsafe paths remain errors, including in unmanaged mode. The configured
harness folder role retains its explicit canonical repository mapping.

Worktree creation, catch-up, status, browse and review defaults use the same
receiving-local resolver: explicit override, registry `default_ref`, then
`origin/main`. Status exposes the configured base and reason separately from
its PR comparison ref. Ahead counts retain their upstream basis when a branch
tracks an upstream. An open PR's actual destination remains authoritative for
catch-up and PR comparisons; it never creates an override. Merged branches
return to the effective tip only when existing landing and work-preservation
checks succeed.

New PR preparation reads `taba --json repo base show <repo>` and uses
`data.effective_ref` without `origin/` as the default target. An existing PR's
actual target stays distinct; disagreement requires inspection, not automatic
retargeting. Production and release refs remain registry-owned. A development
override does not grant permission to target production, merge, deploy or delete
branches. TEST follows the effective development base; PROD keeps the existing
registry-derived deployment branch.

Review `BASE_REF` remains the actual diff target: explicit review `--base`, then
a readable PR destination, then the effective development default. Private
bundles include separate configured-base/reason and observed-PR-base facts;
related patches and development containment use the effective base while old
production snapshots retain registry refs. Public bundles never copy the raw
local reason or registry deployment policy. A public diff target may justify a
redacted development-base fact; other private configuration stays omitted.
These facts do not change review criteria or waive valid branch-target findings.

Local OpenClaw receiving collections use this base for default checkout selection.
Gateway publication destinations and worker authorization records remain separate
and authoritative; local settings are not copied into project or sandbox
metadata. A sandbox uses an existing authorized project's recorded publication destination
when its base is omitted; it never reads or claims to apply a host-local override.
Explicit sandbox bases must still match that destination. Prepare a different
publication destination through the existing explicit gateway workflow. Browse uses the serving CLI's
resolver rather than parsing another configuration format.

Browse checks that its final comparison resolves to a commit after choosing a
readable PR target. An unavailable final comparison is a visible error, not zero
ahead/behind counts or an empty tracked-change list. A readable PR target can
replace an unavailable configured development base.
