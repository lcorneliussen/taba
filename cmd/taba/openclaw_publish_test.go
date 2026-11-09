package main

import (
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"taba.sh/taba/internal/openclaw"
)

// publishPlan runs wtc publish in dir, with stdin, and returns its plan.
func publishPlan(t *testing.T, dir, stdin string, args ...string) (openclaw.Plan, error) {
	t.Helper()
	t.Chdir(dir)
	var asJSON bool
	root := &cobra.Command{Use: "wtc", SilenceUsage: true, SilenceErrors: true}
	addHostCommands(root, &asJSON)
	root.SetArgs(append([]string{"publish"}, args...))
	in, inW, _ := os.Pipe()
	io.WriteString(inW, stdin)
	inW.Close()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	devnull, _ := os.Open(os.DevNull)
	stdinWas, stdout, stderr := os.Stdin, os.Stdout, os.Stderr
	os.Stdin, os.Stdout, os.Stderr = in, w, devnull
	runErr := root.Execute()
	os.Stdin, os.Stdout, os.Stderr = stdinWas, stdout, stderr
	w.Close()
	out, _ := io.ReadAll(r)
	var plan openclaw.Plan
	if runErr == nil {
		if err := json.Unmarshal(out, &plan); err != nil {
			t.Fatalf("plan: %v\n%s", err, out)
		}
	}
	return plan, runErr
}

// The text given to wtc publish is recorded for the card's push, from a
// flag, a file or stdin, and the plan says what is still missing.
func TestPublishRecordsThePullRequestText(t *testing.T) {
	t.Setenv("OPENCLAW_SHELL", "exec")
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	root := t.TempDir()
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=T", "-c", "user.email=t@example.com"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	repo, wt := filepath.Join(root, "docs"), filepath.Join(root, "72-add-thing")
	git(root, "init", "-q", "-b", "main", repo)
	os.WriteFile(filepath.Join(repo, "README.md"), []byte("docs\n"), 0o644)
	git(repo, "add", ".")
	git(repo, "commit", "-qm", "init")
	git(repo, "update-ref", "refs/remotes/origin/main", "HEAD")
	git(repo, "remote", "add", "origin", "https://github.com/example-org/docs.git")
	git(repo, "worktree", "add", "-q", "-b", "openclaw/72-add-thing", wt)
	os.WriteFile(filepath.Join(wt, "notes.md"), []byte("one\n"), 0o644)

	missing := func(plan openclaw.Plan) string {
		for _, w := range plan.Warnings {
			if strings.Contains(w, "No ") && strings.Contains(w, "recorded") {
				return w
			}
		}
		return ""
	}
	plan, err := publishPlan(t, wt, "", "--title", "Add notes")
	if err != nil || !strings.Contains(missing(plan), "No description") {
		t.Fatalf("a title alone: %v %q", err, missing(plan))
	}
	body := filepath.Join(root, "body.md")
	os.WriteFile(body, []byte("What and why.\n"), 0o644)
	if plan, err = publishPlan(t, wt, "", "--body-file", body); err != nil || missing(plan) != "" {
		t.Fatalf("the description from a file: %v %q", err, missing(plan))
	}
	got := openclaw.ReadProposal(wt, "openclaw/72-add-thing")
	if got == nil || got.Title != "Add notes" || got.Body != "What and why." {
		t.Fatalf("recorded %+v", got)
	}
	if _, err := publishPlan(t, wt, "From stdin.", "--body-file", "-"); err != nil {
		t.Fatal(err)
	}
	if got := openclaw.ReadProposal(wt, "openclaw/72-add-thing"); got.Body != "From stdin." {
		t.Fatalf("stdin: %+v", got)
	}
	if _, err := publishPlan(t, wt, "", "--body", "x", "--body-file", body); err == nil || !strings.Contains(err.Error(), "not both") {
		t.Fatalf("both: %v", err)
	}
	if _, err := publishPlan(t, wt, "", "--title", "two\nlines"); err == nil || !strings.Contains(err.Error(), "one line") {
		t.Fatalf("a title of two lines: %v", err)
	}
	// A sandbox's copy has no origin, and cannot hand the text to the card.
	git(wt, "remote", "remove", "origin")
	plan, err = publishPlan(t, wt, "", "--title", "Add notes")
	if err != nil || !strings.Contains(strings.Join(plan.Warnings, "\n"), "sandbox's copy cannot record") {
		t.Fatalf("a sandbox's copy: %v %v", err, plan.Warnings)
	}
}

func TestCatchUpSummary(t *testing.T) {
	for _, tc := range []struct {
		result openclaw.CatchUp
		want   string
	}{
		{openclaw.CatchUp{Branch: "openclaw/x", Dest: "main", Merged: true, FastForward: true}, "openclaw/x is at origin/main again: nothing of its own on it, nothing to publish"},
		// A worker's commits are the task's to publish, fast-forwarded or not.
		{openclaw.CatchUp{Branch: "openclaw/x", Dest: "main", From: "openclaw/x--a", Merged: true, FastForward: true}, "merged openclaw/x--a into openclaw/x; the person publishes it with ↑ when the task is ready"},
		{openclaw.CatchUp{Branch: "openclaw/x", Dest: "main", Merged: true}, "merged origin/main into openclaw/x; publish to send the merge (wtc publish)"},
		{openclaw.CatchUp{Branch: "openclaw/x", Note: "already contains origin/main"}, "openclaw/x already contains origin/main"},
		// A worker catches up with its task.
		{openclaw.CatchUp{Branch: "openclaw/x--a", Dest: "main", From: "openclaw/x", Merged: true, Worker: true}, "merged the task's openclaw/x into the worker's openclaw/x--a"},
		{openclaw.CatchUp{Branch: "openclaw/x--a", From: "openclaw/x", Note: "already contains openclaw/x", Worker: true}, "openclaw/x--a already contains openclaw/x"},
		// A worker that merged origin's (--dest) still reaches the forge through its task.
		{openclaw.CatchUp{Branch: "openclaw/x--a", Dest: "main", Merged: true, Worker: true}, "merged origin/main into openclaw/x--a; it reaches the forge through the task"},
		// A task with "--" in its own name is no worker.
		{openclaw.CatchUp{Branch: "openclaw/fix--login", Dest: "main", Merged: true}, "merged origin/main into openclaw/fix--login; publish to send the merge (wtc publish)"},
		// A detached worktree has no branch: it moves forward to the tip.
		{openclaw.CatchUp{Dest: "main", Merged: true, FastForward: true}, "detached at origin/main's tip again: no work of its own, nothing to publish"},
		{openclaw.CatchUp{Dest: "main", Note: "already contains origin/main"}, "detached, already contains origin/main"},
	} {
		if got := catchUpSummary(tc.result); got != tc.want {
			t.Errorf("%+v: %q, want %q", tc.result, got, tc.want)
		}
	}
}

func TestRefuseWorkerServices(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"site-work", "site-work--docs", "fix--login"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	gateway := func(k string) string { return map[string]string{"OPENCLAW_SHELL": "exec"}[k] }
	local := func(string) string { return "" }
	if err := refuseWorkerServices(filepath.Join(root, "site-work--docs"), gateway); err == nil || !strings.Contains(err.Error(), "services run in its task's collection, site-work") {
		t.Errorf("a worker's collection: %v", err)
	}
	// Its task, a task with "--" in its name, and anything off the gateway run them.
	for _, tc := range []struct {
		dir    string
		getenv func(string) string
	}{{"site-work", gateway}, {"fix--login", gateway}, {"site-work--docs", local}} {
		if err := refuseWorkerServices(filepath.Join(root, tc.dir), tc.getenv); err != nil {
			t.Errorf("%s: %v", tc.dir, err)
		}
	}
}
