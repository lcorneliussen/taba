package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lcorneliussen/wtc-cli/internal/openclaw"
	"github.com/lcorneliussen/wtc-cli/internal/wtc"
	"github.com/spf13/cobra"
)

const platformRegistry = `schema_version: 1
repos:
  - name: api
    remote: git@github.com:example-org/api.git
    default_ref: origin/develop
  - name: web
    remote: https://github.com/example-org/web.git
    default_ref: origin/main
`

func TestGatewayProjectPortsRepairOnStatus(t *testing.T) {
	state, git := harnessGateway(t)
	withGatewayConfig(t, `{"harnesses":[{"name":"platform","remote":"https://github.com/example-org/platform-harness.git","ref":"main"}]}`)
	harness := filepath.Join(state, "repos", "platform-harness")
	registry := "repos:\n  - name: docs\n    aliases: [old-docs]\n    remote: https://github.com/example-org/web.git\n    port_offset: 1\n    owner: /not-a-gateway-owner\n"
	if err := os.WriteFile(filepath.Join(harness, ".harness-repos.yml"), []byte(registry), 0o644); err != nil {
		t.Fatal(err)
	}
	git(harness, "add", ".")
	git(harness, "commit", "-qm", "port registry")
	git(harness, "update-ref", "refs/remotes/origin/main", "HEAD")
	folder := openclaw.NewCollectionFolder(state, "ports", "platform")
	if err := wtc.RecordGatewayHarness(folder, "platform", "https://github.com/example-org/platform-harness.git"); err != nil {
		t.Fatal(err)
	}
	rows := []openclaw.CheckoutStatus{{Repo: "web", Path: filepath.Join(state, "repos", "web")}, {Repo: "tools", Path: filepath.Join(state, "repos", "tools")}}
	for range 2 {
		_, _, warning := linkGatewayFolder("ports", rows)
		if warning != "" {
			t.Fatal(warning)
		}
		data, err := os.ReadFile(filepath.Join(folder, ".env.collection"))
		if err != nil || !strings.Contains(string(data), "DOCS_PORT=42001\n") || !strings.Contains(string(data), "OLD_DOCS_PORT=42001\n") || strings.Contains(string(data), "TOOLS_PORT=") {
			t.Fatalf("gateway env lost selected project ports: %s %v", data, err)
		}
		c, err := wtc.OpenCollection(folder)
		if err != nil || c.Registry.Repos[0].Owner != "" {
			t.Fatalf("copied project owner or invalid registry: %v", err)
		}
		// An already-linked registry must also repair a missing generated env.
		if err := os.Remove(filepath.Join(folder, ".env.collection")); err != nil {
			t.Fatal(err)
		}
	}
}

func TestGatewayPortOriginRequiresFullRepositoryIdentity(t *testing.T) {
	for _, remote := range []string{"https://github.com/example-org/web.git", "git@github.com:example-org/web.git", "ssh://git@bitbucket.org/example-org/web.git"} {
		if _, ok := gatewayPortOrigin(remote); !ok {
			t.Errorf("valid origin rejected: %s", remote)
		}
	}
	for _, remote := range []string{"web", "https://github.com/other", "https://github.com/example-org/web/extra", "https://github.com/example-org/web?token=example", "https://github.com/example-org/%77eb", "https://unknown.invalid/example-org/web", "https://github.com/../web"} {
		if _, ok := gatewayPortOrigin(remote); ok {
			t.Errorf("invalid origin accepted: %s", remote)
		}
	}
}

func TestGatewayProjectPortsRefuseUnavailableOrAmbiguousMetadata(t *testing.T) {
	for _, tc := range []struct {
		name, config, registry, want string
	}{
		{"missing-project", `{"harnesses":[]}`, platformRegistry, "recorded project platform is unavailable"},
		{"ambiguous-origin", `{"harnesses":[{"name":"platform","remote":"https://github.com/example-org/platform-harness.git","ref":"main"}]}`, "repos:\n  - name: docs\n    remote: https://github.com/example-org/web.git\n    port_offset: 1\n  - name: other\n    remote: git@github.com:example-org/web.git\n    port_offset: 2\n", "lists several entries"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state, git := harnessGateway(t)
			withGatewayConfig(t, tc.config)
			harness := filepath.Join(state, "repos", "platform-harness")
			if err := os.WriteFile(filepath.Join(harness, ".harness-repos.yml"), []byte(tc.registry), 0o644); err != nil {
				t.Fatal(err)
			}
			git(harness, "add", ".")
			git(harness, "commit", "-qm", "registry fixture")
			git(harness, "update-ref", "refs/remotes/origin/main", "HEAD")
			folder := openclaw.NewCollectionFolder(state, "ports", "platform")
			if err := wtc.RecordGatewayHarness(folder, "platform", "https://github.com/example-org/platform-harness.git"); err != nil {
				t.Fatal(err)
			}
			linked := []wtc.LinkedRepo{{Name: "web", Remote: "https://github.com/example-org/web.git", DefaultRef: "origin/main", Path: filepath.Join(state, "repos", "web")}}
			c, err := wtc.LinkGatewayCollection(folder, linked)
			if err != nil {
				t.Fatal(err)
			}
			paths := []string{filepath.Join(folder, ".env.collection"), filepath.Join(c.Harness, "repos.yml")}
			before := map[string]string{}
			for _, path := range paths {
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				before[path] = string(data)
			}
			if err := inheritGatewayProjectPorts(c, "ports", linked); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %q, got %v", tc.want, err)
			}
			for _, path := range paths {
				data, _ := os.ReadFile(path)
				if string(data) != before[path] {
					t.Fatalf("refusal changed %s", filepath.Base(path))
				}
			}
		})
	}
}

func TestGatewayPortRepairErrorKeepsServicesVisible(t *testing.T) {
	state, _ := harnessGateway(t)
	withGatewayConfig(t, `{"harnesses":[]}`)
	folder := openclaw.NewCollectionFolder(state, "ports", "platform")
	if err := wtc.RecordGatewayHarness(folder, "platform", "https://github.com/example-org/platform-harness.git"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(state, "repos", "web", ".harness")
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "dekit.tasks.yaml"), []byte("tasks:\n  web/main:\n    cmd: [sleep, '60']\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, services, warning := linkGatewayFolder("ports", []openclaw.CheckoutStatus{{Repo: "web", Path: filepath.Join(state, "repos", "web")}})
	if !strings.Contains(warning, "recorded project platform is unavailable") || len(services) != 1 || services[0].Path != "web/web/main" || services[0].State != "unconfigured" {
		t.Fatalf("port error hid service observations: %v %s", services, warning)
	}
}

func TestGatewayPortsIgnoreUnverifiedProjectRowsAndEquivalentHarnessURL(t *testing.T) {
	state, git := harnessGateway(t)
	withGatewayConfig(t, `{"harnesses":[{"name":"platform","remote":"https://github.com/example-org/platform-harness","ref":"main"}]}`)
	harness := filepath.Join(state, "repos", "platform-harness")
	registry := "repos:\n  - name: docs\n    remote: https://github.com/another-org/web.git\n    port_offset: 9\n  - name: unverified\n    remote: https://unknown.invalid/example-org/web\n    port_offset: 1\n"
	if err := os.WriteFile(filepath.Join(harness, ".harness-repos.yml"), []byte(registry), 0o644); err != nil {
		t.Fatal(err)
	}
	git(harness, "add", ".")
	git(harness, "commit", "-qm", "unrelated port metadata")
	git(harness, "update-ref", "refs/remotes/origin/main", "HEAD")
	folder := openclaw.NewCollectionFolder(state, "ports", "platform")
	if err := wtc.RecordGatewayHarness(folder, "platform", "git@github.com:example-org/platform-harness.git"); err != nil {
		t.Fatal(err)
	}
	_, _, warning := linkGatewayFolder("ports", []openclaw.CheckoutStatus{{Repo: "web", Path: filepath.Join(state, "repos", "web")}})
	if warning != "" {
		t.Fatal(warning)
	}
	env, _ := os.ReadFile(filepath.Join(folder, ".env.collection"))
	if strings.Contains(string(env), "WEB_PORT=") || strings.Contains(string(env), "DOCS_PORT=") || strings.Contains(string(env), "UNVERIFIED_PORT=") {
		t.Fatalf("unverified metadata supplied ports: %s", env)
	}
}

// harnessGateway is a gateway state dir with clones of a harness and of the
// repositories its registry names, as projects.add and the plugin make them.
func harnessGateway(t *testing.T) (state string, git func(dir string, args ...string) string) {
	t.Helper()
	for name, value := range map[string]string{"GIT_AUTHOR_NAME": "T", "GIT_AUTHOR_EMAIL": "t@example.com",
		"GIT_COMMITTER_NAME": "T", "GIT_COMMITTER_EMAIL": "t@example.com", "GIT_CONFIG_GLOBAL": "/dev/null"} {
		t.Setenv(name, value)
	}
	state = t.TempDir()
	git = func(dir string, args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	clone := func(name, files string) {
		dir := filepath.Join(state, "repos", name)
		git(state, "init", "-q", "-b", "main", dir)
		if files != "" {
			if err := os.WriteFile(filepath.Join(dir, ".harness-repos.yml"), []byte(files), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		git(dir, "commit", "-q", "--allow-empty", "-m", "init")
		git(dir, "add", ".")
		git(dir, "commit", "-q", "--allow-empty", "-m", "registry")
		git(dir, "remote", "add", "origin", "https://github.com/example-org/"+name+".git")
		for _, b := range []string{"main", "develop"} {
			git(dir, "update-ref", "refs/remotes/origin/"+b, "HEAD")
		}
	}
	clone("platform-harness", platformRegistry)
	clone("ops-harness", "repos:\n  - name: infra\n    remote: https://github.com/example-org/infra.git\n")
	clone("api", "")
	clone("web", "")
	clone("infra", "")
	// A project on the gateway that no harness lists.
	clone("tools", "")
	t.Setenv("OPENCLAW_STATE_DIR", state)
	t.Setenv("OPENCLAW_SHELL", "exec")
	t.Chdir(t.TempDir())
	return state, git
}

func withGatewayConfig(t *testing.T, config string) {
	t.Helper()
	saved := runOpenClaw
	t.Cleanup(func() { runOpenClaw = saved })
	runOpenClaw = func(args ...string) (string, error) {
		if strings.Join(args, " ") != "config get plugins.entries.wtc.config --json" {
			t.Fatalf("unexpected openclaw %v", args)
		}
		return config, nil
	}
}

type spawned struct {
	Repo string `json:"repo"`
	Args struct {
		Task            string `json:"task"`
		Label           string `json:"label"`
		ProjectID       string `json:"projectId"`
		ProjectGitURL   string `json:"projectGitUrl"`
		WorktreeName    string `json:"worktreeName"`
		WorktreeBaseRef string `json:"worktreeBaseRef"`
		// Absent (nil) keeps OpenClaw's completion handoff.
		ExpectsCompletionMessage *bool `json:"expectsCompletionMessage"`
	} `json:"args"`
}

func addRepoPlan(t *testing.T, args ...string) ([]spawned, error) {
	t.Helper()
	var asJSON bool
	root := &cobra.Command{Use: "wtc", SilenceUsage: true, SilenceErrors: true}
	addAddRepoCommand(root, &asJSON)
	root.SetArgs(append([]string{"add-repo"}, args...))
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	stdout, stderr := os.Stdout, os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	devnull, _ := os.Open(os.DevNull)
	os.Stdout, os.Stderr = w, devnull
	runErr := root.Execute()
	os.Stdout, os.Stderr = stdout, stderr
	w.Close()
	out, _ := io.ReadAll(r)
	if runErr != nil {
		return nil, runErr
	}
	var plan struct {
		Intents []spawned `json:"intents"`
	}
	if err := json.Unmarshal(out, &plan); err != nil {
		t.Fatalf("plan: %v\n%s", err, out)
	}
	return plan.Intents, nil
}

func summary(intents []spawned) string {
	var parts []string
	for _, in := range intents {
		parts = append(parts, in.Repo+"@"+in.Args.ProjectID+":"+in.Args.WorktreeBaseRef)
	}
	return strings.Join(parts, " ")
}

// With one harness configured, a collection works in it: its first add-repo
// brings the harness itself, and names resolve through its registry, with
// their default branches.
func TestAddRepoResolvesThroughTheHarness(t *testing.T) {
	state, git := harnessGateway(t)
	withGatewayConfig(t, `{"teamTailnet": false, "harnesses": [{"name": "platform", "remote": "https://github.com/example-org/platform-harness"}]}`)

	intents, err := addRepoPlan(t, "api", "--worktree", "12-paging")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := summary(intents), "platform-harness@platform-harness:origin/main api@api:origin/develop"; got != want {
		t.Errorf("intents %s, want %s", got, want)
	}
	if got := git(filepath.Join(state, "repos", "api"), "config", "branch.openclaw/12-paging.wtcbase"); got != "develop" {
		t.Errorf("recorded base of api: %q", got)
	}
	folder := filepath.Join(state, "wtc", "platform", "12-paging")
	if name, remote, err := wtc.GatewayHarness(folder); err != nil || name != "platform" || !strings.Contains(remote, "platform-harness") {
		t.Errorf("recorded harness %q %q %v", name, remote, err)
	}

	// Once the collection has its worktree of the harness, the registry is
	// read from there: the task may change it.
	wt := filepath.Join(state, "worktrees", "a1", "12-paging")
	git(filepath.Join(state, "repos", "platform-harness"), "worktree", "add", "-q", "-b", "openclaw/12-paging", wt)
	if err := os.WriteFile(filepath.Join(wt, ".harness-repos.yml"), []byte(platformRegistry+"  - name: docs\n    remote: https://github.com/example-org/web.git\n    default_ref: origin/develop\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	intents, err = addRepoPlan(t, "docs", "--worktree", "12-paging")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := summary(intents), "web@web:origin/develop"; got != want {
		t.Errorf("from the harness worktree: %s, want %s", got, want)
	}
	if h := gatewayHarness(folder, []string{wt}); h == nil || h.Name != "platform" || h.Path != wt || h.String() != "wtc project platform, harness at "+wt+": read its AGENTS.md" {
		t.Errorf("status shows the harness: %+v", h)
	}
	if h := gatewayHarness(folder, nil); h == nil || h.String() != "wtc project platform, no worktree of its harness yet" {
		t.Errorf("status before the harness worktree: %+v", h)
	}
	// --base still wins over the registry's default branch.
	if intents, err = addRepoPlan(t, "api", "--worktree", "12-paging", "--base", "main"); err != nil || summary(intents) != "api@api:origin/main" {
		t.Errorf("--base: %s %v", summary(intents), err)
	}
	// A collection has one harness.
	if _, err := addRepoPlan(t, "api", "--worktree", "12-paging", "--harness", "ops"); err == nil || !strings.Contains(err.Error(), "is in wtc project platform") {
		t.Errorf("another harness: %v", err)
	}
}

// With several harnesses and no default, the choice has to be clear from
// the names; otherwise the agent is told to ask.
func TestAddRepoAsksWhichHarnessWhenItIsNotClear(t *testing.T) {
	_, _ = harnessGateway(t)
	withGatewayConfig(t, `{"harnesses": [
		{"name": "platform", "remote": "https://github.com/example-org/platform-harness"},
		{"name": "ops", "remote": "git@github.com:example-org/ops-harness.git", "ref": "develop"}]}`)

	intents, err := addRepoPlan(t, "infra", "--worktree", "7-dns")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := summary(intents), "ops-harness@ops-harness:origin/develop infra@infra:"; got != want {
		t.Errorf("only ops lists infra: %s, want %s", got, want)
	}
	// A URL counts by its registered remote.
	if intents, err := addRepoPlan(t, "example-org/api", "--worktree", "8-x"); err != nil || !strings.HasPrefix(summary(intents), "platform-harness@") {
		t.Errorf("a URL platform lists: %s %v", summary(intents), err)
	}
	for _, args := range [][]string{{"example-org/elsewhere", "--worktree", "8-y"}, {"api", "infra", "--worktree", "9-x"}} {
		if _, err := addRepoPlan(t, args...); err == nil || !strings.Contains(err.Error(), "Ask the person which wtc project") || !strings.Contains(err.Error(), "platform, ops") {
			t.Errorf("%v: %v", args, err)
		}
	}
	if intents, err := addRepoPlan(t, "api", "--worktree", "10-x", "--harness", "none"); err != nil || summary(intents) != "api@api:" {
		t.Errorf("--harness none: %s %v", summary(intents), err)
	}
	if intents, err := addRepoPlan(t, "api", "--worktree", "11-x", "--harness", "platform"); err != nil || !strings.HasPrefix(summary(intents), "platform-harness@") {
		t.Errorf("--harness platform: %s %v", summary(intents), err)
	}
	if _, err := addRepoPlan(t, "api", "--worktree", "12-x", "--harness", "nope"); err == nil || !strings.Contains(err.Error(), "no wtc project nope") {
		t.Errorf("unknown harness: %v", err)
	}
}

// A repository the harness does not list is the person's choice, even with
// a default harness: ad hoc beside it, registered in it, or a collection of
// its own.
func TestAddRepoAsksAboutAnUnlistedRepository(t *testing.T) {
	state, git := harnessGateway(t)
	withGatewayConfig(t, `{"harnesses": [{"name": "platform", "remote": "https://github.com/example-org/platform-harness"}], "defaultHarness": "platform"}`)
	for _, args := range [][]string{{"infra", "--worktree", "20-x"}, {"api", "infra", "--worktree", "21-x"}} {
		_, err := addRepoPlan(t, args...)
		if err == nil {
			t.Fatalf("%v: no question", args)
		}
		for _, want := range []string{"does not list infra", "--ad-hoc", ".harness-repos.yml", "--project none"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%v: %v, want %q", args, err, want)
			}
		}
	}
	if intents, err := addRepoPlan(t, "api", "infra", "--worktree", "22-x", "--ad-hoc"); err != nil || summary(intents) != "platform-harness@platform-harness:origin/main api@api:origin/develop infra@infra:" {
		t.Errorf("--ad-hoc: %s %v", summary(intents), err)
	}
	if intents, err := addRepoPlan(t, "infra", "--worktree", "23-x", "--harness", "none"); err != nil || summary(intents) != "infra@infra:" {
		t.Errorf("--harness none: %s %v", summary(intents), err)
	}

	// The collection's own harness asks too, and once it has a worktree of
	// the harness the registry to edit is already there.
	if _, err := addRepoPlan(t, "api", "--worktree", "24-x"); err != nil {
		t.Fatal(err)
	}
	if _, err := addRepoPlan(t, "infra", "--worktree", "24-x"); err == nil || !strings.Contains(err.Error(), "run `wtc add-repo https://github.com/example-org/platform-harness --worktree 24-x` first") {
		t.Errorf("a recorded harness without its worktree: %v", err)
	}
	wt := filepath.Join(state, "worktrees", "h1", "24-x")
	git(filepath.Join(state, "repos", "platform-harness"), "worktree", "add", "-q", "-b", "openclaw/24-x", wt)
	_, err := addRepoPlan(t, "infra", "--worktree", "24-x")
	if err == nil || !strings.Contains(err.Error(), "does not list infra") || strings.Contains(err.Error(), "first") {
		t.Errorf("a recorded harness with its worktree: %v", err)
	}
	// A worker of the task is not asked again.
	if _, err := addRepoPlan(t, "infra", "--worktree", "24-x", "--worker", "a", "--work", "x"); err != nil {
		t.Errorf("a worker: %v", err)
	}

	// --harness none binds the collection to none: a later add-repo of a
	// listed repository does not give it a harness after all.
	if _, err := addRepoPlan(t, "api", "--worktree", "23-x"); err != nil {
		t.Errorf("after --harness none: %v", err)
	} else if intents, _ := addRepoPlan(t, "api", "--worktree", "23-x"); summary(intents) != "api@api:" {
		t.Errorf("after --harness none: %s", summary(intents))
	}
	if _, err := addRepoPlan(t, "api", "--worktree", "23-x", "--harness", "platform"); err == nil || !strings.Contains(err.Error(), "without a wtc project") {
		t.Errorf("a harness for a collection without one: %v", err)
	}
	if h := gatewayHarness(openclaw.CollectionFolder(state, "23-x"), nil); h != nil {
		t.Errorf("status shows harness %+v", h)
	}

	// The only harness, without a default, asks as well.
	withGatewayConfig(t, `{"harnesses": [{"name": "platform", "remote": "https://github.com/example-org/platform-harness"}]}`)
	if _, err := addRepoPlan(t, "infra", "--worktree", "25-x"); err == nil || !strings.Contains(err.Error(), "does not list infra") {
		t.Errorf("the only harness: %v", err)
	}
}

// Without harnesses, or without the openclaw CLI, add-repo is as before.
func TestAddRepoWithoutHarnesses(t *testing.T) {
	_, _ = harnessGateway(t)
	saved := runOpenClaw
	t.Cleanup(func() { runOpenClaw = saved })
	for _, run := range []func(...string) (string, error){
		func(...string) (string, error) { return "", errNoOpenClaw },
		func(...string) (string, error) {
			return `{"ok": false, "error": {"message": "Config path is valid but unset: plugins.entries.wtc.config."}}`, &exec.ExitError{}
		},
		func(...string) (string, error) { return `{"teamTailnet": true}`, nil },
	} {
		runOpenClaw = run
		intents, err := addRepoPlan(t, "api", "--worktree", "5-x")
		if err != nil || summary(intents) != "api@api:" {
			t.Errorf("%s %v", summary(intents), err)
		}
	}
	runOpenClaw = func(...string) (string, error) { return "boom", &exec.ExitError{} }
	if _, err := addRepoPlan(t, "api", "--worktree", "5-x"); err == nil || !strings.Contains(err.Error(), "reading plugins.entries.wtc.config") {
		t.Errorf("an unreadable config: %v", err)
	}
	if dir := openclaw.CollectionFolder(os.Getenv("OPENCLAW_STATE_DIR"), "5-x"); dir != "" {
		t.Errorf("an unreadable config made a folder: %s", dir)
	}
	runOpenClaw = func(...string) (string, error) {
		return `{"harnesses": [{"name": "x", "remote": "https://example.org/x.git"}]}`, nil
	}
	if _, err := addRepoPlan(t, "api", "--worktree", "5-x"); err == nil || !strings.Contains(err.Error(), "not a GitHub or Bitbucket") {
		t.Errorf("a bad config: %v", err)
	}
}

// A URL resolves through the registry too, the harness is added once even
// when named, and an add-repo that fails binds the collection to nothing.
func TestAddRepoHarnessEdgeCases(t *testing.T) {
	state, _ := harnessGateway(t)
	withGatewayConfig(t, `{"harnesses": [{"name": "platform", "remote": "https://github.com/example-org/platform-harness"}]}`)
	intents, err := addRepoPlan(t, "git@github.com:example-org/api.git", "example-org/platform-harness", "--worktree", "3-x")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := summary(intents), "platform-harness@platform-harness:origin/main api@api:origin/develop"; got != want {
		t.Errorf("intents %s, want %s", got, want)
	}

	// A repository that is not on the gateway: the plan fails, nothing is
	// recorded, and the collection can still choose.
	if _, err := addRepoPlan(t, "example-org/elsewhere", "--worktree", "4-x", "--ad-hoc"); err == nil || !strings.Contains(err.Error(), "not on the gateway yet") {
		t.Fatalf("not on the gateway: %v", err)
	}
	if dir := openclaw.CollectionFolder(state, "4-x"); dir != "" {
		t.Errorf("a failed add-repo made %s", dir)
	}
	if _, err := addRepoPlan(t, "api", "--worktree", "4-x", "--harness", "none"); err != nil {
		t.Errorf("choosing after a failed add-repo: %v", err)
	}

	// A harness that is not on the gateway, or has no registry.
	withGatewayConfig(t, `{"harnesses": [{"name": "gone", "remote": "https://github.com/example-org/gone-harness"}]}`)
	if _, err := addRepoPlan(t, "api", "--worktree", "5-x"); err == nil || !strings.Contains(err.Error(), "wtc project gone is not on the gateway yet") {
		t.Errorf("harness not on the gateway: %v", err)
	}
	withGatewayConfig(t, `{"harnesses": [{"name": "web", "remote": "https://github.com/example-org/web"}]}`)
	if _, err := addRepoPlan(t, "api", "--worktree", "6-x"); err == nil || !strings.Contains(err.Error(), "has no .harness-repos.yml") {
		t.Errorf("harness without a registry: %v", err)
	}
	for _, w := range []string{"5-x", "6-x"} {
		if dir := openclaw.CollectionFolder(state, w); dir != "" {
			t.Errorf("%s: a folder was made: %s", w, dir)
		}
	}
}

// An invalid worktree name is refused before anything is recorded, with or
// without harnesses.
func TestAddRepoRefusesABadWorktreeNameFirst(t *testing.T) {
	state, _ := harnessGateway(t)
	for _, config := range []string{`{}`, `{"harnesses": [{"name": "platform", "remote": "https://github.com/example-org/platform-harness"}]}`} {
		withGatewayConfig(t, config)
		if _, err := addRepoPlan(t, "api", "--worktree", "Bad/Name", "--base", "develop"); err == nil || !strings.Contains(err.Error(), "not a valid OpenClaw worktree name") {
			t.Errorf("%s: %v", config, err)
		}
		if out, _ := exec.Command("git", "-C", filepath.Join(state, "repos", "api"), "config", "--get-regexp", "wtcbase").Output(); len(out) != 0 {
			t.Errorf("%s: a base was recorded: %s", config, out)
		}
		if entries, _ := os.ReadDir(filepath.Join(state, "wtc")); len(entries) != 0 {
			t.Errorf("%s: a folder was made", config)
		}
	}
}

// A worker gets a collection task--worker of its own, based on the task's
// branch where there is one, through the task's harness but without a
// worktree of it, and is told to commit and hand back.
func TestAddRepoSpawnsAWorker(t *testing.T) {
	state, git := harnessGateway(t)
	withGatewayConfig(t, `{"harnesses": [{"name": "platform", "remote": "https://github.com/example-org/platform-harness"}]}`)
	if _, err := addRepoPlan(t, "api", "--worktree", "12-x"); err != nil {
		t.Fatal(err)
	}
	git(filepath.Join(state, "repos", "api"), "branch", "openclaw/12-x")

	intents, err := addRepoPlan(t, "api", "web", "--worktree", "12-x", "--worker", "a", "--work", "Add paging to the list endpoint.")
	if err != nil {
		t.Fatal(err)
	}
	// A worker reports back; the task's owners do not.
	for _, in := range intents {
		if in.Args.ExpectsCompletionMessage != nil {
			t.Errorf("worker %s: expectsCompletionMessage %v, want the default", in.Repo, *in.Args.ExpectsCompletionMessage)
		}
	}
	if owners, err := addRepoPlan(t, "web", "--worktree", "13-x"); err != nil || len(owners) == 0 {
		t.Fatal(owners, err)
	} else {
		for _, in := range owners {
			if in.Args.ExpectsCompletionMessage == nil || *in.Args.ExpectsCompletionMessage {
				t.Errorf("owner %s reports back", in.Repo)
			}
		}
	}
	// api has the task's branch, web does not yet: its registry default.
	if got, want := summary(intents), "api@api:openclaw/12-x web@web:origin/main"; got != want {
		t.Errorf("intents %s, want %s", got, want)
	}
	for _, in := range intents {
		if in.Args.WorktreeName != "12-x--a" || !strings.Contains(in.Args.Task, "Add paging to the list endpoint.") || !strings.Contains(in.Args.Task, "wtc catch-up --from") || !strings.Contains(in.Args.Task, "Never publish") {
			t.Errorf("worker intent: %+v", in.Args)
		}
	}
	if name, _, _ := wtc.GatewayHarness(filepath.Join(state, "wtc", "platform", "12-x--a")); name != "" {
		t.Errorf("the worker's collection recorded harness %q", name)
	}
	// Its folder goes beside the task's, in the task's harness.
	if got, want := openclaw.NewCollectionFolder(state, "12-x--a", ""), filepath.Join(state, "wtc", "platform", "12-x--a"); openclaw.CollectionFolder(state, "12-x") != filepath.Join(state, "wtc", "platform", "12-x") || got != want {
		t.Errorf("the worker's folder: %s, want %s", got, want)
	}
	// The harness itself can be a worker's repository, measured against the
	// task's base there.
	if intents, err := addRepoPlan(t, "example-org/platform-harness", "--worktree", "12-x", "--worker", "h", "--work", "Document paging."); err != nil || summary(intents) != "platform-harness@platform-harness:origin/main" {
		t.Errorf("a worker on the harness: %s %v", summary(intents), err)
	}
	// On a harness the task has not planned yet, a worker starts at the
	// harness's configured ref.
	withGatewayConfig(t, `{"harnesses": [{"name": "platform", "remote": "https://github.com/example-org/platform-harness", "ref": "develop"}]}`)
	if intents, err := addRepoPlan(t, "example-org/platform-harness", "--worktree", "13-y", "--worker", "h", "--work", "Document paging."); err != nil || summary(intents) != "platform-harness@platform-harness:origin/develop" {
		t.Errorf("a worker on an unplanned harness: %s %v", summary(intents), err)
	}
	withGatewayConfig(t, `{"harnesses": [{"name": "platform", "remote": "https://github.com/example-org/platform-harness"}]}`)
	// A worker is measured against the task's base, not the registry's.
	git(filepath.Join(state, "repos", "web"), "config", "branch.openclaw/12-x.wtcbase", "develop")
	if intents, err := addRepoPlan(t, "web", "--worktree", "12-x", "--worker", "b", "--work", "x"); err != nil || summary(intents) != "web@web:origin/develop" {
		t.Errorf("the task's base: %s %v", summary(intents), err)
	}
	if got := git(filepath.Join(state, "repos", "web"), "config", "branch.openclaw/12-x--b.wtcbase"); got != "develop" {
		t.Errorf("the worker's recorded base: %q", got)
	}
	// An explicit --base that is only a local branch, here another worker's,
	// is where the worker starts; it is still measured against the task's base.
	git(filepath.Join(state, "repos", "web"), "branch", "openclaw/12-x--b")
	if intents, err := addRepoPlan(t, "web", "--worktree", "12-x", "--worker", "c", "--work", "x", "--base", "openclaw/12-x--b"); err != nil || summary(intents) != "web@web:openclaw/12-x--b" {
		t.Errorf("a local --base: %s %v", summary(intents), err)
	}
	if got := git(filepath.Join(state, "repos", "web"), "config", "branch.openclaw/12-x--c.wtcbase"); got != "develop" {
		t.Errorf("the recorded base with a local --base: %q", got)
	}
	// Without a recorded base for the task, the local branch is not recorded
	// as the worker's either: the repository's default applies.
	api := filepath.Join(state, "repos", "api")
	git(api, "branch", "openclaw/14-z--a")
	if intents, err := addRepoPlan(t, "api", "--worktree", "14-z", "--worker", "b", "--work", "x", "--base", "openclaw/14-z--a"); err != nil || summary(intents) != "api@api:openclaw/14-z--a" {
		t.Errorf("a local --base without a task base: %s %v", summary(intents), err)
	}
	if out, err := exec.Command("git", "-C", api, "config", "--get", "branch.openclaw/14-z--b.wtcbase").Output(); err == nil {
		t.Errorf("a local --base was recorded as the worker's base: %q", out)
	}

	for args, want := range map[string]string{
		"api --worktree 12-x --worker a":                            "what should the worker do",
		"api --worktree 12-x --worker a --work x --separate":        "do not go together",
		"api --worktree 12-x --worker Bad --work x":                 "not a worker of 12-x",
		"api --worktree 12-x --worker a--b --work x":                "not a worker of 12-x",
		"example-org/elsewhere --worktree 12-x --worker a --work x": "a worker needs it there",
	} {
		if _, err := addRepoPlan(t, strings.Fields(args)...); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", args, err, want)
		}
	}
}

// The task's status lists its direct workers with what is left to merge, and
// catch-up --from merges one in.
func TestWorkersInStatusAndCatchUp(t *testing.T) {
	state, git := harnessGateway(t)
	api := filepath.Join(state, "repos", "api")
	dirs := map[string]string{}
	for _, w := range []string{"12-x", "12-x--a", "12-x--a--b"} {
		dirs[w] = filepath.Join(state, "worktrees", "t1", w)
		from := "HEAD"
		if w != "12-x" {
			from = "openclaw/12-x"
		}
		git(api, "worktree", "add", "-q", "-b", "openclaw/"+w, dirs[w], from)
		if w == "12-x" {
			continue
		}
		if err := os.WriteFile(filepath.Join(dirs[w], w+".md"), []byte(w+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		git(dirs[w], "add", ".")
		git(dirs[w], "commit", "-qm", w)
	}
	all := map[string][]string{}
	for w, d := range dirs {
		all[w] = []string{d}
	}
	workers := gatewayWorkers("12-x", all)
	if len(workers) != 1 || workers[0].String() != "worker a: api 1 to merge (wtc catch-up --from a)" {
		t.Fatalf("workers: %+v", workers)
	}
	if out, _ := json.Marshal(workers); !strings.Contains(string(out), `"collection":"12-x--a"`) || !strings.Contains(string(out), `"toMerge":1`) {
		t.Errorf("json: %s", out)
	}

	t.Chdir(dirs["12-x"])
	var asJSON bool
	root := &cobra.Command{Use: "wtc", SilenceUsage: true, SilenceErrors: true}
	addCatchUpCommand(root, &asJSON)
	root.SetArgs([]string{"catch-up", "--from", "a"})
	root.SetOut(io.Discard)
	stdout := os.Stdout
	devnull, _ := os.Open(os.DevNull)
	os.Stdout = devnull
	err := root.Execute()
	os.Stdout = stdout
	if err != nil {
		t.Fatal(err)
	}
	if workers := gatewayWorkers("12-x", all); workers[0].String() != "worker a: api merged" {
		t.Errorf("after catch-up --from: %+v", workers)
	}
}

// The add-repo commands for this collection that the question about an
// unlisted repository prints run as printed: choice 1, and the part of
// choice 3 that stays here. They keep the listed repositories and the
// harness this add-repo chose, which a new collection has not recorded yet.
func TestAddRepoUnlistedChoicesRunAsPrinted(t *testing.T) {
	_, _ = harnessGateway(t)
	for _, config := range []string{
		`{"harnesses": [{"name": "platform", "remote": "https://github.com/example-org/platform-harness"}, {"name": "ops", "remote": "git@github.com:example-org/ops-harness.git"}]}`,
		`{"harnesses": [{"name": "platform", "remote": "https://github.com/example-org/platform-harness"}, {"name": "ops", "remote": "git@github.com:example-org/ops-harness.git"}], "defaultHarness": "ops"}`,
	} {
		withGatewayConfig(t, config)
		for i, want := range map[int]string{1: "platform-harness@platform-harness:origin/main api@api:origin/develop tools@tools:", 3: "platform-harness@platform-harness:origin/main api@api:origin/develop"} {
			worktree := fmt.Sprintf("30-x-%d-%d", i, len(config))
			_, err := addRepoPlan(t, "api", "tools", "--worktree", worktree)
			if err == nil {
				t.Fatal("no question")
			}
			var line string
			for _, l := range strings.Split(err.Error(), "\n") {
				if strings.HasPrefix(l, fmt.Sprintf("  %d. ", i)) {
					line = l
				}
			}
			command := line[strings.Index(line, "wtc add-repo"):]
			if i == 3 {
				// The part for this collection.
				command = command[strings.Index(command, ", and here ")+len(", and here "):]
			}
			args := strings.Fields(strings.TrimPrefix(command, "wtc add-repo "))
			if i == 1 && !slices.Contains(args, worktree) {
				t.Errorf("choice 1 does not name the collection: %q", command)
			}
			intents, err := addRepoPlan(t, args...)
			if err != nil || !strings.Contains(summary(intents), want) {
				t.Errorf("%s choice %d %q: %s %v, want %s", config, i, command, summary(intents), err, want)
			}
		}
	}
}
