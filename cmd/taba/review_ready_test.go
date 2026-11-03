package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"taba.sh/taba/internal/wtc"
)

func TestReviewReadyGateRejectsBlockersEvenWithPassingVerdict(t *testing.T) {
	zero, one := 0, 1
	for _, tc := range []struct {
		name  string
		state wtc.ReviewStatus
		allow bool
	}{
		{"current pass", wtc.ReviewStatus{State: "current", Verdict: "pass", Blockers: &zero}, true},
		{"current pass with notes", wtc.ReviewStatus{State: "current", Verdict: "pass-with-notes", Blockers: &zero}, true},
		{"pass with blocker", wtc.ReviewStatus{State: "current", Verdict: "pass", Blockers: &one}, false},
		{"notes with blocker", wtc.ReviewStatus{State: "current", Verdict: "pass-with-notes", Blockers: &one}, false},
		{"missing count", wtc.ReviewStatus{State: "current", Verdict: "pass"}, false},
		{"stale", wtc.ReviewStatus{State: "stale", Verdict: "pass", Blockers: &zero}, false},
		{"pending", wtc.ReviewStatus{State: "current", Verdict: "pending", Blockers: &zero}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := reviewReadyAllowed(tc.state); got != tc.allow {
				t.Fatalf("allowed=%t, want %t", got, tc.allow)
			}
		})
	}
}

func TestReviewReadyRefusesInReviewModeEvenWhenAuthorized(t *testing.T) {
	collection := filepath.Join(t.TempDir(), "sample")
	for _, dir := range []string{filepath.Join(collection, "harness"), filepath.Join(collection, "app", ".git")} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(collection, "harness", ".harness-repos.yml"), []byte("repos:\n  - name: app\n    remote: https://github.com/example/app.git\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(collection, wtc.ReviewModeFile), []byte(`{"schema_version":1,"repo":"app","dir":"app","pr":"7"}`), 0644); err != nil {
		t.Fatal(err)
	}
	// A forge call would fail the test: the refusal must come first.
	t.Setenv("PATH", t.TempDir())
	oldArgs := os.Args
	t.Cleanup(func() { os.Args = oldArgs })
	os.Args = []string{"wtc", "review", "ready", "7", "--repo", "app", "--collection", collection, "--user-authorized", "mark it ready"}
	if err := run(); err == nil || !strings.Contains(err.Error(), "belongs to the PR author") || !strings.Contains(err.Error(), "app#7") {
		t.Fatalf("review ready ran in review mode: %v", err)
	}
}

func TestFirstAgentPromptSaysReviewOnly(t *testing.T) {
	collection := t.TempDir()
	if got := firstAgentPrompt(collection, "claude"); got != "/wtc-start" {
		t.Fatalf("ordinary prompt = %q", got)
	}
	if err := os.WriteFile(filepath.Join(collection, wtc.ReviewModeFile), []byte("{}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"claude", "codex"} {
		if got := firstAgentPrompt(collection, kind); !strings.Contains(got, "review-only") || !strings.Contains(got, "No commits, pushes") {
			t.Fatalf("%s prompt does not say review-only: %q", kind, got)
		}
	}
}

func TestReviewStartRefusesOpenAndNoOpenTogether(t *testing.T) {
	oldArgs := os.Args
	t.Cleanup(func() { os.Args = oldArgs })
	os.Args = []string{"wtc", "review", "start", "app#7", "--open", "--no-open"}
	if err := run(); err == nil || !strings.Contains(err.Error(), "cannot be combined") {
		t.Fatalf("review start accepted --open with --no-open: %v", err)
	}
}
