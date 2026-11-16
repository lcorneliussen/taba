#!/usr/bin/env python3
"""Fail closed on missing, skipped or unexecuted named native tests."""
import collections
import json
from pathlib import Path
import re
import subprocess
import sys
import tempfile
import time

ROOT = Path(__file__).resolve().parents[2]
MANIFEST = Path(__file__).with_suffix(".json")


def selection(manifest, root):
    sources = sorted(str(p.relative_to(root)) for p in root.rglob("*_darwin.go"))
    if sources != manifest["darwin_sources"]:
        raise ValueError("Darwin source inventory changed; review native coverage and update manifest")
    packages = collections.defaultdict(list)
    for group in manifest["groups"]:
        package, tests = group["package"], group["tests"]
        if not re.fullmatch(r"\./[A-Za-z0-9_/-]+", package) or ".." in package:
            raise ValueError("invalid package")
        if not group["risk"].strip() or not tests:
            raise ValueError("each risk needs named tests and a rationale")
        for name in tests:
            if not re.fullmatch(r"Test[A-Za-z0-9_]+", name) or name in packages[package]:
                raise ValueError("invalid or duplicate test name")
            packages[package].append(name)
    if not packages:
        raise ValueError("native selection must not be empty")
    return packages


def pattern(names):
    return "^(" + "|".join(re.escape(n) for n in names) + ")$"


def verify_results(events, names):
    passed = {e.get("Test") for e in events if e.get("Action") == "pass"}
    failed = [e.get("Test", e.get("Package")) for e in events if e.get("Action") == "fail"]
    missing = set(names) - passed
    if missing or failed:
        raise ValueError(f"native tests missing/skipped: {sorted(missing)}; failures: {failed}")


def run_package(package, names):
    regex = pattern(names)
    listed = subprocess.check_output(
        ["go", "test", "-list", regex, package], cwd=ROOT, text=True).splitlines()
    missing = set(names) - set(listed)
    if missing:
        raise ValueError(f"selected tests do not exist: {sorted(missing)}")
    events = []
    start = time.monotonic()
    with subprocess.Popen(
        ["go", "test", "-json", "-count=1", "-timeout", "10m", "-run", regex, package],
        cwd=ROOT, stdout=subprocess.PIPE, text=True,
    ) as process:
        for line in process.stdout:
            try:
                event = json.loads(line)
            except json.JSONDecodeError:
                process.kill()
                process.wait()
                raise ValueError("malformed Go test JSON")
            events.append(event)
            print(event.get("Output", ""), end="", flush=True)
        status = process.wait()
    print(f"Native {package}: {time.monotonic() - start:.3f}s; {len(names)} named tests", flush=True)
    if status:
        raise ValueError(f"go test failed with exit status {status}")
    verify_results(events, names)


def main():
    packages = selection(json.loads(MANIFEST.read_text()), ROOT)
    start = time.monotonic()
    with tempfile.TemporaryDirectory(prefix="taba-native-") as directory:
        binary = str(Path(directory) / "taba")
        subprocess.run(["go", "build", "-o", binary, "./cmd/taba"], cwd=ROOT, check=True)
        # Run outside any collection so smoke checks cannot select a local dev build.
        for flag, expected in [("--version", "version"), ("--help", "Usage:")]:
            output = subprocess.check_output([binary, flag], cwd=directory, text=True)
            if expected not in output:
                raise ValueError(f"native smoke failed: {flag}")
    for package, names in packages.items():
        run_package(package, names)
    print(f"Native build/smoke/tests wall time: {time.monotonic() - start:.3f}s")


if __name__ == "__main__":
    try:
        main()
    except (ValueError, subprocess.CalledProcessError) as error:
        sys.exit(str(error))
