# Release provenance

GoReleaser v2.19.0 validates Git before creating `dist/metadata.json` and
`dist/config.yaml`, then builds binaries. Without the root `/dist/` ignore,
these generated files make Go report `vcs.modified=true` despite clean source
at the initial validation. Both the build and release commands use this
[build pipeline](https://github.com/goreleaser/goreleaser/blob/v2.19.0/internal/pipeline/pipeline.go).

The release workflow runs `check.py preflight` before GoReleaser's `--clean`.
Both release and CI verify the locked helper through `verify-helper.sh`, which
exports its committed sources and embedded assets into a temporary copy, runs
the existing helper checks there, and then checks the original checkout.
Thus helper verification cannot leave ignored `node_modules/` in release source.
It rejects tracked or untracked source changes, locally ignored files outside
`dist/`, hidden index entries, tracked output paths, symlinks and unexpected
output files. Only the eight target directories for the two binaries and
GoReleaser's build metadata files are accepted as removable build outputs.
Archives and other unexpected prior outputs require manual inspection; the
guard never deletes or repairs anything.

GoReleaser's before hook repeats the source/output check, and its shared
post-build hook repeats it for each binary and reads `go version -m` without
executing the target. Missing VCS metadata, a different revision or timestamp,
or `vcs.modified=true` fails the build before publication. Both binary IDs
inherit the hook. No clean value is injected and VCS stamping remains enabled.

Run the synthetic Git/Go tests with:

```sh
python3 -B -m unittest discover -s tools/release-provenance -p '*_test.py'
```

For a clean checkout, `goreleaser build --snapshot` exercises the shared build
pipeline and every target without publishing. Use a disposable checkout;
the provenance hooks intentionally reject modifications even in snapshot mode.
This checks build provenance, not a defense against a malicious build tool or
concurrent edits reverted before inspection. Published v0.2.6 artifacts retain
their original dirty metadata; this change does not replace them.
