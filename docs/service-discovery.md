# Service discovery before first start

`wtc svc`, `wtc svc status` and the initial status inventory include tasks from
checked-out repositories before their first render or start. Workspace-wide
status uses the same declaration merge for each collection, including when its
manifest is absent/invalid or the trusted runtime binary is unavailable. It keeps
the sweep's trusted-binary rule and existing diagnostics. Discovery reads
`.harness/dekit.tasks.yaml` and the collection's `svc/<repo>.yaml` additions in its
runtime home.
A repository must be a Git worktree in the collection, as for runtime planning.

A task absent from the runtime manifest has state `unconfigured`: it is declared,
but has not been imported into this collection's runtime configuration. This is
not a failed task or evidence that it is running. Names remain qualified by their
repository, so two repositories can both declare `web/main`.

Discovery parses files without running task commands, hooks, resource commands,
or shell environment files. It does not render configuration, create runtime
state, link secrets, or start/reload/stop a runner. Existing runtime status queries
and health observations retain their behavior. They run only for recorded tasks;
new declarations do not cause provider login queries or address probes.

Existing observations take precedence over declarations with the same path,
including failure details, live URLs, resource states and tunnel choices. A new
sibling's tasks appear beside these observations without replacing them.

Malformed YAML, invalid task names, duplicate shared/local task names, and
symlinked definition files or directories inside a checkout produce an `unknown`
configuration diagnostic for that repository. Repository links used by gateway
collections are supported: shared files are checked within the resolved checkout
and local additions within the collection. Other repositories and recorded
runtime items remain visible. Missing task files are normal. Discovery is an
inventory, not full validation of runnable configuration: environment variables,
working directories and runtime-specific options are resolved and validated by
an explicit render/start operation. Environment-derived URLs remain unresolved
until then.
