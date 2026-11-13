package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"taba.sh/taba/internal/wtc"
)

func TestDoctorAllReportsConventionsFromTheWorkspaceRoot(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "taba")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %s: %v", out, err)
	}
	workspace := filepath.Join(root, "workspace")
	write := func(path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	registry := "repos:\n  - name: harness\n    remote: https://example.invalid/harness.git\n    default_ref: origin/main\n"
	write(filepath.Join(workspace, "old", "harness", ".harness-repos.yml"), registry)
	write(filepath.Join(workspace, "old", "harness", "wtc.toml"), "")
	write(filepath.Join(workspace, "old", "WTC-SCOPE.md"), "task\n")
	write(filepath.Join(workspace, "moved", "harness", ".harness-repos.yml"), registry)
	write(filepath.Join(workspace, "moved", "harness", "taba.toml"), "")
	write(filepath.Join(workspace, "moved", "SCOPE.md"), "task\n")

	run := func(dir string, args ...string) (string, error) {
		cmd := exec.Command(binary, args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "XDG_CONFIG_HOME="+filepath.Join(root, "xdg-config"), "XDG_DATA_HOME="+filepath.Join(root, "xdg-data"), "WTC_CONFIG_ROOT="+filepath.Join(root, "control"), "TABA_CONFIG_ROOT="+filepath.Join(root, "control"))
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	for _, dir := range []string{workspace, filepath.Join(workspace, "moved")} {
		out, err := run(dir, "doctor", "--all")
		if err != nil {
			t.Fatalf("from %s: %s: %v", dir, out, err)
		}
		for _, want := range []string{"harness-config", "old/WTC-SCOPE.md", "moved: migrated", "2 collection(s): 1 with nothing left to migrate by hand, 1 with wtc conventions to migrate"} {
			if !strings.Contains(out, want) {
				t.Fatalf("from %s: missing %q in:\n%s", dir, want, out)
			}
		}
	}
	out, err := run(workspace, "doctor", "--all", "--json")
	if err != nil {
		t.Fatal(out, err)
	}
	var result struct {
		Data struct {
			Collections []struct {
				Collection string                  `json:"collection"`
				Findings   []wtc.ConventionFinding `json:"findings"`
			} `json:"collections"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil || len(result.Data.Collections) != 2 {
		t.Fatalf("json: %v\n%s", err, out)
	}
	if out, err := run(root, "doctor", "--all"); err == nil {
		t.Fatalf("outside a workspace doctor --all should fail:\n%s", out)
	}

	// The command list doctor looks for after `wtc` follows the CLI.
	out, err = run(root, "commands", "--json")
	if err != nil {
		t.Fatal(out, err)
	}
	var commands struct {
		Data []struct {
			Name string `json:"name"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &commands); err != nil {
		t.Fatal(err)
	}
	known := map[string]bool{}
	for _, name := range wtc.ConventionCommands {
		known[name] = true
	}
	var missing []string
	for _, c := range commands.Data {
		if !known[c.Name] {
			missing = append(missing, c.Name)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("wtc.ConventionCommands misses %v", missing)
	}
}
