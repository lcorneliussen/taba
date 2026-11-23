# Native macOS coverage

`ci.yml` keeps the full Linux gate and the `test-macos` check context.
`macos-native.py` builds and smoke-tests the native CLI, then runs the exact
top-level tests in `macos-native.json`, without Go's result cache. Each group
records its platform risk. Missing names, empty selections, skipped top-level
tests, malformed output and test failures fail the lane.

The small PR lane focuses on native process inspection and ownership-safe
signals, filesystem locks/no-follow/inode races and FIFO reads, executable
handover, and PTY diagnostics. It excludes generic forge/Git scenarios already
covered on Linux. PTY diagnostics use fixtures; executable handover and process
inspection exercise real native subprocesses. No live provider is required.

When adding platform-sensitive behavior, review this manifest and add a named
test with its risk rationale. A new `*_darwin.go` file fails the source inventory
guard until its coverage is considered and the inventory is updated. Shared
Unix implementation changes also need a coverage decision during review; the
inventory does not automatically classify all platform-sensitive behavior.

Run locally from the repository root:

```sh
python3 -B -m unittest discover -s tools/ci -p '*_test.py'
python3 tools/ci/macos-native.py
```

The Linux job also rejects Python bytecode in the head tree and every commit of
the PR or push range; run the same check before pushing:

```sh
python3 tools/ci/check-generated-artifacts.py --base origin/main
```

`macos-full.yml` retains the previous full Mac job: locked helper validation,
Python timing and upgrade-rehearsal tests, the full Go suite with timing output
and Node available, then a native build. It runs daily at 03:23 UTC and through
`workflow_dispatch`. A failed run remains a failed Actions run with normal logs;
there is no failure suppression. After merge, dispatch it from the default
branch and verify completion before claiming scheduled coverage is exercised.

Measure queue wait separately from execution using Actions job `created_at`,
`started_at` and `completed_at`; use step timestamps and the script wall time
for execution comparisons. A shorter lane does not guarantee runner availability.
