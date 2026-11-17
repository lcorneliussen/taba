import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

import check


REPO = Path(__file__).resolve().parents[2]


class ProvenanceTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name) / "source"
        self.root.mkdir()
        # Keep the selected Go toolchain when changing into a synthetic repo.
        go_root = subprocess.check_output(["go", "env", "GOROOT"], cwd=REPO).decode().strip()
        self.environment = patch.dict(os.environ, {
            "GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": os.devnull,
            "GOWORK": "off", "GOFLAGS": "",
            "PATH": str(Path(go_root) / "bin") + os.pathsep + os.environ["PATH"],
        })
        self.environment.start()
        self.addCleanup(self.environment.stop)
        self.run_command("git", "init", "-q")
        self.run_command("git", "config", "user.name", "Release Test")
        self.run_command("git", "config", "user.email", "release@example.invalid")
        (self.root / "go.mod").write_text("module example.invalid/release\n\ngo 1.18\n")
        (self.root / "main.go").write_text("package main\nfunc main() {}\n")
        (self.root / ".gitignore").write_bytes((REPO / ".gitignore").read_bytes())
        self.commit()
        self.binary = Path(self.temp.name) / "binary"

    def run_command(self, *args):
        result = subprocess.run(args, cwd=self.root, capture_output=True)
        if result.returncode:
            self.fail(result.stderr.decode() + result.stdout.decode())
        return result.stdout

    def commit(self):
        self.run_command("git", "add", ".")
        self.run_command("git", "commit", "-qm", "Synthetic release source")

    def build(self, *flags):
        self.run_command("go", "build", *flags, "-o", str(self.binary), ".")
        return self.run_command("go", "version", "-m", str(self.binary)).decode()

    def generated(self):
        dist = self.root / "dist"
        dist.mkdir()
        (dist / "metadata.json").write_text("{}")
        (dist / "config.yaml").write_text("version: 2\n")
        target = dist / "taba_linux_amd64_v1"
        target.mkdir()
        (target / "taba").write_bytes(b"synthetic build output")

    def test_expected_generated_outputs_preserve_exact_clean_metadata(self):
        self.generated()
        check.check_source(self.root)
        check.check_outputs(self.root)
        info = self.build()
        revision = self.run_command("git", "rev-parse", "HEAD").decode().strip()
        self.assertIn("vcs.revision=" + revision, info)
        self.assertIn("vcs.modified=false", info)
        check.check_binary(self.root, self.binary)

    def test_real_tracked_edit_stays_dirty_and_rejected(self):
        self.generated()
        (self.root / "main.go").write_text("package main\nfunc main() { println(1) }\n")
        self.assertIn("vcs.modified=true", self.build())
        with self.assertRaisesRegex(ValueError, "modified"):
            check.check_source(self.root)
        with self.assertRaisesRegex(ValueError, "metadata"):
            check.check_binary(self.root, self.binary)

    def test_real_untracked_source_stays_dirty_and_rejected(self):
        source = self.root / "extra.go"
        source.write_text("package main\n")
        self.assertIn("vcs.modified=true", self.build())
        with self.assertRaisesRegex(ValueError, "untracked"):
            check.check_source(self.root)
        self.assertTrue(source.exists())

    def test_reproduces_generated_output_without_ignore(self):
        (self.root / ".gitignore").unlink()
        self.commit()
        self.assertIn("vcs.modified=false", self.build())
        self.generated()
        self.assertIn("vcs.modified=true", self.build())

    def test_wrong_commit_and_disabled_vcs_are_rejected(self):
        self.build()
        (self.root / "main.go").write_text("package main\nfunc main() { println(2) }\n")
        self.commit()
        with self.assertRaisesRegex(ValueError, "exact clean source commit"):
            check.check_binary(self.root, self.binary)
        self.build("-buildvcs=false")
        with self.assertRaisesRegex(ValueError, "metadata"):
            check.check_binary(self.root, self.binary)

    def test_unexpected_ignored_source_is_not_cleaned(self):
        self.generated()
        unsafe = self.root / "dist" / "extra.go"
        unsafe.write_text("package main\n")
        with self.assertRaisesRegex(ValueError, "unexpected release output"):
            check.check_outputs(self.root)
        self.assertEqual(unsafe.read_text(), "package main\n")

    def test_nested_source_and_unknown_directories_are_rejected(self):
        self.generated()
        target = self.root / "dist" / "taba_linux_amd64_v1" / "extra.go"
        target.write_text("package main\n")
        with self.assertRaises(ValueError):
            check.check_outputs(self.root)
        target.unlink()
        (self.root / "dist" / "unknown").mkdir()
        with self.assertRaises(ValueError):
            check.check_outputs(self.root)

    def test_symlink_outputs_do_not_authorize_removal(self):
        outside = Path(self.temp.name) / "outside"
        outside.mkdir()
        (outside / "keep").write_text("keep")
        (self.root / "dist").symlink_to(outside, target_is_directory=True)
        with self.assertRaises(ValueError):
            check.check_source(self.root)
        self.assertEqual((outside / "keep").read_text(), "keep")
        (self.root / "dist").unlink()
        self.generated()
        (self.root / "dist" / "artifacts.json").symlink_to(outside / "keep")
        with self.assertRaises(ValueError):
            check.check_outputs(self.root)

    def test_tracked_outputs_are_rejected_even_when_clean(self):
        self.generated()
        self.run_command("git", "add", "-f", "dist/metadata.json")
        self.commit()
        with self.assertRaisesRegex(ValueError, "tracked source"):
            check.check_source(self.root)

    def test_local_excludes_cannot_hide_source(self):
        (self.root / ".git" / "info" / "exclude").write_text("hidden.go\n")
        (self.root / "hidden.go").write_text("package main\n")
        with self.assertRaisesRegex(ValueError, "ignored files outside"):
            check.check_source(self.root)

    def test_hidden_tracked_edits_are_rejected(self):
        for flag in ("--assume-unchanged", "--skip-worktree"):
            with self.subTest(flag=flag):
                self.run_command("git", "update-index", flag, "main.go")
                with self.assertRaisesRegex(ValueError, "hidden index entries"):
                    check.check_source(self.root)
                self.run_command("git", "update-index", flag.replace("--", "--no-", 1), "main.go")

    def test_cli_fails_closed_and_keeps_source(self):
        unsafe = self.root / "new.go"
        unsafe.write_text("package main\n")
        result = subprocess.run(
            ["python3", "-B", str(REPO / "tools/release-provenance/check.py"), "preflight"],
            cwd=self.root, capture_output=True, text=True,
        )
        self.assertEqual(result.returncode, 1)
        self.assertIn("Release provenance rejected", result.stderr)
        self.assertTrue(unsafe.exists())


if __name__ == "__main__":
    unittest.main()
