package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"taba.sh/taba/internal/wtc"
)

func TestHarnessCommandsWorkOutsideCollection(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "wtc")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %s: %v", out, err)
	}
	run := func(args ...string) (string, error) {
		cmd := exec.Command(binary, args...)
		cmd.Dir = root
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	if out, err := run("release-notes", "v0.1.38"); err != nil || !strings.HasPrefix(out, "# wtc 0.1.38\n") || !strings.Contains(out, "env.pre") {
		t.Fatal(out, err)
	}
	if out, err := run("release-notes", "--since", "0.1.35", "--json"); err != nil {
		t.Fatal(out, err)
	} else {
		var result struct {
			OK   bool              `json:"ok"`
			Data []wtc.ReleaseNote `json:"data"`
		}
		if err := json.Unmarshal([]byte(out), &result); err != nil || !result.OK || len(result.Data) < 3 || result.Data[0].Version != "0.1.36" {
			t.Fatal(out, err)
		}
		for _, note := range result.Data {
			if note.Version == "unreleased" || note.Content == "" {
				t.Fatal("invalid upgrade range", out)
			}
		}
	}
	if out, err := run("release-notes", "--since", "9.0.0", "--json"); err != nil || !strings.Contains(out, `"data":[]`) {
		t.Fatal(out, err)
	}
	if out, err := run("release-notes", "unreleased"); err != nil || !strings.HasPrefix(out, "# taba/wtc unreleased\n") {
		t.Fatal(out, err)
	}
	if out, err := run("release-notes", "--list", "--json"); err != nil || !strings.Contains(out, `"0.1.0"`) || !strings.Contains(out, `"unreleased"`) {
		t.Fatal(out, err)
	}
	for _, args := range [][]string{
		{"release-notes", "0.1.38", "--since", "0.1.35"},
		{"release-notes", "--since="},
		{"release-notes", "--list", "0.1.38"},
		{"release-notes", "--list", "--since", "0.1.35"},
		{"release-notes", "../../etc/passwd"},
	} {
		if out, err := run(args...); err == nil {
			t.Fatalf("expected error for %v: %s", args, out)
		}
	}
	if out, err := run("docs", "instructions/publication-privacy.md"); err != nil || !strings.Contains(out, "Publication privacy") {
		t.Fatal(out, err)
	}
	if out, err := run("docs", "../../../etc/passwd"); err == nil || !strings.Contains(out, "unknown embedded document") {
		t.Fatal(out, err)
	}
	if out, err := run("harness", "init", "harness", "--remote", "https://example.invalid/harness.git"); err == nil || !strings.Contains(out, "cli-version") {
		t.Fatal(out, err)
	}
	if _, err := os.Stat(filepath.Join(root, "harness")); !os.IsNotExist(err) {
		t.Fatal("invalid command wrote scaffold")
	}
	if out, err := run("harness", "init", "harness", "--remote", "https://example.invalid/harness.git", "--cli-version", "0.1.38"); err != nil || !strings.Contains(out, "created harness") {
		t.Fatal(out, err)
	}
}
