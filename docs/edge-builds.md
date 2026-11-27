# Edge builds

An edge build is the CLI built from one exact `main` commit whose CI push run
succeeded. You can try upcoming changes in a single collection with it, and
the harness pin stays as it is. It is not a release: it has no release notes,
it never becomes `latest`, and nothing selects it unless you name its exact
version.

## Names and contents

```text
0.2.12-edge.20261127151200.g5f525fa5abcd
└─┬──┘      └─────┬──────┘ └─────┬─────┘
next patch  commit time (UTC)    12 characters of the source commit
```

The base is the version after the newest numbered release notes in the
source. That is the patch of a release candidate, or the next patch after a
stable release. Edge builds of the same base sort by commit time and below
that base's candidates and release. The version is also the tag. It has no
`v` prefix, so it never matches the release workflow's `v*` tags or the
`v<version>` lookup in `taba upgrade --version`.

Each edge build is a GitHub prerelease that holds:

- `taba_<version>_<os>_<arch>.tar.gz` for `darwin` and `linux`, `amd64` and
  `arm64`. Each archive contains both executables, `taba` and `wtc`, built
  exactly as the stable release builds them.
- `checksums.txt` with the SHA-256 of every archive.
- `edge.json`, the manifest (`schema: taba-edge/v1`). It names the version,
  the full `source_sha`, the CI run that was the check evidence, and each
  archive with its checksum.
- A build provenance attestation for every archive.

An edge build is never replaced. A second publication of the same version is
refused.

## What edge never changes

| Stays as it is | Why |
| --- | --- |
| Stable and RC releases | Edge uses its own workflow and GoReleaser configuration, never a `v*` tag. |
| Release notes | No numbered notes and no fragment cut. The embedded notes and `taba release-notes unreleased` describe the source. |
| Homebrew | The edge configuration has no tap formula. |
| `taba upgrade` and `upgrade --rollout` | `releases/latest` never returns a prerelease, and `upgrade` refuses edge versions. |
| Committed pins | Selection is a local file in one collection (below). |

## Use an edge build in one collection

At the collection root (the folder that holds `harness/`, not a repository),
add a local mise selection. The collection root is not in Git, and
`taba retire` removes it, so the file is never committed and leaves with the
collection.

```sh
cd <collection>
cat > mise.local.toml <<'EOF'
# Local edge selection. Delete this file to return to the harness pin.
[tools]
"github:lcorneliussen/taba" = "0.2.12-edge.20261127151200.g5f525fa5abcd"
EOF
mise trust mise.local.toml
mise install
taba agent-env --write   # refresh the agent shells' cached toolchain path
taba --version           # prints the edge version
```

mise reads `mise.local.toml` after the generated `mise.toml`, so the edge
version replaces the pin only in this collection. `taba env setup`, catch-up
and `upgrade` regenerate `mise.toml` and leave `mise.local.toml` alone. The
cached toolchain path in `.env.toolchain` refreshes when a collection-local
`mise.local.toml` or `.mise.local.toml` appears, changes or disappears. An
agent shell that refreshes it between writing the file and `mise install` has
no edge install to find, so run `taba agent-env --write` after installing.

Processes that are already running keep the binary they started with.
Restart the status pane and agents to run the edge build (see
[CLI version in status](status-version.md)). The harness compatibility range
still applies, and edge versions count like other prereleases. Like dev
builds, a prerelease of the next minor (`0.3.0-edge.…`) still satisfies a
`<0.3` range, because it sorts below `0.3.0`. Read the unreleased notes
before you try one. A collection dev build (`taba dev status`) follows its
own handover rules and can stand in for the edge build.

## Verify it

```sh
v=0.2.12-edge.20261127151200.g5f525fa5abcd
dir="$(mktemp -d)"
gh release download "$v" --repo lcorneliussen/taba --dir "$dir"
(cd "$dir" && shasum -a 256 --check checksums.txt)     # sha256sum on Linux
jq -r .source_sha "$dir/edge.json"                     # the commit you meant
gh attestation verify "$dir/taba_${v}_darwin_arm64.tar.gz" --repo lcorneliussen/taba \
  --signer-workflow lcorneliussen/taba/.github/workflows/edge.yml
mise where github:lcorneliussen/taba                   # .../<version> for this collection
taba --version && wtc --version                        # both print the edge version
```

## Return to the pinned release

```sh
cd <collection>
rm mise.local.toml
taba agent-env --write
taba --version     # the harness pin again
```

Then restart the panes that ran the edge build. You can also remove the
install with `mise uninstall "github:lcorneliussen/taba@$v"`.

Keeping a selection for the whole workspace or project, and diagnostics that
compare the configured, applied and running versions, are tracked in
[#258](https://github.com/lcorneliussen/taba/issues/258). The running version
in status is covered by [#92](https://github.com/lcorneliussen/taba/issues/92).
Status shows an edge build's version without a release link, like other
prereleases.

## Produce an edge build

The `Edge` workflow (`.github/workflows/edge.yml`) takes the commit as input.
Run it from `main`:

```sh
gh workflow run edge.yml --ref main -f sha=<full commit SHA>
```

1. **Evidence.** The commit must be on `main`, and the CI workflow must have a
   push run on `main` for exactly that commit. Every such run must have
   completed successfully. A missing, running, failed or mismatched run stops
   the workflow (`tools/edge-release evidence`).
2. **Package.** The workflow checks out the commit, names it (`tools/edge-release
   version`) and builds it with `.goreleaser.edge.yaml` in snapshot mode. Nothing
   is published at this step. It then verifies the platforms, checksums and
   both executables and runs them on the build host (`tools/edge-release
   verify`). The result is a workflow artifact.
3. **Publish.** Publication is off until a maintainer enables it. It runs
   only when the repository variable `TABA_EDGE_PUBLISH` is `true`, the run
   is on `main`, and the protected `edge` environment approves it. It
   re-checks the artifact against the evidence and refuses an existing tag.
   Then it attests the archives and creates the prerelease with
   `--latest=false`. GitHub creates a missing environment without
   protection rules when a job names it. So create the `edge` environment,
   with required reviewers and only `main` allowed to deploy, **before**
   you set the variable.

A pull request that changes the edge packaging runs step 2 and nothing
else, so every change to it builds an unpublished package. Only commits that
contain this tooling can be built.

To package locally without publishing, use a clean, disposable checkout. The
provenance hooks reject modifications even in snapshot mode.

```sh
sha="$(git rev-parse HEAD)"
v="$(go run ./tools/edge-release version -sha "$sha" -time "$(git show -s --format=%ct HEAD)")"
TABA_EDGE_VERSION="$v" goreleaser release --snapshot --clean --config .goreleaser.edge.yaml
go run ./tools/edge-release verify -dist dist -version "$v" -sha "$sha" \
  -manifest dist/edge.json -notes dist/edge-notes.md
```
