package wtc

import (
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
			generation := ""
			if operation == "list" {
				generation = ensureDevTest(t, collection).Build.Generation
				selected, err := WithDevGeneration(context.Background(), collection, generation, func(*DevBuild) error { return nil })
				if !selected || err != nil {
					t.Fatalf("positive generation selection: %v %v", selected, err)
				}
			}
			started, release, _ := devBlockGo(t, operation)
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
