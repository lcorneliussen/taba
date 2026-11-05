# Repository ports in OpenClaw collections

A gateway collection keeps links to OpenClaw-owned worktrees. Its local
registry must also retain the selected project's repository port metadata;
allocating a collection port block alone does not create repository variables.

On a collection status refresh, repositories are matched to the recorded
project by their verified Git origins, including verified rename aliases.
Missing `port_offset` fields are inherited from that project. Project names and
aliases become port aliases when the local row has no explicit alias field,
so a renamed repository can still supply an older service's port variable.
For example, project name `docs` pointing at `web.git` supplies both `DOCS_PORT`
and `WEB_PORT`. Repositories outside the selected project receive no guessed
port. Local owner paths, hooks and unrelated project settings are not copied.

An absent field inherits. Explicit `port_offset: null`, zero or another value
is preserved, as is an explicit alias list (including null or an empty list).
That means an explicit alias list controls exported names; add the project's
names there yourself if they should also be exported. Alias and port-variable
collisions are reported before either generated file changes. Ambiguous
project matches are reported instead of choosing by repository basename.

The generated `.env.collection` is repaired even when the registry already has
its ports. Its existing `COLLECTION_PORT_BASE` stays in place, and
`.env.collection.local` remains untouched and takes precedence at service
launch. Refresh runs no hooks and starts or restarts no services. After
refresh, retry the failed service with `wtc svc up <repo>/<service>` or its
Collection control. Running services keep their launch environment until an
explicit restart. A direct `svc up` without a prior collection refresh does
not discover missing project metadata.
