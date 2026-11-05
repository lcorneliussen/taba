# Development and validation

Build with Go 1.27 or newer:

```sh
go build -o wtc ./cmd/wtc
go test ./...
go vet ./...
```

## Running a development build in a collection

When a collection has the wtc-cli repository checked out beside the harness,
build it into the collection with:

```sh
wtc dev install    # builds the wtc-cli sibling into .wtc/dev/wtc
wtc dev status     # which wtc is in effect here
wtc dev uninstall  # back to the pinned release
```

From then on, any `wtc` started inside that collection (a pane, an agent, a
hook; harness-based or ad-hoc) hands over to the dev build, as long as the dev
build is the same version as that `wtc` or newer. The dev build's version comes
from `git describe`: a build three commits past `v0.1.40` is `0.1.41-dev.3`,
above 0.1.40 and below 0.1.41. When the pinned release moves past the build, the
pinned binary runs itself again and notes this on a terminal. Run
`wtc dev install` again after each change. `WTC_DEV_BUILD=off` skips the
handover for one command, and `wtc --version` names the version a dev build
stands in for.

`wtc retire` removes the dev build with the collection.

The dev build is run only if it is an executable owned by you and not writable
by others. Plain `go build` binaries report version `dev` and never hand over.
The handover itself needs a pinned release that includes it (0.1.41 or later).

### Following a local checkout

To rebuild local changes automatically for this collection:

```sh
wtc dev mode on                 # select the unique CLI sibling, including ext.wtc-cli
wtc dev mode on --source /path/to/checkout
wtc dev status                 # selected mode, source and stored/running generation
wtc dev mode off               # persistent exit; use the ordinary selected CLI
```

`on` intentionally follows the selected source even if its version sorts below
an installed release. It does not fetch, switch branches or merge changes: an
older checkout stays that checkout. New invocations wait for a fresh successful
build, or fail clearly instead of running known-stale local code. Go determines
the source and embedded inputs; dirty edits, ignored embedded files and mutable
local module/workspace dependencies participate in freshness checks.

An explicit `--source` can select a compatible CLI fork with a different module
identity; it must be an owned Git checkout and pass the lifecycle capability
probe. AUTO remains limited to recognized CLI modules and registry identity.
The historical one-shot install keeps its existing module validation.

`auto` follows a unique verified CLI checkout only when the project's existing
`wtc.toml` (or `taba.toml`) explicitly names its registered repository:

```toml
[dev]
auto_source = "wtc-cli"
```

Use `wtc dev mode auto` to inherit this convention. A checkout present only as
reference material does nothing without that explicit convention. Ambiguous or
unsafe candidates are refused. Review-only collections and `review` commands
never automatically delegate to candidate code. Immutable review verification
must additionally use its explicit trusted executable with `WTC_DEV_BUILD=off`.
An unversioned `go build` binary retains its existing no-delegation behavior.

`off` overrides the project convention before source discovery or compilation.
It persists separately from generated artifacts, including after `dev uninstall`.
Uninstall removes the active selection and legacy bootstrap slot, but retains immutable
one-shot artifacts and follow generations until
collection retirement so running actions can finish from their original binary.
Final retirement removes this generated state. Ad-hoc directories retain their
existing reuse rules; retirement does not permanently prevent reinitialization.
Use `mode on` or `mode auto` explicitly to enable following again. The existing
`WTC_DEV_BUILD=off` escape works even with broken source; mode/status/uninstall
commands do not require a successful rebuild. Legacy one-shot `dev install`
does not undo persistent off.

One-shot installs also publish an immutable executable through the atomic selection.
A failed publication keeps the previous executable and metadata together. The
classic bootstrap pair is updated after that commit for older shims; if this
compatibility update fails, installation reports a warning and the new selection
remains valid. Older shims and installers do not understand this atomic selection
or its locks, so their concurrent two-file updates have no atomicity guarantee.

An updated status TUI checks in the background, shows rebuilding or failure,
and keeps its last working display while a build fails. It blocks new actions
on known-stale code. Successful replacements restart in the same terminal only
after active actions and reads finish. Navigation is restored; confirmations
and queued actions are never replayed. Disabling follows the same idle handover
back to the original selected executable. A TUI started from a local artifact
without a recorded ambient executable needs an explicit restart to exit it.
Older bootstrap shims cannot supply that path or understand the new mode policy:
use `dev uninstall` to remove their one-shot slot, then restart status explicitly
with `WTC_DEV_BUILD=off`.
Existing older TUIs need one initial restart before they can use this lifecycle.
Following belongs to the invocation's collection. Displaying another collection
with `status <name>` does not opt into or execute that collection's local source.
Follow mode probes the built CLI's structured lifecycle capability before
publishing it. An older checkout without this support is refused for following;
use the existing one-shot `dev install` workflow for it instead. A restart alone
cannot add automatic reload support to older source. A failed capability check
leaves the ordinary CLI's mode/status/off controls available and activates no
replacement generation.

This uses normal CLI invocations and the TUI timer, not a permanent daemon; no
process means no continuous background build. Poltergeist is not required.

Tagged releases build macOS and Linux binaries through GoReleaser. Generic
instructions and skills live under `internal/wtc/defaults/` and are embedded
in the CLI. Project-owned overrides belong in harness repositories.

## Runtime integration

The Go suite covers opt-in configuration, read-only status, resource ownership,
lifecycle failures, and retirement. To enable the real runner tests:

```sh
WTC_TEST_DEKIT=/absolute/path/to/dekit go test ./internal/wtc -run TestDekitRuntimeIntegration -v
WTC_TEST_DEKIT=/absolute/path/to/dekit WTC_TEST_DOCKER_IMAGE=postgres:17-alpine go test ./internal/wtc -run TestDekitSharedDockerPostgres -v
```

Use dekit 0.10.0 and pull the supplied Docker image first. The Docker test
creates one disposable container, without host ports, and two synthetic
collection-owned databases. It removes the container and its volumes afterward.
It does not use an existing shared database.

CI verifies the dekit archive checksum and uses a pinned Postgres image digest.
Actual Azure provisioning and connectivity across machines remain onboarding
checks for the project using them.

[Release notes](release-notes.md) are committed and embedded separately from
generated harness guidance. A change that affects behaviour adds its own
fragment, `internal/wtc/release-notes/unreleased/<issue>-<slug>.md`; it never
edits a shared file, so concurrent branches do not conflict over notes. Before
tagging, `go run ./tools/release-notes cut <version>` turns the fragments into
the numbered note; the release workflow publishes that exact file as the
GitHub release body and fails while fragments remain.

[Public demos](demos/README.md) record actual collection and bootstrap commands
against synthetic local repositories. They need no forge account or agent.

## OpenClaw integration tests

`internal/openclaw` holds what `wtc` does under OpenClaw ([guide](openclaw.md)).
Its integration tests drive a throwaway gateway in Docker and are not part of
`go test ./...`:

```sh
go test -tags openclaw_integration ./internal/openclaw/ -v
```

They build a test image from `internal/openclaw/testdata/gateway.Dockerfile`
(the OpenClaw image named by `WTC_OPENCLAW_IMAGE`, a Git and GitHub CLI recent
enough to publish, and `wtc` from this checkout) and run:

- the intent contract against synthetic repositories, with no account at all;
- whole conversations, in which a scripted model
  (`testdata/stub-model.mjs`) makes real agent turns run `wtc` and carry out
  its plans;
- the collection plugin from `openclaw-plugin/`;
- a conversation whose commands run in OpenClaw's Docker sandbox, from an
  image built out of `testdata/sandbox.Dockerfile`. Its gateway gets the
  Docker socket at `/var/run/docker.sock` and keeps its state on the Docker
  host under `/var/lib/wtc-openclaw-e2e/`, because the daemon resolves the
  sandbox's bind mounts there.

Tests that publish need a throwaway repository and a login, and skip without:

| Variables | Test |
|---|---|
| `WTC_OPENCLAW_GITHUB_REPO=<owner>/<repo>`, `GH_TOKEN` | `GitHub`: the gateway commits, pushes and opens a draft pull request |
| `WTC_OPENCLAW_BITBUCKET_REPO=<workspace>/<repo>`, `TWG_BBC_USER`, `TWG_BBC_TOKEN`, `WTC_OPENCLAW_BITBUCKET_SKILL=<checkout of the Bitbucket skill>` | `Bitbucket`: the skill pushes and opens the pull request |
| the GitHub variables | `SandboxedConversation`: a sandboxed agent turn runs `wtc publish` and `github_publish` opens the draft pull request |
| `OPENAI_API_KEY` with the GitHub variables | `RealModel`: an unscripted model, told only where wtc's guidance is, adds a repository and publishes; the test checks the outcome on GitHub |

Logins exist only inside the test container. The tests leave default branches
alone and remove the branches and pull requests they create.
`WTC_OPENCLAW_KEEP=1` leaves the gateway container running for a look inside
(with its sandbox containers and state directory), and `TestNotYetCovered` lists what no test reaches yet.
