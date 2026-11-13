#!/usr/bin/env python3
"""Run the Go suite with live output and bounded test/Git timing summaries.

Arguments are forwarded to `go test`. An optional first `--git-trace` argument
collects temporary Git traces and prints only aggregate command names, counts
and elapsed seconds, never argv or paths.
"""
import collections
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import time


def git_timings(path):
    commands = {}
    totals = collections.defaultdict(lambda: [0, 0.0])
    if not path.exists():
        return totals
    with path.open() as stream:
        for line in stream:
            event = json.loads(line)
            sid = event.get("sid", "")
            # Child commands are included in their parent's elapsed time.
            if "/" in sid:
                continue
            if event.get("event") == "cmd_name":
                commands[sid] = event["name"]
            elif event.get("event") == "exit":
                command = commands.pop(sid, "unknown")
                totals[command][0] += 1
                totals[command][1] += event.get("t_abs", 0)
    return totals


def report(timings, git, wall):
    print(f"\nGo test wall time: {wall:.3f}s")
    for label, rows in timings.items():
        print(f"\nSlowest {label} (elapsed seconds; overlapping rows are not additive):")
        for elapsed, name in sorted(rows, reverse=True)[:20]:
            print(f"{elapsed:9.3f}  {name}")
    if git is not None:
        print("\nGit root commands (count, aggregate elapsed seconds; nested commands excluded):")
        for command, (count, elapsed) in sorted(git.items(), key=lambda row: row[1][1], reverse=True):
            print(f"{count:6d} {elapsed:9.3f}  {command}")


def main(args):
    trace_git = bool(args and args[0] == "--git-trace")
    if trace_git:
        args = args[1:]
    timings = {"packages": [], "tests": [], "subtests": []}
    malformed = False
    started = time.monotonic()
    with tempfile.TemporaryDirectory(prefix="go-test-timings-") as directory:
        trace = Path(directory) / "git.jsonl"
        env = dict(os.environ)
        if trace_git:
            env["GIT_TRACE2_EVENT"] = str(trace)
        with subprocess.Popen(["go", "test", "-json", *args], env=env,
                              stdout=subprocess.PIPE, text=True) as process:
            for line in process.stdout:
                try:
                    event = json.loads(line)
                except json.JSONDecodeError:
                    # Never turn malformed/truncated test output into success.
                    malformed = True
                    print(line, end="", flush=True)
                    continue
                if "Output" in event:
                    print(event.get("Output", ""), end="", flush=True)
                if event.get("Action") in ("pass", "fail", "skip") and "Elapsed" in event:
                    package = event.get("Package", "")
                    test = event.get("Test")
                    category = "packages" if test is None else "subtests" if "/" in test else "tests"
                    name = package if test is None else f"{package}.{test}"
                    timings[category].append((event["Elapsed"], name))
            status = process.wait()
        report(timings, git_timings(trace) if trace_git else None, time.monotonic() - started)
    return status if status > 0 else 128 - status if status < 0 else int(malformed)


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
