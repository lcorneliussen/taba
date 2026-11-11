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
)

// Runs the runtime command layer in-process from a synthetic collection.
func runtimeCLI(t *testing.T, collection string, args ...string) (string, error) {
	t.Helper()
	t.Chdir(collection)
	var asJSON bool
	root := &cobra.Command{Use: "wtc", SilenceUsage: true, SilenceErrors: true}
	root.PersistentFlags().BoolVar(&asJSON, "json", false, "")
	addRuntimeCommands(root, &asJSON)
	root.SetArgs(args)
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	stdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	runErr := root.Execute()
	os.Stdout = stdout
	w.Close()
	out, _ := io.ReadAll(r)
	return string(out), runErr
}

func TestRuntimeCommandsAreReadOnlyUntilAsked(t *testing.T) {
	collection := filepath.Join(t.TempDir(), "demo")
	write := func(path, body string) {
		t.Helper()
		path = filepath.Join(collection, path)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write("harness/.harness-repos.yml", "repos:\n  - name: harness-example\n    remote: https://example.invalid/harness-example.git\n    default_ref: origin/main\n  - name: widget\n    remote: https://example.invalid/widget.git\n    default_ref: origin/main\n")
	write("harness/wtc.toml", "[runtime]\nbackend = \"dekit\"\n")
	write("widget/.harness/dekit.tasks.yaml", "tasks:\n web/server:\n  cmd: [sh, -c, 'sleep 60']\n web/tunnel:\n  cmd: [sh, -c, 'sleep 60']\n  deps: [web/server]\n  tags: [endpoint]\n")
	for dir, remote := range map[string]string{"harness": "harness-example", "widget": "widget"} {
		for _, argv := range [][]string{{"init", "-q", "-b", "main"}, {"remote", "add", "origin", "https://example.invalid/" + remote + ".git"}} {
			if out, err := exec.Command("git", append([]string{"-C", filepath.Join(collection, dir)}, argv...)...).CombinedOutput(); err != nil {
				t.Fatalf("git %v: %s", argv, out)
			}
		}
	}

	out, err := runtimeCLI(t, collection, "runtime", "render", "--dry-run", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var rendered struct {
		OK   bool `json:"ok"`
		Data struct {
			Items []struct{ Path, Kind string }
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &rendered); err != nil || !rendered.OK || len(rendered.Data.Items) != 2 || rendered.Data.Items[1].Path != "widget/web/tunnel" || rendered.Data.Items[1].Kind != "endpoint" {
		t.Fatalf("bad dry-run output: %s %v", out, err)
	}
	if out, err := runtimeCLI(t, collection, "runtime", "render", "--dry-run"); err != nil || !strings.Contains(out, "widget/web") {
		t.Fatalf("bad text preview: %q %v", out, err)
	}
	out, err = runtimeCLI(t, collection, "runtime", "status", "--json")
	var items []struct{ Path, State string }
	if err != nil || json.Unmarshal([]byte(out), &items) != nil || len(items) != 2 || items[0].Path != "widget/web/server" || items[0].State != "unconfigured" || items[1].State != "unconfigured" {
		t.Fatalf("status without a runtime: %q %v", out, err)
	}
	for _, args := range [][]string{{"svc"}, {"svc", "status"}, {"svc", "ls"}, {"svc", "list"}} {
		if out, err := runtimeCLI(t, collection, args...); err != nil || !strings.Contains(out, "unconfigured") {
			t.Fatalf("declarations missing from %v: %q %v", args, out, err)
		}
	}
	for _, args := range [][]string{
		{"logs", "widget/web/server"},
		{"logs", "widget/web/server", "--json"},
		{"logs", "widget/web/server", "--tail", "-1"},
		{"logs"},
		{"up", "--wait", "-1s"},
		{"up", "widget/web", "extra"},
		{"down", "--wait", "1s"},
		{"runtime", "render", "extra"},
	} {
		if _, err := runtimeCLI(t, collection, args...); err == nil {
			t.Errorf("accepted %v", args)
		}
	}
	if _, err := os.Lstat(filepath.Join(collection, ".wtc", "runtime")); !os.IsNotExist(err) {
		t.Fatal("read-only and rejected commands created a runtime")
	}
}

func TestSvcCommandArguments(t *testing.T) {
	dir := t.TempDir()
	if _, err := runtimeCLI(t, dir, "svc", "add", "web", "npm", "run", "dev"); err == nil || !strings.Contains(err.Error(), "--") {
		t.Fatalf("add without -- before the command: %v", err)
	}
	if _, err := runtimeCLI(t, dir, "svc", "add", "web"); err == nil {
		t.Fatal("add without a command")
	}
	if _, err := runtimeCLI(t, dir, "svc", "tunnel"); err == nil {
		t.Fatal("tunnel without a service")
	}
	if _, err := runtimeCLI(t, dir, "svc", "tunnel", "web", "tailscale", "extra"); err == nil {
		t.Fatal("tunnel with too many arguments")
	}
	if _, err := runtimeCLI(t, dir, "services", "dekit", "--", "--help"); err != nil {
		t.Fatalf("dekit help through the alias: %v", err)
	}
	for _, name := range []string{"up", "down", "restart", "logs", "add", "rm", "tunnel", "dekit", "status"} {
		if _, err := runtimeCLI(t, dir, "svc", name, "--help"); err != nil {
			t.Errorf("svc %s --help: %v", name, err)
		}
	}
}
