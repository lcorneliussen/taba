# A repository's own dekit.yaml

A repository can declare its services once, in a plain `dekit.yaml` at its
root. A bare `dekit up` in the repository runs that file, and taba reads the same
tasks as the repository's declarations. Its tasks are mounted under the
repository like those in `.harness/dekit.tasks.yaml`: `web/main` in the `widget`
repository is `widget/web/main` in `taba svc`. It runs in the collection's runner,
with the collection's environment and logs.

```yaml
# widget/dekit.yaml
defaults:
  env: {RUST_LOG: info}
load:
  - file: services/web.yaml
    at: web
tasks:
  db/main:
    cmd: ["postgres", "-D", ".data/db"]
    ready: {log: "ready to accept connections"}
```

taba follows dekit 0.10's rules for this file and the fragments it loads:

- Only a file named exactly `dekit.yaml` at the repository root is read.
  Without one, nothing changes.
- `load` takes globs, including `**`, relative to the declaring file, as strings
  or `{file, at}`. A fragment holds only `tasks` and `load`. A pattern that
  matches nothing is an error, and so is a fragment named `dekit.yaml`.
- `at` mounts a fragment's tasks, and their relative `deps`, under a path. In a
  dependency, `/db/main` means this repository's `db/main`. In
  `.harness/dekit.tasks.yaml` a leading `/` names a collection path such as
  `/other/db/main`.
- `cwd`, `script` and `add_path` resolve from the file that declares them.
  `defaults.cwd` resolves from `dekit.yaml`.
- `defaults` apply as dekit applies them: a task's own value replaces the default
  whole, `env` included. `stop` combines how to stop with the timeout. The
  supported defaults are `cwd`, `env`, `add_path`, `autostart`, `autorestart`,
  `stop`, `log`, `scrollback_len` and `mouse_scroll_speed`. Any other default is
  an error rather than being dropped.
- The runner's own settings stay with the collection runner: `kernel`, `log`,
  `on_init` and `on_idle` are read but never applied or run. `vars` is accepted.

Reading the file runs nothing: no hook, kernel, task command or environment file.
For that reason a `!js` value, which dekit evaluates when its runner loads the
config, is reported as unsupported, not evaluated or skipped.

## Containment

taba reads only regular files inside the repository. These are errors for the
repository, shown in `taba svc` as an `unknown` configuration item that names the
file:

- a load that leaves the repository or enters `.git`;
- a load cycle;
- an unreadable or malformed file;
- more than 256 files, nesting deeper than 16 loads, a file over 1 MiB, or glob
  patterns visiting more than 10000 directory entries.

A symlinked `dekit.yaml`, fragment or directory on the way to one is refused.
dekit itself follows links that stay inside the project, but taba does not read
symlinked declaration files, as for `.harness/dekit.tasks.yaml`. Replace the link
with the file or directory. A pattern that would descend through a link to a
directory is refused; a link to a file it would not load is skipped.

## With .harness/dekit.tasks.yaml and collection additions

A task path is declared once. The same path in two of the following is an error
that names both files:

- the `dekit.yaml` tree;
- `.harness/dekit.tasks.yaml`;
- the collection's own `svc/<repo>.yaml`.

There is no precedence and no overlay.

`dekit.yaml` may itself load `.harness/dekit.tasks.yaml`. Loaded without `at`, the
file is read once, as part of the `dekit.yaml` tree, with dekit's rules for paths
and dependencies. Loaded at a mount, it is an error, because taba also reads that
file at the repository root. dekit refuses `x-wtc` in any file it loads, so a
harness file loaded this way cannot carry taba metadata.

`x-wtc` is not a dekit field, and dekit refuses it in `dekit.yaml` and its
fragments, so a natively declared task has no `port_env`, mark or tunnel
metadata. Its URL comes only from a literal `ready.http` probe. No port is derived
from environment variables.

`taba svc rm` does not edit `dekit.yaml` or its fragments, even with `--shared`,
and that includes a `.harness/dekit.tasks.yaml` that `dekit.yaml` loads: change the
file in the repository.

## Provenance

`taba svc status --json` names the declaring file of each task in `declaration`.
`scope` is `repository` or `collection`, `file` is relative to that scope, and
`mount` is the `at` path of a loaded fragment:

```json
{"path": "widget/web/main", "source": "/abs/…/widget/services/web.yaml",
 "declaration": {"scope": "repository", "file": "services/web.yaml", "mount": "web"}}
```

`source` keeps its meaning: an absolute path, which for a managed tunnel is its
policy file. A task rendered before this field existed gains `declaration` when
its file still declares it.
