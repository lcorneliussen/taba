# Getting started

taba (formerly wtc) needs Git. A harness repository describing the repositories in your
workspace is optional: an ad-hoc collection works from any empty folder. Mise
manages tool versions and environment; it is optional. Herdr, Neovim, and an
agent CLI are optional interfaces.

An agent setting taba up should start with
[`taba docs setup.md`](../internal/wtc/defaults/setup.md) instead: it decides
between this guide, the harness bootstrap and an OpenClaw gateway.

## Install taba

Install with Homebrew or mise, or download a [published release](https://github.com/lcorneliussen/taba/releases):

```sh
brew install lcorneliussen/taba/taba
# or
mise use -g github:lcorneliussen/taba@0.2.0
```

Each release installs `taba` and `wtc`, the same program under two names. The
examples use `taba`; existing scripts and pins can keep `wtc`. File and
variable names such as `.wtc/` and `WTC_CONFIG_ROOT` stay as they are.

The workspace root is an ordinary folder. Each collection contains a worktree
of your harness and the application repos needed for its task.

## Ad-hoc collection, no harness

For one task across a few repositories, skip the harness:

```sh
mkdir payments-fix && cd payments-fix
taba init
taba add-repo git@github.com:example-org/api.git
taba add-repo ~/code/web            # an existing checkout becomes the owner
```

The folder is the collection. Its registry, `.wtc/config/repos.yml`, is local
and tracked nowhere. The first time a repository is added, `taba add-repo`
asks for the branch worktrees rest on and pull requests target, suggesting the
remote's default; `--target <branch>` answers non-interactively. Worktrees
always start detached at that branch, never at whatever an existing checkout
has checked out. For an existing checkout, taba adds that branch to the
checkout's `remote.origin.fetch` if its refspec does not already cover it, so
a single-branch clone keeps the tip current.

Clones are shared per user under `$XDG_DATA_HOME/wtc/bare` (default
`~/.local/share/wtc`, or `$WTC_DATA_HOME`), and every repository added once is
remembered there by name: the next ad-hoc collection can run `taba add-repo
api`. Secrets come from `$WTC_CONFIG_ROOT`, defaulting to `~/.config/wtc`.
`.wtc/config/` takes the same `wtc.toml`, `hooks/` and overrides as a harness.
`taba new <slug> [repo ...]` from an ad-hoc collection starts the next task in
a sibling folder with the same registrations. `taba retire .` removes the
worktrees and the folder; owners and the per-user registry stay.

## Minimal harness

`taba harness init` creates a small harness whose generic instructions come
from the CLI. Use **v0.1.39 or newer**: v0.1.38 does not include this command
or the complete embedded instruction fallback.

```sh
taba harness init agent-harness \
  --remote git@github.com:example-org/agent-harness.git \
  --cli-version 0.2.0
```

The command requires an explicit version, creates six files, and refuses to
overwrite existing files. It does not initialize Git, publish, install tools,
or run hooks. Use `--dry-run` to preview the filenames.

Commit and publish the harness to your own remote. Then follow the
[versioned bootstrap guide](../internal/wtc/defaults/bootstrap.md) to create
its bare owner and first collection. Add application repositories to its
registry and use `taba add-repo` or `taba new` to bring in worktrees.

The recording uses local synthetic remotes to stay reproducible and offline;
normal workspace bootstrap clones the harness from your forge.

## Existing populated harness

Your own adaptation of the optional
[reference harness](https://github.com/lcorneliussen/taba-boilerplate) remains
supported. Follow the [populated-harness bootstrap](released-bootstrap.md)
for its first bare owner and collection. That path also works with v0.1.38.
Use the [upgrade notes](release-notes.md) when changing an existing harness pin.

## Add a repository without changing ordinary development

The registry records its name, Git remote, development ref, and optional port
offset. `taba add-repo api` brings it into the current collection. Setup hooks
are optional; introduce them only for collection-specific preparation.

Keep `bin/dev`, mise tasks, npm scripts, and other local commands usable outside
taba. Make collection-specific setup optional so developers retain the same
local workflow.

For the opt-in runtime available from v0.1.40, add a small task fragment that
wraps the existing foreground launch command. Start with the [onboarding guide](../internal/wtc/defaults/instructions/runtime.md)
and [synthetic example](../examples/runtime/README.md). Test standalone startup,
parallel collections, logs after shutdown, and teardown failure before adopting
it for real resources.
