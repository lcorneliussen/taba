#!/usr/bin/env bash
# Model-check the intent marker protocol (IntentMarker.tla) with TLC.
#
#   tools/formal/intent-marker/check.sh            every config here
#   tools/formal/intent-marker/check.sh main-review-post.cfg   one config
#   VERBOSE=1 tools/formal/intent-marker/check.sh  also print TLC's output
#
# The first line of each config says what TLC must report: "\* expect: pass",
# or "\* expect: <Invariant>" for a config that must violate that invariant.
# Needs Java 11 or later. Downloads the pinned tla2tools.jar once.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
version=1.7.4
sha256=936a262061c914694dfd669a543be24573c45d5aa0ff20a8b96b23d01e050e88
cache=$here/../.cache
jar=$cache/tla2tools-$version.jar

digest() {
  if command -v sha256sum >/dev/null; then sha256sum "$1"; else shasum -a 256 "$1"; fi | cut -d' ' -f1
}

command -v java >/dev/null || { echo "check.sh: java not found (Java 11 or later)" >&2; exit 2; }
if [ ! -f "$jar" ]; then
  mkdir -p "$cache"
  curl --fail --silent --show-error --location --output "$jar.part" \
    "https://github.com/tlaplus/tlaplus/releases/download/v$version/tla2tools.jar"
  mv "$jar.part" "$jar"
fi
if [ "$(digest "$jar")" != "$sha256" ]; then
  echo "check.sh: $jar does not match sha256 $sha256; delete it to download again" >&2
  exit 2
fi

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

if [ "$#" -eq 0 ]; then
  set -- "$here"/*.cfg
fi

failed=0
for config in "$@"; do
  [ -f "$config" ] || config=$here/$config
  name=$(basename "$config" .cfg)
  expect=$(sed -n '1s/^\\\* expect: *//p' "$config")
  if [ -z "$expect" ]; then
    echo "FAIL  $name: first line must be '\\* expect: pass' or '\\* expect: <Invariant>'"
    failed=1
    continue
  fi
  out=$work/$name.out
  start=$(date +%s)
  status=0
  java -XX:+UseParallelGC -cp "$jar" tlc2.TLC -workers 1 -config "$config" \
    -metadir "$work/$name.states" "$here/IntentMarker.tla" >"$out" 2>&1 || status=$?
  seconds=$(($(date +%s) - start))
  states=$(sed -n 's/^.* \([0-9][0-9]*\) distinct states found.*$/\1/p' "$out" | tail -n 1)
  if [ "$expect" = pass ]; then
    if [ "$status" -eq 0 ] && grep -q '^Model checking completed. No error has been found.$' "$out"; then
      verdict="ok    $name: holds (${states:-?} states, ${seconds}s)"
    else
      verdict="FAIL  $name: expected every invariant to hold, TLC exited $status"
    fi
  elif [ "$status" -eq 12 ] && grep -q "^Error: Invariant $expect is violated.$" "$out"; then
    verdict="ok    $name: violates $expect as expected (${seconds}s)"
  else
    verdict="FAIL  $name: expected a violation of $expect, TLC exited $status"
  fi
  echo "$verdict"
  case $verdict in
    FAIL*) failed=1; cat "$out" ;;
    *) if [ -n "${VERBOSE:-}" ]; then cat "$out"; fi ;;
  esac
done
exit "$failed"
