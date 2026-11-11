package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDevControlInvocationUsesCommandFlags(t *testing.T) {
	for _, args := range [][]string{
		{"--collection", "inspected", "review", "status", "widget", "1"},
		{"--collection=inspected", "review", "status", "widget", "1"},
		{"--json", "--collection", "inspected", "review", "--help"},
		{"-h", "review"},
		{"--source", "selected", "dev", "mode", "on"},
		{"--source=selected", "dev", "mode", "on"},
		{"--dir", "bundle", "review", "bundle", "widget", "1"},
	} {
		if !devControlInvocation(args) {
			t.Errorf("control command not recognized: %q", args)
		}
	}
	for _, args := range [][]string{
		{"status", "review"}, {"--json", "status"},
		{"--collection", "review", "status"}, {"--source=dev", "status"},
	} {
		if devControlInvocation(args) {
			t.Errorf("ordinary command bypassed following: %q", args)
		}
	}
}

func TestDevFollowCLIOverridesSemverPreservesArgsAndCanExit(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a CLI and disposable source")
	}
	collection := t.TempDir()
	source := filepath.Join(collection, "ext.wtc-cli")
	if err := os.MkdirAll(filepath.Join(source, "cmd", "wtc"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(collection, "harness"), 0755); err != nil {
		t.Fatal(err)
	}
	write := func(path, text string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(text), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(collection, "harness", ".harness-repos.yml"), "schema_version: 1\nrepos: []\n")
	write(filepath.Join(source, "go.mod"), "module github.com/lcorneliussen/wtc-cli\n\ngo 1.27.0\n")
	main := filepath.Join(source, "cmd", "wtc", "main.go")
	fixture := `package main
import("fmt";"os";"strings";"path/filepath")
var version="dev"
func main(){if len(os.Args)==4&&os.Args[1]=="--json"&&os.Args[2]=="dev"&&os.Args[3]=="capabilities"{fmt.Print("{\"ok\":true,\"data\":{\"dev_follow\":1,\"status_handover\":1}}");return};if len(os.Args)>1&&os.Args[1]=="fail"{os.Exit(7)};fmt.Println("first:"+strings.Join(os.Args[1:],"|")+":"+filepath.Base(os.Args[0]))}
`
	write(main, fixture)
	for _, args := range [][]string{{"init", "-q"}, {"config", "user.name", "Fixture"}, {"config", "user.email", "fixture@example.invalid"}, {"add", "."}, {"commit", "-qm", "Fixture"}, {"tag", "v0.1.0"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = source
		if raw, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git fixture: %v %s", err, raw)
		}
	}
	pinned := filepath.Join(t.TempDir(), "wtc")
	buildVersioned(t, pinned, "9.0.0")
	taba := filepath.Join(t.TempDir(), "taba")
	if err := os.Symlink(pinned, taba); err != nil {
		t.Fatal(err)
	}
	run := func(binary string, extraEnv []string, args ...string) (string, int) {
		cmd := exec.Command(binary, args...)
		cmd.Dir = collection
		cmd.Env = append(os.Environ(), "GOROOT=", "NO_COLOR=", "GOWORK=off", "WTC_DEV_BUILD=", "WTC_DEV_DELEGATED_FROM=", "WTC_DEV_AMBIENT_EXE=", "WTC_DEV_GENERATION=")
		cmd.Env = append(cmd.Env, extraEnv...)
		raw, err := cmd.CombinedOutput()
		if err == nil {
			return string(raw), 0
		}
		if exit, ok := err.(*exec.ExitError); ok {
			return string(raw), exit.ExitCode()
		}
		t.Fatal(err)
		return "", -1
	}
	if out, code := run(pinned, nil, "dev", "mode", "on", "--source", source); code != 0 {
		t.Fatalf("mode on: %d %s", code, out)
	}
	if out, code := run(taba, nil, "hello", "one two"); code != 0 || strings.TrimSpace(out) != "first:hello|one two:taba" {
		t.Fatalf("handover identity/args: %d %s", code, out)
	}
	write(main, strings.ReplaceAll(fixture, "first:", "second:"))
	if out, code := run(pinned, nil, "hello"); code != 0 || !strings.Contains(out, "second:hello:wtc") {
		t.Fatalf("changed source not followed: %d %s", code, out)
	}
	if out, code := run(pinned, []string{"WTC_DEV_DELEGATED_FROM=0.1.64"}, "hello"); code != 0 || !strings.Contains(out, "second:hello:wtc") {
		t.Fatalf("legacy version-only hop suppressed active freshness: %d %s", code, out)
	}
	if out, code := run(pinned, nil, "fail"); code != 7 {
		t.Fatalf("exit forwarding: %d %s", code, out)
	}
	// Explicit verifier bypass and review commands never select candidate code.
	if out, code := run(pinned, []string{"WTC_DEV_BUILD=off"}, "--version"); code != 0 || !strings.Contains(out, "9.0.0") {
		t.Fatalf("trusted bypass redirected: %d %s", code, out)
	}
	if out, code := run(pinned, nil, "review", "--help"); code != 0 || strings.Contains(out, "second:") {
		t.Fatalf("review command redirected: %d %s", code, out)
	}
	// A review target is operated context, never an alternate executable policy.
	inspected := t.TempDir()
	if err := os.MkdirAll(filepath.Join(inspected, "harness"), 0755); err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(inspected, "harness", ".harness-repos.yml"), "repos: []\n")
	write(filepath.Join(inspected, ".wtc-dev-mode.json"), `{"mode":"on","source":"/missing-unselected-fixture","revision":"fixture"}`)
	if out, code := run(taba, nil, "--collection", inspected, "review", "--help"); code != 0 || strings.Contains(out, "second:") || !strings.Contains(out, "Manage independent local PR reviews") {
		t.Fatalf("flagged review selected source from invocation or target: %d %s", code, out)
	}
	write(filepath.Join(collection, ".wtc-review-mode"), "review fixture")
	if out, code := run(pinned, nil, "--version"); code != 0 || !strings.Contains(out, "9.0.0") {
		t.Fatalf("review-only redirected: %d %s", code, out)
	}
	os.Remove(filepath.Join(collection, ".wtc-review-mode"))
	if out, code := run(pinned, nil, "dev", "install", "--source", source); code != 0 || !strings.Contains(out, "mode on follows") {
		t.Fatalf("install summary ignored active source policy: %d %s", code, out)
	}
	write(main, "broken source")
	if out, code := run(pinned, nil, "hello"); code == 0 || strings.Contains(out, "second:") {
		t.Fatalf("stale code executed: %d %s", code, out)
	}
	if out, code := run(pinned, nil, "--collection", inspected, "review", "--help"); code != 0 || !strings.Contains(out, "Manage independent local PR reviews") {
		t.Fatalf("flagged review required broken candidate source: %d %s", code, out)
	}
	if out, code := run(pinned, nil, "dev", "mode", "off"); code != 0 {
		t.Fatalf("broken-source OFF: %d %s", code, out)
	}
	if out, code := run(pinned, nil, "--version"); code != 0 || !strings.Contains(out, "9.0.0") {
		t.Fatalf("persistent OFF lost: %d %s", code, out)
	}
	if out, code := run(pinned, nil, "--json", "dev", "status"); code != 0 {
		t.Fatalf("mode status: %d %s", code, out)
	} else {
		var body struct {
			Data struct{ Follow struct{ Mode string } }
		}
		if json.Unmarshal([]byte(out), &body) != nil || body.Data.Follow.Mode != "off" {
			t.Fatalf("status mode: %s", out)
		}
	}
}

func buildVersioned(t *testing.T, out, version string) {
	t.Helper()
	cmd := exec.Command("go", "build", "-ldflags", "-X main.version="+version, "-o", out, ".")
	if raw, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, raw)
	}
}

// The pinned binary hands over to a newer dev build, runs itself when the dev
// build is older, and the dev build never hands over again.
func TestPinnedBinaryHandsOverToDevBuild(t *testing.T) {
	if testing.Short() {
		t.Skip("builds two binaries")
	}
	collection := t.TempDir()
	if err := os.MkdirAll(filepath.Join(collection, "harness"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(collection, "harness", ".harness-repos.yml"), []byte("schema_version: 1\nrepositories: []\n"), 0644); err != nil {
		t.Fatal(err)
	}
	devDir := filepath.Join(collection, ".wtc", "dev")
	if err := os.MkdirAll(devDir, 0755); err != nil {
		t.Fatal(err)
	}
	pinned := filepath.Join(t.TempDir(), "wtc")
	buildVersioned(t, pinned, "0.1.39")
	devVersion := "0.1.40-dev.2+dev.gabc1234"
	buildVersioned(t, filepath.Join(devDir, "wtc"), devVersion)
	writeVersion := func(v string) {
		if err := os.WriteFile(filepath.Join(devDir, "version"), []byte(v+"\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	writeVersion(devVersion)
	// Handover executes the legacy-named dev artifact but must retain the
	// invocation used by its caller for help, version and command hints.
	taba := filepath.Join(t.TempDir(), "taba")
	if err := os.Symlink(pinned, taba); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"--version"}, {"svc", "add", "--help"}} {
		cmd := exec.Command(taba, args...)
		cmd.Dir = filepath.Join(collection, "harness")
		cmd.Env = append(os.Environ(), "WTC_DEV_DELEGATED_FROM=", "WTC_DEV_BUILD=")
		raw, err := cmd.CombinedOutput()
		if err != nil || !strings.Contains(string(raw), "taba ") || strings.Contains(string(raw), "wtc version") || strings.Contains(string(raw), "wtc svc") {
			t.Fatalf("dev handover lost invocation identity: %v %s", err, raw)
		}
	}
	run := func(env ...string) string {
		cmd := exec.Command(pinned, "--version")
		cmd.Dir = filepath.Join(collection, "harness")
		cmd.Env = append(os.Environ(), env...)
		raw, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("wtc --version: %v\n%s", err, raw)
		}
		return strings.TrimSpace(string(raw))
	}
	if got := run("WTC_DEV_DELEGATED_FROM=", "WTC_DEV_BUILD="); !strings.Contains(got, devVersion+" (dev build, in place of 0.1.39)") {
		t.Fatalf("pinned binary did not hand over: %q", got)
	}
	if got := run("WTC_DEV_DELEGATED_FROM=", "WTC_DEV_BUILD=off"); !strings.Contains(got, "version 0.1.39") {
		t.Fatalf("WTC_DEV_BUILD=off must keep the pinned binary: %q", got)
	}
	// An ad-hoc collection (no harness repository) hands over the same way.
	adHoc := t.TempDir()
	if err := os.MkdirAll(filepath.Join(adHoc, ".wtc", "config"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(adHoc, ".wtc", "config", "repos.yml"), []byte("schema_version: 1\nrepositories: []\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(adHoc, ".wtc", "dev"), 0755); err != nil {
		t.Fatal(err)
	}
	buildVersioned(t, filepath.Join(adHoc, ".wtc", "dev", "wtc"), devVersion)
	if err := os.WriteFile(filepath.Join(adHoc, ".wtc", "dev", "version"), []byte(devVersion+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	adHocRun := exec.Command(pinned, "--version")
	adHocRun.Dir = adHoc
	adHocRun.Env = append(os.Environ(), "WTC_DEV_DELEGATED_FROM=", "WTC_DEV_BUILD=")
	if raw, err := adHocRun.CombinedOutput(); err != nil || !strings.Contains(string(raw), devVersion+" (dev build, in place of 0.1.39)") {
		t.Fatalf("ad-hoc collection did not hand over: %q %v", raw, err)
	}
	// Both namespaces can move while .wtc remains for generated files.
	if err := os.MkdirAll(filepath.Join(adHoc, ".taba"), 0755); err != nil {
		t.Fatal(err)
	}
	for _, namespace := range []string{"config", "dev"} {
		if err := os.Rename(filepath.Join(adHoc, ".wtc", namespace), filepath.Join(adHoc, ".taba", namespace)); err != nil {
			t.Fatal(err)
		}
	}
	adHocRun = exec.Command(taba, "--version")
	adHocRun.Dir = adHoc
	adHocRun.Env = append(os.Environ(), "WTC_DEV_DELEGATED_FROM=", "WTC_DEV_BUILD=")
	if raw, err := adHocRun.CombinedOutput(); err != nil || !strings.HasPrefix(string(raw), "taba version "+devVersion+" (dev build, in place of 0.1.39)") {
		t.Fatalf("alternate namespaces lost dev handover or invocation: %q %v", raw, err)
	}
	// The dev build run directly, through a symlinked collection path, is
	// itself and must not hand over to itself.
	link := filepath.Join(t.TempDir(), "collection")
	if err := os.Symlink(collection, link); err != nil {
		t.Fatal(err)
	}
	self := exec.Command(filepath.Join(link, ".wtc", "dev", "wtc"), "--version")
	self.Dir = filepath.Join(link, "harness")
	self.Env = append(os.Environ(), "WTC_DEV_DELEGATED_FROM=", "WTC_DEV_BUILD=")
	if raw, err := self.CombinedOutput(); err != nil || strings.TrimSpace(string(raw)) != "wtc version "+devVersion {
		t.Fatalf("the dev build re-executed itself: %q %v", raw, err)
	}
	// The pinned release moved past the build: it keeps running itself.
	writeVersion("0.1.38-dev.1+dev.gabc1234")
	if got := run("WTC_DEV_DELEGATED_FROM=", "WTC_DEV_BUILD="); !strings.Contains(got, "version 0.1.39") || strings.Contains(got, "dev build,") {
		t.Fatalf("an older dev build must not take over: %q", got)
	}
}

// The handover marker belongs to one process: children such as hooks, agents
// and herdr panes must not inherit it, or they would never hand over.
func TestDelegatedMarkerIsNotInherited(t *testing.T) {
	t.Setenv("WTC_DEV_DELEGATED_FROM", "0.1.39")
	takeDelegatedFrom()
	defer func() { delegatedFrom = "" }()
	if delegatedFrom != "0.1.39" || !strings.Contains(versionLabel(), "in place of 0.1.39") {
		t.Fatalf("marker not taken: %q", delegatedFrom)
	}
	if _, set := os.LookupEnv("WTC_DEV_DELEGATED_FROM"); set {
		t.Fatal("the marker is still in the environment for child processes")
	}
	child, err := exec.Command("/usr/bin/env").Output()
	if err != nil || strings.Contains(string(child), "WTC_DEV_DELEGATED_FROM") {
		t.Fatalf("a child process inherited the marker: %v", err)
	}
}

func TestDevOrdinaryStartupCapturesAmbient(t *testing.T) {
	oldFrom, oldAmbient, oldGeneration := delegatedFrom, devAmbientExecutable, devRunningGeneration
	defer func() { delegatedFrom, devAmbientExecutable, devRunningGeneration = oldFrom, oldAmbient, oldGeneration }()
	t.Setenv("WTC_DEV_DELEGATED_FROM", "")
	t.Setenv(devAmbientEnv, "")
	t.Setenv(devGenerationEnv, "")
	takeDelegatedFrom()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if devAmbientExecutable != self {
		t.Fatalf("ordinary startup lost ambient executable: got %q, want %q", devAmbientExecutable, self)
	}
}
