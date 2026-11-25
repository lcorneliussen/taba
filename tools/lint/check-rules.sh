#!/bin/sh
# Asserts that golangci-lint's ruleguard rules flag exactly the lines of each
# tools/lint/testdata/<rule>/fixture.go marked "want <rule>".
set -eu
cd "$(dirname "$0")/../.."
# A fresh cache: golangci-lint keys its cache on Go files, not on the rules
# files, so a cached run would hide a change to a rule.
GOLANGCI_LINT_CACHE=$(mktemp -d)
export GOLANGCI_LINT_CACHE
trap 'rm -rf "$GOLANGCI_LINT_CACHE"' EXIT
status=0
for dir in tools/lint/testdata/*/; do
	rule=$(basename "$dir")
	fixture="${dir}fixture.go"
	want=$(grep -n "// want $rule" "$fixture" | cut -d: -f1 | sort -n)
	got=$(golangci-lint run --issues-exit-code=0 --max-same-issues=0 --max-issues-per-linter=0 \
		--output.text.path=stdout "./$dir" 2>&1 |
		grep -E "^$fixture:[0-9]+:[0-9]+: ruleguard:" | cut -d: -f2 | sort -n)
	if [ "$want" != "$got" ]; then
		printf '%s rule: want lines\n%s\ngot lines\n%s\n' "$rule" "$want" "$got" >&2
		status=1
		continue
	fi
	echo "$rule rule flags all $(echo "$want" | wc -l | tr -d ' ') fixture lines"
done
exit $status
