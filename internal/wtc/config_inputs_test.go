package wtc

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLegacyInputSelection(t *testing.T) {
	for _, pair := range [][2]string{{"wtc.toml", "taba.toml"}, {".wtc-cli-version", ".taba-version"}, {"wtc.env", "taba.env"}, {".wtc-base.sha256", ".taba-base.sha256"}} {
		t.Run(pair[0], func(t *testing.T) {
			dir := t.TempDir()
			check := func(want string) {
				t.Helper()
				got, err := legacyInput(dir, pair[0], pair[1])
				if err != nil || got != filepath.Join(dir, want) {
					t.Fatalf("selected %q, %v; want %s", got, err, want)
				}
			}
			check(pair[0])
			entries, _ := os.ReadDir(dir)
			if len(entries) != 0 {
				t.Fatal("path selection created files")
			}
			fixtureFile(t, filepath.Join(dir, pair[1]), "current\n", 0644)
			check(pair[1])
			fixtureFile(t, filepath.Join(dir, pair[0]), "legacy\n", 0644)
			check(pair[0])
		})
	}
}

func TestTabaConfigDoesNotMaskBrokenLegacyInput(t *testing.T) {
	for _, kind := range []string{"invalid", "directory", "dangling-symlink"} {
		t.Run(kind, func(t *testing.T) {
			c := fixture(t)
			fixtureFile(t, filepath.Join(c.Harness, "taba.toml"), "[herdr]\nsession = 'current'\n", 0644)
			legacy := filepath.Join(c.Harness, "wtc.toml")
			switch kind {
			case "invalid":
				fixtureFile(t, legacy, "invalid = [", 0644)
			case "directory":
				if err := os.Mkdir(legacy, 0755); err != nil {
					t.Fatal(err)
				}
			case "dangling-symlink":
				if err := os.Symlink("missing.toml", legacy); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := OpenCollection(c.Collection); err == nil {
				t.Fatal("broken legacy configuration was masked by the alias")
			}
		})
	}
}

func TestTabaHarnessConfigAndPinInputs(t *testing.T) {
	c := fixture(t)
	fixtureFile(t, filepath.Join(c.Harness, "taba.toml"), "[herdr]\nsession = 'current'\n", 0644)
	fixtureFile(t, filepath.Join(c.Harness, ".taba-version"), "0.1.54\n", 0644)
	opened, err := OpenCollection(c.Collection)
	if err != nil || opened.Config.Herdr.Session != "current" {
		t.Fatalf("alias config did not load: %v, %v", opened, err)
	}
	data, err := opened.RenderMise()
	if err != nil || !strings.Contains(string(data), `"github:lcorneliussen/taba" = "0.1.54"`) {
		t.Fatalf("alias pin did not render: %s, %v", data, err)
	}
	for _, name := range []string{"wtc.toml", ".wtc-cli-version"} {
		if _, err := os.Lstat(filepath.Join(c.Harness, name)); !os.IsNotExist(err) {
			t.Fatalf("reading created %s", name)
		}
	}
	fixtureFile(t, filepath.Join(c.Harness, "wtc.toml"), "[herdr]\nsession = 'legacy'\n", 0644)
	fixtureFile(t, filepath.Join(c.Harness, ".wtc-cli-version"), "0.1.9\n", 0644)
	opened, err = OpenCollection(c.Collection)
	if err != nil || opened.Config.Herdr.Session != "legacy" {
		t.Fatalf("legacy config did not stay authoritative: %v, %v", opened, err)
	}
	data, err = opened.RenderMise()
	if err != nil || !strings.Contains(string(data), `"github:lcorneliussen/taba" = "0.1.9"`) {
		t.Fatalf("legacy pin did not stay authoritative: %s, %v", data, err)
	}
}

func TestTabaUserRootsPreserveLegacyDefaultsAndOverrides(t *testing.T) {
	for _, item := range []struct {
		env  string
		root func() (string, error)
	}{{"XDG_CONFIG_HOME", UserConfigRoot}, {"XDG_DATA_HOME", UserDataRoot}} {
		t.Run(item.env, func(t *testing.T) {
			parent := t.TempDir()
			t.Setenv(item.env, parent)
			t.Setenv("WTC_DATA_HOME", "")
			t.Setenv("TABA_DATA_HOME", "")
			check := func(name string) {
				t.Helper()
				got, err := item.root()
				if err != nil || got != filepath.Join(parent, name) {
					t.Fatalf("root %q, %v; want %s", got, err, name)
				}
			}
			check("wtc")
			entries, _ := os.ReadDir(parent)
			if len(entries) != 0 {
				t.Fatal("root lookup created a directory")
			}
			if err := os.Mkdir(filepath.Join(parent, "taba"), 0755); err != nil {
				t.Fatal(err)
			}
			check("taba")
			if err := os.Mkdir(filepath.Join(parent, "wtc"), 0755); err != nil {
				t.Fatal(err)
			}
			check("wtc")
			if item.env == "XDG_DATA_HOME" {
				want := t.TempDir()
				t.Setenv("TABA_DATA_HOME", want)
				if got, err := item.root(); err != nil || got != want {
					t.Fatalf("explicit data root ignored: %q %v", got, err)
				}
			}
		})
	}
}

func TestTabaMachineDefaultsAndValueFreeInventory(t *testing.T) {
	c := fixture(t)
	for _, key := range []string{"WTC_TWG_SITE", "TABA_TWG_SITE"} {
		t.Setenv(key, "")
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
	}
	fixtureFile(t, filepath.Join(c.ConfigRoot, "taba.env"), "TABA_TWG_SITE='current-value'\n", 0600)
	if got, err := machineTWGSite(c.ConfigRoot); err != nil || got != "current-value" {
		t.Fatalf("alias defaults %q, %v", got, err)
	}
	inventory, err := c.ListEnv()
	if err != nil || inventory.Files[0].Path != "taba.env" || !inventory.Files[0].Exists {
		t.Fatalf("selected defaults absent from inventory: %+v %v", inventory, err)
	}
	encoded, _ := json.Marshal(inventory)
	if strings.Contains(string(encoded), "current-value") || !strings.Contains(string(encoded), "TABA_TWG_SITE") {
		t.Fatalf("inventory leaked values or lost names: %s", encoded)
	}
	fixtureFile(t, filepath.Join(c.ConfigRoot, "wtc.env"), "WTC_TWG_SITE=legacy-value\n", 0600)
	if got, err := machineTWGSite(c.ConfigRoot); err != nil || got != "legacy-value" {
		t.Fatalf("legacy defaults lost authority: %q, %v", got, err)
	}
}

func TestTabaConfigRebuildsUnversionedToolchainCache(t *testing.T) {
	c := fixture(t)
	cached, current := filepath.Join(c.Collection, "cached"), filepath.Join(c.Collection, "current")
	for _, path := range []string{cached, current} {
		if err := os.Mkdir(path, 0755); err != nil {
			t.Fatal(err)
		}
	}
	cache := filepath.Join(c.Collection, ".env.toolchain")
	fixtureFile(t, cache, "WTC_TOOLCHAIN_PATH="+cached+"\n", 0600)
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(cache, old, old); err != nil {
		t.Fatal(err)
	}
	fixtureFile(t, filepath.Join(c.Harness, "taba.toml"), "[agent_env]\nprepend_paths = ['current']\n", 0644)
	opened, err := OpenCollection(c.Collection)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir()) // No mise: exercise config and cache selection only.
	if got, err := opened.AgentToolchainPath(false); err != nil || got != current {
		t.Fatalf("stale toolchain cache used: %q %v", got, err)
	}
}

func TestTabaScopeIsPreservedWithoutCreatingLegacyScope(t *testing.T) {
	c := fixture(t)
	path := filepath.Join(c.Collection, "SCOPE.md")
	fixtureFile(t, path, "Keep this task context.\n", 0644)
	var result SkillRenderResult
	if err := c.seedScope(false, &result); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(c.Collection, "WTC-SCOPE.md")); !os.IsNotExist(err) {
		t.Fatal("scope seeding shadowed SCOPE.md")
	}
	if data, _ := os.ReadFile(path); string(data) != "Keep this task context.\n" {
		t.Fatal("scope seeding overwrote the current task context")
	}
}

func TestTabaConfigSelectionInvalidatesOlderToolchainCache(t *testing.T) {
	for _, transition := range []string{"remove-legacy", "add-legacy", "edit-alternate"} {
		for _, clearPaths := range []bool{false, true} {
			t.Run(transition+map[bool]string{false: "/replace", true: "/clear"}[clearPaths], func(t *testing.T) {
				c := fixture(t)
				for _, name := range []string{"previous", "current"} {
					if err := os.Mkdir(filepath.Join(c.Collection, name), 0755); err != nil {
						t.Fatal(err)
					}
				}
				t.Setenv("PATH", t.TempDir())
				legacy, alias := filepath.Join(c.Harness, "wtc.toml"), filepath.Join(c.Harness, "taba.toml")
				before := "[agent_env]\nprepend_paths = ['previous']\n"
				after := "[agent_env]\nprepend_paths = ['current']\n"
				want := filepath.Join(c.Collection, "current")
				if clearPaths {
					after, want = "[agent_env]\nprepend_paths = []\n", ""
				}
				initial := alias
				if transition == "remove-legacy" {
					initial = legacy
					fixtureFile(t, alias, after, 0644)
				}
				fixtureFile(t, initial, before, 0644)
				opened, err := OpenCollection(c.Collection)
				if err != nil {
					t.Fatal(err)
				}
				if got, err := opened.AgentToolchainPath(false); err != nil || got != filepath.Join(c.Collection, "previous") {
					t.Fatalf("initial toolchain %q %v", got, err)
				}
				selected := alias
				switch transition {
				case "remove-legacy":
					if err := os.Remove(legacy); err != nil {
						t.Fatal(err)
					}
				case "add-legacy":
					selected = legacy
					fixtureFile(t, selected, after, 0644)
				case "edit-alternate":
					fixtureFile(t, selected, after, 0644)
				}
				old := time.Now().Add(-time.Hour)
				if err := os.Chtimes(selected, old, old); err != nil {
					t.Fatal(err)
				}
				opened, err = OpenCollection(c.Collection)
				if err != nil {
					t.Fatal(err)
				}
				if got, err := opened.AgentToolchainPath(false); err != nil || got != want {
					t.Fatalf("toolchain after selection change = %q %v, want %q", got, err, want)
				}
				if got := readToolchainCache(filepath.Join(c.Collection, ".env.toolchain")); got != want {
					t.Fatalf("stale paths persisted in cache: %q", got)
				}
			})
		}
	}
}

func TestHarnessInitRefusesToShadowTabaInputs(t *testing.T) {
	for _, name := range []string{"taba.toml", ".taba-version"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			fixtureFile(t, filepath.Join(dir, name), "keep\n", 0644)
			for _, dryRun := range []bool{true, false} {
				_, err := InitHarness(HarnessInitOptions{Dir: dir, Name: "widget", Remote: "https://example.invalid/widget.git", CLIVersion: "0.1.54", DryRun: dryRun})
				if err == nil || !strings.Contains(err.Error(), "shadow") {
					t.Fatalf("existing alias was not protected (dry=%v): %v", dryRun, err)
				}
			}
			entries, _ := os.ReadDir(dir)
			if len(entries) != 1 {
				t.Fatal("refused init left output files behind")
			}
		})
	}
}
