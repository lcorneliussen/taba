package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestProjectRootSelectionWithRealMise runs the whole root path with the real
// mise in an isolated configuration and no global CLI default: the shim fails
// at the project root, `env root` generates the root selection, and the shim
// then runs the selected release, which creates a collection with `new`.
// Releases are linked locally, so nothing is downloaded.
func TestProjectRootSelectionWithRealMise(t *testing.T) {
	mise, err := exec.LookPath("mise")
	if err != nil {
		if os.Getenv("TABA_TEST_REQUIRE_MISE") != "" {
			t.Fatal("mise is required by TABA_TEST_REQUIRE_MISE")
		}
		t.Skip("mise is not installed")
	}
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not installed")
	}
	const version = "0.2.9-synthetic.1"
	const tool = "github:lcorneliussen/taba"
	work, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(work, "home")
	state := filepath.Join(work, "mise")
	for _, dir := range []string{home, filepath.Join(state, "data", "shims"), filepath.Join(state, "config"), filepath.Join(state, "cache"), filepath.Join(state, "state")} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	global := filepath.Join(state, "global.toml")
	if err := os.WriteFile(global, nil, 0644); err != nil {
		t.Fatal(err)
	}
	gitconfig := filepath.Join(home, ".gitconfig")
	if err := os.WriteFile(gitconfig, []byte("[user]\n\tname = synthetic\n\temail = synthetic@example.invalid\n[init]\n\tdefaultBranch = main\n[commit]\n\tgpgsign = false\n"), 0644); err != nil {
		t.Fatal(err)
	}
	shims := filepath.Join(state, "data", "shims")
	// Only mise and git from the caller: a shim without a selection falls back
	// to the next executable on PATH, which must not be an installed CLI.
	tools := filepath.Join(work, "tools")
	if err := os.Mkdir(tools, 0755); err != nil {
		t.Fatal(err)
	}
	for name, path := range map[string]string{"mise": mise, "git": git} {
		if err := os.Symlink(path, filepath.Join(tools, name)); err != nil {
			t.Fatal(err)
		}
	}
	env := []string{
		"PATH=" + shims + ":" + tools + ":/usr/bin:/bin",
		"HOME=" + home,
		"MISE_DATA_DIR=" + filepath.Join(state, "data"),
		"MISE_CONFIG_DIR=" + filepath.Join(state, "config"),
		"MISE_CACHE_DIR=" + filepath.Join(state, "cache"),
		"MISE_STATE_DIR=" + filepath.Join(state, "state"),
		"MISE_GLOBAL_CONFIG_FILE=" + global,
		"MISE_SYSTEM_CONFIG_FILE=" + filepath.Join(state, "system.toml"),
		"MISE_OFFLINE=1",
		"MISE_YES=1",
		"GIT_CONFIG_GLOBAL=" + gitconfig,
		"GIT_CONFIG_NOSYSTEM=1",
		"TABA_DEV_BUILD=off",
		"WTC_DEV_BUILD=off",
	}
	run := func(dir string, name string, args ...string) (string, error) {
		t.Helper()
		// Resolve within the isolated PATH, not this test process's PATH.
		switch name {
		case "taba":
			name = filepath.Join(shims, "taba")
		case "git":
			name = filepath.Join(tools, "git")
		}
		cmd := exec.Command(name, args...)
		cmd.Dir, cmd.Env = dir, env
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	must := func(dir string, name string, args ...string) string {
		t.Helper()
		out, err := run(dir, name, args...)
		if err != nil {
			t.Fatalf("%s %v in %s: %v\n%s", name, args, dir, err, out)
		}
		return out
	}

	// The release the harness pins, linked into the isolated mise.
	release := filepath.Join(work, "release")
	if err := os.MkdirAll(filepath.Join(release, "bin"), 0755); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(release, "bin", "taba")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	must(work, mise, "link", "-f", tool+"@"+version, release)
	// Offline reshim skips linked installs; this is the symlink shim mise makes.
	if err := os.Symlink(mise, filepath.Join(shims, "taba")); err != nil {
		t.Fatal(err)
	}

	// A synthetic project: harness origin, shared owner, one collection.
	origin := filepath.Join(work, "origin", "harness")
	project := filepath.Join(work, "project")
	files := map[string]string{
		".harness-repos.yml": "schema_version: 1\nrepositories:\n  - name: harness\n    remote: " + origin + "\n    default_ref: origin/main\n",
		".taba-version":      version + "\n",
		"taba.toml":          "[compatibility]\nrequires = \">=0.2.0,<0.3\"\n",
	}
	if err := os.MkdirAll(origin, 0755); err != nil {
		t.Fatal(err)
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(origin, name), []byte(data), 0644); err != nil {
			t.Fatal(err)
		}
	}
	must(origin, "git", "init", "-q")
	must(origin, "git", "add", "-A")
	must(origin, "git", "commit", "-qm", "harness")
	owner := filepath.Join(project, ".bare", "harness.git")
	must(work, "git", "clone", "-q", "--bare", origin, owner)
	must(work, "git", "--git-dir="+owner, "config", "remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/*")
	must(work, "git", "--git-dir="+owner, "fetch", "-q", "--prune", "origin")
	must(work, "git", "--git-dir="+owner, "worktree", "add", "-q", "--detach", filepath.Join(project, "main", "harness"), "origin/main")
	main := filepath.Join(project, "main")
	must(main, binary, "env", "setup")

	// An unrelated project beside it must stay untouched.
	unrelated := filepath.Join(work, "unrelated")
	unrelatedConfig := "[tools]\n\"" + tool + "\" = \"0.0.1\"\n"
	if err := os.MkdirAll(unrelated, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(unrelated, "mise.toml"), []byte(unrelatedConfig), 0644); err != nil {
		t.Fatal(err)
	}

	if out := must(main, "taba", "--version"); !strings.Contains(out, "version") {
		t.Fatalf("collection shim: %s", out)
	}
	// Failing before: no selection at the root and no project to create from.
	if out, err := run(project, "taba", "--version"); err == nil {
		t.Fatalf("root shim resolved without a root selection: %s", out)
	}
	if out, err := run(project, binary, "new", "early", "--no-open"); err == nil {
		t.Fatalf("root new succeeded without a root selection: %s", out)
	}

	must(main, "taba", "env", "root")
	// The real mise reads the generated file, including its [_.taba]
	// metadata, without complaint and selects the pinned release.
	if out := must(project, mise, "config", "ls"); !strings.Contains(out, filepath.Join(".mise", "config.toml")) {
		t.Fatalf("mise does not read the root selection:\n%s", out)
	}
	current := must(project, mise, "current", tool)
	if strings.TrimSpace(current) != version {
		t.Fatalf("root selects %q, want %s", current, version)
	}
	if out := must(project, mise, "which", "taba"); !strings.HasPrefix(strings.TrimSpace(out), filepath.Join(state, "data", "installs")) {
		t.Fatalf("root resolves another executable: %s", out)
	}
	if out := must(project, mise, "env"); strings.Contains(out, "WTC_") || strings.Contains(out, "TABA_") || strings.Contains(out, "PORT") {
		t.Fatalf("root receives collection environment:\n%s", out)
	}

	// Precedence: a collection's own selection wins inside it, and user root
	// configuration wins at the root and is reported, not changed.
	collectionConfig := filepath.Join(main, "mise.toml")
	data, err := os.ReadFile(collectionConfig)
	if err != nil {
		t.Fatal(err)
	}
	override := strings.Replace(string(data), version, "0.2.9-synthetic.2", 1)
	if err := os.WriteFile(collectionConfig, []byte(override), 0644); err != nil {
		t.Fatal(err)
	}
	must(main, mise, "trust", collectionConfig)
	if out := strings.TrimSpace(must(main, mise, "current", tool)); out != "0.2.9-synthetic.2" {
		t.Fatalf("collection selection lost precedence: %q", out)
	}
	if err := os.WriteFile(collectionConfig, data, 0644); err != nil {
		t.Fatal(err)
	}
	userRoot := "[tools]\n\"" + tool + "\" = \"0.0.2\"\n"
	if err := os.WriteFile(filepath.Join(project, "mise.toml"), []byte(userRoot), 0644); err != nil {
		t.Fatal(err)
	}
	must(project, mise, "trust", filepath.Join(project, "mise.toml"))
	if out := strings.TrimSpace(must(project, mise, "current", tool)); out != "0.0.2" {
		t.Fatalf("user root configuration lost precedence: %q", out)
	}
	if out := must(project, binary, "env", "root", "--check"); !strings.Contains(out, "root override:   mise.toml") {
		t.Fatalf("user root override not reported:\n%s", out)
	}
	if err := os.Remove(filepath.Join(project, "mise.toml")); err != nil {
		t.Fatal(err)
	}

	// The selected CLI runs from the root and creates a collection there.
	if out := must(project, "taba", "new", "demo", "--no-open"); !strings.Contains(out, "done:") {
		t.Fatalf("root new: %s", out)
	}
	tip := strings.TrimSpace(must(work, "git", "--git-dir="+owner, "rev-parse", "origin/main"))
	if head := strings.TrimSpace(must(work, "git", "-C", filepath.Join(project, "demo", "harness"), "rev-parse", "HEAD")); head != tip {
		t.Fatalf("new collection harness at %s, want %s", head, tip)
	}
	if out := strings.TrimSpace(must(filepath.Join(project, "demo"), mise, "current", tool)); out != version {
		t.Fatalf("new collection selects %q", out)
	}
	if out := must(filepath.Join(project, "demo"), "taba", "env", "root", "--check"); !strings.Contains(out, "current") || !strings.Contains(out, "collection:      demo selects "+version) {
		t.Fatalf("diagnostics:\n%s", out)
	}

	// Global and unrelated configuration are unchanged.
	if data, _ := os.ReadFile(global); len(data) != 0 {
		t.Fatalf("global mise configuration changed: %q", data)
	}
	if entries, _ := os.ReadDir(filepath.Join(state, "config")); len(entries) != 0 {
		t.Fatalf("mise config directory changed: %v", entries)
	}
	if data, _ := os.ReadFile(filepath.Join(unrelated, "mise.toml")); string(data) != unrelatedConfig {
		t.Fatalf("unrelated project changed: %q", data)
	}
	if _, err := os.Lstat(filepath.Join(unrelated, ".mise")); !os.IsNotExist(err) {
		t.Fatalf("unrelated project received a root selection: %v", err)
	}
}
