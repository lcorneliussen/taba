# Bootstrap a workspace

A workspace root is a plain folder. Shared bare Git owners live in `.bare/`;
each task gets a collection of worktrees, including its harness.

The minimal harness path requires **v0.1.39 or newer**. Earlier releases use a
populated harness; see the compatible bootstrap in the CLI repository:
https://github.com/lcorneliussen/taba/blob/main/docs/released-bootstrap.md .

## 1. Create your harness repo

Install WTC v0.1.39 or newer from a published release. Git is required;
mise is useful for pins and environment. Herdr, Neovim, agents, and forge CLIs
are optional interfaces. Your own Git remote must be available before the next
step.

```sh
wtc harness init agent-harness \
  --remote git@github.com:example-org/agent-harness.git \
  --cli-version 0.1.59
cd agent-harness
```

This creates six configuration and guidance files. It refuses to overwrite
existing files and does not initialize Git, publish, install tools, or run hooks.
Initialize, commit, and publish this as your own repository using your usual
Git workflow. Configure project tool pins under `[mise.tools]` in `wtc.toml`.

## 2. First bare owner and collection

Clone the published harness from its forge remote, rather than another local
worktree. The workspace root itself must not be initialized as a Git repo.

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

With mise, run `mise install` in `main`. The generated tool entry comes from the
harness's `.wtc-cli-version`; edits to the generated `mise.toml` are replaced at
the next refresh. `AGENTS.md` and `.wtc/instructions/` now provide the pinned
CLI's collection guidance, with project overrides where present.

## 3. Add application repos

The harness's `.harness-repos.yml` is the tracked registry. Keep its self entry
and add each application repo, for example:

```yaml
  - name: api
    remote: git@github.com:example-org/api.git
    default_ref: origin/main
    port_offset: 1
```

Commit the registry, then bring the repo into the collection:

```sh
wtc add-repo api
wtc status --local --no-watch
wtc new fix-login api web --no-open  # after registering web too
```

Missing bare owners are cloned on demand. Worktrees begin detached; create a
branch before the first commit. An agent or editor opens the collection root
so it can see all sibling repos. Use `wtc open` if you want herdr panes.

## 4. Customize only what the project owns

Read `wtc docs instructions/customize.md`. Add hooks, secret path declarations,
and project policy as needed. Use `wtc eject <path>` to take ownership of a
specific default; existing files are never overwritten. Keep credentials out
of Git. Collection-local overrides and logs are disposable; durable work
belongs in repository commits and forge records.

Optional services and resources use `wtc docs instructions/runtime.md`. Wrap
ordinary foreground development commands so people outside WTC keep their
existing local workflow. `wtc down` stops processes; retirement also runs strict
resource teardown before deleting the collection.
