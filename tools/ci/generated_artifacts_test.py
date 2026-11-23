"""Synthetic Git fixtures for check-generated-artifacts.py.

Fixture files hold placeholder bytes only; no real bytecode is created.
"""
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest

SCRIPT = Path(__file__).with_name("check-generated-artifacts.py")
GITIGNORE = Path(__file__).resolve().parents[2] / ".gitignore"
PLACEHOLDER = b"synthetic placeholder, not bytecode\n"


class Repo:
    def __init__(self, path):
        self.path = Path(path)
        self.env = dict(os.environ, GIT_CONFIG_GLOBAL=os.devnull, GIT_CONFIG_NOSYSTEM="1",
                        GIT_AUTHOR_NAME="Fixture", GIT_AUTHOR_EMAIL="fixture@example.invalid",
                        GIT_COMMITTER_NAME="Fixture", GIT_COMMITTER_EMAIL="fixture@example.invalid",
                        GIT_AUTHOR_DATE="2000-01-01T00:00:00Z",
                        GIT_COMMITTER_DATE="2000-01-01T00:00:00Z")
        self.env.pop("GIT_DIR", None)
        self.env.pop("GIT_INDEX_FILE", None)

    def git(self, *args, input=None, env=None):
        return subprocess.run(["git", *args], cwd=self.path, input=input, check=True,
                              capture_output=True, env=env or self.env).stdout

    def init(self):
        self.path.mkdir(parents=True, exist_ok=True)
        self.git("init", "-q", "-b", "main")
        return self

    def commit(self, files, parents=(), message="fixture"):
        """Commit a tree holding exactly `files` ({bytes path: bytes}) via plumbing."""
        index = self.path / ".git" / "fixture-index"
        env = dict(self.env, GIT_INDEX_FILE=str(index))
        if index.exists():
            index.unlink()
        entries = b""
        for path, content in files.items():
            blob = self.git("hash-object", "-w", "--stdin", input=content).strip()
            entries += b"100644 " + blob + b"\t" + path + b"\0"
        self.git("update-index", "-z", "--index-info", input=entries, env=env)
        tree = self.git("write-tree", env=env).strip().decode()
        args = ["commit-tree", tree, "-m", message]
        for parent in parents:
            args += ["-p", parent]
        return self.git(*args).strip().decode()

    def check(self, base, head):
        return subprocess.run([sys.executable, "-B", str(SCRIPT), f"--base={base}", f"--head={head}"],
                              cwd=self.path, capture_output=True, env=self.env)


BASE_FILES = {b"README.md": b"fixture\n", b"tool/run.py": b"print('ok')\n"}


def tree(extra):
    return {**BASE_FILES, **extra}


class GeneratedArtifactsTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, self.tmp)
        self.repo = Repo(Path(self.tmp) / "repo").init()
        self.base = self.repo.commit(BASE_FILES)

    def assertClean(self, result):
        self.assertEqual(result.returncode, 0, result.stderr.decode())

    def assertFindings(self, result, *paths):
        self.assertEqual(result.returncode, 1, result.stderr.decode())
        for path in paths:
            self.assertIn(path, result.stderr.decode())

    def assertUnverifiable(self, result):
        self.assertEqual(result.returncode, 2, result.stdout.decode() + result.stderr.decode())
        self.assertIn("cannot verify", result.stderr.decode())

    def test_clean_range(self):
        head = self.repo.commit(tree({b"tool/new.py": b"x = 1\n"}), [self.base])
        self.assertClean(self.repo.check(self.base, head))

    def test_empty_range_still_checks_head_tree(self):
        self.assertClean(self.repo.check(self.base, self.base))

    def test_initial_committed_artifact(self):
        root = self.repo.commit({b"tool/__pycache__/run.cpython-311.pyc": PLACEHOLDER})
        self.assertFindings(self.repo.check(root, root), "tool/__pycache__/run.cpython-311.pyc")

    def test_artifact_in_final_tree(self):
        head = self.repo.commit(tree({b"tool/run.pyo": PLACEHOLDER}), [self.base])
        self.assertFindings(self.repo.check(self.base, head), "tool/run.pyo")

    def test_add_then_remove_is_missed_by_net_tree_but_rejected(self):
        added = self.repo.commit(tree({b"tool/__pycache__/run.pyc": PLACEHOLDER}),
                                 [self.base])
        head = self.repo.commit(BASE_FILES, [added])
        # A net-tree-only check sees no difference at all.
        self.assertEqual(self.repo.git("diff", "--name-only", self.base, head), b"")
        self.assertEqual(self.repo.git("ls-tree", "-r", "--name-only", head),
                         self.repo.git("ls-tree", "-r", "--name-only", self.base))
        result = self.repo.check(self.base, head)
        self.assertFindings(result, "tool/__pycache__/run.pyc", added[:12])

    def test_artifact_on_merged_side_branch(self):
        added = self.repo.commit(tree({b"lib/__pycache__/a.pyc": PLACEHOLDER}),
                                 [self.base])
        removed = self.repo.commit(BASE_FILES, [added])
        mainline = self.repo.commit(tree({b"docs.md": b"d\n"}), [self.base])
        merge = self.repo.commit(tree({b"docs.md": b"d\n"}), [mainline, removed])
        self.assertEqual(self.repo.git("diff", "--name-only", mainline, merge), b"")
        self.assertFindings(self.repo.check(mainline, merge), "lib/__pycache__/a.pyc")

    def test_artifact_introduced_by_merge_commit_itself(self):
        side = self.repo.commit(tree({b"side.py": b"s\n"}), [self.base])
        mainline = self.repo.commit(tree({b"docs.md": b"d\n"}), [self.base])
        merge = self.repo.commit(tree({b"side.py": b"s\n", b"docs.md": b"d\n",
                                                     b"x/__pycache__/m.pyc": PLACEHOLDER}),
                                 [mainline, side])
        head = self.repo.commit(tree({b"side.py": b"s\n", b"docs.md": b"d\n"}),
                                [merge])
        self.assertFindings(self.repo.check(mainline, head), "x/__pycache__/m.pyc", merge[:12])

    def test_clean_merge_of_base_into_branch(self):
        branch = self.repo.commit(tree({b"b.py": b"b\n"}), [self.base])
        mainline = self.repo.commit(tree({b"docs.md": b"d\n"}), [self.base])
        merge = self.repo.commit(tree({b"b.py": b"b\n", b"docs.md": b"d\n"}),
                                 [branch, mainline])
        self.assertClean(self.repo.check(mainline, merge))

    def test_artifact_already_in_base_history_is_not_reported(self):
        added = self.repo.commit(tree({b"old/__pycache__/o.pyc": PLACEHOLDER}),
                                 [self.base])
        accepted = self.repo.commit(BASE_FILES, [added])
        head = self.repo.commit(tree({b"n.py": b"n\n"}), [accepted])
        self.assertClean(self.repo.check(accepted, head))

    def test_odd_filenames_are_matched_and_escaped(self):
        odd = {
            b"dir with space/__pycache__/a b.pyc": "dir with space/__pycache__/a b.pyc",
            b"-lead/line\nbreak.pyc": "-lead/line\\x0abreak.pyc",
            b"q\"uote\\/__pycache__/t\tab": "q\"uote\\\\/__pycache__/t\\x09ab",
            b"latin\xe9\xff/UPPER.PYC": "latin\\xe9\\xff/UPPER.PYC",
        }
        files = tree({b"not_pycache/ok.pyx": b"ok\n", b"pycache.py": b"ok\n"})
        files.update({path: PLACEHOLDER for path in odd})
        head = self.repo.commit(files, [self.base])
        result = self.repo.check(self.base, head)
        self.assertFindings(result, *odd.values())
        report = result.stderr.decode("ascii")  # only escaped ASCII is ever printed
        self.assertNotIn("ok.pyx", report)
        self.assertNotIn("pycache.py", report)
        self.assertNotIn(PLACEHOLDER.decode(), report)

    def test_untracked_ignored_caches_do_not_fail_and_are_ignored(self):
        self.repo.git("reset", "-q", "--hard", self.base)
        shutil.copyfile(GITIGNORE, self.repo.path / ".gitignore")
        for path in ("tool/__pycache__/run.cpython-311.pyc", "tool/run.pyc", "tool/run.pyo"):
            target = self.repo.path / path
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_bytes(PLACEHOLDER)
        status = self.repo.git("status", "--porcelain", "-z", "--untracked-files=all")
        self.assertEqual(status, b"?? .gitignore\0")
        self.assertClean(self.repo.check(self.base, "HEAD"))

    def test_missing_base_or_head(self):
        missing = "0" * 40
        self.assertUnverifiable(self.repo.check(missing, self.base))
        self.assertUnverifiable(self.repo.check(self.base, missing))
        self.assertUnverifiable(self.repo.check("refs/heads/absent", self.base))

    def test_option_like_revision_is_not_an_option(self):
        self.assertUnverifiable(self.repo.check("--all", self.base))

    def test_unrelated_history(self):
        orphan = self.repo.commit({b"other.txt": b"o\n"})
        self.assertUnverifiable(self.repo.check(self.base, orphan))

    def shallow_clone(self, head, depth):
        self.repo.git("update-ref", "refs/heads/main", head)
        clone = Repo(Path(self.tmp) / f"shallow-{depth}")
        subprocess.run(["git", "clone", "-q", "--no-local", f"--depth={depth}", "--branch", "main",
                        self.repo.path.as_uri(), str(clone.path)],
                       check=True, capture_output=True, env=self.repo.env)
        return clone

    def test_shallow_history_inside_range_cannot_pass(self):
        added = self.repo.commit(tree({b"t/__pycache__/s.pyc": PLACEHOLDER}),
                                 [self.base])
        removed = self.repo.commit(BASE_FILES, [added])
        head = self.repo.commit(tree({b"n.py": b"n\n"}), [removed])
        clone = self.shallow_clone(head, 1)
        # Base is absent from the shallow clone.
        self.assertUnverifiable(clone.check(self.base, head))
        # Base is present but the boundary hides the commits between base and head.
        clone.git("fetch", "-q", "--depth=1", "origin", self.base)
        self.assertUnverifiable(clone.check(self.base, head))

    def test_shallow_clone_covering_range_passes_or_fails_on_content(self):
        added = self.repo.commit(tree({b"t/__pycache__/s.pyc": PLACEHOLDER}),
                                 [self.base])
        head = self.repo.commit(BASE_FILES, [added])
        clone = self.shallow_clone(head, 3)
        self.assertFindings(clone.check(self.base, head), "t/__pycache__/s.pyc")
        clone.git("fetch", "-q", "--unshallow", "origin")
        self.assertFindings(clone.check(self.base, head), "t/__pycache__/s.pyc")


if __name__ == "__main__":
    unittest.main()
