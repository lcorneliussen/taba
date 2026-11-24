package wtc

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func newReviewStageBundle(t *testing.T) string {
	t.Helper()
	bundle := t.TempDir()
	for path, body := range map[string]string{
		"concerns/code.md":   "Review code.\n",
		"findings/code.json": "{}\n",
		"summary.md":         "old summary\n",
		".logs/code.out":     "runner log\n",
		"comment.id":         "301\n",
	} {
		full := filepath.Join(bundle, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("concerns", filepath.Join(bundle, "linked")); err != nil {
		t.Fatal(err)
	}
	return bundle
}

// The stage keeps the bundle's name and holds the bundle without poster state
// or other stages, keeps links as links, matches the bundle under any umask,
// and returns results and runner directories.
func TestReviewStageCopiesBundleAndReturnsRunChanges(t *testing.T) {
	bundle := newReviewStageBundle(t)
	other := filepath.Join(bundle, reviewStagePrefix+"other")
	if err := os.MkdirAll(other, 0700); err != nil {
		t.Fatal(err)
	}
	old := syscall.Umask(0077)
	stage, err := newReviewStage(bundle)
	syscall.Umask(old)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(stage.dir) != filepath.Base(bundle) || filepath.Dir(filepath.Dir(stage.dir)) != bundle {
		t.Fatalf("stage %s is not <bundle>/%s*/<bundle name>", stage.dir, reviewStagePrefix)
	}
	for _, rel := range []string{"comment.id", filepath.Base(other)} {
		if _, err := os.Lstat(filepath.Join(stage.dir, rel)); !os.IsNotExist(err) {
			t.Fatalf("%s copied into the stage: %v", rel, err)
		}
	}
	if target, err := os.Readlink(filepath.Join(stage.dir, "linked")); err != nil || target != "concerns" {
		t.Fatalf("link not kept as a link: %q %v", target, err)
	}
	write := func(rel, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(stage.dir, rel), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write("findings/code.json", `{"concern":"code"}`+"\n")
	write("notes.md", "new\n")
	write(".logs/code.out", "this run's log\n")
	if err := os.Remove(filepath.Join(stage.dir, "summary.md")); err != nil {
		t.Fatal(err)
	}
	if err := stage.finish(bundle); err != nil {
		t.Fatalf("finish under a stricter umask than the bundle's: %v", err)
	}
	for rel, want := range map[string]string{"findings/code.json": `{"concern":"code"}` + "\n", "notes.md": "new\n", "comment.id": "301\n", ".logs/code.out": "this run's log\n"} {
		if got, err := os.ReadFile(filepath.Join(bundle, rel)); err != nil || string(got) != want {
			t.Fatalf("%s = %q %v", rel, got, err)
		}
	}
	if _, err := os.Stat(filepath.Join(bundle, "summary.md")); !os.IsNotExist(err) {
		t.Fatalf("removal not returned: %v", err)
	}
	if _, err := os.Stat(stage.root); !os.IsNotExist(err) {
		t.Fatalf("stage not removed: %v", err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatalf("a fresh stage of another run was removed: %v", err)
	}
}

// A bundle file changed outside the run is not overwritten, and a link the
// run left is refused; either way the stage is removed.
func TestReviewStageRefusesConflictsAndIrregularFiles(t *testing.T) {
	t.Run("conflict", func(t *testing.T) {
		bundle := newReviewStageBundle(t)
		stage, err := newReviewStage(bundle)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(stage.dir, "findings", "code.json"), []byte("run\n"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(bundle, "findings", "code.json"), []byte("outside\n"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := stage.finish(bundle); err == nil || !strings.Contains(err.Error(), "findings/code.json changed in the bundle") {
			t.Fatalf("finish = %v", err)
		}
		if got, _ := os.ReadFile(filepath.Join(bundle, "findings", "code.json")); string(got) != "outside\n" {
			t.Fatalf("outside change overwritten: %q", got)
		}
		if _, err := os.Stat(filepath.Join(bundle, "summary.md")); !os.IsNotExist(err) {
			t.Fatalf("earlier summary left to post for a run whose results did not return: %v", err)
		}
		if _, err := os.Stat(stage.root); !os.IsNotExist(err) {
			t.Fatalf("stage not removed: %v", err)
		}
	})
	t.Run("irregular", func(t *testing.T) {
		bundle := newReviewStageBundle(t)
		stage, err := newReviewStage(bundle)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(filepath.Join(stage.dir, "findings", "code.json")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("/dev/null", filepath.Join(stage.dir, "findings", "code.json")); err != nil {
			t.Fatal(err)
		}
		if err := stage.finish(bundle); err == nil || !strings.Contains(err.Error(), "not a regular file") {
			t.Fatalf("finish = %v", err)
		}
		if got, _ := os.ReadFile(filepath.Join(bundle, "findings", "code.json")); string(got) != "{}\n" {
			t.Fatalf("bundle changed through a refused link: %q", got)
		}
		if _, err := os.Stat(stage.root); !os.IsNotExist(err) {
			t.Fatalf("stage not removed: %v", err)
		}
	})
}

// Stages older than any run are swept when a run starts; fresh ones stay.
func TestReviewStageSweepsOnlyStaleStages(t *testing.T) {
	bundle := t.TempDir()
	stale := filepath.Join(bundle, reviewStagePrefix+"stale")
	fresh := filepath.Join(bundle, reviewStagePrefix+"fresh")
	for _, dir := range []string{filepath.Join(stale, "findings"), fresh} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	past := time.Now().Add(-reviewStageMaxAge - time.Hour)
	if err := os.Chtimes(stale, past, past); err != nil {
		t.Fatal(err)
	}
	stage, err := newReviewStage(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale stage kept: %v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatalf("fresh stage removed: %v", err)
	}
	if err := stage.finish(bundle); err != nil {
		t.Fatal(err)
	}
}
