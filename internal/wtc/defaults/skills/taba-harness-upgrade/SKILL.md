---
name: taba-harness-upgrade
description: Upgrade a harness from its upstream implementation through a reviewable pull request, preserving local configuration and overlays.
---

# Upgrade a harness

Use this for a downstream harness that pins `wtc` or ports the shell-era reference harness. Work in the downstream's own collection and follow its branch and review policy. Read that harness's instructions before changing files.

## 1. Establish the versions

Read the downstream's recorded upstream version or commit and the target release or upstream ref. Confirm the target exists. If no machine-readable pin exists in the shell era, use merge commits, port trailers, and the last upgrade PR to establish the base; record any uncertainty in the private upgrade PR. Do not guess from file timestamps.

For a CLI release, start with `taba upgrade --dry-run` (or `--version <X>`): it names the target and prints the published notes since the pin without writing anything. Otherwise, before changing the pin, download the published target binary and run that binary's `release-notes --since <old-pin>` command. The archive is offline and specific to that binary: running the old installed CLI cannot reveal future notes. `taba release-notes <version>` reads one release; `taba release-notes unreleased` explicitly reads upcoming changes, which are not evidence that a feature has shipped. If the target predates this command, read its published GitHub release notes instead.

Use the full release range to list changed commands, skills, instructions, registry schema, and hook contracts, including later corrections to earlier notes. Identify harness adoption steps and compatibility requirements. Compare each local override with the corresponding new default and inspect `taba skills diff`. The upgrade is a single version transition, not a series of unrelated cherry-picks.

## 2. Prepare the branch

Create a branch at the first commit, following the harness policy. For a CLI release, run `taba upgrade` (with the same `--version`): it bumps the committed version pin (`.taba-version`, or `.wtc-cli-version` in older harnesses; never add the other name) and the harness compatibility range together, regenerates the collection-root `mise.toml`, runs `mise install`, and prints the target binary's own notes, which can correct earlier ones. Without the command, make those changes by hand. Update the recorded upstream version/ref. Re-render shipped skills and instructions, then reconcile only the affected local overlays. When a CLI release is not yet available, port the shell implementation and record the upstream commit or merge range.

Keep local content in config, hooks, providers, and overlays. If the upgrade needs a new extension point, open a generic upstream change first, then take its released version. Do not silently edit a generated file to carry a permanent customization.

## 3. Verify and open the PR

Run the upstream contract suite against the harness profile, plus the harness's own tests. Exercise changed hooks and generated content in a disposable collection. Record exact test results and any intentional skips.

Open a downstream PR with the old and new pins, relevant changelog, overlay changes, tests, and rollout effects. This PR is the durable record of what the harness adopted. Follow its checks and review under the harness's normal procedure. Keep private adoption and delivery links in the downstream PR; public upstream records only describe generic technical changes.

## 4. Roll out

Rolling out to other collections needs the user's explicit scope. After the PR merges, run `taba upgrade --rollout --dry-run`, then `taba upgrade --rollout`. It catches every collection's harness up to the merged tip and installs the new pin; collections with a dirty harness, a conflicting branch, or a working or blocked agent stay untouched as `needs-owner`. Hand each of those to its owning agent with its report row, or record the handoff, and check releases newer than the pin before calling the upgrade done.
