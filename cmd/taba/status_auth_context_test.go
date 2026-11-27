package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"taba.sh/taba/internal/wtc"
)

func TestStatusTUIAuth441LegacyStartupDoesNotPaint(t *testing.T) {
	statusAuth441ClearCredentials(t)
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GH_TOKEN", "synthetic-context-B")
	t.Setenv("GH_CONFIG_DIR", t.TempDir())
	c := &wtc.Context{Collection: t.TempDir()}
	raw := `{"schema":1,"collection":"` + filepath.Base(c.Collection) + `","repos":[],"prs":[{"repo":"widget","number":"7","state":"MERGED","title":"Legacy context A"}]}`
	if err := os.WriteFile(filepath.Join(c.Collection, ".wtc-status.json"), []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	model := newStatusTUIModel(c, false, false, false, true, true, false, time.Minute, 5*time.Minute, time.Minute, ctx, cancel)
	model.width, model.height = 100, 30
	if model.snapshotLoaded || len(model.snapshot.PRs) != 0 || strings.Contains(model.View().Content, "Legacy context A") {
		t.Fatal("active startup briefly painted an unscoped snapshot")
	}
	// The explicit offline reader remains a separate user-selected local record.
	if snapshot, err := c.ReadStatusSnapshot(); err != nil || snapshot.PRs[0].Title != "Legacy context A" {
		t.Fatal("offline record unavailable", err)
	}
}

func statusAuth441ModelFixture(t *testing.T) (*wtc.Context, wtc.StatusSnapshot) {
	statusAuth441ClearCredentials(t)
	t.Helper()
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GH_TOKEN", "synthetic-A")
	t.Setenv("GH_CONFIG_DIR", t.TempDir())
	t.Setenv("GH_HOST", "")
	t.Setenv("WTC_CONFIG_ROOT", "")
	t.Setenv("TABA_CONFIG_ROOT", "")
	t.Setenv("WTC_FORGE_MIN_INTERVAL", "0")
	collection := filepath.Join(t.TempDir(), "fixture")
	harness := filepath.Join(collection, "harness")
	if err := os.MkdirAll(harness, 0700); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("synthetic Git setup: %v %s", err, out)
		}
	}
	git("init", "-q", "-b", "main", harness)
	git("-C", harness, "-c", "user.name=Synthetic", "-c", "user.email=synthetic@example.invalid", "commit", "-q", "--allow-empty", "-m", "initial")
	git("-C", harness, "remote", "add", "origin", "https://github.com/example/widget.git")
	git("-C", harness, "update-ref", "refs/remotes/origin/main", "HEAD")
	files := map[string]string{filepath.Join(harness, ".harness-repos.yml"): "repos:\n  - name: harness\n    remote: https://github.com/example/widget.git\n    default_ref: origin/main\n", filepath.Join(collection, ".wtc-prs"): "harness 7 main https://github.com/example/widget/pull/7 Synthetic\n"}
	bin := t.TempDir()
	files[filepath.Join(bin, "gh")] = `#!/bin/sh
case "$1 $2" in
 "pr list") printf '%s\n' '[{"number":7,"state":"OPEN","title":"Provider A","headRefName":"main"}]';;
 "pr view") printf '%s\n' '{"number":7,"state":"OPEN","title":"Provider A"}';;
 *) printf '{}\n';;
esac
`
	for path, raw := range files {
		if err := os.WriteFile(path, []byte(raw), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	c, err := wtc.OpenCollection(collection)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := c.StatusForgePreview()
	if err != nil || len(snapshot.PRs) != 1 || snapshot.PRs[0].Title != "Provider A" {
		t.Fatalf("scoped collector fixture: %+v %v", snapshot.PRs, err)
	}
	return c, snapshot
}
func TestStatusTUIAuth441UpdateRejectsChangedContext(t *testing.T) {
	for _, event := range []string{"delivery", "signature"} {
		t.Run(event, func(t *testing.T) {
			c, snapshot := statusAuth441ModelFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			model := statusTUIModel{context: c, snapshot: snapshot, snapshotLoaded: true, refreshing: true, resourceSignature: c.StatusResourceSignature(), uiContext: ctx, startedAt: time.Now(), interval: time.Minute, background: 5 * time.Minute}
			t.Setenv("GH_TOKEN", "synthetic-B")
			var next interface{}
			if event == "delivery" {
				next, _ = model.Update(statusLoadedMsg{snapshot: snapshot, at: time.Now()})
			} else {
				next, _ = model.Update(statusTickMsg(time.Now()))
			}
			updated := next.(statusTUIModel)
			if updated.snapshotLoaded || len(updated.snapshot.PRs) != 0 {
				t.Fatal("Update retained provider observations after credential switch")
			}
			if event == "delivery" && !strings.Contains(updated.errorText, "credential context changed") {
				t.Fatal("late delivery lacked truthful rejection")
			}
		})
	}
}

func statusAuth441ClearCredentials(t *testing.T) {
	t.Helper()
	for _, name := range []string{"GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN", "GH_HOST", "GH_REPO", "BB_API_USER", "BB_API_PASS", "BB_USERNAME", "BB_API_TOKEN", "ATLASSIAN_ACCOUNT_EMAIL", "ATLASSIAN_API_TOKEN", "XDG_CONFIG_HOME"} {
		t.Setenv(name, "")
	}
}
