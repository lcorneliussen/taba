package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"taba.sh/taba/internal/wtc"
)

func retireHandoffFixture(t *testing.T, sourceID string) (*wtc.Context, string) {
	t.Helper()
	root := t.TempDir()
	collection := filepath.Join(root, "finished")
	harness := filepath.Join(collection, "harness")
	if err := os.MkdirAll(harness, 0755); err != nil {
		t.Fatal(err)
	}
	registry := "repos:\n  - name: widget\n    remote: https://github.com/example/widget.git\n    default_ref: main\n"
	if err := os.WriteFile(filepath.Join(harness, ".harness-repos.yml"), []byte(registry), 0644); err != nil {
		t.Fatal(err)
	}
	c, err := wtc.OpenCollection(collection)
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0755); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(root, "herdr.log")
	fixture := `#!/usr/bin/env python3
import json, os, sys
args = sys.argv[3:]
with open(os.environ["RETIRE_TEST_LOG"], "a") as out: out.write(" ".join(args) + "\n")
def emit(value): print(json.dumps({"result": value}))
if args == ["workspace", "list"]:
    emit({"workspaces": [{"label":"finished", "workspace_id":os.environ["RETIRE_TEST_SOURCE_ID"]}] + ([{"label":"--cleanup--", "workspace_id":"cleanup-id"}] if os.environ.get("RETIRE_TEST_REUSE") else [])})
elif args[:2] == ["pane", "list"]:
    panes = [{"pane_id":"source:p1", "label":"agent", "agent":"codex", "agent_status":"working"}]
    if os.environ.get("RETIRE_TEST_EXTRA_AGENT"): panes.append({"pane_id":"source:p2", "label":"agent", "agent":"claude", "agent_status":"working"})
    emit({"panes":panes})
elif args[:2] == ["workspace", "create"]:
    emit({"workspace":{"workspace_id":"cleanup-id"}, "root_pane":{"pane_id":"cleanup:p1"}})
elif args[:2] == ["tab", "create"]:
    emit({"root_pane":{"pane_id":"cleanup:p2"}})
elif args[:2] == ["pane", "run"]:
    emit({})
else:
    sys.exit(2)
`
	if err := os.WriteFile(filepath.Join(bin, "herdr"), []byte(fixture), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("RETIRE_TEST_LOG", log)
	t.Setenv("RETIRE_TEST_SOURCE_ID", sourceID)
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_SESSION", "fixture")
	t.Setenv("HERDR_WORKSPACE_ID", "source-id")
	t.Setenv("HERDR_PANE_ID", "source:p1")
	return c, log
}

func TestSelfRetireHandsOffToCleanupWorkspace(t *testing.T) {
	c, log := retireHandoffFixture(t, "source-id")
	if err := handoffSelfRetire(c, "finished", false, true); err != nil {
		t.Fatal(err)
	}
	calls, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	text := string(calls)
	if !strings.Contains(text, "workspace create --label --cleanup-- --cwd "+c.Workspace+" --no-focus") || !strings.Contains(text, "pane run cleanup:p1 ") || !strings.Contains(text, " retire-worker 'finished'") {
		t.Fatalf("cleanup handoff incomplete: %s", text)
	}
	if strings.Contains(text, "workspace close") {
		t.Fatalf("source workspace closed during handoff: %s", text)
	}
	if _, err := os.Stat(c.Collection); err != nil {
		t.Fatalf("handoff deleted the collection before worker ran: %v", err)
	}
}

func TestSelfRetireReusesCleanupWorkspaceAndRejectsWrongSource(t *testing.T) {
	c, log := retireHandoffFixture(t, "source-id")
	t.Setenv("RETIRE_TEST_REUSE", "1")
	if err := handoffSelfRetire(c, "finished", false, true); err != nil {
		t.Fatal(err)
	}
	calls, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(calls), "tab create --workspace cleanup-id --label finished") || !strings.Contains(string(calls), "pane run cleanup:p2 ") {
		t.Fatalf("existing cleanup workspace was not reused: %s", calls)
	}
	t.Setenv("RETIRE_TEST_SOURCE_ID", "some-other-workspace")
	if err := handoffSelfRetire(c, "finished", false, true); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("wrong source workspace was accepted: %v", err)
	}
	updated, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(updated), "pane run ") != 1 {
		t.Fatalf("wrong source scheduled another deletion: %s", updated)
	}
}

func TestSelfRetireRefusesToCloseAnotherAgent(t *testing.T) {
	c, log := retireHandoffFixture(t, "source-id")
	t.Setenv("RETIRE_TEST_EXTRA_AGENT", "1")
	if err := handoffSelfRetire(c, "finished", false, true); err == nil || !strings.Contains(err.Error(), "another agent") {
		t.Fatalf("other agent was not protected: %v", err)
	}
	calls, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(calls), "workspace create") || strings.Contains(string(calls), "pane run") {
		t.Fatalf("cleanup was scheduled despite another agent: %s", calls)
	}
}

func TestSelfRetireRequiresMatchingHerdrPane(t *testing.T) {
	c, log := retireHandoffFixture(t, "source-id")
	t.Setenv("HERDR_ENV", "")
	if err := handoffSelfRetire(c, "finished", false, true); err == nil || !strings.Contains(err.Error(), "requires its Herdr pane") {
		t.Fatalf("non-Herdr caller was accepted: %v", err)
	}
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_PANE_ID", "other-pane")
	if err := handoffSelfRetire(c, "finished", false, true); err == nil || !strings.Contains(err.Error(), "not in the target workspace") {
		t.Fatalf("unmatched pane was accepted: %v", err)
	}
	calls, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(calls), "workspace create") || strings.Contains(string(calls), "pane run") {
		t.Fatalf("invalid caller scheduled cleanup: %s", calls)
	}
}

func retireWorkerFixture(t *testing.T) (string, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "finished") // Same basename as the collection is valid.
	if err := os.MkdirAll(root, 0755); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("git", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	owner := filepath.Join(root, ".bare", "harness.git")
	if err := os.MkdirAll(filepath.Dir(owner), 0755); err != nil {
		t.Fatal(err)
	}
	git("init", "--bare", "--initial-branch=main", owner)
	seed := filepath.Join(root, "seed")
	git("clone", owner, seed)
	registry := "repos:\n  - name: harness\n    remote: https://github.com/example/harness.git\n    default_ref: main\n"
	if err := os.WriteFile(filepath.Join(seed, ".harness-repos.yml"), []byte(registry), 0644); err != nil {
		t.Fatal(err)
	}
	git("-C", seed, "add", ".harness-repos.yml")
	git("-C", seed, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "synthetic harness")
	git("-C", seed, "push", "origin", "HEAD:main")
	head := git("--git-dir="+owner, "rev-parse", "main")
	git("--git-dir="+owner, "update-ref", "refs/remotes/origin/main", head)
	target := filepath.Join(root, "finished")
	if err := os.MkdirAll(target, 0755); err != nil {
		t.Fatal(err)
	}
	git("--git-dir="+owner, "worktree", "add", "--detach", filepath.Join(target, "harness"), "main")
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0755); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(root, "worker-herdr.log")
	fixture := `#!/usr/bin/env python3
import json, os, sys
args = sys.argv[3:]
with open(os.environ["RETIRE_TEST_LOG"], "a") as out: out.write(" ".join(args) + "\n")
def emit(value): print(json.dumps({"result": value}))
if args == ["workspace", "list"]:
    cleanup_label = "wrong-label" if os.environ.get("RETIRE_TEST_BAD_LABEL") else "--cleanup--"
    emit({"workspaces":[{"label":"finished","workspace_id":"source-id"},{"label":cleanup_label,"workspace_id":"cleanup-id"}]})
elif args[:2] == ["pane", "list"]:
    with open(os.environ["RETIRE_TEST_LOG"]) as stream: count = sum(line.startswith("pane list ") for line in stream)
    status = "working" if os.environ.get("RETIRE_TEST_WORKING_FIRST") and count == 1 else "done"
    panes = [{"pane_id":"source:p1","agent":"codex","agent_status":status}]
    if os.environ.get("RETIRE_TEST_EXTRA_AGENT"): panes.append({"pane_id":"source:p2","agent":"claude","agent_status":"working"})
    emit({"panes":panes})
elif args[:2] == ["workspace", "close"]:
    if os.environ.get("RETIRE_TEST_CLOSE_FAIL"): sys.exit(3)
    if os.environ.get("RETIRE_TEST_DIRTY_ON_CLOSE"):
        with open(os.path.join(os.environ["RETIRE_TEST_TARGET"], "harness", "new-work.txt"), "w") as out: out.write("agent work\n")
    emit({})
else:
    sys.exit(2)
`
	if err := os.WriteFile(filepath.Join(bin, "herdr"), []byte(fixture), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("RETIRE_TEST_LOG", log)
	t.Setenv("RETIRE_TEST_TARGET", target)
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_SESSION", "fixture")
	t.Setenv("HERDR_WORKSPACE_ID", "cleanup-id")
	t.Setenv("WTC_RETIRE_SESSION", "fixture")
	t.Setenv("WTC_RETIRE_SOURCE_WORKSPACE", "source-id")
	t.Setenv("WTC_RETIRE_SOURCE_PANE", "source:p1")
	t.Setenv("WTC_RETIRE_TARGET", "finished")
	t.Setenv("HARNESS_HERDR_SESSION", "fixture")
	prior, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(prior) })
	return target, log
}

func TestRetireWorkerRejectsWrongIdentityBeforeDeletion(t *testing.T) {
	target, log := retireWorkerFixture(t)
	t.Setenv("WTC_RETIRE_TARGET", "another")
	if err := runRetireWorker("finished", false, false); err == nil || !strings.Contains(err.Error(), "not in its scheduled") {
		t.Fatalf("wrong worker identity was accepted: %v", err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("target was removed: %v", err)
	}
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Fatalf("worker contacted Herdr before identity validation: %v", err)
	}
}

func TestRetireWorkerRefusesNewAgent(t *testing.T) {
	target, log := retireWorkerFixture(t)
	t.Setenv("RETIRE_TEST_EXTRA_AGENT", "1")
	if err := runRetireWorker("finished", false, false); err == nil || !strings.Contains(err.Error(), "another agent appeared") {
		t.Fatalf("second agent was not protected: %v", err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("target was removed: %v", err)
	}
	calls, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(calls), "workspace close") {
		t.Fatalf("workspace closed despite second agent: %s", calls)
	}
}

func TestRetireWorkerClosesVerifiedWorkspaceAndKeepsBareOwner(t *testing.T) {
	target, log := retireWorkerFixture(t)
	if err := runRetireWorker("finished", false, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("target remains: %v", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(target), ".bare", "harness.git")); err != nil {
		t.Fatalf("bare owner was removed: %v", err)
	}
	calls, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(calls), "workspace close source-id") {
		t.Fatalf("verified workspace was not closed: %s", calls)
	}
}

func TestRetireWorkerStopsIfSourceWorkspaceCannotClose(t *testing.T) {
	target, log := retireWorkerFixture(t)
	t.Setenv("RETIRE_TEST_CLOSE_FAIL", "1")
	if err := runRetireWorker("finished", false, false); err == nil || !strings.Contains(err.Error(), "cannot close source workspace") {
		t.Fatalf("workspace close failure did not stop retirement: %v", err)
	}
	if _, err := os.Stat(filepath.Join(target, "harness")); err != nil {
		t.Fatalf("target was removed after workspace close failure: %v", err)
	}
	calls, err := os.ReadFile(log)
	if err != nil || !strings.Contains(string(calls), "workspace close source-id") {
		t.Fatalf("workspace close was not attempted: %q (%v)", calls, err)
	}
}

func TestRetireWorkerRechecksWorkAfterSourceWorkspaceCloses(t *testing.T) {
	target, log := retireWorkerFixture(t)
	t.Setenv("RETIRE_TEST_DIRTY_ON_CLOSE", "1")
	if err := runRetireWorker("finished", false, false); err == nil || !strings.Contains(err.Error(), "uncommitted changes") {
		t.Fatalf("new work after first preflight did not block retirement: %v", err)
	}
	if _, err := os.Stat(filepath.Join(target, "harness", "new-work.txt")); err != nil {
		t.Fatalf("new work was removed after the second preflight: %v", err)
	}
	calls, err := os.ReadFile(log)
	if err != nil || !strings.Contains(string(calls), "workspace close source-id") {
		t.Fatalf("workspace was not closed before the final preflight: %q (%v)", calls, err)
	}
}

func TestRetireWorkerWaitsForSourceAgent(t *testing.T) {
	target, log := retireWorkerFixture(t)
	t.Setenv("RETIRE_TEST_WORKING_FIRST", "1")
	if err := runRetireWorker("finished", false, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("target remains after agent finished: %v", err)
	}
	calls, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(calls), "pane list ") < 3 {
		t.Fatalf("worker did not wait for agent completion: %s", calls)
	}
}

func TestRetireWorkerRechecksIdentityAndPreflight(t *testing.T) {
	for _, scenario := range []string{"same-workspace", "wrong-label", "inside-target", "dirty-target"} {
		t.Run(scenario, func(t *testing.T) {
			target, log := retireWorkerFixture(t)
			switch scenario {
			case "same-workspace":
				t.Setenv("HERDR_WORKSPACE_ID", "source-id")
			case "wrong-label":
				t.Setenv("RETIRE_TEST_BAD_LABEL", "1")
			case "inside-target":
				if err := os.Chdir(target); err != nil {
					t.Fatal(err)
				}
			case "dirty-target":
				if err := os.WriteFile(filepath.Join(target, "harness", "untracked.txt"), []byte("keep"), 0644); err != nil {
					t.Fatal(err)
				}
			}
			if err := runRetireWorker("finished", false, false); err == nil {
				t.Fatal("changed context was accepted")
			}
			if _, err := os.Stat(target); err != nil {
				t.Fatalf("target was removed: %v", err)
			}
			if calls, err := os.ReadFile(log); err == nil && strings.Contains(string(calls), "workspace close") {
				t.Fatalf("workspace closed after refusal: %s", calls)
			}
		})
	}
}
