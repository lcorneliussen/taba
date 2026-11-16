package main

import (
	"encoding/json"
	"io"
	"os"
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
