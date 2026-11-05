#!/usr/bin/env python3
"""Capture synthetic native status frames and check them before review.

Run with Python containing pyte==0.8.2 and wcwidth==0.9.4:
  python -B tools/visual-evidence/capture.py --out /tmp/status-evidence
Add --chrome /path/to/google-chrome --require-png to require screenshots.
Outputs are review artifacts, not application assets. Inspect before publishing.
"""

import argparse
import hashlib
import html
import importlib.util
import importlib.metadata
import json
import os
from pathlib import Path
import re
import select
import shutil
import signal
import stat
import subprocess
import sys
import tempfile
import time

from wcwidth import wcswidth

sys.dont_write_bytecode = True

ROOT = Path(__file__).resolve().parents[2]
FIXTURE = Path(__file__).parent / "fixtures/status.json"
MAX_BYTES = 2 * 1024 * 1024
ANSI = re.compile(r"\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\x1b\[[0-?]*[ -/]*[@-~]")
PALETTE = {"default": "#d8dee9", "black": "#2e3440", "red": "#bf616a",
           "green": "#a3be8c", "brown": "#ebcb8b", "blue": "#81a1c1",
           "magenta": "#b48ead", "cyan": "#88c0d0", "white": "#eceff4"}


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def source_fingerprint(env):
    """Hash tracked files and extra Go inputs; omit other untracked data."""
    tracked = run(["git", "ls-files", "--cached", "-z"], ROOT, env, 10, strip_output=False)
    # Include ignored Go inputs too: Git ignore rules do not affect compilation.
    untracked = run(["git", "ls-files", "--others", "-z"], ROOT, env, 10, strip_output=False)
    names = tracked + untracked
    paths = sorted(set(name for name in tracked.split("\0") if name) |
                   {name for name in untracked.split("\0") if name and (name.endswith(".go") or Path(name).name in ("go.mod", "go.sum"))})
    if len(paths) > 20000 or "\ufffd" in names:
        raise ValueError("source inventory is too large or cannot be decoded")
    fingerprint, total = hashlib.sha256(), 0
    for name in paths:
        relative = Path(name)
        if relative.is_absolute() or ".." in relative.parts:
            raise ValueError("source inventory escapes checkout")
        path = ROOT / relative
        if not path.resolve().is_relative_to(ROOT.resolve()):
            raise ValueError("source link escapes checkout")
        fingerprint.update(name.encode() + b"\0")
        try:
            metadata = path.lstat()
        except FileNotFoundError:
            fingerprint.update(b"missing\0")
            continue
        if not stat.S_ISREG(metadata.st_mode):
            raise ValueError("source fingerprint requires regular checkout files")
        fingerprint.update(str(stat.S_IMODE(metadata.st_mode)).encode() + b"\0")
        file_hash = hashlib.sha256()
        with path.open("rb") as source:
            while data := source.read(65536):
                total += len(data)
                if total > 256 * 1024 * 1024:
                    raise ValueError("source fingerprint exceeds byte limit")
                file_hash.update(data)
        fingerprint.update(file_hash.digest())
    return fingerprint.hexdigest()


def run(command, cwd, env, timeout=300, strip_output=True):
    """Bound both time and output, including descendants of a failed command."""
    process = subprocess.Popen(command, cwd=cwd, env=env, stdout=subprocess.PIPE,
                               stderr=subprocess.STDOUT, start_new_session=True)
    output = bytearray()
    deadline = time.monotonic() + timeout
    try:
        while True:
            if time.monotonic() >= deadline:
                raise RuntimeError("process timed out")
            ready, _, _ = select.select([process.stdout], [], [], 0.1)
            if ready:
                data = os.read(process.stdout.fileno(), 65536)
                if not data:
                    break
                output.extend(data)
                if len(output) > MAX_BYTES:
                    raise RuntimeError("process exceeded output limit")
        try:
            code = process.wait(timeout=max(0.1, deadline - time.monotonic()))
        except subprocess.TimeoutExpired as error:
            raise RuntimeError("process timed out") from error
        if code:
            raise RuntimeError(f"process exited {code}: " + output.decode(errors="replace")[-2000:])
        text = output.decode(errors="replace")
        return text.strip() if strip_output else text
    finally:
        try:
            os.killpg(process.pid, signal.SIGKILL)
        except ProcessLookupError:
            pass
        if process.poll() is None:
            process.wait()
        process.stdout.close()


def load_fixture(path):
    if path.stat().st_size > MAX_BYTES:
        raise ValueError("fixture exceeds size limit")
    data = json.loads(path.read_text())
    if not isinstance(data, dict) or set(data) != {"version", "scenarios"} or type(data["version"]) is not int or data["version"] != 1:
        raise ValueError("unsupported fixture format")
    scenarios = data["scenarios"]
    if not isinstance(scenarios, list) or not 1 <= len(scenarios) <= 6:
        raise ValueError("supply one to six scenarios")
    names = set()
    for case in scenarios:
        if not isinstance(case, dict) or set(case) != {"name", "columns", "rows", "frames"}:
            raise ValueError("invalid scenario fields")
        name = case["name"]
        if not isinstance(name, str) or not re.fullmatch(r"[a-z][a-z0-9-]{0,39}", name) or name in names:
            raise ValueError("invalid or duplicate scenario name")
        names.add(name)
        for key, low, high in (("columns", 30, 200), ("rows", 10, 80)):
            if type(case[key]) is not int or not low <= case[key] <= high:
                raise ValueError("invalid terminal dimensions")
        if not isinstance(case["frames"], list) or not 1 <= len(case["frames"]) <= 6:
            raise ValueError("supply one to six frames")
        for frame in case["frames"]:
            if not isinstance(frame, dict) or set(frame) != {"at", "phase", "snapshot", "facts", "keys", "assert"}:
                raise ValueError("invalid frame fields")
            if type(frame["at"]) not in (int, float) or not 0 <= frame["at"] <= 60:
                raise ValueError("invalid frame time")
            if not isinstance(frame["phase"], str) or not re.fullmatch(r"[a-z][a-z0-9-]{0,39}", frame["phase"]):
                raise ValueError("invalid frame phase")
            if not isinstance(frame["snapshot"], dict) or not isinstance(frame["facts"], list) or len(frame["facts"]) > 20:
                raise ValueError("invalid synthetic snapshot/facts")
            if not isinstance(frame["keys"], list) or len(frame["keys"]) > 8 or any(key not in ("n", "s", "j", "k", "?", "space", "right", "esc") for key in frame["keys"]):
                raise ValueError("recording keys must be selection only")
            checks = frame["assert"]
            if not isinstance(checks, dict) or set(checks) != {"contains", "same_row", "aligned"}:
                raise ValueError("invalid assertions")
            groups = [checks["contains"]] + checks["same_row"] + checks["aligned"] if all(isinstance(checks[k], list) for k in checks) else []
            if not groups or len(groups) > 64 or not checks["contains"] or any(not isinstance(g, list) or not 1 <= len(g) <= 32 for g in groups):
                raise ValueError("assertions need nonempty text groups")
            if any(not isinstance(t, str) or not 1 <= len(t) <= 80 or any(ord(c) < 32 for c in t) for g in groups for t in g):
                raise ValueError("assertion text must be bounded and single-line")
    return data


def check_frame(content, columns, rows, checks):
    plain = ANSI.sub("", content)
    lines = plain.splitlines()
    failures = []
    if len(lines) > rows:
        failures.append("native frame exceeds terminal rows")
    if any((ord(char) < 32 and char != "\n") or 127 <= ord(char) < 160 for char in plain) or any(wcswidth(line) < 0 or wcswidth(line) > columns for line in lines):
        failures.append("native frame has controls or exceeds terminal columns")
    for value in checks["contains"]:
        if value not in plain:
            failures.append(f"required visible text missing or split: {value!r}")
    for values in checks["same_row"]:
        if not any(all(value in line for value in values) for line in lines):
            failures.append(f"expected text is not on one row: {values!r}")
    for values in checks["aligned"]:
        positions = [[wcswidth(line[:line.index(value)]) for line in lines if value in line] for value in values]
        if any(len(pos) != 1 for pos in positions) or len({pos[0] for pos in positions if pos}) != 1:
            failures.append(f"expected unique tokens are not column-aligned: {values!r}")
    return plain, failures


def color(value, background=False):
    if value == "default":
        return "#2e3440" if background else "#d8dee9"
    if value in PALETTE:
        return PALETTE[value]
    return "#" + value if re.fullmatch(r"[0-9a-fA-F]{6}", value) else PALETTE["default"]


def renderer_environment(temporary, base):
    # Preserve account HOME only while locating Go's public dependency/build
    # caches. Renderer/Chrome children get no HOME, no ambient token/profile
    # variables, and explicit temporary config/data roots. This is environment
    # isolation, not a filesystem sandbox for arbitrary source code.
    base = {**base, "GOWORK": "off"}
    cache_env = dict(base)
    if "HOME" in os.environ:
        cache_env["HOME"] = os.environ["HOME"]
    caches = json.loads(run(["go", "env", "-json", "GOCACHE", "GOMODCACHE"], ROOT, cache_env, 30))
    env = dict(base)
    for name in ("GOCACHE", "GOMODCACHE"):
        if not isinstance(caches.get(name), str) or not Path(caches[name]).is_absolute():
            raise ValueError("Go cache paths must be absolute")
        env[name] = caches[name]
    for name in ("XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "WTC_CONFIG_ROOT", "WTC_DATA_HOME"):
        path = temporary / name.lower()
        path.mkdir(mode=0o700)
        env[name] = str(path)
    return env


def render_html(content, columns, rows, label, recorder):
    # Decode actual terminal cells, then escape their text. OSC URLs never become
    # HTML links, and capture content cannot inject active markup or CSS values.
    screen = recorder.RecordingScreen(columns, rows)
    recorder.RecordingStream(screen).feed(content.replace("\n", "\r\n"))
    expected = "\n".join(line.rstrip() for line in ANSI.sub("", content).splitlines()).rstrip("\n")
    decoded = "\n".join(line.rstrip() for line in screen.display).rstrip("\n")
    if expected != decoded:
        raise ValueError("terminal decoding changed visible frame text")
    lines = []
    for row in range(rows):
        spans = []
        for column in range(columns):
            cell = screen.buffer[row][column]
            fg, bg = color(cell.fg), color(cell.bg, True)
            if cell.reverse:
                fg, bg = bg, fg
            style = f"color:{fg};background:{bg};font-weight:{700 if cell.bold else 400}"
            spans.append(f'<span style="{style}">{html.escape(cell.data)}</span>')
        lines.append("".join(spans))
    return ('<!doctype html><meta charset="utf-8"><meta http-equiv="Content-Security-Policy" '
            'content="default-src \'none\'; style-src \'unsafe-inline\'">'
            f'<style>*{{box-sizing:border-box}}body{{width:{columns * 10 + 40}px;margin:0;padding:20px;background:#242933;color:#d8dee9}}'
            'header{font:14px monospace;margin-bottom:12px;overflow-wrap:anywhere}pre{font:16px/20px monospace;margin:0;white-space:pre}</style>'
            f'<header>{html.escape(label)}</header><pre>{chr(10).join(lines)}</pre>')


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--out", required=True, type=Path)
    parser.add_argument("--chrome", help="headless Chrome executable (otherwise search PATH)")
    parser.add_argument("--require-png", action="store_true", help="fail if a PNG cannot be produced")
    args = parser.parse_args()
    out = args.out.resolve()
    if out == ROOT or ROOT in out.parents or (out.exists() and (not out.is_dir() or any(out.iterdir()))):
        parser.error("--out must be an empty directory outside the source checkout")
    out.mkdir(parents=True, exist_ok=True)
    manifest = {"schema": 1, "kind": "native TUI renderer; synthetic facts; not full CLI/provider execution",
                "limitations": ["Inspect text and screenshots before publishing; capture does not establish privacy or provider behavior.",
                                "PNG is a local pyte/Chrome terminal-cell preview; terminal fonts and unsupported style attributes may differ.",
                                "Screenshots supplement semantic assertions; they do not establish review approval or passing forge checks."],
                "fixture_sha256": None, "generator_sha256": digest(Path(__file__)),
                "scenarios": [], "errors": [], "tools": {}}
    env = {key: os.environ[key] for key in ("PATH", "TMPDIR", "SYSTEMROOT") if key in os.environ}
    env.update({"TERM": "xterm-256color", "NO_COLOR": "", "WTC_DEV_BUILD": "off", "GOENV": "off", "GOAUTH": "off",
                "GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": os.devnull})
    try:
        manifest["head"] = run(["git", "rev-parse", "HEAD"], ROOT, env, 10)
        source_status = run(["git", "status", "--porcelain", "--untracked-files=normal"], ROOT, env, 10)
        manifest["dirty"] = bool(source_status)
        manifest["source_sha256"] = source_fingerprint(env)
        fixture = load_fixture(FIXTURE)
        manifest["fixture_sha256"] = digest(FIXTURE)
        recorder_path = ROOT / "docs/demos/record.py"
        spec = importlib.util.spec_from_file_location("native_recording", recorder_path)
        recorder = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(recorder)
        manifest["recorder_sha256"] = digest(recorder_path)
        manifest["adapter_sha256"] = None
        package = next((p for p in ("cmd/taba", "cmd/wtc") if (ROOT / p / "status_recording_test.go").is_file()), None)
        if not package:
            raise ValueError("native status recording adapter is unavailable")
        manifest["adapter_sha256"] = digest(ROOT / package / "status_recording_test.go")
        chrome = args.chrome or shutil.which("google-chrome") or shutil.which("chromium") or shutil.which("chromium-browser")
        with tempfile.TemporaryDirectory(prefix="wtc-visual-evidence-") as temporary:
            temporary = Path(temporary)
            env = renderer_environment(temporary, env)
            manifest["tools"].update({"go": run(["go", "version"], ROOT, env, 20),
                                      "pyte": importlib.metadata.version("pyte"), "wcwidth": importlib.metadata.version("wcwidth")})
            chrome_error = None
            manifest["tools"]["chrome"] = None
            if chrome:
                try:
                    manifest["tools"]["chrome"] = run([chrome, "--version"], ROOT, env, 20)
                except (OSError, RuntimeError) as error:
                    chrome_error = "Chrome version probe failed"
                    chrome = None
            for case in fixture["scenarios"]:
                name = case["name"]
                input_path, output_path = temporary / "input.json", temporary / "output.json"
                request = {key: case[key] for key in ("columns", "rows")}
                request["frames"] = [{k: v for k, v in frame.items() if k != "assert"} for frame in case["frames"]]
                input_path.write_text(json.dumps(request))
                output_path.unlink(missing_ok=True)
                env.update({"WTC_RECORDING_INPUT": str(input_path), "WTC_RECORDING_OUTPUT": str(output_path)})
                result = {"name": name, "columns": case["columns"], "rows": case["rows"], "frames": []}
                manifest["scenarios"].append(result)
                try:
                    run(["go", "test", "-tags=recording", "-count=1", "-run=^TestStatusRecordingFrames$", "./" + package], ROOT, env)
                    if output_path.stat().st_size > MAX_BYTES:
                        raise ValueError("native frame output exceeds size limit")
                    native = json.loads(output_path.read_text())
                    (out / f"{name}.native.json").write_text(json.dumps(native, indent=2) + "\n")
                    if len(native["frames"]) != len(case["frames"]):
                        raise ValueError("native frame count differs from request")
                    for index, frame in enumerate(native["frames"]):
                        stem = f"{name}-{index + 1}"
                        content = frame["content"]
                        plain, failures = check_frame(content, case["columns"], case["rows"], case["frames"][index]["assert"])
                        (out / f"{stem}.ansi").write_text(content)
                        (out / f"{stem}.txt").write_text(plain)
                        label = f"Native TUI renderer · synthetic facts · {case['columns']}×{case['rows']} · {frame['phase']}"
                        page = out / f"{stem}.html"
                        page.write_text(render_html(content, case["columns"], case["rows"], label, recorder))
                        capture = {"phase": frame["phase"], "assertions": case["frames"][index]["assert"],
                                   "failures": failures, "png": "failed" if chrome_error else "unavailable"}
                        result["frames"].append(capture)
                        if chrome:
                            try:
                                png = out / f"{stem}.png"
                                run([chrome, "--headless", "--disable-gpu", "--no-first-run", "--disable-background-networking",
                                     "--disable-extensions", "--disable-sync", "--hide-scrollbars",
                                     f"--user-data-dir={temporary / 'chrome'}", f"--window-size={case['columns'] * 10 + 40},{case['rows'] * 20 + 90}",
                                     f"--screenshot={png}", page.as_uri()], ROOT, env, 45)
                                if not png.is_file() or png.stat().st_size > 20 * MAX_BYTES or not png.read_bytes().startswith(b"\x89PNG\r\n\x1a\n"):
                                    raise ValueError("Chrome did not produce a bounded PNG")
                                capture["png"] = "captured"
                            except (OSError, RuntimeError, ValueError) as error:
                                capture["png"] = "failed"
                                failures.append(str(error))
                        elif chrome_error:
                            failures.append(chrome_error)
                        elif args.require_png:
                            failures.append("PNG required but Chrome is unavailable")
                        manifest["errors"].extend(f"{stem}: {error}" for error in failures)
                except (OSError, RuntimeError, ValueError, KeyError) as error:
                    result["error"] = str(error)
                    manifest["errors"].append(f"{name}: {error}")
        manifest["source_after_sha256"] = source_fingerprint(env)
        if manifest["source_sha256"] != manifest["source_after_sha256"] or manifest["head"] != run(["git", "rev-parse", "HEAD"], ROOT, env, 10) or source_status != run(["git", "status", "--porcelain", "--untracked-files=normal"], ROOT, env, 10):
            manifest["errors"].append("source revision or working tree changed during capture")
    except (OSError, RuntimeError, ValueError, ImportError) as error:
        manifest["errors"].append(str(error))
    manifest["passed"] = not manifest["errors"]
    manifest["artifacts"] = {path.name: digest(path) for path in sorted(out.iterdir()) if path.is_file()}
    (out / "manifest.json").write_text(json.dumps(manifest, indent=2) + "\n")
    print(json.dumps({"passed": manifest["passed"], "errors": manifest["errors"]}))
    return 0 if manifest["passed"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
