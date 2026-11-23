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

- By default, a `dekit.yaml` at the repository root is read. Without one,
  nothing changes. Other locations are configured (see Configuration below).
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

## Trust

A repository's `dekit.yaml`, like its `.harness/dekit.tasks.yaml`, is code the
collection runs. Its tasks' commands, scripts, `add_path` and environment run in
the collection's runner when the services start. Reading the file to list them
runs nothing. Every checked-out repository with a root `dekit.yaml` is opted in
by default. A collection checked out to review an untrusted change should start
only the services it means to, or leave `dekit.yaml` out of `[runtime]
declarations`.

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
with the file or directory. Link targets are never looked at. A pattern that
names a link as a directory to descend through is refused, `**` does not descend
into links, and a link that a pattern matches as a file is refused when loaded.
A file is opened without following a final link or blocking on a special file,
and only if it is still the file that was checked.

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

## Configuration

Two settings in the harness `taba.toml` (or `wtc.toml`) `[runtime]` table can be
overridden from the environment:

```toml
[runtime]
declarations = ["dekit.yaml", ".harness/dekit.tasks.yaml"]  # the default
health_path  = "/health"                                      # default: none
```

| Setting | Environment | Default |
| --- | --- | --- |
| `declarations` | `TABA_RUNTIME_DECLARATIONS`, comma-separated | `dekit.yaml`, `.harness/dekit.tasks.yaml` |
| `health_path` | `TABA_RUNTIME_HEALTH_PATH` | none |

The order of precedence is `TABA_` variable, `WTC_` variable, harness file,
default. An empty variable counts as unset. Only the environment taba runs in is
read; collection env files are not sourced for these.

`declarations` lists the repository-relative files taba reads as each
repository's declarations, in order. A configured list **replaces** the default;
nothing is added to it, so to keep a default file, list it.

- A file named `dekit.yaml` is read as a native dekit project, with the rules on
  this page. Its directory is the project directory: its loads must stay inside
  it, and a task without `cwd` runs there. Its tasks are still named under the
  repository, as `<repo>/<task>`.
- Any other file is read as a taba task file, in the format of
  `.harness/dekit.tasks.yaml`, with `x-wtc` and `resources`.
- The collection's own `svc/<repo>.yaml` is always read as well and is not part
  of the list.
- An entry must be a plain, clean, relative file path: no `..` or `.`, no `.git`,
  no glob or control characters. An empty list, more than 32 entries or the same
  entry twice is an error.
- Every configured file must be a regular file inside the repository, at most
  1 MiB. A symlinked file, or a file under a symlinked directory, is refused.
  `.harness/dekit.tasks.yaml` keeps the checks it already had.
- `taba svc add --shared` writes `.harness/dekit.tasks.yaml`. It refuses when that
  file is not in the list, because taba would not read it back, and when a
  `dekit.yaml` loads it, because that file is then part of the native tree, which
  `svc rm` does not edit either.

`health_path` is a URL path, such as `/health`, for services whose readiness
taba makes itself: a task with `x-wtc` `port_env` and no `ready` of its own, or
only a `ready.timeout`.

- Such a task is ready when `http://127.0.0.1:<port><health_path>` answers,
  instead of when the port accepts connections.
- `taba svc status` checks that same address for `answering`, and shows it as
  `health_url` when the task's URL is the port-derived one.
- An explicit `ready` (`log`, `http`, `tcp` or any other) is never changed, and an
  explicit `x-wtc` `url` or `url_env` keeps its own answering check.
- A task declared natively in `dekit.yaml` has no `port_env`, so this setting
  does not affect it.
- The value must start with `/` and contain only URL path characters. A scheme,
  host, `//`, credentials, query, fragment, `.` or `..` segment, or whitespace is
  refused, and rendering or starting services fails with the reason.
- The check stays on the service's own local port. Status's existing check runs
  only for recorded ready tasks; discovery never probes.

Without either setting, behaviour is unchanged: readiness is the port accepting
connections, and existing definitions keep their digests.

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
