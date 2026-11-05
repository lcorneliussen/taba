#!/usr/bin/env bash
# Rehearse a harness's CLI upgrade in a throwaway workspace (#332).
#
#   tools/upgrade-rehearsal/rehearse.sh --harness <git-remote> --to <version> [options]
#
# Creates a workspace under --work (default: a new temporary directory) with
# its own config root and mise data, clones the harness bare from its remote,
# creates one collection on the harness's current pin, records its state,
# upgrades it with the pinned binary, catches it up with the new one and
# records the state again. Nothing outside --work is written: no pushes and
# no forge writes; --from commits its pin on a local branch of the clone. Forge reads (clone, fetch, release lookups) use
# your normal git and gh authentication.
#
# The harness's own hooks run, as they would in a real collection. Rehearse
# only harnesses whose code you would run anyway.
#
# Options:
#   --harness URL        harness remote to clone (required)
#   --to VERSION         release to upgrade to, without "v" (required), e.g. 0.2.0-rc.1
#   --from VERSION       start from this release instead of the harness pin
#   --repos a,b          registry repositories to add before the upgrade
#   --cli-checkout       add an unmanaged ext.wtc-cli sibling whose owner is a
#                        clone of --cli-repo outside .bare/
#   --registry-followup  after the upgrade, rename the CLI registry entry to
#                        taba (aliases: [wtc-cli]) and catch up again
#   --cli-repo SLUG      GitHub repository releases are installed from and the
#                        ext.wtc-cli clone comes from (default lcorneliussen/wtc-cli,
#                        the name 0.1.x pins use); the pin file holds only a version
#   --work DIR           workspace parent; must not exist or be empty
#
# Output: <work>/report.md, plus logs/ and the before/after/followup
# snapshots beside it. Exit status is 0 when every step ran and passed, 1
# when a step failed or the rehearsal could not continue (the report says
# which), 2 on usage errors or a missing prerequisite (no report is written).
set -uo pipefail

usage() { sed -n '2,/^set -uo/p' "$0" | sed '$d; s/^# \{0,1\}//'; exit 2; }

harness="" to="" from="" repos="" cli_checkout=0 followup=0 work=""
cli_repo="lcorneliussen/wtc-cli"
while [ $# -gt 0 ]; do
  case "$1" in
    --harness|--to|--from|--repos|--cli-repo|--work)
      [ $# -ge 2 ] && [ -n "$2" ] || { echo "$1 needs a value" >&2; usage; } ;;
  esac
  case "$1" in
    --harness) harness="$2"; shift 2 ;;
    --to) to="${2#v}"; shift 2 ;;
    --from) from="${2#v}"; shift 2 ;;
    --repos) repos="$2"; shift 2 ;;
    --cli-checkout) cli_checkout=1; shift ;;
    --registry-followup) followup=1; shift ;;
    --cli-repo) cli_repo="$2"; shift 2 ;;
    --work) work="$2"; shift 2 ;;
    -h|--help) usage ;;
    *) echo "unknown option: $1" >&2; usage ;;
  esac
done
[ -n "$harness" ] && [ -n "$to" ] || usage
for tool in git gh mise perl; do
  command -v "$tool" >/dev/null || { echo "missing $tool on PATH" >&2; exit 2; }
done

if [ -z "$work" ]; then
  work="$(mktemp -d "${TMPDIR:-/tmp}/upgrade-rehearsal.XXXXXX")"
elif [ -e "$work" ] && [ -n "$(ls -A "$work" 2>/dev/null)" ]; then
  echo "--work $work is not empty" >&2; exit 2
fi
mkdir -p "$work" && work="$(cd "$work" && pwd -P)"
ws="$work/workspace" logs="$work/logs"
mkdir -p "$ws/.bare" "$logs" "$work/mise"/{data,cache,state,config}
report="$work/report.md"

# Isolation: the caller's collection environment must not leak in, and tool
# installs land in the throwaway mise data directory.
while IFS='=' read -r name _; do
  case "$name" in WTC_*|TABA_*|MISE_*) unset "$name" ;; esac
done < <(env)
: > "$work/mise/global.toml"
export MISE_DATA_DIR="$work/mise/data" MISE_CACHE_DIR="$work/mise/cache" \
  MISE_STATE_DIR="$work/mise/state" MISE_CONFIG_DIR="$work/mise/config" \
  MISE_GLOBAL_CONFIG_FILE="$work/mise/global.toml" MISE_TRUSTED_CONFIG_PATHS="$work" \
  MISE_YES=1
base_path="$PATH"

failures=0
results=()
# step <name> <command...>: run, log, and record pass/fail without stopping.
step() {
  local name="$1"; shift
  local log="$logs/$(printf '%02d' "${#results[@]}")-${name// /-}.log"
  printf '→ %s\n' "$name" >&2
  if "$@" >"$log" 2>&1; then
    results+=("| $name | pass | \`${log#"$work"/}\` |")
  else
    local code=$?
    results+=("| $name | **fail ($code)** | \`${log#"$work"/}\` |")
    failures=$((failures + 1))
    printf '  failed (%s), see %s\n' "$code" "$log" >&2
  fi
}
skip() { results+=("| $1 | skipped | $2 |"); }
# stop <reason>: end early with a short report of the steps so far.
stop() {
  { echo "# Upgrade rehearsal stopped: ${harness_name:-harness}"; echo
    echo "$1"; echo; echo "| step | result | log |"; echo "| --- | --- | --- |"
    printf '%s\n' "${results[@]}"; } > "$report"
  echo "$1 Report: $report" >&2
  exit 1
}

harness_name="$(basename "${harness%.git}")"
coll="$ws/rehearsal"
hdir="$coll/harness"

clone_harness() {
  git clone --bare "$harness" "$ws/.bare/$harness_name.git" &&
  git --git-dir="$ws/.bare/$harness_name.git" config remote.origin.fetch '+refs/heads/*:refs/remotes/origin/*' &&
  git --git-dir="$ws/.bare/$harness_name.git" fetch origin --prune &&
  git --git-dir="$ws/.bare/$harness_name.git" remote set-head origin --auto &&
  git --git-dir="$ws/.bare/$harness_name.git" worktree add --detach "$hdir" origin/HEAD
}
step "clone harness" clone_harness
[ -d "$hdir" ] || stop "The harness could not be cloned."
[ -f "$hdir/.harness-repos.yml" ] || stop "$harness has no .harness-repos.yml, so collections cannot be created from it."

if [ -z "$from" ]; then
  for pin in .wtc-cli-version .taba-version; do
    [ -s "$hdir/$pin" ] && { from="$(tr -d ' \n' < "$hdir/$pin")"; break; }
  done
fi
[ -n "$from" ] || stop "The harness has no CLI pin; pass --from."
# --from stands for a harness pinned to that release, so the pin must agree:
# env rendering and upgrade read it, not the binary's own version.
pin_file=.wtc-cli-version
[ -f "$hdir/.taba-version" ] && [ ! -f "$hdir/.wtc-cli-version" ] && pin_file=.taba-version
# upgrade refuses an uncommitted pin, so the change is committed on a local
# branch in the throwaway clone, where catch-up leaves it alone. It is never
# pushed.
if [ "$(tr -d ' \n' < "$hdir/$pin_file" 2>/dev/null)" != "$from" ]; then
  pin_from() {
    printf '%s\n' "$from" > "$hdir/$pin_file" &&
    git -C "$hdir" switch -q -c rehearsal-pin &&
    git -C "$hdir" add "$pin_file" &&
    git -C "$hdir" -c user.name=rehearsal -c user.email=rehearsal@localhost \
      commit -q -m "rehearsal: pin $from"
  }
  step "pin harness to $from" pin_from
fi

install_from() { mise install "github:$cli_repo@$from"; }
step "install $from" install_from
from_dir="$(mise where "github:$cli_repo@$from" 2>/dev/null)"
[ -x "$from_dir/wtc" ] || stop "wtc $from could not be installed."
export PATH="$from_dir:$base_path"

cd "$coll" || stop "The collection directory is missing."
step "env setup ($from)" wtc env setup
step "skills render ($from)" wtc skills render --seed-scope
step "mcp render ($from)" wtc mcp render
if [ -n "$repos" ]; then
  IFS=',' read -r -a repo_list <<< "$repos"
  step "add-repo ${repos}" wtc add-repo "${repo_list[@]}"
fi

if [ "$cli_checkout" = 1 ]; then
  add_cli_checkout() {
    git clone "https://github.com/$cli_repo.git" "$work/owners/wtc-cli" &&
    git -C "$work/owners/wtc-cli" worktree add --detach "$coll/ext.wtc-cli" origin/HEAD
  }
  step "ext.wtc-cli checkout" add_cli_checkout
fi

# snapshot <name>: record names and shapes, never secret values.
snapshot() {
  local out="$work/$1"
  mkdir -p "$out"
  # Only the release's own taba counts, not one elsewhere on the caller's PATH.
  local bin; bin="$(dirname "$(command -v wtc)")"
  { wtc --version; [ -x "$bin/taba" ] && "$bin/taba" --version || echo "taba: not in this release"; } > "$out/versions.txt" 2>&1
  wtc env list > "$out/env-list.txt" 2>&1
  [ -f .env.collection ] && sed -E 's/=.*$//' .env.collection | sort > "$out/env-collection-names.txt"
  for f in mise.toml .mcp.json .cursor/mcp.json .codex/config.toml; do
    [ -f "$f" ] && mkdir -p "$out/files/$(dirname "$f")" && cp "$f" "$out/files/$f"
  done
  # Skills are rendered as files and as directory links into the harness.
  for dir in .claude/skills .agents/skills .codex/skills .cursor/skills; do
    [ -d "$dir" ] || continue
    for entry in "$dir"/*; do
      [ -e "$entry" ] || [ -L "$entry" ] || continue
      if [ -L "$entry" ]; then
        printf '%s -> %s%s\n' "$entry" "$(readlink "$entry")" "$([ -e "$entry" ] || echo ' (broken)')"
      else
        printf '%s\n' "$entry"
      fi
    done
  done > "$out/rendered-skills.txt"
  ls -A | grep -vE '^\.(last-)?wtc-status' > "$out/collection-entries.txt"
  for d in */; do
    d="${d%/}"
    [ -e "$d/.git" ] && printf '%s %s\n' "$d" "$(git -C "$d" remote get-url origin 2>/dev/null)"
  done > "$out/origins.txt"
  wtc status --json 2>&1 | perl -MJSON::PP -0777 -ne '
    my $j = eval { JSON::PP->new->decode($_) } or do { print; exit };
    sub scrub { my $v = shift;
      if (ref $v eq "HASH") { for (keys %$v) { $v->{$_} = /(_at|_age|cli_version)$/ ? "<volatile>" : scrub($v->{$_}) } }
      elsif (ref $v eq "ARRAY") { $_ = scrub($_) for @$v }
      $v }
    print JSON::PP->new->canonical->pretty->encode(scrub($j))' > "$out/status.json"
  wtc doctor > "$out/doctor.txt" 2>&1; echo "exit $?" >> "$out/doctor.txt"
}
snapshot before

if wtc upgrade --help >/dev/null 2>&1; then
  step "upgrade --dry-run" wtc upgrade --dry-run --version "$to"
  step "upgrade" wtc upgrade --version "$to"
else
  # Releases before `upgrade` existed: the harness owner installs the new
  # release and lets it move the pin, which is what this reproduces.
  skip "upgrade with $from" "$from has no upgrade command; the new release moves the pin"
  manual_upgrade() {
    mise install "github:$cli_repo@$to" &&
    "$(mise where "github:$cli_repo@$to")/wtc" upgrade --version "$to"
  }
  step "upgrade with $to" manual_upgrade
fi

# The release may be installed under github:<owner>/wtc-cli or
# github:<owner>/taba; take the one the
# collection's mise config now resolves, then any install of that version.
to_dir="$(dirname "$(mise which wtc 2>/dev/null)" 2>/dev/null)"
if [ ! -x "$to_dir/wtc" ] || [ "$("$to_dir/wtc" --version 2>/dev/null | awk '{print $3}')" != "$to" ]; then
  to_dir="$(find "$MISE_DATA_DIR/installs" -mindepth 2 -maxdepth 2 -type d -name "$to" 2>/dev/null | head -n 1)"
fi
if [ -x "$to_dir/wtc" ]; then
  export PATH="$to_dir:$base_path"
  # Releases from 0.2.0 on, pre-releases included, ship taba next to wtc.
  IFS=. read -r to_major to_minor _ <<< "${to%%-*}"
  if [ "${to_major:-0}" -gt 0 ] || [ "${to_minor:-0}" -ge 2 ]; then
    check_both() { "$to_dir/wtc" --version && "$to_dir/taba" --version; }
    step "wtc and taba $to on PATH" check_both
  fi
  step "catch-up ($to)" wtc catch-up --report "$logs/catch-up-after.json"
  snapshot after
else
  results+=("| locate release $to | **fail** | the upgrade left no installed \`wtc $to\` |")
  failures=$((failures + 1))
fi

if [ "$followup" = 1 ] && [ -x "${to_dir:-}/wtc" ]; then
  registry="$hdir/.harness-repos.yml"
  if grep -qE '^[[:blank:]]*-[[:blank:]]*name:[[:blank:]]*wtc-cli[[:blank:]]*$' "$registry" 2>/dev/null; then
    rename_entry() {
      # Only the wtc-cli entry changes: from its "- name:" line to the next entry.
      perl -0pi -e 's{^([ \t]*)-([ \t]*)name:[ \t]*wtc-cli[ \t]*\n((?:(?!\1-[ \t]*name:).*\n)*)}{
        my ($i, $g, $body) = ($1, $2, $3);
        $body =~ s{^([ \t]*remote:[ \t]*\S*?[:/])wtc-cli((?:\.git)?[ \t]*)$}{$1taba$2}m;
        "$i-${g}name: taba\n$i $g" . "aliases: [wtc-cli]\n$body" }me' "$registry"
    }
    step "registry: name taba, aliases [wtc-cli]" rename_entry
    step "catch-up after registry rename" wtc catch-up --report "$logs/catch-up-followup.json"
    snapshot followup
  else
    skip "registry follow-up" "no wtc-cli entry in .harness-repos.yml"
  fi
fi

# Names the 0.2 compatibility aliases cannot cover: hard-coded CLI
# repository paths (including github: mise keys) and release archive names.
# WTC_* variables keep working in 0.2, so they are only counted.
grep -rnIE "($cli_repo|wtc_[0-9]+\.|wtc_\\\$|wtc_\\\{)" "$hdir" --exclude-dir=.git \
  | sed "s|^$hdir/||" > "$work/harness-scan.txt"
scan_count="$(wc -l < "$work/harness-scan.txt" | tr -d ' ')"
wtc_vars="$(grep -rhoIE '\bWTC_[A-Z0-9_]+' "$hdir" --exclude-dir=.git | sort | uniq -c | sort -rn)"

{
  echo "# Upgrade rehearsal: $harness_name $from → $to"
  echo
  echo "- Harness: \`$harness\` at \`$(git -C "$hdir" rev-parse --short HEAD 2>/dev/null)\`"
  echo "- Unmanaged \`ext.wtc-cli\` checkout: $([ "$cli_checkout" = 1 ] && echo yes || echo no)"
  echo "- Registry follow-up: $([ "$followup" = 1 ] && echo yes || echo no)"
  echo "- Workspace: \`$ws\`"
  echo
  echo "## Steps"
  echo
  echo "| step | result | log |"
  echo "| --- | --- | --- |"
  printf '%s\n' "${results[@]}"
  for pair in "before after" "after followup"; do
    set -- $pair
    [ -d "$work/$1" ] && [ -d "$work/$2" ] || continue
    echo
    echo "## Changes: $1 → $2"
    echo
    echo '```diff'
    diff -ru "$work/$1" "$work/$2" | sed "s|$work/||g"
    echo '```'
  done
  echo
  echo "## Hard-coded CLI names ($scan_count)"
  echo
  echo "References to \`$cli_repo\` and \`wtc_<version>\` release archives. From 0.2.0 archives are"
  echo "named \`taba_<version>_<os>_<arch>.tar.gz\`, and the repository moves. Anything that"
  echo "builds an archive name or download URL needs updating."
  echo
  echo '```text'
  head -n 200 "$work/harness-scan.txt"
  [ "$scan_count" -gt 200 ] && echo "… $((scan_count - 200)) more in harness-scan.txt"
  echo '```'
  echo
  echo "## WTC_ variables the harness uses"
  echo
  echo "These keep working in 0.2: collections generate both the \`WTC_\` and \`TABA_\` names."
  echo
  echo '```text'
  printf '%s\n' "${wtc_vars:-none}"
  echo '```'
} > "$report"

echo "report: $report" >&2
[ "$failures" -eq 0 ]
