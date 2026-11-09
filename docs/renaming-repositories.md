# Renaming a repository

A registry entry's `name` is the repository's identity in a workspace: it names the sibling folder, the shared owner `.bare/<name>.git`, the `<NAME>_PORT` variable, service files, PR enlistments and secrets in the control root. Renaming an entry used to make wtc treat it as a new repository. `aliases` lets the name change without breaking collections that already exist ([#295](https://github.com/lcorneliussen/taba/issues/295)).

## Registry

```yaml
repos:
  - name: taba
    aliases: [wtc-cli]
    remote: git@github.com:<org>/taba.git
    default_ref: origin/main
    port_offset: 3
```

An alias must be a valid repository name, must not be `harness`, and must not repeat another entry's name or alias. Its port variable must not collide with another one.

## What keeps working

- **Lookups.** Every command that takes a repository name accepts an alias, including `catch-up --repos`. Enlisted PRs, review records and control-root secrets stored under the old name still resolve.
- **Folders.** A collection created before the rename keeps its `wtc-cli/` sibling. Status, catch-up, review, service and retirement commands work on it as before; services stay declared under the folder name. `wtc status` marks the folder "renamed to taba" (`renamed_to` in JSON).
- **Owner.** When `.bare/taba.git` does not exist, the owner under an alias is used in place. It is never cloned a second time or moved, because worktrees in every collection link into it.
- **Remote.** After a rename on the same forge host and owner, the next catch-up or checkout runs `git remote set-url origin` and reports it once. That needs both to be URLs on the same host and owner, an `origin` that still names the repository by its registry name or an alias, and a registry `remote` with a new name. Everything else is left alone: spellings of one repository (SSH or HTTPS, a `.git` suffix), forks and transfers to another owner, other repositories of the same owner such as a mirror, and plain filesystem paths. Update such an `origin` by hand. A collection whose harness still has the registry from before the rename does not set `origin` back. Ad-hoc collections and owners that are a user's own checkout are never changed.
- **Ports.** `.env.collection` emits the port under the current and every alias name, with one value.

New collections and `wtc add-repo` use the current name for the folder, even when asked for an alias. `add-repo` refuses a second checkout when the repository is already present under any of its names.

## Removing an alias

Remove an alias once no collection has the old folder any more and nothing reads the old port variable. Removing it earlier turns those folders into unregistered siblings.

## Ad-hoc collections

Ad-hoc collections remember repositories per user, keyed by their remote URL. Adding a renamed repository by its new URL registers it as a new repository with a new owner; asking for the old name with `--name` and the new URL is refused, because that name already stands for the old URL. Until ad-hoc registries learn aliases, keep using the old URL: forges such as GitHub redirect it to the renamed repository.
