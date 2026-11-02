package wtc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReviewModeTakesEffectUnderEitherName(t *testing.T) {
	collection := t.TempDir()
	if _, ok := ReadReviewMode(collection); ok {
		t.Fatal("no marker must not be review-only")
	}
	alias := filepath.Join(collection, ".taba-review-mode")
	fixtureFile(t, alias, `{"pr":"alias","dir":"widget"}`, 0644)
	mode, ok := ReadReviewMode(collection)
	if !ok || mode.PR != "alias" {
		t.Fatalf("alternate marker ignored: %+v %v", mode, ok)
	}
	if !reviewOnlyWorktree(filepath.Join(collection, "widget")) {
		t.Fatal("alternate marker did not lock its worktree")
	}
	if _, err := os.Lstat(filepath.Join(collection, ReviewModeFile)); !os.IsNotExist(err) {
		t.Fatal("reading created the legacy marker")
	}
	fixtureFile(t, filepath.Join(collection, ReviewModeFile), `{"pr":"legacy"}`, 0644)
	if mode, ok := ReadReviewMode(collection); !ok || mode.PR != "legacy" {
		t.Fatalf("legacy marker must win: %+v %v", mode, ok)
	}
	// A broken legacy marker must not end the mode.
	if err := os.Remove(filepath.Join(collection, ReviewModeFile)); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(collection, ReviewModeFile), 0755); err != nil {
		t.Fatal(err)
	}
	if mode, ok := ReadReviewMode(collection); !ok || mode.PR != "alias" {
		t.Fatalf("broken legacy marker opened the mode: %+v %v", mode, ok)
	}
}

func TestUnparseableAlternateReviewMarkerFailsClosed(t *testing.T) {
	collection := t.TempDir()
	fixtureFile(t, filepath.Join(collection, ".taba-review-mode"), "not json", 0644)
	if !reviewOnlyWorktree(filepath.Join(collection, "anything")) {
		t.Fatal("unparseable alternate marker must lock every sibling")
	}
}

func TestRetiredMarkerUnderEitherName(t *testing.T) {
	dir := t.TempDir()
	if RetiredFolder(dir) {
		t.Fatal("unmarked folder reads as retired")
	}
	fixtureFile(t, filepath.Join(dir, ".taba-retired"), `{"cli_version":"alias"}`, 0644)
	if !RetiredFolder(dir) {
		t.Fatal("alternate marker ignored")
	}
	if marker, err := ReadRetiredMarker(dir); err != nil || marker.CLIVersion != "alias" {
		t.Fatalf("alternate marker unread: %+v %v", marker, err)
	}
	if err := retiredFolderError(dir); !strings.Contains(err.Error(), ".taba-retired") {
		t.Fatalf("diagnostic must name the marker found: %v", err)
	}
	if leftovers, err := retireLeftovers(dir); err != nil || len(leftovers) != 0 {
		t.Fatalf("a marker is no leftover: %v %v", leftovers, err)
	}
	fixtureFile(t, filepath.Join(dir, RetiredMarkerFile), `{"cli_version":"legacy"}`, 0644)
	if marker, err := ReadRetiredMarker(dir); err != nil || marker.CLIVersion != "legacy" {
		t.Fatalf("legacy marker must win: %+v %v", marker, err)
	}
}

func TestFinishingRetiredFolderRemovesBothMarkers(t *testing.T) {
	target := filepath.Join(t.TempDir(), "gone")
	if err := os.Mkdir(target, 0755); err != nil {
		t.Fatal(err)
	}
	fixtureFile(t, filepath.Join(target, RetiredMarkerFile), "{}", 0644)
	fixtureFile(t, filepath.Join(target, ".taba-retired"), "{}", 0644)
	result, err := (&Context{}).finishRetiredFolder(target)
	if err != nil || !result.FolderRemoved {
		t.Fatalf("folder survived: %+v %v", result, err)
	}
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		t.Fatal("folder still exists")
	}
}

func TestRetirementRemovesAlternateReviewAndLockState(t *testing.T) {
	target := t.TempDir()
	for _, name := range []string{".taba-review-mode", ".taba-runtime.lock", ".taba-status-watchers.lock"} {
		fixtureFile(t, filepath.Join(target, name), "", 0644)
	}
	fixtureFile(t, filepath.Join(target, ".taba", "review-hooks", "pre-push"), "#!/bin/sh\n", 0755)
	fixtureFile(t, filepath.Join(target, ".wtc", "review-hooks", "pre-push"), "#!/bin/sh\n", 0755)
	if err := removeRetiredGeneratedFiles(target, false); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(target)
	if err != nil || len(entries) != 0 {
		t.Fatalf("left behind: %v %v", entries, err)
	}
}

func TestReviewReceiptUnderEitherNameAndRevocation(t *testing.T) {
	collection := t.TempDir()
	fields := []string{"github", "example/app", "7", "head", "301", "pass"}
	if reviewReceiptExists(collection, fields...) {
		t.Fatal("no receipt must not prove a post")
	}
	paths := reviewReceiptPaths(collection, fields...)
	if paths[0] != reviewReceiptPath(collection, fields...) || filepath.Base(filepath.Dir(paths[0])) != ".wtc-review-posted" {
		t.Fatalf("receipts must still be written to the legacy name: %v", paths)
	}
	fixtureFile(t, paths[1], "", 0600)
	if !reviewReceiptExists(collection, fields...) {
		t.Fatal("alternate receipt ignored")
	}
	fixtureFile(t, paths[0], "", 0600)
	removeReviewReceipts(collection, fields...)
	if reviewReceiptExists(collection, fields...) {
		t.Fatal("a stale receipt survived revocation")
	}
}

func TestReviewRoundsContinueAcrossBundleRoots(t *testing.T) {
	collection := t.TempDir()
	roots := ReviewBundleRoots(collection)
	if filepath.Base(roots[0]) != ".wtc-reviews" {
		t.Fatalf("bundles must still be written to the legacy name: %v", roots)
	}
	dir := filepath.Join(roots[1], "widget-7-r4")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := writeReviewManifest(dir, ReviewManifest{Repo: "widget", PR: "7", Round: 4}); err != nil {
		t.Fatal(err)
	}
	if round := nextReviewRound(roots[0], "widget", "7", "", "", roots[1]); round != 5 {
		t.Fatalf("round restarted: %d", round)
	}
}

func TestRetirementStopsWatcherLeasesUnderEitherName(t *testing.T) {
	target := t.TempDir()
	for _, name := range statusWatcherDirs {
		fixtureFile(t, filepath.Join(target, name, "watcher-stale"), "", 0600)
	}
	if err := stopRegisteredStatusWatchers(target); err != nil {
		t.Fatal(err)
	}
	for _, name := range statusWatcherDirs {
		if _, err := os.Lstat(filepath.Join(target, name)); !os.IsNotExist(err) {
			t.Fatalf("%s survived: %v", name, err)
		}
	}
}

func TestBadAlternateWatcherRegistryStopsNothing(t *testing.T) {
	target := t.TempDir()
	lease := filepath.Join(target, statusWatcherDir, "watcher-live")
	fixtureFile(t, lease, "", 0600)
	fixtureFile(t, filepath.Join(target, ".taba-status-watchers"), "not a directory", 0644)
	if err := stopRegisteredStatusWatchers(target); err == nil {
		t.Fatal("bad registry accepted")
	}
	if info, err := os.Stat(lease); err != nil || info.Size() != 0 {
		t.Fatalf("lease was touched before the check failed: %v %v", info, err)
	}
}
