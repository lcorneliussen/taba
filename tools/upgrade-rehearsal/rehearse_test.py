"""Exercise the real rehearsal script with local Git and synthetic release tools."""
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest

SCRIPT = Path(__file__).with_name("rehearse.sh")

RELEASE = r'''
import json, os, pathlib, shutil, sys
p = pathlib.Path(sys.argv[0])
a = sys.argv[1:]
v = p.parent.name
root = pathlib.Path(os.environ["FIXTURE"])
state = root / "stage"
stage = state.read_text() if state.exists() else "preflight"
with (root / "calls").open("a") as f:
    f.write(json.dumps({"exe": str(p), "args": a, "stage": stage}) + "\n")
command = {("--version",): p.name + " version", ("env", "list"): "env list",
           ("status", "--json"): "status", ("doctor",): "doctor"}.get(tuple(a))
if stage == os.environ.get("FAIL_STAGE") and command == os.environ.get("FAIL_COMMAND"):
    print("synthetic observation failed", file=sys.stderr)
    sys.exit(37)
if a == ["--version"]:
    wrong = os.environ.get("INVALID")
    if wrong == "from-version" and v == "0.1.9" or wrong == "to-version" and v == "0.2.5" or wrong == "alias-version" and p.name == "taba":
        v = "9.9.9"
    print("wtc version " + v)
elif a == ["env", "setup"]:
    if os.environ.get("INVALID") == "setup": sys.exit(38)
    pathlib.Path(".env.collection").write_text("WTC_COLLECTION='rehearsal'\n")
elif a == ["mcp", "render"]:
    state.write_text("before")
elif a == ["upgrade", "--help"]:
    state.write_text("preflight")
    if os.environ.get("LEGACY"): sys.exit(1)
elif a[:1] == ["upgrade"] and "--dry-run" not in a:
    dest = pathlib.Path(os.environ["MISE_DATA_DIR"]) / "installs" / "github-synthetic-taba" / "0.2.5"
    shutil.copytree(root / "releases" / "0.2.5", dest, dirs_exist_ok=True)
    bad = os.environ.get("INVALID")
    if bad == "missing-alias": (dest / "taba").unlink()
    if bad in ("to-link", "alias-link"):
        name = "wtc" if bad == "to-link" else "taba"
        (dest / name).unlink()
        (dest / name).symlink_to(root / "exterior" / "0.2.5" / name)
    pathlib.Path("harness/.wtc-cli-version").write_text("0.2.5\n")
elif a[:1] == ["catch-up"]:
    state.write_text("followup" if stage == "after" else "after")
    if "--report" in a: pathlib.Path(a[a.index("--report") + 1]).write_text("{}\n")
elif a == ["status", "--json"]:
    print('{"synthetic":true}')
'''

MISE = r'''
import os, pathlib, shutil, sys
root = pathlib.Path(os.environ["FIXTURE"])
a = sys.argv[1:]
installs = pathlib.Path(os.environ["MISE_DATA_DIR"]) / "installs"
if a[0] in ("install", "where"):
    v = a[1].rsplit("@", 1)[1]
    dest = installs / "github-synthetic-wtc-cli" / v
    if a[0] == "install":
        shutil.copytree(root / "releases" / v, dest, dirs_exist_ok=True)
        if os.environ.get("INVALID") == "from-link":
            (dest / "wtc").unlink()
            (dest / "wtc").symlink_to(root / "exterior" / v / "wtc")
        if os.environ.get("INVALID") == "install-tree-link":
            shutil.rmtree(installs)
            installs.symlink_to(root / "exterior", target_is_directory=True)
    elif os.environ.get("INVALID") == "from-exterior": print(root / "exterior" / v)
    else: print(dest)
elif a == ["which", "wtc"]:
    if os.environ.get("INVALID") == "external-candidate": print(root / "exterior" / "0.2.5" / "wtc")
    else: print(root / "missing" / "wtc")
else: sys.exit(91)
'''


class RehearsalTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="rehearsal tests ")
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.bin = self.root / "bin"
        self.bin.mkdir()
        for name in ("bash", "sh", "env", "cat", "sed", "mkdir", "dirname", "basename",
                     "tr", "sort", "cp", "ls", "grep", "readlink", "perl", "awk",
                     "find", "head", "wc", "diff", "mktemp", "uniq"):
            source = shutil.which(name, path=os.defpath)
            self.assertIsNotNone(source, name)
            (self.bin / name).symlink_to(source)
        self.env = {"HOME": str(self.root / "home"), "PATH": str(self.bin),
                    "TMPDIR": str(self.root / "tmp"), "FIXTURE": str(self.root),
                    "GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": os.devnull,
                    "GIT_TERMINAL_PROMPT": "0", "PYTHONDONTWRITEBYTECODE": "1",
                    "LC_ALL": "C"}
        for name in ("home", "tmp"):
            (self.root / name).mkdir()
        git = shutil.which("git", path=os.defpath)
        self.executable("git", "\n".join([
            "import os,sys", "a=sys.argv[1:]",
            "if a[:2]==['clone','https://github.com/synthetic/wtc-cli.git']: a[1]=os.environ['FIXTURE']+'/remote.git'",
            "os.execve(" + repr(git) + ", ['git','-c','protocol.allow=never','-c','protocol.file.allow=always','-c','core.hooksPath='+os.devnull,'-c','credential.helper=']+a, os.environ)",
        ]))
        self.executable("gh", "raise SystemExit('unexpected forge operation')")
        self.executable("mise", MISE)
        for area in ("releases", "exterior"):
            for v in ("0.1.9", "0.2.5"):
                for name in (("wtc",) if v == "0.1.9" else ("wtc", "taba")):
                    self.executable(str(self.root / area / v / name), RELEASE)
        self.seed = self.root / "seed"
        self.seed.mkdir()
        self.git("init", "-b", "main", str(self.seed))
        (self.seed / ".wtc-cli-version").write_text("0.1.9\n")
        (self.seed / ".harness-repos.yml").write_text(
            "repositories:\n  - name: wtc-cli\n"
            "    remote: https://github.com/synthetic/wtc-cli.git\n"
            "    default_ref: origin/main\n  - name: widget\n    port_offset: 7\n")
        self.git("-C", str(self.seed), "add", ".")
        self.git("-C", str(self.seed), "-c", "user.name=Synthetic",
                 "-c", "user.email=synthetic@example.invalid", "-c", "commit.gpgsign=false",
                 "commit", "-qm", "Synthetic fixture")
        self.git("clone", "--bare", str(self.seed), str(self.root / "remote.git"))

    def executable(self, name, body):
        path = self.bin / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text("#!" + sys.executable + " -B\n" + body + "\n")
        path.chmod(0o700)

    def git(self, *args):
        return subprocess.run([str(self.bin / "git"), *args], env=self.env,
                              check=True, capture_output=True, text=True)

    def run_rehearsal(self, *, invalid="", fail_stage="", fail_command="", extra=()):
        work = self.root / "work"
        env = dict(self.env, INVALID=invalid, FAIL_STAGE=fail_stage, FAIL_COMMAND=fail_command)
        if invalid == "legacy": env["LEGACY"] = "1"
        result = subprocess.run(
            [str(self.bin / "bash"), str(SCRIPT), "--harness", str(self.root / "remote.git"),
             "--cli-repo", "synthetic/wtc-cli", "--to", "0.2.5", "--work", str(work), *extra],
            env=env, cwd=self.root, capture_output=True, text=True, timeout=60)
        report = (work / "report.md").read_text() if (work / "report.md").exists() else ""
        calls = [json.loads(line) for line in (self.root / "calls").read_text().splitlines()] if (self.root / "calls").exists() else []
        return result, report, calls

    def test_positive_flow_and_registry_followup(self):
        result, report, calls = self.run_rehearsal(extra=("--cli-checkout", "--registry-followup"))
        self.assertEqual(result.returncode, 0, result.stderr)
        for stage in ("before", "after", "followup"):
            self.assertTrue((self.root / "work" / stage / "status.json").is_file())
        self.assertIn("upgrade --dry-run | pass", report)
        self.assertIn("catch-up after registry rename | pass", report)
        self.assertEqual(sum(c["args"][:1] == ["catch-up"] for c in calls), 2)
        for c in calls:
            if c["args"][:1] == ["upgrade"]: self.assertIn("0.1.9", c["exe"])
            if c["args"][:1] == ["catch-up"]: self.assertIn("0.2.5", c["exe"])
        registry = (self.root / "work/workspace/rehearsal/harness/.harness-repos.yml").read_text()
        self.assertIn("aliases: [wtc-cli]", registry)
        self.assertIn("port_offset: 7", registry)
        before = (self.root / "work/before/origins.txt").read_text()
        after = (self.root / "work/followup/origins.txt").read_text()
        self.assertEqual([s for s in before.splitlines() if s.startswith("ext.wtc-cli ")],
                         [s for s in after.splitlines() if s.startswith("ext.wtc-cli ")])

    def test_snapshot_failures_are_reported(self):
        for stage in ("before", "after", "followup"):
            for command in ("wtc version", "taba version", "env list", "status", "doctor"):
                with self.subTest(stage=stage, command=command):
                    # Separate fixtures prevent a preceding run from influencing selection or snapshots.
                    case = RehearsalTests()
                    case.setUp()
                    try:
                        result, report, _ = case.run_rehearsal(
                            fail_stage=stage, fail_command=command,
                            extra=("--from", "0.2.5", "--registry-followup"))
                        self.assertEqual(result.returncode, 1, result.stderr)
                        self.assertIn("snapshot " + stage + ": " + command + " | **fail (37)**", report)
                        self.assertTrue((case.root / "work/followup/doctor.txt").is_file())
                    finally:
                        case.doCleanups()

    def test_invalid_releases_do_not_reach_dependent_commands(self):
        for invalid in ("from-version", "from-exterior", "from-link", "install-tree-link",
                        "to-version", "alias-version", "missing-alias", "to-link", "alias-link"):
            with self.subTest(invalid=invalid):
                case = RehearsalTests()
                case.setUp()
                try:
                    result, report, calls = case.run_rehearsal(invalid=invalid, extra=("--registry-followup",))
                    self.assertEqual(result.returncode, 1, result.stderr)
                    self.assertIn("**fail", report)
                    self.assertFalse(any(c["args"][:1] == ["catch-up"] for c in calls))
                    self.assertFalse(any("exterior" in c["exe"] for c in calls), calls)
                    if invalid.startswith("from-") or invalid == "install-tree-link":
                        self.assertFalse(any(c["args"] == ["env", "setup"] for c in calls))
                finally:
                    case.doCleanups()

    def test_external_candidate_is_not_probed_and_valid_fallback_is_used(self):
        result, _, calls = self.run_rehearsal(invalid="external-candidate")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertFalse(any("exterior" in c["exe"] for c in calls))
        self.assertTrue(any(c["args"][:1] == ["catch-up"] for c in calls))

    def test_legacy_install_key_fallback(self):
        result, report, _ = self.run_rehearsal(invalid="legacy")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("has no upgrade command", report)
        self.assertIn("upgrade with 0.2.5 | pass", report)

    def test_tracked_step_failure(self):
        result, report, _ = self.run_rehearsal(invalid="setup")
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertIn("env setup (0.1.9) | **fail (38)**", report)

    def test_nonempty_work_directory_is_refused(self):
        work = self.root / "work"
        work.mkdir()
        sentinel = work / "keep"
        sentinel.write_text("unchanged")
        result, report, calls = self.run_rehearsal()
        self.assertEqual(result.returncode, 2)
        self.assertEqual(sentinel.read_text(), "unchanged")
        self.assertEqual(report, "")
        self.assertEqual(calls, [])


if __name__ == "__main__":
    unittest.main()
