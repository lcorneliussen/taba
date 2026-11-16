package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"taba.sh/taba/internal/openclaw"
	"taba.sh/taba/internal/wtc"
)

func TestRepositoryBaseGatewayKeepsPublicationSeparate(t *testing.T) {
	state, _ := harnessGateway(t)
	withGatewayConfig(t, `{"harnesses":[{"name":"platform","remote":"https://github.com/example-org/platform-harness.git","ref":"main"}]}`)
	folder := openclaw.NewCollectionFolder(state, "staging", "platform")
	if err := wtc.RecordGatewayHarness(folder, "platform", "https://github.com/example-org/platform-harness.git"); err != nil {
		t.Fatal(err)
	}
	c, err := wtc.LinkGatewayCollection(folder, []wtc.LinkedRepo{{Name: "api", Remote: "https://github.com/example-org/api.git", DefaultRef: "origin/develop", Path: filepath.Join(state, "repos", "api")}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.SetRepositoryBase("api", "beta", "PRIVATE_LOCAL_REASON_CANARY"); err != nil {
		t.Fatal(err)
	}
	project := filepath.Join(state, "repos", "api")
	if _, _, err := harnessRepos([]string{"api"}, "staging", "", "platform", false, false); err == nil || !strings.Contains(err.Error(), "publication destination") {
		t.Fatalf("mismatched publication record allowed: %v", err)
	}
	if openclaw.RecordedBase(project, "staging") != "" {
		t.Fatal("local override persisted to gateway record")
	}
	if err := openclaw.RecordBase(project, "staging", "beta"); err != nil {
		t.Fatal(err)
	}
	adds, _, err := harnessRepos([]string{"api"}, "staging", "", "platform", false, false)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, add := range adds {
		if strings.Contains(add.arg, "api.git") {
			found = true
			if add.branch != "beta" {
				t.Fatal("local override ignored")
			}
		}
	}
	if !found || openclaw.RecordedBase(project, "staging") != "beta" {
		t.Fatal("existing authorized destination changed")
	}
	if _, _, err := harnessRepos([]string{"api"}, "staging", "", "platform", false, true); err == nil {
		t.Fatal("worker publication authority inferred from local override")
	}
	// The private file remains receiving-local, with no new metadata transport.
	projects, err := localGatewayProjects([]string{"platform"}, "staging")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range projects {
		for _, repo := range p.Repos {
			if repo.Name == "api" && repo.DefaultRef != "origin/develop" {
				t.Fatal("project default_ref overwritten by local base")
			}
		}
	}
}

func TestRepositoryBaseBrowseUsesServingExecutable(t *testing.T) {
	root := t.TempDir()
	collection := filepath.Join(root, "receiver")
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(collection, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(root, "arguments")
	program := "#!/bin/sh\nif [ \"$1\" = --server ];then exit 1;fi\nprintf '%s\\n' \"$@\" > \"$BASE_BROWSE_LOG\"\n"
	if err := os.WriteFile(filepath.Join(bin, "nvim"), []byte(program), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BASE_BROWSE_LOG", log)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	if err := browseHere(&wtc.Context{Collection: collection, Workspace: root}); err != nil {
		t.Fatal(err)
	}
	args, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(args), "vim.g.wtc_base_command = ") || !strings.Contains(string(args), executable) {
		t.Fatal("browse can dispatch to an unrelated installed CLI")
	}
	view, err := wtc.ReadDefault("browse/wtc-browse.lua")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(view), "default_ref:%") || !strings.Contains(string(view), `"repo", "base", "show"`) {
		t.Fatal("browse retained an independent registry parser")
	}
}

func TestRepositoryBaseSandboxPreservesRecordedAuthorityWithoutLocalReads(t *testing.T) {
	for _, destination := range []string{"develop", "beta"} {
		t.Run(destination, func(t *testing.T) {
			body := strings.Replace(sandboxMetadata, `"destination":"develop"`, `"destination":"`+destination+`"`, 1)
			sandboxMetadataServer(t, 200, body)
			state := t.TempDir()
			folder := filepath.Join(state, "wtc", "platform", "42-fix", ".wtc")
			if err := os.MkdirAll(folder, 0700); err != nil {
				t.Fatal(err)
			}
			poison := filepath.Join(folder, "repo-bases.toml")
			data := []byte("PRIVATE_INACCESSIBLE_REASON_CANARY malformed local file")
			if err := os.WriteFile(poison, data, 0000); err != nil {
				t.Fatal(err)
			}
			t.Setenv("OPENCLAW_STATE_DIR", state)
			plan, err := addRepoPlan(t, "api", "--worktree", "42-fix", "--separate")
			if err != nil {
				t.Fatal(err)
			}
			if len(plan) != 2 || plan[1].Args.WorktreeBaseRef != "origin/"+destination {
				t.Fatalf("recorded destination ignored: %+v", plan)
			}
			payload, err := json.Marshal(plan)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(payload), "PRIVATE_INACCESSIBLE") || strings.Contains(string(payload), state) {
				t.Fatal("local state leaked into sandbox intent")
			}
			info, err := os.Stat(poison)
			if err != nil || info.Mode().Perm() != 0 {
				t.Fatal("sandbox changed inaccessible local state")
			}
			if err := os.Chmod(poison, 0600); err != nil {
				t.Fatal(err)
			}
			after, err := os.ReadFile(poison)
			if err := os.Chmod(poison, 0000); err != nil {
				t.Fatal(err)
			}
			if err != nil || !bytes.Equal(after, data) {
				t.Fatal("sandbox changed local state")
			}
			if _, err := addRepoPlan(t, "api", "--worktree", "42-fix", "--base", destination, "--separate"); err != nil {
				t.Fatalf("matching explicit base refused: %v", err)
			}
			if _, err := addRepoPlan(t, "api", "--worktree", "42-fix", "--base", "different", "--separate"); err == nil {
				t.Fatal("different explicit base admitted")
			}
		})
	}
}
