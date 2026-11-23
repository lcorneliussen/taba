#!/usr/bin/env python3
"""Reject generated Python bytecode in a head tree and every commit of a range.

A net diff can be clean while an intermediate commit still carries bytecode,
and a merge commit keeps those objects. This check therefore inspects the head
tree and the tree of every commit reachable from --head but not from --base,
including merge commits. Only tree objects are read, so a blob-filtered
partial clone is sufficient; file contents are never read or printed.

Missing revisions, unrelated histories and shallow boundaries inside the
range cannot be verified and fail with exit status 2. Findings exit 1.

    python3 tools/ci/check-generated-artifacts.py [--base origin/main] [--head HEAD]
"""
import argparse
import os
import subprocess
import sys

GENERATED_DIRS = {b"__pycache__"}
GENERATED_SUFFIXES = (b".pyc", b".pyo")


class Unverifiable(Exception):
    pass


def git(*args, check=True):
    env = dict(os.environ, GIT_NO_REPLACE_OBJECTS="1", GIT_OPTIONAL_LOCKS="0",
               GIT_TERMINAL_PROMPT="0", GIT_NO_LAZY_FETCH="1")
    result = subprocess.run(["git", *args], stdin=subprocess.DEVNULL,
                            capture_output=True, env=env)
    if check and result.returncode != 0:
        raise Unverifiable(f"git {args[0]} failed (exit {result.returncode})")
    return result


def is_generated(path):
    parts = path.split(b"/")
    return (any(part in GENERATED_DIRS for part in parts)
            or parts[-1].lower().endswith(GENERATED_SUFFIXES))


def display(path):
    """Repository-relative path with every non-printable or non-ASCII byte escaped."""
    out = []
    for byte in path:
        if byte == 0x5C:
            out.append("\\\\")
        elif 0x20 <= byte < 0x7F:
            out.append(chr(byte))
        else:
            out.append(f"\\x{byte:02x}")
    return "".join(out)


def resolve(name, revision):
    # "--end-of-options" keeps a revision such as "-x" from becoming an option.
    result = git("rev-parse", "--verify", "--quiet", "--end-of-options",
                 revision + "^{commit}", check=False)
    if result.returncode != 0:
        raise Unverifiable(f"{name} revision is not available locally; fetch it first")
    return result.stdout.decode().strip()


def shallow_commits():
    if git("rev-parse", "--is-shallow-repository").stdout.strip() != b"true":
        return set()
    path = git("rev-parse", "--path-format=absolute", "--git-path", "shallow").stdout
    try:
        with open(path.decode().strip(), "rb") as stream:
            return {line.strip().decode() for line in stream if line.strip()}
    except FileNotFoundError:
        return set()


def range_commits(base, head):
    """(commit, tree) for every commit in base..head, oldest first."""
    if git("merge-base", base, head, check=False).returncode != 0:
        hint = "; history is shallow, fetch the full range" if shallow_commits() else ""
        raise Unverifiable("base and head share no available history" + hint)
    output = git("rev-list", "--reverse", "--topo-order", "--format=%T",
                 f"{base}..{head}").stdout.decode().split()
    # rev-list --format emits "commit <sha>" followed by the formatted tree id.
    commits = []
    for index in range(0, len(output), 3):
        if output[index] != "commit":
            raise Unverifiable("unexpected rev-list output")
        commits.append((output[index + 1], output[index + 2]))
    boundary = shallow_commits().intersection(commit for commit, _ in commits)
    if boundary:
        raise Unverifiable("history is shallow inside the range; fetch the full range "
                           f"(boundary {sorted(boundary)[0]})")
    return commits


def generated_paths(tree):
    listing = git("ls-tree", "-r", "-z", "--full-tree", "--name-only", tree).stdout
    return [path for path in listing.split(b"\0") if path and is_generated(path)]


def check(base_rev, head_rev):
    head = resolve("head", head_rev)
    base = resolve("base", base_rev)
    findings = {}
    seen_trees = set()
    head_tree = git("rev-parse", "--verify", head + "^{tree}").stdout.decode().strip()
    for commit, tree in range_commits(base, head) + [(head, head_tree)]:
        if tree in seen_trees:
            continue
        seen_trees.add(tree)
        for path in generated_paths(tree):
            findings.setdefault(path, commit)
    return findings


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    parser.add_argument("--base", default="origin/main",
                        help="revision whose history is already accepted (default: origin/main)")
    parser.add_argument("--head", default="HEAD", help="revision to verify (default: HEAD)")
    args = parser.parse_args(argv)
    try:
        findings = check(args.base, args.head)
    except Unverifiable as error:
        print(f"generated-artifacts: cannot verify range: {error}", file=sys.stderr)
        return 2
    if not findings:
        print("generated-artifacts: no Python bytecode in head tree or range commits")
        return 0
    print(f"generated-artifacts: {len(findings)} generated Python path(s) in history; "
          "remove them from every commit, not only the final tree:", file=sys.stderr)
    for path in sorted(findings):
        print(f"  {findings[path][:12]}  {display(path)}", file=sys.stderr)
    return 1


if __name__ == "__main__":
    sys.exit(main())
