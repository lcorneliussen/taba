package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"taba.sh/taba/internal/openclaw"
	"taba.sh/taba/internal/wtc"
)

func TestOpenClawRenameOldAndNewCollectionsReuseProject(t *testing.T) {
	state, git := harnessGateway(t)
	withGatewayConfig(t, `{"harnesses":[{"name":"platform","remote":"https://github.com/example-org/platform-harness.git"}]}`)
	root := filepath.Join(state, "repos", "api")
	git(root, "remote", "set-url", "origin", "https://github.com/example-org/service-api.git")
	aliasRecord, _ := json.Marshal(map[string]any{"version": 1, "origin": "https://github.com/example-org/service-api.git", "repositoryId": "verified-test-id", "aliases": []string{"https://github.com/example-org/api.git"}})
	if err := os.WriteFile(filepath.Join(root, ".git", "wtc-origin-aliases.json"), aliasRecord, 0o600); err != nil {
		t.Fatal(err)
	}
	// The older conversation keeps a worktree of its older registry and repository.
	oldHarness := filepath.Join(state, "worktrees", "harness", "old-task")
	oldRepo := filepath.Join(state, "worktrees", "api", "old-task")
	git(filepath.Join(state, "repos", "platform-harness"), "worktree", "add", "-q", "-b", "openclaw/old-task", oldHarness, "main")
	git(root, "worktree", "add", "-q", "-b", "openclaw/old-task", oldRepo, "main")
	before := git(oldRepo, "rev-parse", "HEAD")
	// A newer registry uses the new logical name and explicitly retains the old one.
	harness := filepath.Join(state, "repos", "platform-harness")
	registry := `schema_version: 1
repos:
  - name: service-api
    aliases: [api]
    remote: https://github.com/example-org/service-api.git
    default_ref: origin/develop
`
	if err := os.WriteFile(filepath.Join(harness, ".harness-repos.yml"), []byte(registry), 0o644); err != nil {
		t.Fatal(err)
	}
	git(harness, "add", ".")
	git(harness, "commit", "-qm", "rename registry")
	git(harness, "update-ref", "refs/remotes/origin/main", "HEAD")
	for _, task := range []string{"old-task", "new-task"} {
		for i, input := range []string{"api", "service-api", "https://github.com/example-org/api.git", "git@github.com:example-org/service-api.git"} {
			t.Run(task+"/"+input, func(t *testing.T) {
				args := []string{input, "--worktree", task}
				if i == 3 && task == "old-task" {
					args = append(args, "--worker", "review", "--work", "Review only")
				}
				plan, err := addRepoPlan(t, args...)
				if err != nil {
					t.Fatal(err)
				}
				var got *spawned
				for j := range plan {
					if plan[j].Args.ProjectID == "api" {
						got = &plan[j]
					}
				}
				if got == nil {
					t.Fatalf("no stable api project: %s", summary(plan))
				}
				if got.Args.ProjectGitURL != "" {
					t.Fatalf("rename requested a second clone: %+v", got)
				}
				if !strings.HasSuffix(got.Args.Label, " api") {
					t.Fatalf("collection label lost stable project identity: %s", got.Args.Label)
				}
			})
		}
	}
	if git(oldRepo, "rev-parse", "HEAD") != before || git(oldRepo, "status", "--porcelain") != "" {
		t.Fatal("older conversation's checkout changed")
	}
	// Each sandbox sees only its permitted metadata, never the gateway filesystem.
	snapshots := map[string]string{}
	for _, task := range []string{"old-task", "new-task"} {
		projects, err := localGatewayProjects([]string{"platform"}, task)
		if err != nil {
			t.Fatal(err)
		}
		data, _ := json.Marshal(openclaw.SandboxProjects{Projects: projects, Collection: task, CollectionProject: "platform"})
		if strings.Contains(string(data), state) || strings.Contains(string(data), "verified-test-id") {
			t.Fatal("private clone metadata escaped into discovery")
		}
		snapshots[task] = string(data)
	}
	for task, snapshot := range snapshots {
		t.Run("sandbox/"+task, func(t *testing.T) {
			sandboxMetadataServer(t, 200, snapshot)
			for _, input := range []string{"api", "service-api", "https://github.com/example-org/api.git", "git@github.com:example-org/service-api.git"} {
				plan, err := addRepoPlan(t, input, "--worktree", task)
				if err != nil {
					t.Fatalf("%s: %v", input, err)
				}
				found := false
				for _, in := range plan {
					if in.Args.ProjectID == "api" && in.Args.ProjectGitURL == "" {
						found = true
					}
				}
				if !found {
					t.Fatalf("%s lost stable project: %s", input, summary(plan))
				}
			}
			if _, err := addRepoPlan(t, "https://github.com/another-org/api.git", "--worktree", task); err == nil {
				t.Fatal("alias granted an unrelated repository")
			}
		})
	}
}

func TestOpenClawRegistryAliasResolutionDoesNotGuessOtherOwners(t *testing.T) {
	r := harnessRegistry{repos: []wtc.Repo{{Name: "service", Aliases: []string{"api"}, Remote: "https://github.com/example-org/service.git"}}}
	if got, ok := r.find("api"); !ok || got.Name != "service" {
		t.Fatal("declared alias not found")
	}
	if _, ok := r.findRemote("https://github.com/another-org/api"); ok {
		t.Fatal("alias matched another owner")
	}
	if _, ok := r.findRemote("https://bitbucket.org/example-org/api"); ok {
		t.Fatal("alias matched another forge")
	}
	r.repos = append(r.repos, wtc.Repo{Name: "api", Remote: "https://github.com/example-org/different.git"})
	if got, ok := r.find("api"); !ok || got.Name != "api" {
		t.Fatal("alias shadowed explicit registry name")
	}
}

func TestOpenClawRemoteAliasesRequireVerifiedIdentityAndPreferExact(t *testing.T) {
	// A logical alias is useful as a short name but grants no forge identity.
	t.Setenv(openclaw.GatewayGitEnv, "http://gateway.invalid")
	r := harnessRegistry{repos: []wtc.Repo{{Name: "service", Aliases: []string{"api"}, Remote: "https://github.com/example-org/service.git"}}}
	oldURL := "https://github.com/example-org/api.git"
	if _, ok := r.findRemote(oldURL); ok {
		t.Fatal("logical alias granted an unverified URL")
	}
	r.originAliases = map[string][]string{"service": {oldURL}}
	if got, ok := r.findRemote(oldURL); !ok || got.Name != "service" {
		t.Fatal("verified origin alias not resolved")
	}
	r.repos = append(r.repos, wtc.Repo{Name: "legacy", Remote: oldURL})
	if got, ok := r.findRemote(oldURL); !ok || got.Name != "legacy" {
		t.Fatal("verified alias shadowed an exact registered URL")
	}
	r.repos = append(r.repos, wtc.Repo{Name: "duplicate", Remote: oldURL})
	if _, ok := r.findRemote(oldURL); ok {
		t.Fatal("duplicate exact remotes were guessed")
	}
}

func TestOpenClawSandboxRetainsDeclaredAliasAndRegisteredURLPrecedence(t *testing.T) {
	state, g := harnessGateway(t)
	withGatewayConfig(t, `{"harnesses":[{"name":"platform","remote":"https://github.com/example-org/platform-harness.git"}]}`)
	harness := filepath.Join(state, "repos", "platform-harness")
	registry := `schema_version: 1
repos:
  - name: service
    aliases: [old-api]
    remote: https://github.com/example-org/service.git
  - name: backend
    remote: https://github.com/example-org/old-api.git
  - name: old-entry
    remote: https://github.com/example-org/api.git
  - name: current-entry
    remote: https://github.com/example-org/service-api.git
`
	if err := os.WriteFile(filepath.Join(harness, ".harness-repos.yml"), []byte(registry), 0o644); err != nil {
		t.Fatal(err)
	}
	g(harness, "add", ".")
	g(harness, "commit", "-qm", "registry collisions")
	g(harness, "update-ref", "refs/remotes/origin/main", "HEAD")
	root := filepath.Join(state, "repos", "api")
	g(root, "remote", "set-url", "origin", "https://github.com/example-org/service-api.git")
	data, _ := json.Marshal(map[string]any{"version": 1, "origin": "https://github.com/example-org/service-api.git", "repositoryId": "test-id", "aliases": []string{"https://github.com/example-org/api.git"}})
	if err := os.WriteFile(filepath.Join(root, ".git", "wtc-origin-aliases.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	h := openclaw.Harness{Name: "platform", Remote: "https://github.com/example-org/platform-harness.git", Ref: "main"}
	local := readHarnessRegistry(h, "alias-task")
	p := sandboxProjectMetadata(gatewayProject{Name: h.Name, Harness: h.Remote, Ref: h.Ref}, h, local, "alias-task")
	var snapshot openclaw.SandboxProjects
	snapshot.Projects = []openclaw.DiscoveryProject{p}
	snapshot.Collection = "alias-task"
	snapshot.CollectionProject = "platform"
	body, _ := json.Marshal(snapshot)
	sandboxMetadataServer(t, 200, string(body))
	_, regs, _, err := harnessProjectContext("alias-task")
	if err != nil {
		t.Fatal(err)
	}
	projected := regs["platform"]
	for _, r := range []harnessRegistry{local, projected} {
		if got, ok := r.find("old-api"); !ok || got.Name != "service" {
			t.Fatalf("inferred name shadowed declared alias: %+v %v", got, ok)
		}
		for remote, want := range map[string]string{"https://github.com/example-org/api.git": "old-entry", "https://github.com/example-org/service-api.git": "current-entry"} {
			if got, ok := r.findRemote(remote); !ok || got.Name != want {
				t.Fatalf("registered remote %s: %+v %v", remote, got, ok)
			}
		}
	}
	r := harnessRegistry{repos: []wtc.Repo{{Name: "logical", Remote: "https://github.com/example-org/Mixed-Case.git"}}}
	if got, ok := r.find("mixed-case"); !ok || got.Name != "logical" {
		t.Fatal("origin short names lost case-insensitive matching")
	}
}
