package wtc

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func devFollowFixture(t *testing.T) (string, string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	collection := filepath.Join(root, "collection")
	source := filepath.Join(collection, "ext.wtc-cli")
	fixtureFile(t, filepath.Join(collection, "harness", ".harness-repos.yml"), "schema_version: 1\nrepos: [{name: wtc-cli, remote: 'https://github.com/example/cli.git'}]\n", 0644)
	fixtureFile(t, filepath.Join(source, "go.mod"), "module github.com/lcorneliussen/wtc-cli\n\ngo 1.27.0\n", 0644)
	fixtureFile(t, filepath.Join(source, "cmd", "wtc", "main.go"), `package main
import ("fmt"; "os")
var version = "dev"
func main(){
 if len(os.Args)==4&&os.Args[1]=="--json"&&os.Args[2]=="dev"&&os.Args[3]=="capabilities" {fmt.Print("{\"ok\":true,\"data\":{\"dev_follow\":1,\"status_handover\":1}}");return}
 fmt.Println("first")
}
`, 0644)
	for _, args := range [][]string{{"init", "-q"}, {"config", "user.name", "Fixture"}, {"config", "user.email", "fixture@example.invalid"}, {"add", "."}, {"commit", "-qm", "Fixture"}, {"tag", "v0.1.0"}, {"remote", "add", "origin", "https://github.com/example/cli.git"}} {
		command := exec.Command("git", args...)
		command.Dir = source
		if out, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git fixture: %v %s", err, out)
		}
	}
	t.Setenv("GOWORK", "off")
	return collection, source
}

func ensureDevTest(t *testing.T, collection string) DevFollowState {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	state, err := EnsureDevFollow(ctx, collection)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func TestDevFollowModesConventionAndPersistentExit(t *testing.T) {
	collection, source := devFollowFixture(t)
	state, err := DevFollowConfiguration(collection)
	if err != nil || state.Active {
		t.Fatalf("reference checkout activated without policy: %+v %v", state, err)
	}
	fixtureFile(t, filepath.Join(collection, "harness", "wtc.toml"), "[dev]\nauto_source = 'wtc-cli'\n", 0644)
	state, err = DevFollowConfiguration(collection)
	if err != nil || !state.Active || state.Source != source {
		t.Fatalf("explicit convention did not resolve ext alias: %+v %v", state, err)
	}
	// Role is free-form metadata and grants no execution authority.
	fixtureFile(t, filepath.Join(collection, "harness", "wtc.toml"), "", 0644)
	if state, _ := DevFollowConfiguration(collection); state.Active {
		t.Fatal("reference-only checkout activated")
	}
	if err := SetDevMode(collection, "on", source); err != nil {
		t.Fatal(err)
	}
	if state := ensureDevTest(t, collection); state.Build == nil || state.Build.Generation == "" {
		t.Fatalf("no immutable generation: %+v", state)
	}
	fixtureFile(t, filepath.Join(collection, "harness", "wtc.toml"), "[dev]\nauto_source = 'wtc-cli'\n", 0644)
	build := ReadDevBuild(collection)
	if _, err := UninstallDevBuild(collection); err != nil {
		t.Fatal(err)
	}
	state = ensureDevTest(t, collection)
	if state.Mode != "off" || state.Active || ReadDevBuild(collection) != nil {
		t.Fatalf("convention reactivated after uninstall: %+v", state)
	}
	if _, err := os.Stat(build.Binary); err != nil {
		t.Fatal("uninstall removed an executable needed by a running action")
	}
	if err := SetDevMode(collection, "auto", ""); err != nil {
		t.Fatal(err)
	}
	if state, err := DevFollowConfiguration(collection); err != nil || !state.Active {
		t.Fatalf("explicit auto did not restore convention: %+v %v", state, err)
	}
	for _, marker := range reviewModeNames {
		fixtureFile(t, filepath.Join(collection, marker), "invalid but review-only", 0644)
		if state, err := DevFollowConfiguration(collection); err != nil || state.Active {
			t.Fatalf("review context activated: %+v %v", state, err)
		}
		os.Remove(filepath.Join(collection, marker))
	}
}

func TestDevFollowRejectsAmbiguityAliasesAndUnsafeState(t *testing.T) {
	collection, source := devFollowFixture(t)
	fixtureFile(t, filepath.Join(collection, "harness", "wtc.toml"), "[dev]\nauto_source = 'wtc-cli'\n", 0644)
	second := filepath.Join(collection, "other-cli")
	fixtureFile(t, filepath.Join(second, "go.mod"), "module github.com/lcorneliussen/wtc-cli\n", 0644)
	if _, err := DevFollowConfiguration(collection); err == nil {
		t.Fatal("ambiguous module candidates accepted")
	}
	os.RemoveAll(second)
	if err := SetDevMode(collection, "on", source); err != nil {
		t.Fatal(err)
	}
	policy, err := os.ReadFile(filepath.Join(collection, devPolicyFile))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(collection, devPolicyFile), filepath.Join(collection, ".taba-dev-mode.json")); err != nil {
		t.Fatal(err)
	}
	if state, err := DevFollowConfiguration(collection); err != nil || !state.Active {
		t.Fatalf("alias policy not read: %+v %v", state, err)
	}
	var conflicting DevMode
	json.Unmarshal(policy, &conflicting)
	conflicting.Mode = "off"
	raw, _ := json.Marshal(conflicting)
	fixtureFile(t, filepath.Join(collection, devPolicyFile), string(raw), 0600)
	if _, err := DevFollowConfiguration(collection); err == nil {
		t.Fatal("conflicting policy aliases accepted")
	}
	if err := SetDevMode(collection, "off", ""); err != nil {
		t.Fatal(err)
	}
	os.Remove(filepath.Join(collection, devPolicyFile))
	outside := filepath.Join(t.TempDir(), "keep")
	fixtureFile(t, outside, "untouched", 0600)
	if err := os.Symlink(outside, filepath.Join(collection, devPolicyFile)); err != nil {
		t.Fatal(err)
	}
	if err := SetDevMode(collection, "off", ""); err == nil {
		t.Fatal("recovery replaced a linked policy")
	}
	if raw, _ := os.ReadFile(outside); string(raw) != "untouched" {
		t.Fatal("linked state target changed")
	}
}

func TestDevFollowDirtyInputsFailureAndImmutableGeneration(t *testing.T) {
	collection, source := devFollowFixture(t)
	if err := SetDevMode(collection, "on", source); err != nil {
		t.Fatal(err)
	}
	first := ensureDevTest(t, collection).Build
	main := filepath.Join(source, "cmd", "wtc", "main.go")
	raw, _ := os.ReadFile(main)
	for _, value := range []string{"second", "third"} {
		fixtureFile(t, main, strings.ReplaceAll(string(raw), "first", value), 0644)
		state := ensureDevTest(t, collection)
		if state.Build.Generation == first.Generation || state.Build.Binary == first.Binary {
			t.Fatal("dirty edit reused old generation")
		}
		out, err := exec.Command(state.Build.Binary).Output()
		if err != nil || strings.TrimSpace(string(out)) != value {
			t.Fatalf("generation mismatch: %v %s", err, out)
		}
	}
	last := ReadDevBuild(collection)
	fixtureFile(t, main, "not valid Go", 0644)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := EnsureDevFollow(ctx, collection); err == nil {
		t.Fatal("broken source reported fresh")
	}
	if current := ReadDevBuild(collection); current == nil || current.Generation != last.Generation {
		t.Fatal("failed build replaced last good manifest")
	}
	if out, err := exec.Command(first.Binary).Output(); err != nil || strings.TrimSpace(string(out)) != "first" {
		t.Fatalf("published generation was overwritten: %v %s", err, out)
	}
}

func TestDevFingerprintIncludesIgnoredEmbedsAndMutableLocalModules(t *testing.T) {
	_, source := devFollowFixture(t)
	main := filepath.Join(source, "cmd", "wtc", "main.go")
	fixtureFile(t, main, "package main\nimport(_ \"embed\"; \"fmt\")\n//go:embed ignored.txt\nvar text string\nfunc main(){fmt.Print(text)}\n", 0644)
	fixtureFile(t, filepath.Join(source, ".gitignore"), "ignored.txt\n", 0644)
	asset := filepath.Join(source, "cmd", "wtc", "ignored.txt")
	fixtureFile(t, asset, "one", 0644)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	a, err := devFingerprint(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	fixtureFile(t, asset, "two", 0644)
	b, err := devFingerprint(ctx, source)
	if err != nil || a == b {
		t.Fatalf("ignored embed edit not detected: %v", err)
	}
	dependency := filepath.Join(filepath.Dir(filepath.Dir(source)), "library")
	fixtureFile(t, filepath.Join(dependency, "go.mod"), "module example.invalid/library\n\ngo 1.27.0\n", 0644)
	lib := filepath.Join(dependency, "value.go")
	fixtureFile(t, lib, "package library\nconst Value = 1\n", 0644)
	fixtureFile(t, main, "package main\nimport(\"fmt\"; \"example.invalid/library\")\nfunc main(){fmt.Print(library.Value)}\n", 0644)
	fixtureFile(t, filepath.Join(source, "go.mod"), "module github.com/lcorneliussen/wtc-cli\n\ngo 1.27.0\nrequire example.invalid/library v0.0.0\nreplace example.invalid/library => ../../library\n", 0644)
	a, err = devFingerprint(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	fixtureFile(t, lib, "package library\nconst Value = 2\n", 0644)
	b, err = devFingerprint(ctx, source)
	if err != nil || a == b {
		t.Fatalf("local replace edit not detected: %v", err)
	}
	work := filepath.Join(filepath.Dir(dependency), "go.work")
	fixtureFile(t, work, "go 1.27.0\nuse (\n./collection/ext.wtc-cli\n./library\n)\n", 0644)
	t.Setenv("GOWORK", work)
	a, err = devFingerprint(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	fixtureFile(t, work, "// changed workspace input\ngo 1.27.0\nuse (\n./collection/ext.wtc-cli\n./library\n)\n", 0644)
	b, err = devFingerprint(ctx, source)
	if err != nil || a == b {
		t.Fatalf("workspace contents not detected: %v", err)
	}
}

// The compiler boundary is a disposable PATH wrapper, not a production
// scheduling hook. Its marker acknowledges the exact external operation.
func devBlockGo(t *testing.T, operation string) (string, string, string) {
	t.Helper()
	realGo, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	started, release, calls := filepath.Join(dir, "started"), filepath.Join(dir, "release"), filepath.Join(dir, "calls")
	fixtureFile(t, filepath.Join(dir, "go"), "#!/bin/sh\nif [ \"$1\" = \"$DEV_TEST_OPERATION\" ]; then\n echo call >> \"$DEV_TEST_CALLS\"\n : > \"$DEV_TEST_STARTED\"\n while [ ! -e \"$DEV_TEST_RELEASE\" ]; do sleep 0.02; done\nfi\nexec \"$DEV_TEST_GO\" \"$@\"\n", 0755)
	t.Setenv("DEV_TEST_GO", realGo)
	t.Setenv("DEV_TEST_OPERATION", operation)
	t.Setenv("DEV_TEST_CALLS", calls)
	t.Setenv("DEV_TEST_STARTED", started)
	t.Setenv("DEV_TEST_RELEASE", release)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Cleanup(func() { _ = os.WriteFile(release, nil, 0600) })
	return started, release, calls
}

func waitDevMarker(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("Go operation did not reach marker %s", path)
}

func TestDevFollowOffDuringBuildAndCachedInputRead(t *testing.T) {
	for _, operation := range []string{"build", "list"} {
		t.Run(operation, func(t *testing.T) {
			collection, source := devFollowFixture(t)
			if err := SetDevMode(collection, "on", source); err != nil {
				t.Fatal(err)
			}
			started, release, _ := devBlockGo(t, operation)
			generation := ""
			if operation == "list" {
				t.Setenv("DEV_TEST_OPERATION", "")
				generation = ensureDevTest(t, collection).Build.Generation
				t.Setenv("DEV_TEST_OPERATION", operation)
				selected, err := WithDevGeneration(context.Background(), collection, generation, func(*DevBuild) error { return nil })
				if !selected || err != nil {
					t.Fatalf("positive generation selection: %v %v", selected, err)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			type result struct {
				state DevFollowState
				err   error
			}
			done := make(chan result, 1)
			go func() { s, e := EnsureDevFollow(ctx, collection); done <- result{s, e} }()
			waitDevMarker(t, started)
			if err := SetDevMode(collection, "off", ""); err != nil {
				t.Fatal(err)
			}
			fixtureFile(t, release, "release", 0600)
			r := <-done
			if r.err == nil && r.state.Active {
				t.Fatalf("OFF lost to %s: %+v", operation, r.state)
			}
			if generation != "" {
				called, err := WithDevGeneration(ctx, collection, generation, func(*DevBuild) error { t.Fatal("disabled delegate executed"); return nil })
				if called || err != nil {
					t.Fatalf("disabled selection: %v %v", called, err)
				}
			}
			if operation == "build" && ReadDevBuild(collection) != nil {
				t.Fatal("build published after OFF")
			}
		})
	}
}

func TestDevFollowBuildLockAndSharedSuccessfulGeneration(t *testing.T) {
	collection, source := devFollowFixture(t)
	if err := SetDevMode(collection, "on", source); err != nil {
		t.Fatal(err)
	}
	started, release, calls := devBlockGo(t, "build")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := EnsureDevFollow(ctx, collection); done <- err }()
	waitDevMarker(t, started)
	// Acknowledged blocked compilation holds the actual shared lock; an
	// independent lock attempt must time out before the compiler is released.
	short, stop := context.WithTimeout(ctx, 100*time.Millisecond)
	unlock, err := lockDevFile(short, filepath.Join(collection, ".wtc-dev-build.lock"))
	stop()
	if err == nil {
		unlock()
		t.Fatal("build did not hold shared lock")
	}
	fixtureFile(t, release, "release", 0600)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	first := ReadDevBuild(collection)
	second := ensureDevTest(t, collection).Build
	if first == nil || second == nil || first.Generation != second.Generation {
		t.Fatal("generation not reused")
	}
	raw, _ := os.ReadFile(calls)
	if strings.Count(string(raw), "call") != 1 {
		t.Fatalf("redundant compile: %s", raw)
	}
}

func TestDevFollowDiscardsSourceChangedDuringCompilation(t *testing.T) {
	collection, source := devFollowFixture(t)
	if err := SetDevMode(collection, "on", source); err != nil {
		t.Fatal(err)
	}
	started, release, calls := devBlockGo(t, "build")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := EnsureDevFollow(ctx, collection); done <- err }()
	waitDevMarker(t, started)
	main := filepath.Join(source, "cmd", "wtc", "main.go")
	raw, _ := os.ReadFile(main)
	fixtureFile(t, main, strings.ReplaceAll(string(raw), "first", "changed"), 0644)
	fixtureFile(t, release, "release", 0600)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	build := ReadDevBuild(collection)
	if build == nil {
		t.Fatal("no final generation")
	}
	fingerprint, err := devFingerprint(ctx, source)
	if err != nil || build.Generation != fingerprint {
		t.Fatalf("stale generation published: %+v %v", build, err)
	}
	output, err := exec.Command(build.Binary).Output()
	if err != nil || strings.TrimSpace(string(output)) != "changed" {
		t.Fatalf("wrong executable: %v %s", err, output)
	}
	counts, _ := os.ReadFile(calls)
	if strings.Count(string(counts), "call") != 2 {
		t.Fatalf("changed attempt not discarded and rebuilt: %s", counts)
	}
}

func TestDevFollowFailureBackoffAndUnsafePublication(t *testing.T) {
	collection, source := devFollowFixture(t)
	if err := SetDevMode(collection, "on", source); err != nil {
		t.Fatal(err)
	}
	good := ensureDevTest(t, collection).Build
	main := filepath.Join(source, "cmd", "wtc", "main.go")
	validMain, err := os.ReadFile(main)
	if err != nil {
		t.Fatal(err)
	}
	fixtureFile(t, main, "package main\nfunc main(){notDefined()}\n", 0644)
	started, release, calls := devBlockGo(t, "build")
	fixtureFile(t, release, "release", 0600)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := EnsureDevFollow(ctx, collection); err == nil {
		t.Fatal("invalid program compiled")
	}
	waitDevMarker(t, started)
	if _, err := EnsureDevFollow(ctx, collection); err == nil || !strings.Contains(err.Error(), "last build failed") {
		t.Fatalf("failure not cached: %v", err)
	}
	raw, _ := os.ReadFile(calls)
	if strings.Count(string(raw), "call") != 1 {
		t.Fatalf("unchanged failed source caused build storm: %s", raw)
	}
	if current := ReadDevBuild(collection); current == nil || current.Generation != good.Generation {
		t.Fatal("failure replaced good generation")
	}
	// A linked current pointer is never followed or replaced by a build.
	fixtureFile(t, main, string(validMain), 0644)
	current := filepath.Join(collection, ".wtc", "dev", "current.json")
	validManifest, err := os.ReadFile(current)
	if err != nil || ReadDevBuild(collection) == nil {
		t.Fatalf("regular valid-manifest positive control: %v", err)
	}
	outside := filepath.Join(t.TempDir(), "keep.json")
	fixtureFile(t, outside, string(validManifest), 0600)
	if err := os.Remove(current); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, current); err != nil {
		t.Fatal(err)
	}
	if ReadDevBuild(collection) != nil {
		t.Fatal("linked generation selected")
	}
	if _, err := EnsureDevFollow(ctx, collection); err == nil {
		t.Fatal("linked manifest accepted")
	}
	if _, err := InstallDevBuild(collection, source); err == nil {
		t.Fatal("one-shot publisher replaced a linked current-state slot")
	}
	if data, _ := os.ReadFile(outside); string(data) != string(validManifest) {
		t.Fatal("linked target changed")
	}
}

func TestDevFollowRequiresCompatibleCapabilityNotSuccessfulHelp(t *testing.T) {
	collection, source := devFollowFixture(t)
	// Older CLIs can print parent help with exit 0 for an unknown nested
	// command. A successful process is not evidence of handover capability.
	fixtureFile(t, filepath.Join(source, "cmd", "wtc", "main.go"), "package main\nimport \"fmt\"\nvar version=\"dev\"\nfunc main(){fmt.Println(\"Usage: wtc dev [install|status|uninstall]\")}\n", 0644)
	if err := SetDevMode(collection, "on", source); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := EnsureDevFollow(ctx, collection); err == nil || !strings.Contains(err.Error(), "lacks compatible follow") {
		t.Fatalf("legacy CLI accepted: %v", err)
	}
	if ReadDevBuild(collection) != nil {
		t.Fatal("incompatible generation activated")
	}
	if err := SetDevMode(collection, "off", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallDevBuild(collection, source); err != nil {
		t.Fatalf("legacy one-shot build regressed: %v", err)
	}
	if state := ensureDevTest(t, collection); state.Active || state.Mode != "off" {
		t.Fatalf("legacy controls lost OFF: %+v", state)
	}
}

func TestDevFollowExplicitCompatibleForkDoesNotWeakenAuto(t *testing.T) {
	collection, source := devFollowFixture(t)
	fixtureFile(t, filepath.Join(source, "go.mod"), "module example.invalid/compatible-cli\n\ngo 1.27.0\n", 0644)
	fixtureFile(t, filepath.Join(collection, "harness", "wtc.toml"), "[dev]\nauto_source = 'wtc-cli'\n", 0644)
	if _, err := DevFollowConfiguration(collection); err == nil {
		t.Fatal("AUTO accepted an unrecognized module")
	}
	if err := SetDevMode(collection, "on", source); err != nil {
		t.Fatal(err)
	}
	if build := ensureDevTest(t, collection).Build; build == nil || build.FollowProtocol != 1 {
		t.Fatalf("explicit compatible source failed: %+v", build)
	}
	if _, err := InstallDevBuild(collection, source); err == nil {
		t.Fatal("historical one-shot module validation was weakened")
	}
}

func TestDevFollowInactiveAutoAllowsNewOneShotInstall(t *testing.T) {
	collection, source := devFollowFixture(t)
	if err := SetDevMode(collection, "on", source); err != nil {
		t.Fatal(err)
	}
	followed := ensureDevTest(t, collection).Build
	if err := SetDevMode(collection, "auto", ""); err != nil {
		t.Fatal(err)
	}
	state, err := DevFollowConfiguration(collection)
	if err != nil || state.Active {
		t.Fatalf("inactive AUTO control: %+v %v", state, err)
	}
	fixtureFile(t, filepath.Join(source, "cmd", "wtc", "main.go"), "package main\nimport \"fmt\"\nvar version=\"dev\"\nfunc main(){fmt.Println(\"legacy-restored\")}\n", 0644)
	installed, err := InstallDevBuild(collection, source)
	if err != nil {
		t.Fatal(err)
	}
	selected := ReadDevBuild(collection)
	if selected == nil || selected.Generation != "" || selected.Binary != installed.Binary || !DevBuildTakesOver("0.1.0", selected) {
		t.Fatalf("retained follow pointer hid successful one-shot: installed=%+v selected=%+v", installed, selected)
	}
	out, err := exec.Command(selected.Binary).Output()
	if err != nil || strings.TrimSpace(string(out)) != "legacy-restored" {
		t.Fatalf("new one-shot not selected: %v %s", err, out)
	}
	if _, err := os.Stat(followed.Binary); err != nil {
		t.Fatal("one-shot publication unlinked active generation")
	}
}

func TestDevFollowAndOneShotPublishersAreOrdered(t *testing.T) {
	collection, source := devFollowFixture(t)
	if err := SetDevMode(collection, "on", source); err != nil {
		t.Fatal(err)
	}
	started, release, calls := devBlockGo(t, "build")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	oneShot := make(chan error, 1)
	go func() { _, err := InstallDevBuild(collection, source); oneShot <- err }()
	waitDevMarker(t, started)
	// The compiler has acknowledged entry. Independently attempting the
	// exact shared lock proves exclusion; this does not infer overlap from sleep.
	short, stop := context.WithTimeout(ctx, 100*time.Millisecond)
	unlock, err := lockDevFile(short, filepath.Join(collection, ".wtc-dev-build.lock"))
	stop()
	if err == nil {
		unlock()
		t.Fatal("one-shot compiler did not hold the shared build lock")
	}
	waitCtx, cancelWait := context.WithCancel(ctx)
	observed := &devObservedContext{Context: waitCtx, waiting: make(chan struct{})}
	follow := make(chan error, 1)
	go func() {
		_, err := EnsureDevFollow(observed, collection)
		follow <- err
	}()
	select {
	case <-observed.waiting:
	case <-ctx.Done():
		t.Fatal("second publisher did not reach its cancellable wait")
	}
	cancelWait()
	if err := <-follow; err != context.Canceled {
		t.Fatalf("second publisher did not stop at the lock boundary: %v", err)
	}
	fixtureFile(t, release, "release", 0600)
	if err := <-oneShot; err != nil {
		t.Fatal(err)
	}
	state := ensureDevTest(t, collection)
	if state.Build == nil || state.Build.Generation == "" {
		t.Fatalf("ON lost selected follow source: %+v", state)
	}
	selected := ReadDevBuild(collection)
	if selected == nil || selected.Generation != state.Build.Generation {
		t.Fatalf("final publication mismatch: %+v", selected)
	}
	if _, err := os.Stat(selected.Binary); err != nil {
		t.Fatal("published record points to a missing binary")
	}
	raw, _ := os.ReadFile(calls)
	if strings.Count(string(raw), "call") != 2 {
		t.Fatalf("expected one one-shot and one follow compile: %s", raw)
	}
	if err := SetDevMode(collection, "off", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallDevBuild(collection, source); err != nil {
		t.Fatal(err)
	}
	if state := ensureDevTest(t, collection); state.Active || state.Mode != "off" {
		t.Fatalf("manual publication undid OFF: %+v", state)
	}
}

// Done is observed without altering cancellation behavior. While the first
// compiler owns the lock, the second call must return the raw lock cancellation
// before that compiler is released, not a wrapped input-query/build error.
type devObservedContext struct {
	context.Context
	waiting chan struct{}
	once    sync.Once
}

func (c *devObservedContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.waiting) })
	return c.Context.Done()
}

func TestDevFollowRetirementRemovesGeneratedState(t *testing.T) {
	realGo, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"off", "install", "follow"} {
		for _, namespace := range []string{".wtc", ".taba"} {
			t.Run(mode+namespace, func(t *testing.T) {
				c := newWorkspaceFixture(t)
				t.Setenv("PATH", filepath.Dir(realGo)+string(os.PathListSeparator)+os.Getenv("PATH"))
				made, err := c.NewCollection(NewOptions{Slug: "finished", Repos: []string{"widget"}})
				if err != nil {
					t.Fatal(err)
				}
				target := made.Collection
				if err := os.Remove(filepath.Join(target, "widget", "init-ran")); err != nil {
					t.Fatal(err)
				}
				if err := SetDevMode(target, "off", ""); err != nil {
					t.Fatal(err)
				}
				if mode != "off" {
					_, source := devFollowFixture(t)
					if mode == "install" {
						if _, err := InstallDevBuild(target, source); err != nil {
							t.Fatal(err)
						}
					} else {
						if err := SetDevMode(target, "on", source); err != nil {
							t.Fatal(err)
						}
						ensureDevTest(t, target)
					}
				}
				if namespace == ".taba" {
					if mode != "off" {
						if err := os.Rename(filepath.Join(target, ".wtc"), filepath.Join(target, namespace)); err != nil {
							t.Fatal(err)
						}
					}
					for _, suffix := range []string{"-dev-mode.json", "-dev-mode.lock", "-dev-build.lock"} {
						if err := os.Rename(filepath.Join(target, ".wtc"+suffix), filepath.Join(target, namespace+suffix)); err != nil && !os.IsNotExist(err) {
							t.Fatal(err)
						}
					}
				}
				retired, err := c.RetireCollection(RetireOptions{Name: "finished"})
				if err != nil || !retired.FolderRemoved {
					t.Fatalf("generated dev state prevented retirement: %+v %v", retired, err)
				}
			})
		}
	}
}

func TestDevFollowRetirementPreservesUnknownAndLinkedState(t *testing.T) {
	c := newWorkspaceFixture(t)
	made, err := c.NewCollection(NewOptions{Slug: "finished", Repos: []string{"widget"}})
	if err != nil {
		t.Fatal(err)
	}
	target := made.Collection
	if err := os.Remove(filepath.Join(target, "widget", "init-ran")); err != nil {
		t.Fatal(err)
	}
	if err := SetDevMode(target, "off", ""); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(target, "keep.txt")
	fixtureFile(t, keep, "user state", 0600)
	retired, err := c.RetireCollection(RetireOptions{Name: "finished"})
	if err != nil || retired.FolderRemoved || strings.Join(retired.Leftovers, ",") != "keep.txt" {
		t.Fatalf("unknown state not preserved: %+v %v", retired, err)
	}
	// Exercise the same cleanup through the repeated-retirement entry point.
	fixtureFile(t, filepath.Join(target, ".taba-dev-mode.json"), "generated", 0600)
	fixtureFile(t, filepath.Join(target, ".taba", "dev-generations", "retained", "wtc"), "generated", 0700)
	if err := os.Remove(keep); err != nil {
		t.Fatal(err)
	}
	retired, err = c.RetireCollection(RetireOptions{Name: "finished"})
	if err != nil || !retired.FolderRemoved {
		t.Fatalf("repeated retirement left dev state: %+v %v", retired, err)
	}
	for _, namespace := range []string{".wtc", ".taba"} {
		root, outside := t.TempDir(), t.TempDir()
		sentinel := filepath.Join(outside, "dev-generations", "keep")
		fixtureFile(t, sentinel, "external state", 0600)
		if err := os.Symlink(outside, filepath.Join(root, namespace)); err != nil {
			t.Fatal(err)
		}
		if err := removeRetiredGeneratedFiles(root, false); err != nil {
			t.Fatal(err)
		}
		if raw, err := os.ReadFile(sentinel); err != nil || string(raw) != "external state" {
			t.Fatalf("linked namespace followed: %s %v", raw, err)
		}
		if _, err := os.Lstat(filepath.Join(root, namespace)); err != nil {
			t.Fatal("linked namespace was removed")
		}
	}
}

func TestDevFollowOneShotNamespaceMove(t *testing.T) {
	collection, source := devFollowFixture(t)
	installed, err := InstallDevBuild(collection, source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(collection, ".wtc"), filepath.Join(collection, ".taba")); err != nil {
		t.Fatal(err)
	}
	selected := ReadDevBuild(collection)
	if selected == nil || selected.Generation != "" || selected.Binary != filepath.Join(collection, ".taba", "dev-generations", filepath.Base(filepath.Dir(installed.Binary)), "wtc") || selected.Version != installed.Version {
		t.Fatalf("successful moved one-shot not selected: %+v", selected)
	}
	if err := os.Chmod(selected.Binary, 0775); err != nil {
		t.Fatal(err)
	}
	if ReadDevBuild(collection) != nil {
		t.Fatal("unsafe moved binary selected")
	}
	if err := os.Chmod(selected.Binary, 0755); err != nil {
		t.Fatal(err)
	}
	installed.Binary = filepath.Join(t.TempDir(), "wtc")
	raw, _ := json.Marshal(installed)
	fixtureFile(t, filepath.Join(collection, ".taba", "dev", "current.json"), string(raw), 0600)
	if ReadDevBuild(collection) != nil {
		t.Fatal("arbitrary published path was remapped")
	}
}

func TestDevFollowGenerationNamespaceMove(t *testing.T) {
	collection, source := devFollowFixture(t)
	if err := SetDevMode(collection, "on", source); err != nil {
		t.Fatal(err)
	}
	build := ensureDevTest(t, collection).Build
	if err := os.Rename(filepath.Join(collection, ".wtc"), filepath.Join(collection, ".taba")); err != nil {
		t.Fatal(err)
	}
	expected := filepath.Join(collection, ".taba", "dev-generations", build.Generation, "wtc")
	selected := ReadDevBuild(collection)
	if selected == nil || selected.Binary != expected {
		t.Fatalf("moved generation not selected: %+v", selected)
	}
	called, err := WithDevGeneration(context.Background(), collection, build.Generation, func(current *DevBuild) error {
		if current.Binary != expected {
			t.Fatalf("handover used old namespace: %+v", current)
		}
		return nil
	})
	if err != nil || !called {
		t.Fatalf("valid moved generation not eligible for handover: %v %v", called, err)
	}
}

func TestDevFollowRetirementRejectsWaitingAndReplacementLocks(t *testing.T) {
	collection, source := devFollowFixture(t)
	if err := SetDevMode(collection, "on", source); err != nil {
		t.Fatal(err)
	}
	root, err := devCollectionRoot(collection, nil)
	if err != nil {
		t.Fatal(err)
	}
	unlock, err := lockDevRetirement(collection)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if unlock != nil {
			unlock()
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	observed := &devObservedContext{Context: ctx, waiting: make(chan struct{})}
	done := make(chan error, 1)
	go func() { _, err := EnsureDevFollow(observed, collection); done <- err }()
	select {
	case <-observed.waiting:
	case <-ctx.Done():
		t.Fatal("publisher did not acknowledge its blocked lock")
	}
	// Simulate the exact finalization interval: worktree boundary gone,
	// existing guard written, generated lock names unlinked, then guard removed.
	if err := os.RemoveAll(filepath.Join(collection, "harness")); err != nil {
		t.Fatal(err)
	}
	if err := writeRetiredMarker(collection, nil); err != nil {
		t.Fatal(err)
	}
	if err := removeRetiredGeneratedFiles(collection, false); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(collection, RetiredMarkerFile)); err != nil {
		t.Fatal(err)
	}
	// The folder still exists and has no guard. A replacement-inode caller
	// must nevertheless refuse its missing collection boundary.
	for _, operation := range []func() error{
		func() error { return SetDevMode(collection, "off", "") },
		func() error { _, err := InstallDevBuild(collection, source); return err },
		func() error { _, err := EnsureDevFollow(ctx, collection); return err },
	} {
		if err := operation(); err == nil {
			t.Fatal("publisher accepted final-removal gap")
		}
	}
	for _, name := range []string{".wtc-dev-build.lock", ".wtc-dev-mode.lock", ".wtc-dev-mode.json", ".wtc"} {
		if _, err := os.Lstat(filepath.Join(collection, name)); !os.IsNotExist(err) {
			t.Fatalf("publisher recreated generated state: %s %v", name, err)
		}
	}
	unlock()
	unlock = nil
	if err := <-done; err == nil || !strings.Contains(err.Error(), "lock was removed or replaced") {
		t.Fatalf("orphaned-inode publisher was not rejected: %v", err)
	}
	// Reusing the same pathname for a new valid root cannot satisfy a caller
	// that captured the previous directory's identity before waiting.
	if err := os.Rename(collection, collection+"-old"); err != nil {
		t.Fatal(err)
	}
	fixtureFile(t, filepath.Join(collection, "harness", ".harness-repos.yml"), "repos: []\n", 0644)
	if _, err := devCollectionRoot(collection, root); err == nil {
		t.Fatal("replaced root satisfied old publisher")
	}
}

func TestDevFollowRetirementDoesNotLockAcrossTeardown(t *testing.T) {
	realGo, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	c := newWorkspaceFixture(t)
	made, err := c.NewCollection(NewOptions{Slug: "finished", Repos: []string{"widget"}})
	if err != nil {
		t.Fatal(err)
	}
	probe, marker := filepath.Join(t.TempDir(), "probe"), filepath.Join(t.TempDir(), "hook-ran")
	program := probe + ".go"
	fixtureFile(t, program, `package main
import("os";"syscall")
func main(){for _,name:=range []string{".wtc-dev-build.lock",".wtc-dev-mode.lock"}{
 f,e:=os.OpenFile(os.Args[1]+"/"+name,os.O_CREATE|os.O_RDWR,0600);if e!=nil{panic(e)}
 if e=syscall.Flock(int(f.Fd()),syscall.LOCK_EX|syscall.LOCK_NB);e!=nil{panic(e)};f.Close()
};if e:=os.WriteFile(os.Args[2],[]byte("hook"),0600);e!=nil{panic(e)}}
`, 0600)
	if out, err := exec.Command(realGo, "build", "-o", probe, program).CombinedOutput(); err != nil {
		t.Fatalf("build lock probe: %v %s", err, out)
	}
	widget := filepath.Join(made.Collection, "widget")
	if err := os.Remove(filepath.Join(widget, "init-ran")); err != nil {
		t.Fatal(err)
	}
	fixtureFile(t, filepath.Join(widget, ".harness", "teardown.sh"), "#!/bin/sh\n\""+probe+"\" \"$PWD/..\" \""+marker+"\"\n", 0755)
	fixtureGit(t, "-C", widget, "add", ".harness/teardown.sh")
	fixtureGit(t, "-C", widget, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "teardown lock fixture")
	head := fixtureGit(t, "-C", widget, "rev-parse", "HEAD")
	fixtureGit(t, "--git-dir="+filepath.Join(c.Workspace, ".bare", "widget.git"), "update-ref", "refs/remotes/origin/teardown-fixture", head)
	retired, err := c.RetireCollection(RetireOptions{Name: "finished"})
	if err != nil || !retired.FolderRemoved {
		t.Fatalf("retire with dev-aware hook: %+v %v", retired, err)
	}
	if raw, err := os.ReadFile(marker); err != nil || string(raw) != "hook" {
		t.Fatalf("teardown ran under dev locks: %s %v", raw, err)
	}
}

func TestDevFollowRetirementPreflightsOnlyIntroducedState(t *testing.T) {
	for _, kind := range []string{"unused", "private-root", "lock-directory", "lock-link"} {
		t.Run(kind, func(t *testing.T) {
			c := newWorkspaceFixture(t)
			made, err := c.NewCollection(NewOptions{Slug: "finished", Repos: []string{"widget"}})
			if err != nil {
				t.Fatal(err)
			}
			target := made.Collection
			if err := os.Remove(filepath.Join(target, "widget", "init-ran")); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "unused", "private-root":
				if kind == "private-root" {
					if err := SetDevMode(target, "off", ""); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.Chmod(target, 0775); err != nil {
					t.Fatal(err)
				}
			case "lock-directory":
				if err := os.Mkdir(filepath.Join(target, ".wtc-dev-build.lock"), 0700); err != nil {
					t.Fatal(err)
				}
			case "lock-link":
				outside := filepath.Join(t.TempDir(), "keep")
				fixtureFile(t, outside, "external state", 0600)
				if err := os.Symlink(outside, filepath.Join(target, ".taba-dev-mode.lock")); err != nil {
					t.Fatal(err)
				}
			}
			retired, err := c.RetireCollection(RetireOptions{Name: "finished"})
			if kind == "unused" {
				if err != nil || !retired.FolderRemoved {
					t.Fatalf("unused dev state changed retirement eligibility: %+v %v", retired, err)
				}
			} else {
				if err == nil || len(retired.Removed) != 0 {
					t.Fatalf("dev prerequisite failed after teardown: %+v %v", retired, err)
				}
				if _, err := os.Stat(filepath.Join(target, "harness", ".git")); err != nil {
					t.Fatal("preflight removed the harness")
				}
			}
		})
	}
}

func TestDevFollowRetirementLateLockFailureKeepsGuard(t *testing.T) {
	for _, kind := range []string{"guard", "blocked-guard"} {
		t.Run(kind, func(t *testing.T) {
			c := newWorkspaceFixture(t)
			made, err := c.NewCollection(NewOptions{Slug: "finished", Repos: []string{"widget"}})
			if err != nil {
				t.Fatal(err)
			}
			widget := filepath.Join(made.Collection, "widget")
			if err := os.Remove(filepath.Join(widget, "init-ran")); err != nil {
				t.Fatal(err)
			}
			hook := "#!/bin/sh\nmkdir \"$PWD/../.wtc-dev-build.lock\"\n"
			if kind == "blocked-guard" {
				hook += "mkdir \"$PWD/../.wtc-retired\"\n"
			}
			fixtureFile(t, filepath.Join(widget, ".harness", "teardown.sh"), hook, 0755)
			fixtureGit(t, "-C", widget, "add", ".harness/teardown.sh")
			fixtureGit(t, "-C", widget, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "late lock fixture")
			head := fixtureGit(t, "-C", widget, "rev-parse", "HEAD")
			fixtureGit(t, "--git-dir="+filepath.Join(c.Workspace, ".bare", "widget.git"), "update-ref", "refs/remotes/origin/teardown-fixture", head)
			retired, err := c.RetireCollection(RetireOptions{Name: "finished"})
			if kind == "blocked-guard" {
				if err == nil || len(retired.Removed) != 2 || !strings.Contains(err.Error(), "preserving retired guard failed") {
					t.Fatalf("guard failure was hidden: %+v %v", retired, err)
				}
				if info, err := os.Lstat(filepath.Join(made.Collection, RetiredMarkerFile)); err != nil || !info.IsDir() {
					t.Fatal("unexpected marker slot was replaced")
				}
				return
			}
			if err == nil || len(retired.Removed) != 2 || !RetiredFolder(made.Collection) {
				t.Fatalf("late failure stranded an undiscoverable folder: %+v %v", retired, err)
			}
			if err := os.Remove(filepath.Join(made.Collection, ".wtc-dev-build.lock")); err != nil {
				t.Fatal(err)
			}
			retired, err = c.RetireCollection(RetireOptions{Name: "finished"})
			if err != nil || !retired.FolderRemoved {
				t.Fatalf("corrected late blocker could not finish normally: %+v %v", retired, err)
			}
		})
	}
}

func TestDevFollowUnusedAutoAddsNoPrivateRootRequirement(t *testing.T) {
	collection, _ := devFollowFixture(t)
	if err := os.Chmod(collection, 0775); err != nil {
		t.Fatal(err)
	}
	state, err := DevFollowConfiguration(collection)
	if err != nil || state.Active || state.Mode != "auto" {
		t.Fatalf("unused AUTO rejected ordinary collection: %+v %v", state, err)
	}
	state, err = EnsureDevFollow(context.Background(), collection)
	if err != nil || state.Active || state.Build != nil {
		t.Fatalf("passive inactive check mutated eligibility: %+v %v", state, err)
	}
	if _, err := os.Stat(filepath.Join(collection, ".wtc-dev-build.lock")); !os.IsNotExist(err) {
		t.Fatal("inactive check created a build lock")
	}
}

func TestDevRetirementLockOnlyRetryEligibility(t *testing.T) {
	c := newWorkspaceFixture(t)
	made, err := c.NewCollection(NewOptions{Slug: "finished", Repos: []string{"widget"}})
	if err != nil {
		t.Fatal(err)
	}
	target := made.Collection
	if err := os.Remove(filepath.Join(target, "widget", "init-ran")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(target, 0775); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(target, "HANDOFF.md")); err != nil {
		t.Fatal(err)
	}
	fixtureFile(t, filepath.Join(target, "HANDOFF.md", "keep"), "synthetic blocker", 0600)
	retired, err := c.RetireCollection(RetireOptions{Name: "finished"})
	if err == nil || len(retired.Removed) != 2 || !RetiredFolder(target) {
		t.Fatalf("late cleanup control: %+v %v", retired, err)
	}
	if err := os.RemoveAll(filepath.Join(target, "HANDOFF.md")); err != nil {
		t.Fatal(err)
	}
	retired, err = c.RetireCollection(RetireOptions{Name: "finished"})
	if err != nil || !retired.FolderRemoved {
		t.Fatalf("lock-only state changed unused 0775 retry eligibility: %+v %v", retired, err)
	}
}

func TestDevOneShotFailedPublishKeepsPair(t *testing.T) {
	collection, source := devFollowFixture(t)
	old, err := InstallDevBuild(collection, source)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(old.Binary)
	if err != nil {
		t.Fatal(err)
	}
	unlock, err := lockDevFile(context.Background(), filepath.Join(collection, ".wtc-dev-mode.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	fixtureFile(t, filepath.Join(source, "cmd", "wtc", "main.go"), "package main\nimport \"fmt\"\nvar version=\"dev\"\nfunc main(){fmt.Println(\"replacement\")}\n", 0644)
	if _, err := InstallDevBuild(collection, source); err == nil {
		t.Fatal("publication lock control unexpectedly succeeded")
	}
	selected := ReadDevBuild(collection)
	if selected == nil {
		t.Fatal("lost previous record")
	}
	after, err := os.ReadFile(selected.Binary)
	if err != nil {
		t.Fatal(err)
	}
	if selected.Version != old.Version || !bytes.Equal(before, after) {
		t.Fatalf("failed publication changed selected executable/metadata pair: old=%+v selected=%+v", old, selected)
	}
}
func TestDevCompilerCancellationClosesInheritedPipes(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "started")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	command := exec.Command("/bin/sh", "-c", "sleep 4 & echo started > \""+marker+"\"; wait")
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	done := make(chan error, 1)
	go func() { done <- runDevCompiler(ctx, command) }()
	waitDevMarker(t, marker)
	start := time.Now()
	cancel()
	err := <-done
	if err != context.Canceled {
		t.Fatalf("cancellation control: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("descendant-held pipes defeated cancellation bound: %v", elapsed)
	}
}
func TestDevFingerprintIgnoresUnrelatedBrokenPackage(t *testing.T) {
	_, source := devFollowFixture(t)
	t.Setenv("GOPROXY", "off")
	fixtureFile(t, filepath.Join(source, "unrelated", "broken.go"), "package unrelated\nimport _ \"example.invalid/never-downloaded\"\n", 0644)
	build := exec.Command("go", "build", "-o", filepath.Join(t.TempDir(), "wtc"), "./cmd/wtc")
	build.Dir = source
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("CLI positive build: %v %s", err, out)
	}
	if _, err := devFingerprint(context.Background(), source); err != nil {
		t.Fatalf("unrelated package rejected valid CLI graph: %v", err)
	}
}
func TestDevFingerprintIncludesArchitectureTuning(t *testing.T) {
	_, source := devFollowFixture(t)
	t.Setenv("GOARCH", "amd64")
	t.Setenv("CGO_ENABLED", "0")
	t.Setenv("GOAMD64", "v1")
	a, err := devFingerprint(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOAMD64", "v3")
	b, err := devFingerprint(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("architecture target changed but generation fingerprint was reused")
	}
}

func TestDevFingerprintIncludesCompilerVersionArguments(t *testing.T) {
	_, source := devFollowFixture(t)
	firstVersion, err := devBuildVersion(source)
	if err != nil {
		t.Fatal(err)
	}
	first, err := devFingerprint(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"tag", "-d", "v0.1.0"}, {"tag", "v0.2.0"}} {
		command := exec.Command("git", args...)
		command.Dir = source
		if out, err := command.CombinedOutput(); err != nil {
			t.Fatalf("tag fixture: %v %s", err, out)
		}
	}
	secondVersion, err := devBuildVersion(source)
	if err != nil || firstVersion == secondVersion {
		t.Fatalf("version control: %s %s %v", firstVersion, secondVersion, err)
	}
	second, err := devFingerprint(context.Background(), source)
	if err != nil || first == second {
		t.Fatalf("compiler ldflags changed without fingerprint: %v", err)
	}
}

func TestDevOneShotFailedPublishPreservesLegacyPair(t *testing.T) {
	collection, source := devFollowFixture(t)
	if _, err := InstallDevBuild(collection, source); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(collection, ".wtc", "dev", "current.json")); err != nil {
		t.Fatal(err)
	}
	old := ReadDevBuild(collection)
	if old == nil {
		t.Fatal("legacy fallback control missing")
	}
	before, err := os.ReadFile(old.Binary)
	if err != nil {
		t.Fatal(err)
	}
	unlock, err := lockDevFile(context.Background(), filepath.Join(collection, ".wtc-dev-mode.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	fixtureFile(t, filepath.Join(source, "cmd", "wtc", "main.go"), "package main\nvar version=\"dev\"\nfunc main(){}\n", 0644)
	if _, err := InstallDevBuild(collection, source); err == nil {
		t.Fatal("blocked publication unexpectedly succeeded")
	}
	selected := ReadDevBuild(collection)
	if selected == nil || *selected != *old {
		t.Fatal("failed commit replaced legacy metadata")
	}
	after, err := os.ReadFile(old.Binary)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("failed commit replaced legacy binary")
	}
}

func TestDevImmutableArtifactNeverOverwritesExistingSlot(t *testing.T) {
	collection, source := devFollowFixture(t)
	build, err := InstallDevBuild(collection, source)
	if err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(build.Binary)
	if err != nil {
		t.Fatal(err)
	}
	stage := filepath.Join(collection, ".wtc", "dev-generations", "staged-fixture")
	fixtureFile(t, filepath.Join(stage, "wtc"), "#!/bin/sh\necho other\n", 0755)
	fixtureFile(t, filepath.Join(stage, "version"), build.Version+"\n"+build.Source+"\n", 0644)
	if err := retainDevArtifact(collection, filepath.Dir(build.Binary), stage); err == nil {
		t.Fatal("different executable overwrote immutable artifact")
	}
	after, err := os.ReadFile(build.Binary)
	if err != nil || !bytes.Equal(original, after) {
		t.Fatal("immutable artifact changed")
	}
}

func TestDevFinalSelectionGuardBothArtifactKinds(t *testing.T) {
	for _, follow := range []bool{false, true} {
		t.Run(map[bool]string{false: "oneshot", true: "follow"}[follow], func(t *testing.T) {
			collection, source := devFollowFixture(t)
			var selected *DevBuild
			if follow {
				if err := SetDevMode(collection, "on", source); err != nil {
					t.Fatal(err)
				}
				selected = ensureDevTest(t, collection).Build
			} else {
				var err error
				selected, err = InstallDevBuild(collection, source)
				if err != nil {
					t.Fatal(err)
				}
			}
			called, err := WithDevBuild(context.Background(), collection, selected, func(*DevBuild) error { return nil })
			if err != nil || !called {
				t.Fatalf("selected positive control: %v %v", called, err)
			}
			// The expected record was read before OFF completed; final authority must be fresh.
			if err := SetDevMode(collection, "off", ""); err != nil {
				t.Fatal(err)
			}
			called, err = WithDevBuild(context.Background(), collection, selected, func(*DevBuild) error { t.Fatal("OFF delegate executed"); return nil })
			if err != nil || called {
				t.Fatalf("OFF guard: %v %v", called, err)
			}
			mode, selectedSource := "auto", ""
			if follow {
				mode, selectedSource = "on", source
			}
			if err := SetDevMode(collection, mode, selectedSource); err != nil {
				t.Fatal(err)
			}
			changed := *selected
			changed.Version += ".changed"
			if err := publishDevBuild(collection, devBuildDir(collection), &changed); err != nil {
				t.Fatal(err)
			}
			called, err = WithDevBuild(context.Background(), collection, selected, func(*DevBuild) error { t.Fatal("superseded delegate executed"); return nil })
			if err != nil || called {
				t.Fatalf("selector guard: %v %v", called, err)
			}
		})
	}
}

func TestDevRetirementLockOnlyVersusActualPrivateState(t *testing.T) {
	for _, path := range []string{".wtc-dev-build.lock", ".wtc-dev-mode.lock", ".wtc-dev-mode.json", ".wtc/dev/current.json", ".wtc/dev-generations/fixture"} {
		t.Run(path, func(t *testing.T) {
			root := t.TempDir()
			fixtureFile(t, filepath.Join(root, path), "fixture", 0600)
			if err := os.Chmod(root, 0775); err != nil {
				t.Fatal(err)
			}
			used, err := preflightDevRetirement(root)
			lock := strings.HasSuffix(path, ".lock")
			if lock {
				if err != nil || used {
					t.Fatalf("benign lock changed eligibility: %v %v", used, err)
				}
			} else if err == nil {
				t.Fatal("actual dev state on unsafe root accepted")
			}
			if lock {
				if err := os.Chmod(filepath.Join(root, path), 0666); err != nil {
					t.Fatal(err)
				}
				if _, err := preflightDevRetirement(root); err == nil {
					t.Fatal("unsafe lock slot accepted")
				}
			}
		})
	}
}

func TestDevBridgeFailureDoesNotRollBackCommittedSelector(t *testing.T) {
	collection, source := devFollowFixture(t)
	build, err := InstallDevBuild(collection, source)
	if err != nil {
		t.Fatal(err)
	}
	dir := devBuildDir(collection)
	commit, cleanup, err := prepareDevLegacyBridge(collection, dir, build)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if err := publishDevBuild(collection, dir, build); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "version")); err != nil {
		t.Fatal(err)
	}
	fixtureFile(t, filepath.Join(dir, "version", "unexpected"), "preserve", 0600)
	if err := commit(); err == nil {
		t.Fatal("blocked bridge commit unexpectedly succeeded")
	}
	selected := ReadDevBuild(collection)
	if selected == nil || *selected != *build {
		t.Fatal("bridge failure invalidated committed selection")
	}
	if _, err := os.Stat(build.Binary); err != nil {
		t.Fatal("bridge failure removed committed immutable executable")
	}
}

func TestDevOutputCommandBoundsDescendantHeldPipes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	marker := filepath.Join(t.TempDir(), "started")
	command := exec.CommandContext(ctx, "/bin/sh", "-c", "sleep 4 & echo started > \""+marker+"\"; wait")
	done := make(chan error, 1)
	go func() { _, err := devCommandOutput(command); done <- err }()
	waitDevMarker(t, marker)
	start := time.Now()
	cancel()
	if err := <-done; err == nil {
		t.Fatal("canceled output command succeeded")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("descendant-held output exceeded cancellation bound: %v", elapsed)
	}
}
