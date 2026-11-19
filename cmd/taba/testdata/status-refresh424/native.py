"""Synthetic PTY acceptance for explicit Refresh, with no live forge calls."""
import fcntl
import json
import os
import pathlib
import pty
import re
import select
import signal
import struct
import subprocess
import sys
import tempfile
import termios
import time

binary = pathlib.Path(sys.argv[1]).resolve()
with tempfile.TemporaryDirectory(prefix="status-refresh-") as temporary:
    root = pathlib.Path(temporary)
    fixture = root / "fixture"
    config = fixture / ".wtc" / "config"
    config.mkdir(parents=True)
    (config / "repos.yml").write_text("repos:\n  - name: widget\n    remote: https://github.com/example/widget.git\n    default_ref: origin/main\n")
    (fixture / ".wtc-prs").write_text("widget 7 - https://github.com/example/widget/pull/7 Synthetic title\n")
    response = root / "response.json"
    response.write_text(json.dumps(dict(number=7, state="OPEN", isDraft=True, title="Synthetic draft")))
    tools = root / "bin"
    tools.mkdir()
    provider = tools / "gh"
    provider.write_text('''#!/usr/bin/env python3
import json,os,pathlib,sys
root=pathlib.Path(os.environ['NATIVE_424_ROOT'])
args=sys.argv[1:]
if args[:2]==['pr','view']:
 with (root/'views').open('a') as f:f.write('view\\n')
 if (root/'limited').exists():
  print('HTTP 429 Retry-After: 60',file=sys.stderr);sys.exit(1)
 print((root/'response.json').read_text())
elif args[:2]==['pr','list']:
 raw=json.loads((root/'response.json').read_text());print(json.dumps([raw]))
elif args[:2]==['auth','token']:print('synthetic-token')
else:print('{}')
''')
    provider.chmod(0o700)
    env = {k: os.environ[k] for k in ("PATH", "LANG", "TMPDIR") if k in os.environ}
    (root / "tmp").mkdir()
    env.update(TMPDIR=str(root / "tmp"), HOME=str(root), PATH=str(tools) + os.pathsep + env.get("PATH", "/usr/bin:/bin"), GH_TOKEN="synthetic-native-fixture", BB_USERNAME="synthetic-native-user", BB_API_TOKEN="synthetic-native-fixture", TERM="xterm-256color", NO_COLOR="1", TABA_DEV_BUILD="off", WTC_DEV_BUILD="off", GIT_CONFIG_GLOBAL=os.devnull, GIT_CONFIG_NOSYSTEM="1", NATIVE_424_ROOT=str(root), NATIVE_424_CHILDROOT=str(fixture))
    args = [str(binary), "-test.run=^TestStatus424NativeChild$"] if len(sys.argv) > 2 else [str(binary), "status", "--tui", "--no-fetch", "--no-click"]
    pid, master = pty.fork()
    if pid == 0:
        os.chdir(fixture)
        os.execve(str(binary), args, env)
    fcntl.ioctl(master, termios.TIOCSWINSZ, struct.pack("HHHH", 40, 100, 0, 0))
    os.set_blocking(master, False)
    output = bytearray()
    reaped = False

    def pump():
        if select.select([master], [], [], 0.02)[0]:
            try:
                data = os.read(master, 65536)
            except (OSError, BlockingIOError):
                data = b""
            output.extend(data)
            if b"\x1b[6n" in data:
                os.write(master, b"\x1b[1;1R")
            if b"\x1b[c" in data:
                os.write(master, b"\x1b[?1;2c")

    def wait_for(predicate, label, seconds=8):
        deadline = time.monotonic() + seconds
        while time.monotonic() < deadline:
            pump()
            if predicate():
                return
        raise AssertionError(label + "\n" + output.decode(errors="replace")[-12000:])

    def plain_output():
        return b" ".join(re.sub(rb"\x1b\[[0-?]*[ -/]*[@-~]", b"", output).split())

    def snapshot_state():
        try:
            return json.loads((fixture / ".wtc-status.json").read_text())["prs"][0]
        except (OSError, ValueError, KeyError, IndexError):
            return {}

    try:
        wait_for(lambda: snapshot_state().get("state") == "DRAFT" and b"q quit" in output, "initial draft did not render")
        initial_views = (root / "views").read_text().count("view") if (root / "views").exists() else 0
        response.write_text(json.dumps(dict(number=7, state="OPEN", isDraft=False, title="Synthetic ready")))
        os.write(master, b"r")
        wait_for(lambda: snapshot_state().get("state") == "OPEN" and b"Synthetic ready" in output, "explicit refresh reused draft/list/TTL")
        assert (root / "views").read_text().count("view") == initial_views + 1, "explicit refresh must spend exactly one metadata request"
        os.write(master, b"l")
        wait_for(lambda: b"1 live, 0 deferred" in output, "confirmed foreground outcome missing")
        (root / "limited").touch()
        os.write(master, b"r")
        wait_for(lambda: snapshot_state().get("freshness") == "deferred" and b"provider-cooldown" in output and b"retry after" in plain_output(), "cooldown feedback missing")
        row = snapshot_state()
        assert row["state"] == "OPEN" and row["title"] == "Synthetic ready" and row["observed_at"], "deferral lost known facts"
        os.write(master, b"q")
        deadline = time.monotonic() + 4
        while time.monotonic() < deadline:
            pump()
            ended, status = os.waitpid(pid, os.WNOHANG)
            if ended:
                reaped = True
                assert os.waitstatus_to_exitcode(status) == 0, "UI quit failed"
                break
        assert reaped, "UI did not quit"
        print("native Refresh: draft->open, one accounted metadata request, cooldown reason/retry, preserved facts, quit: PASS")
    finally:
        if not reaped:
            os.kill(pid, signal.SIGTERM)
            os.waitpid(pid, 0)
        os.close(master)
