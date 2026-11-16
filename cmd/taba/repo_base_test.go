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

func repositoryBaseCLI(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var asJSON bool
	root := &cobra.Command{Use: "taba", SilenceErrors: true, SilenceUsage: true}
	root.PersistentFlags().BoolVar(&asJSON, "json", false, "")
	addRepoBaseCommands(root, &asJSON)
	root.SetArgs(args)
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	previous := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	runErr := root.Execute()
	os.Stdout = previous
	w.Close()
	out, _ := io.ReadAll(r)
	r.Close()
	return string(out), runErr
}
func TestRepositoryBaseCommandsUseExplicitReceiver(t *testing.T) {
	source, receiver := filepath.Join(t.TempDir(), "source"), filepath.Join(t.TempDir(), "receiver")
	for _, collection := range []string{source, receiver} {
		if err := os.MkdirAll(filepath.Join(collection, "harness"), 0700); err != nil {
			t.Fatal(err)
		}
		body := "repos:\n  - name: widget\n    aliases: [legacy]\n    remote: https://example.invalid/widget.git\n    default_ref: origin/main\n"
		if err := os.WriteFile(filepath.Join(collection, "harness", ".harness-repos.yml"), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(source)
	for _, step := range []struct {
		args []string
		want string
	}{
		{[]string{"repo", "base", "set", "legacy", "beta", "--reason", "Feature staging", "--collection", receiver, "--json"}, "origin/beta"},
		{[]string{"repo", "base", "show", "widget", "--collection", receiver, "--json"}, "origin/beta"},
		{[]string{"repo", "base", "show", "widget", "--json"}, "origin/main"},
		{[]string{"repo", "base", "clear", "legacy", "--collection", receiver, "--json"}, "origin/main"},
	} {
		out, err := repositoryBaseCLI(t, step.args...)
		if err != nil {
			t.Fatalf("%v: %s %v", step.args, out, err)
		}
		var result struct {
			Data struct {
				EffectiveRef string `json:"effective_ref"`
			}
		}
		if json.Unmarshal([]byte(out), &result) != nil || result.Data.EffectiveRef != step.want {
			t.Fatalf("wrong result: %s", out)
		}
	}
	if _, err := repositoryBaseCLI(t, "repo", "base", "set", "widget", "beta"); err == nil {
		t.Fatal("missing reason accepted")
	}
	out, err := repositoryBaseCLI(t, "repo", "base", "set", "widget", "beta", "--reason", "Feature staging")
	if err != nil || !strings.Contains(out, `reason: "Feature staging"`) || !strings.Contains(out, "checkout unchanged") {
		t.Fatalf("text output: %s %v", out, err)
	}
}

func TestRepositoryBaseCommandOrphanOutcomeHasNoResolution(t *testing.T) {
	collection := filepath.Join(t.TempDir(), "receiver")
	if err := os.MkdirAll(filepath.Join(collection, "harness"), 0700); err != nil {
		t.Fatal(err)
	}
	registry := filepath.Join(collection, "harness", ".harness-repos.yml")
	original := "repos:\n  - name: widget\n    remote: https://example.invalid/widget.git\n    default_ref: origin/main\n"
	if err := os.WriteFile(registry, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(collection)
	if _, err := repositoryBaseCLI(t, "repo", "base", "set", "widget", "beta", "--reason", "Staging work"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(registry, []byte(strings.ReplaceAll(original, "widget", "renamed")), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := repositoryBaseCLI(t, "repo", "base", "show", "widget"); err == nil || !strings.Contains(err.Error(), "clear widget") {
		t.Fatalf("missing recovery instruction: %v", err)
	}
	out, err := repositoryBaseCLI(t, "repo", "base", "clear", "widget", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var result struct{ Data map[string]any }
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(err)
	}
	if result.Data["cleared"] != true || result.Data["orphaned"] != true || result.Data["exact_key"] != "widget" {
		t.Fatalf("wrong cleanup outcome: %s", out)
	}
	for _, key := range []string{"resolution", "effective_ref", "source"} {
		if _, exists := result.Data[key]; exists {
			t.Fatalf("fabricated resolution in cleanup: %s", out)
		}
	}
	if _, err := repositoryBaseCLI(t, "repo", "base", "clear", "widget"); err == nil {
		t.Fatal("unknown unstored key cleared")
	}
}

func TestRepositoryBaseCommandsKeepReusedAliasOrphanSeparate(t *testing.T) {
	collection := t.TempDir()
	if err := os.MkdirAll(filepath.Join(collection, "harness"), 0700); err != nil {
		t.Fatal(err)
	}
	registry := "repos:\n  - name: replacement\n    aliases: [widget]\n    remote: https://example.invalid/replacement.git\n    default_ref: origin/main\n"
	if err := os.WriteFile(filepath.Join(collection, "harness", ".harness-repos.yml"), []byte(registry), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(collection, ".wtc"), 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(collection, ".wtc", "repo-bases.toml")
	if err := os.WriteFile(file, []byte("schema_version=1\n[repos.widget]\nbase='beta'\nreason='Old canonical owner'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "init", "-q", "-b", "main", filepath.Join(collection, "widget")).CombinedOutput(); err != nil {
		t.Fatalf("synthetic worktree: %s %v", out, err)
	}
	call := func(args ...string) map[string]any {
		t.Helper()
		args = append(args, "--collection", collection, "--json")
		out, err := repositoryBaseCLI(t, args...)
		if err != nil {
			t.Fatalf("%v: %s %v", args, out, err)
		}
		var result struct {
			Data map[string]any `json:"data"`
		}
		if err := json.Unmarshal([]byte(out), &result); err != nil {
			t.Fatal(err)
		}
		return result.Data
	}
	for _, selector := range []string{"widget", "replacement"} {
		if _, err := repositoryBaseCLI(t, "repo", "base", "show", selector, "--collection", collection); err == nil || !strings.Contains(err.Error(), "clear widget") {
			t.Fatalf("CLI hid retained alias intent for %s: %v", selector, err)
		}
	}
	base := call("repo", "base", "set", "replacement", "gamma", "--reason", "Explicit new owner")
	if base["repository"] != "replacement" || base["effective_ref"] != "origin/gamma" {
		t.Fatalf("explicit alias did not select current canonical owner: %+v", base)
	}
	base = call("repo", "base", "show", "widget", "--unmanaged")
	if base["effective_ref"] != "origin/HEAD" || base["source"] != "fallback" || base["reason"] != nil {
		t.Fatalf("CLI browse applied orphan or alias owner override: %+v", base)
	}
	before, _ := os.ReadFile(file)
	if _, err := repositoryBaseCLI(t, "repo", "base", "clear", "replacement", "--collection", collection); err == nil || !strings.Contains(err.Error(), "clear widget") {
		t.Fatalf("CLI canonical clear hid retained related intent: %v", err)
	}
	after, _ := os.ReadFile(file)
	if string(before) != string(after) {
		t.Fatal("CLI refused canonical clear mutated store")
	}
	cleared := call("repo", "base", "clear", "widget")
	if cleared["orphaned"] != true || cleared["exact_key"] != "widget" || cleared["resolution"] != nil {
		t.Fatalf("CLI orphan cleanup followed alias: %+v", cleared)
	}
	base = call("repo", "base", "show", "replacement")
	if base["effective_ref"] != "origin/gamma" || base["reason"] != "Explicit new owner" {
		t.Fatalf("CLI orphan cleanup lost current intent: %+v", base)
	}
	if err := os.WriteFile(file, []byte("schema_version=2\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := repositoryBaseCLI(t, "repo", "base", "show", "widget", "--unmanaged", "--collection", collection); err == nil {
		t.Fatal("CLI unmanaged fallback bypassed corrupt store")
	}
}
