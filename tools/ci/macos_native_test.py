import contextlib
import copy
import importlib.util
import io
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("macos_native", Path(__file__).with_name("macos-native.py"))
native = importlib.util.module_from_spec(spec)
spec.loader.exec_module(native)


class NativeSelectionTests(unittest.TestCase):
    def test_manifest_and_future_darwin_source_require_review(self):
        manifest = json.loads(native.MANIFEST.read_text())
        self.assertTrue(native.selection(manifest, native.ROOT))
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            empty = {"darwin_sources": [], "groups": copy.deepcopy(manifest["groups"])}
            native.selection(empty, root)
            (root / "new_darwin.go").touch()
            with self.assertRaisesRegex(ValueError, "inventory changed"):
                native.selection(empty, root)

    def test_empty_duplicate_and_regex_selections_are_rejected(self):
        manifest = {"darwin_sources": [], "groups": []}
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            with self.assertRaises(ValueError):
                native.selection(manifest, root)
            for tests in [[], ["TestOne", "TestOne"], ["Test.*"], ["TestOne/Subtest"]]:
                manifest["groups"] = [{"package": "./fixture", "risk": "locks", "tests": tests}]
                with self.assertRaises(ValueError):
                    native.selection(manifest, root)

    def test_real_go_execution_rejects_missing_skipped_and_failed_tests(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "go.mod").write_text("module example.invalid/native-fixture\n\ngo 1.27.0\n")
            (root / "native_test.go").write_text('''package fixture
import "testing"
func TestPass(t *testing.T) { t.Run("child", func(t *testing.T) {}) }
func TestSkip(t *testing.T) { t.Skip("synthetic") }
func TestFail(t *testing.T) { t.Fatal("synthetic") }
''')
            with patch.object(native, "ROOT", root), contextlib.redirect_stdout(io.StringIO()):
                native.run_package("./", ["TestPass"])
                for names in [["TestMissing"], ["TestSkip"], ["TestPass", "TestMissing"], ["TestFail"]]:
                    with self.assertRaises(ValueError, msg=str(names)):
                        native.run_package("./", names)

    def test_zero_events_and_subtest_only_do_not_count_as_coverage(self):
        for events in [[], [{"Action": "pass", "Test": "TestOne/child"}],
                       [{"Action": "skip", "Test": "TestOne"}]]:
            with self.assertRaises(ValueError):
                native.verify_results(events, ["TestOne"])
        with self.assertRaises(ValueError):
            native.verify_results([{"Action": "pass", "Test": "TestOne"},
                                   {"Action": "fail", "Package": "fixture"}], ["TestOne"])


if __name__ == "__main__":
    unittest.main()
