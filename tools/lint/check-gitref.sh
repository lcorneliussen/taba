#!/bin/sh
# Asserts that golangci-lint's gitref rule flags exactly the lines of
# tools/lint/testdata/gitref/fixture.go marked "want gitref".
set -eu
cd "$(dirname "$0")/../.."
fixture=tools/lint/testdata/gitref/fixture.go
# A fresh cache: golangci-lint keys its cache on Go files, not on the rules
# file, so a cached run would hide a change to the rule.
GOLANGCI_LINT_CACHE=$(mktemp -d)
export GOLANGCI_LINT_CACHE
trap 'rm -rf "$GOLANGCI_LINT_CACHE"' EXIT
want=$(grep -n '// want gitref' "$fixture" | cut -d: -f1 | sort -n)
got=$(golangci-lint run --issues-exit-code=0 --max-same-issues=0 --max-issues-per-linter=0 \
	--output.text.path=stdout ./tools/lint/testdata/gitref/ 2>&1 |
	grep -E "^$fixture:[0-9]+:[0-9]+: ruleguard:" | cut -d: -f2 | sort -n)
if [ "$want" != "$got" ]; then
	printf 'gitref rule: want lines\n%s\ngot lines\n%s\n' "$want" "$got" >&2
	exit 1
fi
echo "gitref rule flags all $(echo "$want" | wc -l | tr -d ' ') fixture lines"
