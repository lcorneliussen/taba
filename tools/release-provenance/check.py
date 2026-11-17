#!/usr/bin/env python3
"""Reject source contamination and dishonest release binary provenance.

Never repairs the checkout. Run preflight before GoReleaser's --clean;
the post-build hook checks source again and reads each target without executing it.
"""
import argparse
from datetime import datetime, timezone
from pathlib import Path
import re
import subprocess
import sys


def output(root, *args):
    return subprocess.check_output(args, cwd=root)


def git(root, *args):
    return output(root, "git", *args)


def check_source(root):
    if git(root, "status", "--porcelain=v1", "--untracked-files=all"):
        raise ValueError("release source is modified or contains untracked files")
    # Local excludes must not hide additional source from the release guard.
    ignored = git(root, "ls-files", "--others", "--ignored", "--exclude-standard", "-z")
    if any(not p.startswith(b"dist/") for p in ignored.split(b"\0") if p):
        raise ValueError("unexpected ignored files outside release outputs")
    # These index flags can conceal edits even from ordinary git diff/status.
    entries = git(root, "ls-files", "-v", "-z").split(b"\0")
    if any(e and (e[:1].islower() or e[:1] == b"S") for e in entries):
        raise ValueError("release source uses hidden index entries")
    if git(root, "ls-files", "-z", "--", "dist"):
        raise ValueError("release output directory contains tracked source")
    if (root / "dist").is_symlink():
        raise ValueError("release output directory must not be a symlink")


def check_outputs(root):
    """Accept build outputs only; refuse unsafe contents before --clean."""
    dist = root / "dist"
    if not dist.exists():
        return
    if not dist.is_dir():
        raise ValueError("release output path must be a directory")
    targets = {f"{name}_{os}_{arch}/{name}"
               for name in ("taba", "wtc") for os in ("darwin", "linux")
               for arch in ("amd64_v1", "arm64_v8.0")}
    files = targets | {"metadata.json", "artifacts.json", "config.yaml"}
    directories = {str(Path(p).parent) for p in targets}
    for path in dist.rglob("*"):
        relative = path.relative_to(dist).as_posix()
        if path.is_symlink() or not (
            (path.is_dir() and relative in directories)
            or (path.is_file() and relative in files)
        ):
            raise ValueError("unexpected release output; inspect it without cleaning source")


def check_binary(root, binary):
    info = output(root, "go", "version", "-m", str(binary)).decode()
    settings = dict(re.findall(r"^\s*build\s+(vcs(?:\.[a-z]+)?)=(\S+)\s*$", info, re.M))
    revision = git(root, "rev-parse", "HEAD").decode().strip()
    timestamp = int(git(root, "show", "-s", "--format=%ct", "HEAD").strip())
    expected = {
        "vcs": "git", "vcs.revision": revision, "vcs.modified": "false",
        "vcs.time": datetime.fromtimestamp(timestamp, timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),
    }
    if settings != expected:
        raise ValueError("binary VCS metadata must identify the exact clean source commit")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("mode", choices=("preflight", "binary"))
    parser.add_argument("binary", nargs="?")
    args = parser.parse_args()
    root = Path(git(Path.cwd(), "rev-parse", "--show-toplevel").decode().strip())
    check_source(root)
    check_outputs(root)
    if args.mode == "binary":
        if not args.binary:
            parser.error("binary mode requires a binary path")
        check_binary(root, args.binary)


if __name__ == "__main__":
    try:
        main()
    except (ValueError, OSError, subprocess.CalledProcessError) as error:
        print(f"Release provenance rejected: {error}", file=sys.stderr)
        sys.exit(1)
