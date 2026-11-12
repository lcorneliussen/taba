package main

import (
	"encoding/json"
	"github.com/spf13/cobra"
	"io"
	"os"
	"path/filepath"
	"taba.sh/taba/internal/wtc"
	"testing"
)

func TestNativePublicationCommandRequiresReceiptAndCollection(t *testing.T) {
	root := &cobra.Command{Use: "openclaw"}
	addOpenClawPublicationCommand(root)
	root.SetArgs([]string{"enlist-publication", "--url", "https://github.com/example/widget/pull/7"})
	if err := root.Execute(); err == nil {
		t.Fatal("accepted incomplete receipt")
	}
}

func TestNativePublicationRuntimeAssetsAreInstalled(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "plugin")
	if err := writeOpenClawPlugin(dir, "test"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"publication.js", "dist/control-ui/v1/pr-preview.js", "dist/control-ui/v1/vendor/marked.esm.js", "dist/control-ui/v1/vendor/marked.LICENSE"} {
		if data, err := os.ReadFile(filepath.Join(dir, name)); err != nil || len(data) == 0 {
			t.Fatalf("runtime asset %s: %v", name, err)
		}
	}
	data, err := os.ReadFile(filepath.Join(dir, "openclaw.plugin.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Contracts struct {
			Dispatch []string `json:"gatewayMethodDispatch"`
		} `json:"contracts"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil || len(manifest.Contracts.Dispatch) != 1 || manifest.Contracts.Dispatch[0] != "authenticated-request" {
		t.Fatalf("dispatch contract missing: %+v %v", manifest, err)
	}
}

func TestNativePublicationCommandEnlistsWithoutHooks(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	checkout := filepath.Join(t.TempDir(), "checkout")
	if err := os.MkdirAll(checkout, 0700); err != nil {
		t.Fatal(err)
	}
	openReviewTestGit(t, checkout, "init", "-q", "-b", "topic")
	openReviewTestGit(t, checkout, "commit", "--allow-empty", "-qm", "Synthetic base")
	openReviewTestGit(t, checkout, "remote", "add", "origin", "https://github.com/example/widget.git")
	folder := filepath.Join(t.TempDir(), "collection")
	c, err := wtc.LinkGatewayCollection(folder, []wtc.LinkedRepo{{Name: "widget", Remote: "https://github.com/example/widget.git", DefaultRef: "origin/main", Path: checkout}})
	if err != nil {
		t.Fatal(err)
	}
	hooks := filepath.Join(c.Harness, "hooks", "wtc")
	if err := os.MkdirAll(hooks, 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "hook-ran")
	t.Setenv("TABA_TEST_HOOK_MARKER", marker)
	for _, name := range []string{"pr.enlist.pre.sh", "pr.enlist.post.sh"} {
		if err := os.WriteFile(filepath.Join(hooks, name), []byte("#!/bin/sh\nprintf invoked > \"$TABA_TEST_HOOK_MARKER\"\nexit 1\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	root := &cobra.Command{Use: "taba"}
	addOpenClawCommands(root)
	root.SetArgs([]string{"openclaw", "enlist-publication", "--collection", folder, "--worktree", checkout, "--url", "https://github.com/example/widget/pull/7", "--branch", "published/topic"})
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout := os.Stdout
	os.Stdout = writer
	defer func() { os.Stdout = stdout; reader.Close(); writer.Close() }()
	err = root.Execute()
	writer.Close()
	os.Stdout = stdout
	data, readErr := io.ReadAll(reader)
	if err != nil || readErr != nil {
		t.Fatalf("enlist command: %v %v", err, readErr)
	}
	var result struct {
		OK   bool         `json:"ok"`
		Data wtc.PRRecord `json:"data"`
	}
	if err := json.Unmarshal(data, &result); err != nil || !result.OK || result.Data.Repo != "widget" || result.Data.Number != "7" || result.Data.Branch != "published/topic" {
		t.Fatalf("publication envelope: %s %v", data, err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("publication tracking ran a collection hook")
	}
	records, err := c.ListPRs()
	if err != nil || len(records) != 1 || records[0].URL != result.Data.URL {
		t.Fatalf("registry result: %+v %v", records, err)
	}
}
