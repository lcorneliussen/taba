package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"taba.sh/taba/internal/openclaw"
	"taba.sh/taba/internal/wtc"
)

const sandboxMetadata = `{"collection":"42-fix","collection_project":"platform","projects":[{"name":"platform","harness":"https://github.com/example/platform-harness.git","ref":"main","default":true,"project_id":"harness-id","destination":"main","repos":[{"name":"api","remote":"https://github.com/example/api.git","role":"HTTP service","default_ref":"origin/develop","project_id":"api-id","destination":"develop"}]}]}`

func sandboxMetadataServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/wtc/session" {
			_, _ = io.WriteString(w, `{"repo":"api","branch":"openclaw/42-fix","base":"main","repos":["api"]}`)
			return
		}
		if r.Method != http.MethodGet || r.URL.Path != "/wtc/projects" || r.URL.RawQuery != "" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(server.Close)
	t.Setenv(openclaw.GatewayGitEnv, server.URL)
	t.Setenv("OPENCLAW_SHELL", "exec")
	state := filepath.Join(t.TempDir(), "gateway-unmounted")
	t.Setenv("OPENCLAW_STATE_DIR", state)
	t.Cleanup(func() {
		if _, err := os.Stat(state); !os.IsNotExist(err) {
			t.Error("a sandbox plan wrote gateway state")
		}
	})
	saved := runOpenClaw
	runOpenClaw = func(...string) (string, error) {
		t.Error("sandbox fell back to the local OpenClaw CLI")
		return "", errNoOpenClaw
	}
	t.Cleanup(func() { runOpenClaw = saved })
	return server
}

func TestSandboxProjectsAndAddRepoUseGatewayMetadata(t *testing.T) {
	sandboxMetadataServer(t, 200, sandboxMetadata)
	out, err := runProjects(t, "--json")
	if err != nil || !strings.Contains(out, `"name":"api"`) || !strings.Contains(out, `"role":"HTTP service"`) {
		t.Fatalf("projects: %s %v", out, err)
	}
	intents, err := addRepoPlan(t, "api", "--worktree", "42-fix", "--separate")
	if err != nil || summary(intents) != "platform-harness@harness-id:origin/main api@api-id:origin/develop" {
		t.Fatalf("add-repo: %s %v", summary(intents), err)
	}
	if intents[1].Args.ProjectGitURL != "" {
		t.Fatal("a known gateway repository became a clone request")
	}
	if _, err := addRepoPlan(t, "api", "--worktree", "42-fix", "--project", "hidden", "--separate"); err == nil || !strings.Contains(err.Error(), "is in wtc project platform") {
		t.Fatalf("changed existing project: %v", err)
	}
	if _, err := addRepoPlan(t, "api", "--worktree", "43-new", "--project", "hidden", "--separate"); err == nil || !strings.Contains(err.Error(), "another collection") {
		t.Fatalf("unlisted project: %v", err)
	}
}

func TestSandboxProjectFailuresNeverFallBackToAdHoc(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"denied", 403, `{"error":"project discovery is not enabled for this agent"}`},
		{"unavailable", 503, `{"error":"gateway project metadata unavailable"}`},
		{"malformed", 200, `not JSON`},
		{"missing list", 200, `{}`},
		{"null list", 200, `{"projects":null}`},
		{"incomplete project", 200, `{"projects":[{}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sandboxMetadataServer(t, tc.status, tc.body)
			if out, err := runProjects(t, "--json"); err == nil || out != "" {
				t.Fatalf("projects succeeded: %s %v", out, err)
			}
			for _, flag := range []string{"platform", "none"} {
				if plan, err := addRepoPlan(t, "api", "--worktree", "42-fix", "--project", flag, "--separate"); err == nil || len(plan) != 0 {
					t.Fatalf("add-repo succeeded after discovery failure: %+v %v", plan, err)
				}
			}
		})
	}
	t.Run("offline", func(t *testing.T) {
		server := sandboxMetadataServer(t, 200, sandboxMetadata)
		server.Close()
		if _, err := gatewayProjects(); err == nil {
			t.Fatal("unreachable gateway succeeded")
		}
		if _, err := addRepoPlan(t, "api", "--worktree", "42-fix"); err == nil {
			t.Fatal("unreachable gateway became an ad-hoc plan")
		}
	})
}

func TestSandboxDiscoveryDoesNotAuthorizeCloning(t *testing.T) {
	body := strings.ReplaceAll(sandboxMetadata, `,"project_id":"api-id"`, "")
	body = strings.ReplaceAll(body, `"default_ref":"origin/develop"`, `"default_ref":""`)
	sandboxMetadataServer(t, 200, body)
	if _, err := addRepoPlan(t, "api", "--worktree", "42-fix"); err == nil || !strings.Contains(err.Error(), "grants no clone permission") || !strings.Contains(err.Error(), "cannot be resolved from this sandbox") || !strings.Contains(err.Error(), "gateway collection session") {
		t.Fatalf("listing granted clone permission: %v", err)
	}
	// A separate GitHub conversation still produces the existing URL intent;
	// the gateway must authorize it. No clone is performed by this command.
	intents, err := addRepoPlan(t, "api", "--worktree", "42-fix", "--separate")
	if err != nil || intents[1].Args.ProjectGitURL != "https://github.com/example/api.git" {
		t.Fatalf("explicit separate conversation: %+v %v", intents, err)
	}
}

func TestSandboxProjectProjectionFiltersAndRedacts(t *testing.T) {
	_, _ = harnessGateway(t)
	t.Setenv(openclaw.GatewayGitEnv, "")
	withGatewayConfig(t, `{"harnesses":[{"name":"platform","remote":"https://user:secret@github.com/example-org/platform-harness"},{"name":"hidden","remote":"https://github.com/example-org/hidden-harness"},{"name":"gone","remote":"https://github.com/example-org/gone-harness"}],"defaultHarness":"hidden"}`)
	projects, err := localGatewayProjects([]string{"platform", "gone"}, "")
	if err != nil || len(projects) != 2 || projects[0].Default || projects[0].Harness != "https://github.com/example-org/platform-harness.git" || projects[0].Repos[0].DefaultRef != "origin/develop" || projects[0].Repos[0].ProjectID != "api" {
		t.Fatalf("projection: %+v %v", projects, err)
	}
	if projects[1].Error != "project registry unavailable on the gateway" {
		t.Fatalf("raw error: %+v", projects[1])
	}
	h := openclaw.Harness{Name: "platform", Remote: "https://user:secret@github.com/example-org/platform-harness", Ref: "main"}
	for _, remote := range []string{"https://user:secret@github.com/example-org/api.git?token=secret", "/fixture/secret-repository", "file:///fixture/secret-repository"} {
		p := sandboxProjectMetadata(gatewayProject{Name: h.Name, Ref: h.Ref}, h, harnessRegistry{repos: []wtc.Repo{{Name: "api", Remote: remote}}}, "")
		body, _ := json.Marshal(p)
		for _, forbidden := range []string{"secret", "token", "/fixture"} {
			if strings.Contains(string(body), forbidden) {
				t.Fatalf("projection exposed %s: %s", forbidden, body)
			}
		}
	}
	p := sandboxProjectMetadata(gatewayProject{}, h, harnessRegistry{err: errors.New("/fixture/private-config secret")}, "")
	if p.Error != "project registry unavailable on the gateway" {
		t.Fatalf("diagnostic leaked: %+v", p)
	}
}

func TestSandboxProjectsCommandRequiresExplicitSelection(t *testing.T) {
	saved := runOpenClaw
	runOpenClaw = func(...string) (string, error) { t.Fatal("read config without an allowlist"); return "", nil }
	t.Cleanup(func() { runOpenClaw = saved })
	cmd := sandboxProjectsCommand()
	cmd.SetArgs(nil)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	if err := cmd.Execute(); err == nil {
		t.Fatal("missing selection allowed")
	}
}

func TestSandboxDoctorReportsProjectSource(t *testing.T) {
	server := sandboxMetadataServer(t, 200, sandboxMetadata)
	gw := openclaw.GatewayFromEnv(os.Getenv)
	checks := sandboxChecks(gw, func(string) string { return "" }, func(string, ...string) (string, error) { return "", errors.New("absent") })
	if len(checks) != 3 || !checks[2].OK || checks[2].Required || !strings.Contains(checks[2].Detail, "1 operator-allowed projects") {
		t.Fatalf("projects check: %+v", checks)
	}
	server.Close()
	checks = sandboxChecks(gw, func(string) string { return "" }, func(string, ...string) (string, error) { return "", errors.New("absent") })
	if checks[2].OK || !strings.Contains(checks[2].Detail, "discovery unavailable") {
		t.Fatalf("offline check: %+v", checks[2])
	}
}

func TestSandboxProjectsGatewayCommandReturnsOnlyAllowedMetadata(t *testing.T) {
	_, _ = harnessGateway(t)
	withGatewayConfig(t, `{"harnesses":[{"name":"platform","remote":"https://github.com/example-org/platform-harness"},{"name":"hidden","remote":"https://github.com/example-org/hidden-harness"}]}`)
	cmd := sandboxProjectsCommand()
	cmd.SetArgs([]string{"--project", "platform"})
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout := os.Stdout
	os.Stdout = w
	err = cmd.Execute()
	os.Stdout = stdout
	w.Close()
	body, _ := io.ReadAll(r)
	r.Close()
	var result openclaw.SandboxProjects
	if err != nil || json.Unmarshal(body, &result) != nil || len(result.Projects) != 1 || result.Projects[0].Name != "platform" {
		t.Fatalf("command: %s %v", body, err)
	}
	if strings.Contains(string(body), "hidden") {
		t.Fatalf("unallowed metadata: %s", body)
	}
}

func executeSandboxProjects(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := sandboxProjectsCommand()
	cmd.SetArgs(args)
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout := os.Stdout
	os.Stdout = w
	err = cmd.Execute()
	os.Stdout = stdout
	w.Close()
	body, _ := io.ReadAll(r)
	r.Close()
	return string(body), err
}

func TestSandboxProjectsUseExistingHarnessWorktree(t *testing.T) {
	state, git := harnessGateway(t)
	withGatewayConfig(t, `{"harnesses":[{"name":"platform","remote":"https://github.com/example-org/platform-harness"}]}`)
	dir := filepath.Join(state, "worktrees", "platform-harness", "42-fix")
	git(filepath.Join(state, "repos", "platform-harness"), "worktree", "add", "-q", "-b", "openclaw/42-fix", dir)
	t.Chdir(dir)
	registry := platformRegistry + "  - name: tools\n    remote: https://github.com/example-org/tools.git\n    default_ref: origin/main\n"
	if err := os.WriteFile(filepath.Join(dir, ".harness-repos.yml"), []byte(registry), 0o644); err != nil {
		t.Fatal(err)
	}
	// The harness worktree establishes the project even before a separate
	// collection record exists. Unpublished registry edits are visible.
	body, err := executeSandboxProjects(t, "--project", "platform")
	var metadata openclaw.SandboxProjects
	if err != nil || json.Unmarshal([]byte(body), &metadata) != nil || metadata.Collection != "42-fix" || metadata.CollectionProject != "platform" || !metadata.Projects[0].HasWorktree || len(metadata.Projects[0].Repos) != 3 {
		t.Fatalf("worktree metadata: %s %v", body, err)
	}
	sandboxMetadataServer(t, 200, body)
	plan, err := addRepoPlan(t, "tools", "--worktree", "42-fix", "--separate")
	if err != nil || summary(plan) != "tools@tools:origin/main" {
		t.Fatalf("existing harness was spawned again: %s %v", summary(plan), err)
	}
}

func TestSandboxProjectsEnforceRecordedCollectionVisibility(t *testing.T) {
	for _, project := range []string{"platform", "hidden"} {
		t.Run(project, func(t *testing.T) {
			state, git := harnessGateway(t)
			withGatewayConfig(t, `{"harnesses":[{"name":"platform","remote":"https://github.com/example-org/platform-harness"}]}`)
			dir := filepath.Join(state, "worktrees", "api", "42-fix")
			git(filepath.Join(state, "repos", "api"), "worktree", "add", "-q", "-b", "openclaw/42-fix", dir)
			t.Chdir(dir)
			folder := openclaw.NewCollectionFolder(state, "42-fix", project)
			if err := wtc.RecordGatewayHarness(folder, project, "https://github.com/example-org/"+project+"-harness"); err != nil {
				t.Fatal(err)
			}
			body, err := executeSandboxProjects(t, "--project", "platform")
			if project == "hidden" {
				if err == nil || body != "" {
					t.Fatalf("unallowed collection disclosed: %s %v", body, err)
				}
				return
			}
			var result openclaw.SandboxProjects
			if err != nil || json.Unmarshal([]byte(body), &result) != nil || result.CollectionProject != project {
				t.Fatalf("allowed binding: %s %v", body, err)
			}
		})
	}
}

func TestSandboxRemoteProjectionSupportsForgeTransports(t *testing.T) {
	for _, tc := range []struct{ remote, want string }{
		{"https://user:secret@bitbucket.org/example/api.git?token=secret#secret", "https://bitbucket.org/example/api.git"},
		{"ssh://git@github.com/example/api.git", "https://github.com/example/api.git"},
		{"git@bitbucket.org:example/api.git", "https://bitbucket.org/example/api.git"},
		{"https://github.com/exa%20mple/api.git", ""},
		{"https://github.com/example/api%20name.git", ""},
	} {
		got, err := projectMetadataRemote(tc.remote)
		if tc.want == "" {
			if err == nil {
				t.Fatalf("odd name accepted: %q", got)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Fatalf("remote projection: %q %v; want %q", got, err, tc.want)
		}
	}
}

func TestSandboxWorkerAndBitbucketPlansWriteNoGatewayState(t *testing.T) {
	for _, tc := range []struct {
		name, remote, projectID string
		args                    []string
		wantError               bool
	}{
		{"worker missing project", "https://github.com/example/api.git", "", []string{"--worker", "part", "--work", "test"}, true},
		{"worker known project", "https://github.com/example/api.git", "api-id", []string{"--worker", "part", "--work", "test"}, true},
		{"worker explicit base", "https://github.com/example/api.git", "api-id", []string{"--worker", "part", "--work", "test", "--base", "feature"}, true},
		{"Bitbucket missing project", "https://bitbucket.org/example/api.git", "", []string{"--separate"}, true},
		{"Bitbucket known project", "https://bitbucket.org/example/api.git", "api-id", []string{"--separate"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := strings.ReplaceAll(sandboxMetadata, "https://github.com/example/api.git", tc.remote)
			body = strings.ReplaceAll(body, `,"project_id":"api-id"`, "")
			if tc.projectID != "" {
				body = strings.Replace(body, `"name":"api"`, `"name":"api","project_id":"`+tc.projectID+`"`, 1)
			}
			sandboxMetadataServer(t, 200, body)
			args := append([]string{"api", "--worktree", "42-fix"}, tc.args...)
			plan, err := addRepoPlan(t, args...)
			if tc.wantError {
				if err == nil || !(strings.Contains(err.Error(), "gateway collection session") || strings.Contains(err.Error(), "grants no clone permission")) {
					t.Fatalf("expected refusal: %+v %v", plan, err)
				}
				return
			}
			if err != nil || len(plan) == 0 || plan[len(plan)-1].Args.ProjectID != tc.projectID {
				t.Fatalf("known project: %+v %v", plan, err)
			}
		})
	}
}

func TestSandboxPlansRequireExistingPublicationDestination(t *testing.T) {
	for _, tc := range []struct {
		name, destination string
		args              []string
		rejected          bool
	}{
		{"registry differs", "main", nil, true},
		{"recorded destination matches", "develop", nil, false},
		{"unknown destination", "", nil, true},
		{"explicit matching base", "main", []string{"--base", "main"}, false},
		{"explicit different base", "develop", []string{"--base", "feature"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := strings.Replace(sandboxMetadata, `"destination":"develop"`, `"destination":"`+tc.destination+`"`, 1)
			sandboxMetadataServer(t, 200, body)
			plan, err := addRepoPlan(t, append([]string{"api", "--worktree", "42-fix", "--separate"}, tc.args...)...)
			if tc.rejected {
				if err == nil || len(plan) != 0 || !strings.Contains(err.Error(), "gateway base record") {
					t.Fatalf("unsafe destination: %+v %v", plan, err)
				}
				return
			}
			if err != nil || len(plan) != 2 {
				t.Fatalf("safe destination: %+v %v", plan, err)
			}
		})
	}
}

func TestSandboxProjectsGatewayAdapterRefusesSandboxTransport(t *testing.T) {
	saved := runOpenClaw
	runOpenClaw = func(...string) (string, error) { t.Fatal("sandbox adapter read local gateway config"); return "", nil }
	t.Cleanup(func() { runOpenClaw = saved })
	t.Setenv(openclaw.GatewayGitEnv, "http://127.0.0.1:1")
	body, err := executeSandboxProjects(t, "--project", "hidden")
	if err == nil || body != "" || !strings.Contains(err.Error(), "authenticated gateway service") {
		t.Fatalf("local bypass: %s %v", body, err)
	}
}

func TestSandboxUnsupportedHarnessIsContained(t *testing.T) {
	_, _ = harnessGateway(t)
	project := sandboxProjectMetadata(gatewayProject{Name: "platform", Ref: "main"}, openclaw.Harness{Remote: "https://example.invalid/harness.git"}, harnessRegistry{}, "")
	if project.Error == "" || project.Harness != "" || len(project.Repos) != 0 {
		t.Fatalf("unsupported harness: %+v", project)
	}
}

func TestSandboxSchemeLessHarnessFailureIsContained(t *testing.T) {
	_, _ = harnessGateway(t)
	withGatewayConfig(t, `{"harnesses":[{"name":"platform","remote":"https://github.com/example-org/platform-harness"},{"name":"bare","remote":"github.com:example-org/ops-harness"}]}`)
	projects, err := localGatewayProjects([]string{"platform", "bare"}, "")
	if err != nil || len(projects) != 2 || projects[0].Error != "" || len(projects[0].Repos) != 2 || projects[1].Error == "" {
		t.Fatalf("per-project containment: %+v %v", projects, err)
	}
	body, err := json.Marshal(openclaw.SandboxProjects{Projects: projects})
	if err != nil {
		t.Fatal(err)
	}
	sandboxMetadataServer(t, http.StatusOK, string(body))
	metadata, err := openclaw.GatewayFromEnv(os.Getenv).Projects()
	if err != nil || len(metadata.Projects) != 2 || metadata.Projects[0].Error != "" {
		t.Fatalf("one unavailable harness broke the full listing: %+v %v", metadata, err)
	}
}
