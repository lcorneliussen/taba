#!/usr/bin/env bash
set -euo pipefail

# npm ci belongs in a disposable copy, outside the source used for VCS stamping.
root="$(git rev-parse --show-toplevel)"
python3 -B "$root/tools/release-provenance/check.py" preflight
helper_check="$(mktemp -d "${RUNNER_TEMP:-${TMPDIR:-/tmp}}/taba-release-helper.XXXXXX")"
trap 'rm -rf -- "$helper_check"' EXIT
git -C "$root" archive --format=tar HEAD tools/devtunnel-host internal/wtc/assets |
  tar -xf - -C "$helper_check"
node "$helper_check/tools/devtunnel-host/check.mjs"
python3 -B "$root/tools/release-provenance/check.py" preflight
