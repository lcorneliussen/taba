# First workspace with a populated harness

This is the populated-harness path, including v0.1.38. The
[minimal scaffold](getting-started.md#minimal-harness) is available from
v0.1.39. Start with your own harness repository adapted from the optional
[reference harness](https://github.com/lcorneliussen/taba-boilerplate).

Git and WTC are required. Authenticate Git for your forge before cloning.
Mise is useful for version pins and environment loading. Herdr, Neovim, an
agent CLI, and `gh` are optional; forge operations need the corresponding
forge CLI and authentication.

## Prepare the harness

In your harness repository, commit an exact published CLI version without
the `v` prefix in `.wtc-cli-version`. Commit a `.harness-repos.yml` registry
with the harness's own remote:

```yaml
schema_version: 1
repositories:
  - name: agent-harness
    remote: git@github.com:example-org/agent-harness.git
    default_ref: origin/main
```

Publish it to your own remote. The repository name must match the bare owner;
a custom name can be configured with `[harness] name = "..."` in `wtc.toml`.

## Create the first collection

The workspace root is a plain folder, not a Git repo. Clone the bare from the
forge, not another local worktree:

```sh
mkdir -p ~/Code/example-workspace/.bare
cd ~/Code/example-workspace
git clone --bare git@github.com:example-org/agent-harness.git .bare/agent-harness.git
git --git-dir=.bare/agent-harness.git config remote.origin.fetch '+refs/heads/*:refs/remotes/origin/*'
git --git-dir=.bare/agent-harness.git fetch origin --prune
git --git-dir=.bare/agent-harness.git worktree add --detach main/harness origin/main
cd main
wtc env setup
wtc skills render --seed-scope
```

With mise, run `mise install`. WTC generates the collection-root `mise.toml`
from the committed CLI pin and harness tool configuration. Local environment
overrides belong in `.env.collection.local` and survive environment refreshes.

## Add repos and tasks

Add application entries to the tracked registry, with their own remote and
`default_ref`. A repo that serves a port can use a unique `port_offset`:

```yaml
  - name: api
    remote: git@github.com:example-org/api.git
    default_ref: origin/main
    port_offset: 1
```

Commit the registry change, then run:

```sh
wtc add-repo api
wtc status --local --no-watch
wtc new fix-login api --no-open
```

Missing bare owners clone on demand. Each task gets detached worktrees; create
branches before committing. Open an editor or agent at the collection root to
see every repo in scope. `wtc open` optionally creates herdr panes.

Use `wtc customize` for tool pins and hooks. Application setup hooks are
optional: keep ordinary development commands usable outside WTC. Follow the
[secret guide](../internal/wtc/defaults/instructions/secrets.md) for credentials
and optional workspace-specific tool identity stores.
