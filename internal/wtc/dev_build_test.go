package wtc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDevVersionSortsBetweenReleases(t *testing.T) {
	for describe, want := range map[string]string{
		"v0.1.39-0-g69e3a79":       "0.1.39+dev.g69e3a79",
		"v0.1.39-3-g69e3a79":       "0.1.40-dev.3+dev.g69e3a79",
		"v0.1.39-0-g69e3a79-dirty": "0.1.40-dev.0+dev.g69e3a79.dirty",
		"v1.2.9-12-gabc1234-dirty": "1.2.10-dev.12+dev.gabc1234.dirty",
	} {
		if got, err := DevVersion(describe); err != nil || got != want {
			t.Fatalf("%s: got %q, %v; want %q", describe, got, err, want)
		}
	}
	if _, err := DevVersion("69e3a79"); err == nil {
		t.Fatal("a checkout without a release tag has no version")
	}
	build := func(v string) *DevBuild { return &DevBuild{Version: v} }
	for _, tc := range []struct {
		running, dev string
		want         bool
	}{
		{"0.1.39", "0.1.40-dev.3+dev.g69e3a79", true},
		{"0.1.39", "0.1.39+dev.g69e3a79", true},
		{"0.1.40", "0.1.40-dev.3+dev.g69e3a79", false},
		{"0.1.41", "0.1.40-dev.3+dev.g69e3a79", false},
		{"dev", "0.1.40-dev.3+dev.g69e3a79", false},
	} {
		if got := DevBuildTakesOver(tc.running, build(tc.dev)); got != tc.want {
			t.Fatalf("running %s, dev %s: takes over = %v", tc.running, tc.dev, got)
		}
	}
	if DevBuildTakesOver("0.1.39", nil) {
		t.Fatal("no dev build cannot take over")
	}
}

func writeDevBuild(t *testing.T, collection, version string, mode os.FileMode) string {
	t.Helper()
	fixtureFile(t, filepath.Join(collection, "harness", ".harness-repos.yml"), "repos: []\n", 0644)
	dir := filepath.Join(collection, ".wtc", "dev")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(dir, "wtc")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\n"), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(binary, mode); err != nil {
		t.Fatal(err)
	}
	if version != "" {
		if err := os.WriteFile(filepath.Join(dir, "version"), []byte(version+"\n/src/wtc-cli\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	return binary
}

func TestReadDevBuildOnlyTrustsPrivateExecutables(t *testing.T) {
	collection := t.TempDir()
	if ReadDevBuild(collection) != nil {
		t.Fatal("no dev build expected")
	}
	binary := writeDevBuild(t, collection, "0.1.40-dev.1+dev.gabc1234", 0755)
	build := ReadDevBuild(collection)
	if build == nil || build.Binary != binary || build.Source != "/src/wtc-cli" {
		t.Fatalf("dev build not read: %+v", build)
	}
	if err := os.Chmod(binary, 0775); err != nil {
		t.Fatal(err)
	}
	if ReadDevBuild(collection) != nil {
		t.Fatal("a group-writable binary must not be run")
	}
	writeDevBuild(t, collection, "not-a-version", 0755)
	if ReadDevBuild(collection) != nil {
		t.Fatal("a build without a valid version must not be run")
	}
}

func TestDevBuildSourceFindsWtcCheckout(t *testing.T) {
	collection := t.TempDir()
	for name, module := range map[string]string{"harness": "", "app": "example.invalid/app", "ext.wtc-cli": wtcModule} {
		dir := filepath.Join(collection, name)
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		if module != "" {
			if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module "+module+"\n\ngo 1.27\n"), 0644); err != nil {
				t.Fatal(err)
			}
		}
	}
	if got, err := DevBuildSource(collection); err != nil || got != filepath.Join(collection, "ext.wtc-cli") {
		t.Fatalf("source = %q, %v", got, err)
	}
	if err := os.RemoveAll(filepath.Join(collection, "ext.wtc-cli")); err != nil {
		t.Fatal(err)
	}
	if _, err := DevBuildSource(collection); err == nil {
		t.Fatal("a collection without wtc-cli has no source")
	}
}

// Builds this repository into a scratch collection, as `wtc dev install` does.
func TestInstallAndUninstallDevBuild(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the CLI")
	}
	source, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gitOutput("-C", source, "describe", "--tags", "--match", "v[0-9]*"); err != nil {
		t.Skip("checkout has no release tag (shallow clone)")
	}
	collection := t.TempDir()
	fixtureFile(t, filepath.Join(collection, "harness", ".harness-repos.yml"), "repos: []\n", 0644)
	fixtureFile(t, filepath.Join(collection, ".taba", "dev", "wtc"), "#!/bin/sh\n", 0755)
	fixtureFile(t, filepath.Join(collection, ".taba", "dev", "version"), "0.1.40-dev.1+dev.gabc1234\n", 0644)
	build, err := InstallDevBuild(collection, source)
	if err != nil {
		t.Fatal(err)
	}
	read := ReadDevBuild(collection)
	if read == nil || read.Version != build.Version || read.Source != source || !strings.Contains(build.Version, "dev") {
		t.Fatalf("installed build not readable: %+v / %+v", build, read)
	}
	if filepath.Dir(filepath.Dir(build.Binary)) != filepath.Join(collection, ".wtc", "dev-generations") || !strings.HasPrefix(filepath.Base(filepath.Dir(build.Binary)), "oneshot-") || build.Generation != "" || build.FollowProtocol != 0 {
		t.Fatalf("one-shot is not an immutable default-namespace artifact: %+v", build)
	}
	bridge, err := os.ReadFile(filepath.Join(collection, ".wtc", "dev", "wtc"))
	artifact, artifactErr := os.ReadFile(build.Binary)
	legacy, legacyErr := os.ReadFile(filepath.Join(collection, ".wtc", "dev", "version"))
	if err != nil || artifactErr != nil || legacyErr != nil || string(bridge) != string(artifact) || string(legacy) != build.Version+"\n"+source+"\n" {
		t.Fatal("legacy bootstrap bridge does not match the committed artifact")
	}
	if _, err := os.Stat(filepath.Join(collection, ".wtc", "dev", ".wtc.tmp")); !os.IsNotExist(err) {
		t.Fatal("temporary binary left behind")
	}
	if _, err := InstallDevBuild(collection, t.TempDir()); err == nil {
		t.Fatal("a directory that is not wtc-cli must be refused")
	}
	if removed, err := UninstallDevBuild(collection); err != nil || !removed || ReadDevBuild(collection) != nil {
		t.Fatalf("uninstall: %v %v", removed, err)
	}
	if removed, err := UninstallDevBuild(collection); err != nil || removed {
		t.Fatalf("second uninstall: %v %v", removed, err)
	}
}

func TestFindCollectionRootRecognizesHarnessAndAdHocCollections(t *testing.T) {
	for _, marker := range []string{filepath.Join("harness", ".harness-repos.yml"), filepath.Join(AdHocConfigDir, adHocRegistryFile)} {
		collection := t.TempDir()
		if err := os.MkdirAll(filepath.Join(collection, filepath.Dir(marker)), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(collection, marker), []byte("schema_version: 1\n"), 0644); err != nil {
			t.Fatal(err)
		}
		nested := filepath.Join(collection, "repo", "sub")
		if err := os.MkdirAll(nested, 0755); err != nil {
			t.Fatal(err)
		}
		want, _ := filepath.EvalSymlinks(collection)
		got, _ := filepath.EvalSymlinks(FindCollectionRoot(nested))
		if got != want {
			t.Fatalf("%s: root = %q, want %q", marker, got, want)
		}
	}
	if FindCollectionRoot(t.TempDir()) != "" {
		t.Fatal("a plain directory is not a collection")
	}
}
