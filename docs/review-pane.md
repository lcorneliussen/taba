# Review pane

`wtc open --add review` adds an optional diff viewer. Default workspaces still
have only agent and status; `--full` still adds browse and shell only.

In wide layouts review sits in the right column, above status and below browse
when browse is present. In narrow layouts it sits on the tools tab, between
browse and shell when both are present. Layout switches move existing panes
and preserve their processes. Reopening keeps a running viewer as it is.
After the viewer exits, `--add review` starts it again with a freshly resolved
range; plain `wtc open` leaves the idle review pane at its prompt.

The viewer prefers the newest applicable native `manifest.json` under the
collection's `.wtc-reviews/` or `.taba-reviews/` directory (manifest modification time, with directory
name as a deterministic tie breaker). It must identify this collection, a
checked-out sibling, the sibling's forge/repository, and its current branch.
The manifest's source directory is ignored. The pinned base and head must be
full commit IDs available locally; a matching bundle with unavailable objects
produces an error. Other branches and foreign or malformed manifests are ignored.
Bundles for multiple current siblings are ambiguous and produce an error.
Custom bundle locations outside those directories are not searched.

Without an applicable bundle, open reads current PR metadata for checked-out
branches. Local enlistment selects a PR when present; otherwise the forge is
queried. The PR must be open and match the local branch and source repository.
Multiple matching PRs require an explicit bundle or a narrower collection.
PR commit IDs come from the forge, including its exact base, rather than a
possibly stale tracking branch or unpushed local HEAD. Fork source repositories
that differ from the sibling's configured repository are not selected.

Both endpoints are validated as commits and their unique merge base is computed
before starting the viewer. The command reports the exact displayed endpoints.
It never fetches, checks out, merges, or substitutes working-tree changes.
Fetch missing objects explicitly, or build a bundle for the intended range.

When `hunk` is on PATH, open runs `hunk diff <merge-base> <head>`. Otherwise it
uses stock Git diff piped into `less`, with external diffs and text conversion
disabled and the pager in secure mode. Neither tool is installed automatically;
if neither Hunk nor less is available, open reports what is missing.

`wtc open --trim` closes a review pane only after its viewer has exited to an
unfocused shell prompt. A running viewer, focused pane, or failed process
inspection keeps the pane. Agent and status panes are always preserved.
