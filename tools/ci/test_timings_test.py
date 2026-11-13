"""Synthetic subprocess fixtures verify CI failure propagation and trace privacy."""
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

SCRIPT = Path(__file__).with_name("test-timings.py")
spec = importlib.util.spec_from_file_location("timings", SCRIPT)
timings = importlib.util.module_from_spec(spec)
spec.loader.exec_module(timings)


class TimingsTest(unittest.TestCase):
    def run_fixture(self, status, output):
        with tempfile.TemporaryDirectory() as directory:
            go = Path(directory) / "go"
            go.write_text(f"#!{sys.executable}\nimport sys\nprint({output!r})\nsys.exit({status})\n")
            go.chmod(0o755)
            return subprocess.run([sys.executable, str(SCRIPT), "./..."],
                                  env=dict(os.environ, PATH=directory + os.pathsep + os.environ["PATH"]),
                                  capture_output=True, text=True)

    def test_preserves_failed_suite_and_diagnostic(self):
        result = self.run_fixture(7, json.dumps({"Action": "output", "Output": "failure detail\n"}))
        self.assertEqual(result.returncode, 7)
        self.assertIn("failure detail", result.stdout)
        self.assertIn("Go test wall time", result.stdout)

    def test_valid_success(self):
        result = self.run_fixture(0, json.dumps({"Action": "pass", "Package": "example/test", "Elapsed": 1.25}))
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("1.250  example/test", result.stdout)

    def test_malformed_output_fails_closed(self):
        result = self.run_fixture(0, "{truncated")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("{truncated", result.stdout)

    def test_git_children_are_not_double_counted_or_disclosed(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "trace"
            events = [
                {"event": "cmd_name", "sid": "root", "name": "fetch", "argv": ["sensitive-argument"]},
                {"event": "cmd_name", "sid": "root/child", "name": "upload-pack"},
                {"event": "exit", "sid": "root/child", "t_abs": 1},
                {"event": "exit", "sid": "root", "t_abs": 2},
            ]
            path.write_text("\n".join(map(json.dumps, events)) + "\n")
            self.assertEqual(dict(timings.git_timings(path)), {"fetch": [1, 2.0]})


if __name__ == "__main__":
    unittest.main()
