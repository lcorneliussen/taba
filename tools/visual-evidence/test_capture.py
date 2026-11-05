import copy
import importlib.util
import contextlib
import io
import json
import os
from pathlib import Path
import sys
import subprocess
import tempfile
import unittest
from unittest.mock import patch

import capture


class FrameAssertionsTest(unittest.TestCase):
    checks = {"contains": ["topic/api", "topic/web"],
              "same_row": [["api", "topic/api"], ["web", "topic/web"]],
              "aligned": [["topic/api", "topic/web"]]}

    def test_checks_accept_native_style_without_counting_controls_as_cells(self):
        content = "\x1b[32mapi   topic/api\x1b[0m\nweb   topic/web\n"
        plain, failures = capture.check_frame(content, 20, 3, self.checks)
        self.assertEqual(plain, "api   topic/api\nweb   topic/web\n")
        self.assertEqual(failures, [])

    def test_missing_split_overflow_and_misalignment_are_failures(self):
        cases = {
            "missing": "api   topic/api\n",
            "split": "api   topic/a\npi\nweb   topic/web\n",
            "overflow": "api   topic/api extra text past width\nweb   topic/web\n",
            "misaligned": "api   topic/api\nweb    topic/web\n",
        }
        for name, content in cases.items():
            with self.subTest(name=name):
                self.assertTrue(capture.check_frame(content, 20, 3, self.checks)[1])

    def test_vertical_clipping_and_active_controls_fail(self):
        self.assertTrue(capture.check_frame("one\ntwo\nthree\n", 20, 2, {"contains": ["one"], "same_row": [], "aligned": []})[1])
        self.assertTrue(capture.check_frame("one\x00", 20, 2, {"contains": ["one"], "same_row": [], "aligned": []})[1])

    def test_wide_glyph_alignment_uses_cells_not_character_indexes(self):
        text = "界 abc\nxx def\n"
        checks = {"contains": ["abc", "def"], "same_row": [], "aligned": [["abc", "def"]]}
        self.assertEqual(capture.check_frame(text, 10, 2, checks)[1], [])


class FixtureTest(unittest.TestCase):
    def test_repository_cell_loss_and_swaps_fail_both_fixture_layouts(self):
        for case in capture.load_fixture(capture.FIXTURE)["scenarios"]:
            frame = case["frames"][0]
            repos = frame["snapshot"]["repos"]
            def rows(names):
                return "\n".join(f"{name:6}{repo['branch_display']:16}" +
                                 ("#" + repo["pr"]["number"] if repo.get("pr") else "")
                                 for name, repo in zip(names, repos))
            with self.subTest(layout=case["name"]):
                self.assertEqual(capture.check_frame(rows([r["dir"] for r in repos]), case["columns"], case["rows"], frame["assert"])[1], [])
                self.assertTrue(capture.check_frame(rows(["", ""]), case["columns"], case["rows"], frame["assert"])[1])
                self.assertTrue(capture.check_frame(rows([r["dir"] for r in reversed(repos)]), case["columns"], case["rows"], frame["assert"])[1])

    def test_malformed_fixtures_fail_before_execution(self):
        valid = json.loads(capture.FIXTURE.read_text())
        cases = [[], {"version": True, "scenarios": []}, {"version": 1, "scenarios": []}]
        for key, value in (("name", "../escape"), ("columns", True), ("rows", 999), ("frames", [])):
            data = copy.deepcopy(valid)
            data["scenarios"][0][key] = value
            cases.append(data)
        for key, value in (("at", float("nan")), ("phase", "../escape"), ("keys", ["enter"]),
                           ("assert", {"contains": [], "same_row": [], "aligned": []}),
                           ("assert", {"contains": ["ok"], "same_row": [7], "aligned": []})):
            data = copy.deepcopy(valid)
            data["scenarios"][0]["frames"][0][key] = value
            cases.append(data)
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "fixture.json"
            for data in cases:
                with self.subTest(data=data):
                    path.write_text(json.dumps(data))
                    with self.assertRaises(ValueError):
                        capture.load_fixture(path)

    def test_html_is_inert_and_label_is_escaped(self):
        spec = importlib.util.spec_from_file_location("recording", capture.ROOT / "docs/demos/record.py")
        recorder = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(recorder)
        page = capture.render_html('\x1b]8;;https://example.invalid/\x1b\\<script>\x1b]8;;\x1b\\', 30, 10, '<img src=x>', recorder)
        self.assertNotIn("<script>", page)
        self.assertNotIn("<img ", page)
        self.assertNotIn("href=", page)
        self.assertNotIn("https://example.invalid", page)
        self.assertIn("&lt;img src=x&gt;", page)
        self.assertIn("default-src 'none'", page)

    def test_native_newlines_survive_terminal_decoding(self):
        spec = importlib.util.spec_from_file_location("recording", capture.ROOT / "docs/demos/record.py")
        recorder = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(recorder)
        page = capture.render_html("api   topic/api\nweb   topic/web\n", 30, 10, "synthetic", recorder)
        from html.parser import HTMLParser

        class Text(HTMLParser):
            parts = []
            def handle_data(self, data):
                self.parts.append(data)

        text = Text()
        text.feed(page)
        rendered = "".join(text.parts)
        self.assertIn("api   topic/api", rendered)
        self.assertIn("web   topic/web", rendered)

    def test_lossy_terminal_decoding_is_rejected(self):
        spec = importlib.util.spec_from_file_location("recording", capture.ROOT / "docs/demos/record.py")
        recorder = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(recorder)
        # A clear-screen sequence disappears from ANSI-stripped text but erases
        # already visible cells. Accepting that screenshot would lose evidence.
        destructive = "api\x1b[2Jweb"
        checks = {"contains": ["apiweb"], "same_row": [], "aligned": []}
        self.assertEqual(capture.check_frame(destructive, 30, 10, checks)[1], [])
        with self.assertRaisesRegex(ValueError, "decoding changed visible frame text"):
            capture.render_html(destructive, 30, 10, "synthetic", recorder)


class SourceFingerprintTest(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.env = {"PATH": os.environ["PATH"], "GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": os.devnull}
        self.git("init", "-q", "-b", "main")
        (self.root / "renderer.go").write_text("package fixture\n")
        self.git("add", "renderer.go")
        self.git("-c", "user.name=Example", "-c", "user.email=example@example.invalid", "-c", "core.hooksPath=/dev/null", "commit", "-qm", "Synthetic source fixture")

    def git(self, *args):
        return subprocess.check_output(["git", "-C", str(self.root), *args], env=self.env, text=True)

    def fingerprint(self):
        with patch.object(capture, "ROOT", self.root):
            return capture.source_fingerprint(self.env)

    def test_already_dirty_source_changes_despite_identical_head_and_status(self):
        renderer = self.root / "renderer.go"
        renderer.write_text("package fixture // first edit\n")
        head, status, before = self.git("rev-parse", "HEAD"), self.git("status", "--porcelain"), self.fingerprint()
        renderer.write_text("package fixture // second edit\n")
        self.assertEqual(self.git("rev-parse", "HEAD"), head)
        self.assertEqual(self.git("status", "--porcelain"), status)
        self.assertNotEqual(self.fingerprint(), before)

    def test_ignored_go_inputs_are_hashed_but_untracked_credentials_are_not(self):
        (self.root / ".git/info/exclude").write_text("extra.go\ncredentials\n")
        extra = self.root / "extra.go"
        extra.write_text("package fixture // first\n")
        credentials = self.root / "credentials"
        credentials.write_text("synthetic credential marker one")
        before = self.fingerprint()
        credentials.write_text("synthetic credential marker two")
        self.assertEqual(self.fingerprint(), before)
        extra.write_text("package fixture // second\n")
        self.assertNotEqual(self.fingerprint(), before)

    def test_external_source_link_is_refused(self):
        with tempfile.TemporaryDirectory() as outside:
            target = Path(outside) / "external.go"
            target.write_text("not a source input")
            (self.root / "extra.go").symlink_to(target)
            with self.assertRaisesRegex(ValueError, "source link escapes checkout"):
                self.fingerprint()


class ProcessBoundsTest(unittest.TestCase):
    def test_parent_go_workspace_is_not_selected(self):
        with tempfile.TemporaryDirectory() as temporary:
            temporary = Path(temporary)
            repo, sealed = temporary / "repo", temporary / "sealed"
            repo.mkdir()
            sealed.mkdir()
            (repo / "go.mod").write_text("module example.invalid/fixture\n\ngo 1.27.0\n")
            (repo / "fixture.go").write_text("package fixture\n")
            (temporary / "go.work").write_text("not a valid workspace\n")
            base = {key: os.environ[key] for key in ("PATH", "TMPDIR") if key in os.environ}
            base.update({"GOENV": "off", "GOAUTH": "off"})
            with patch.object(capture, "ROOT", repo):
                env = capture.renderer_environment(sealed, base)
            self.assertEqual(env["GOWORK"], "off")
            capture.run(["go", "test", "./..."], repo, env, 30)

    def test_existing_output_file_and_nonempty_directory_are_preserved(self):
        with tempfile.TemporaryDirectory() as temporary:
            temporary = Path(temporary)
            marker = temporary / "marker"
            marker.write_text("preserve")
            for destination in (marker, temporary, capture.ROOT):
                with self.subTest(destination=destination), patch.object(sys, "argv", ["capture.py", "--out", str(destination)]):
                    with contextlib.redirect_stderr(io.StringIO()), self.assertRaises(SystemExit) as error:
                        capture.main()
                    self.assertEqual(error.exception.code, 2)
                    self.assertEqual(marker.read_text(), "preserve")

    def test_ambient_credentials_and_profile_roots_are_not_forwarded(self):
        with tempfile.TemporaryDirectory() as temporary:
            temporary = Path(temporary)
            marker = temporary / "ambient-profile-marker"
            marker.write_text("not renderer input")
            poisoned = {"HOME": str(marker), "WTC_CONFIG_ROOT": str(marker), "WTC_DATA_HOME": str(marker),
                        "XDG_CONFIG_HOME": str(marker), "XDG_DATA_HOME": str(marker), "GH_TOKEN": "synthetic-secret-marker"}
            caches = json.dumps({"GOCACHE": "/tmp/example-go-cache", "GOMODCACHE": "/tmp/example-module-cache"})
            with patch.dict(os.environ, poisoned), patch.object(capture, "run", return_value=caches):
                env = capture.renderer_environment(temporary, {"PATH": os.environ["PATH"], "GOENV": "off", "GOAUTH": "off"})
            self.assertNotIn("HOME", env)
            self.assertNotIn("GH_TOKEN", env)
            for name in ("XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "WTC_CONFIG_ROOT", "WTC_DATA_HOME"):
                self.assertNotEqual(env[name], str(marker))
                self.assertTrue(Path(env[name]).is_dir())
                self.assertEqual(list(Path(env[name]).iterdir()), [])
            self.assertEqual(marker.read_text(), "not renderer input")

    def test_process_output_is_bounded(self):
        with patch.object(capture, "MAX_BYTES", 100):
            with self.assertRaisesRegex(RuntimeError, "output limit"):
                capture.run([sys.executable, "-c", "print('x'*1000)"], capture.ROOT, os.environ.copy(), 5)

    def test_process_time_is_bounded(self):
        with self.assertRaisesRegex(RuntimeError, "timed out"):
            capture.run([sys.executable, "-c", "import time; time.sleep(30)"], capture.ROOT, os.environ.copy(), 0.1)

    def test_failed_assertions_retain_capture_and_manifest(self):
        with tempfile.TemporaryDirectory() as temporary:
            temporary = Path(temporary)
            fixture = copy.deepcopy(json.loads(capture.FIXTURE.read_text()))
            fixture["scenarios"] = fixture["scenarios"][:1]
            fixture["scenarios"][0]["frames"][0]["assert"]["contains"] = ["not-present"]
            fixture_path = temporary / "fixture.json"
            fixture_path.write_text(json.dumps(fixture))

            def fake_run(command, cwd, env, timeout=300, **kwargs):
                if command[:3] == ["git", "rev-parse", "HEAD"]:
                    return "a" * 40
                if command[:2] == ["git", "status"]:
                    return ""
                if command[:2] == ["git", "ls-files"]:
                    return "go.mod\0" if "--cached" in command else ""
                if command[:2] == ["go", "version"]:
                    return "go version test"
                if command[:2] == ["go", "env"]:
                    return json.dumps({"GOCACHE": "/tmp/example-go-cache", "GOMODCACHE": "/tmp/example-module-cache"})
                if command[:2] == ["go", "test"]:
                    Path(env["WTC_RECORDING_OUTPUT"]).write_text(json.dumps({"frames": [{"content": "api   topic/api\nweb   topic/web\n", "phase": "ready"}]}))
                    return "ok"
                self.fail(f"unexpected command: {command}")

            out = temporary / "artifacts"
            with patch.object(capture, "FIXTURE", fixture_path), patch.object(capture, "run", fake_run), patch.object(capture.shutil, "which", return_value=None), patch.object(sys, "argv", ["capture.py", "--out", str(out)]):
                with contextlib.redirect_stdout(io.StringIO()):
                    self.assertEqual(capture.main(), 1)
            manifest = json.loads((out / "manifest.json").read_text())
            self.assertFalse(manifest["passed"])
            self.assertIn("missing or split", manifest["errors"][0])
            self.assertEqual(manifest["scenarios"][0]["frames"][0]["png"], "unavailable")
            self.assertTrue((out / "wide-1.txt").is_file())
            self.assertTrue((out / "wide-1.html").is_file())
            self.assertIn("wide-1.txt", manifest["artifacts"])

    def test_changed_source_fingerprint_fails_with_same_head_and_status(self):
        with tempfile.TemporaryDirectory() as temporary:
            out = Path(temporary) / "artifacts"
            def fake_run(command, cwd, env, timeout=300, **kwargs):
                if command[:3] == ["git", "rev-parse", "HEAD"]:
                    return "a" * 40
                if command[:2] == ["git", "status"]:
                    return "M renderer.go"
                if command[:2] == ["go", "version"]:
                    return "go version test"
                if command[:2] == ["go", "env"]:
                    return json.dumps({"GOCACHE": "/tmp/example-go-cache", "GOMODCACHE": "/tmp/example-module-cache"})
                if command[:2] == ["go", "test"]:
                    request = json.loads(Path(env["WTC_RECORDING_INPUT"]).read_text())
                    frames = []
                    for frame in request["frames"]:
                        content = "\n".join(f"{repo['dir']:6}{repo['branch_display']:16}" +
                                            ("#" + repo["pr"]["number"] if repo.get("pr") else "")
                                            for repo in frame["snapshot"]["repos"])
                        frames.append({"content": content, "phase": frame["phase"]})
                    Path(env["WTC_RECORDING_OUTPUT"]).write_text(json.dumps({"frames": frames}))
                    return "ok"
                self.fail(f"unexpected command: {command}")

            with patch.object(capture, "run", fake_run), patch.object(capture, "source_fingerprint", side_effect=["a" * 64, "b" * 64]), patch.object(capture.shutil, "which", return_value=None), patch.object(sys, "argv", ["capture.py", "--out", str(out)]):
                with contextlib.redirect_stdout(io.StringIO()):
                    self.assertEqual(capture.main(), 1)
            manifest = json.loads((out / "manifest.json").read_text())
            self.assertFalse(manifest["passed"])
            self.assertIn("source revision or working tree changed during capture", manifest["errors"])
            self.assertTrue((out / "wide-1.txt").is_file())


if __name__ == "__main__":
    unittest.main()
