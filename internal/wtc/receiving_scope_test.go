package wtc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestReceivingRootRequiresDirectory(t *testing.T) {
	for _, kind := range []string{"file", "fifo", "planned"} {
		t.Run(kind, func(t *testing.T) {
			c := fixture(t)
			for _, key := range rootAliases {
				t.Setenv(key, "")
			}
			switch kind {
			case "file":
				fixtureFile(t, c.ConfigRoot, "synthetic", 0600)
			case "fifo":
				if err := unix.Mkfifo(c.ConfigRoot, 0600); err != nil {
					t.Fatal(err)
				}
			}
			_, err := c.ValidateControlRoot()
			if kind == "planned" {
				if err != nil {
					t.Fatal(err)
				}
				if _, err := os.Stat(c.ConfigRoot); !os.IsNotExist(err) {
					t.Fatal("resolver created planned directory")
				}
			} else {
				if err == nil || !strings.Contains(err.Error(), "directory") {
					t.Fatalf("non-directory root accepted: %v", err)
				}
				if _, err := c.ReceivingCommand("false"); err == nil {
					t.Fatal("launch accepted non-directory control root")
				}
			}
		})
	}
}

func TestReceivingPermissionIsExactAndLocal(t *testing.T) {
	p := localProject(t)
	for _, key := range rootAliases {
		t.Setenv(key, "")
	}
	shared := realPath(t, t.TempDir())
	policy := filepath.Join(p.ConfigDir, "taba.toml")
	fixtureFile(t, policy, "[secrets]\nroot='"+shared+"'\n", 0644)
	if _, err := ResolveReceivingScope(ReceivingContext{Project: p}); err == nil {
		t.Fatal("external root accepted without opt-in")
	}
	fixtureFile(t, policy, "[secrets]\nroot='"+shared+"'\nallow_external=true\n", 0644)
	scope, err := ResolveReceivingScope(ReceivingContext{Project: p})
	if err != nil || scope.Root != shared || !scope.External || scope.PolicySource != policy {
		t.Fatalf("explicit permission: %+v %v", scope, err)
	}
	t.Setenv("TABA_CONFIG_ROOT", filepath.Join(shared, "another"))
	if _, err := ResolveReceivingScope(ReceivingContext{Project: p}); err == nil {
		t.Fatal("exact permission authorized a different root")
	}
	t.Setenv("TABA_CONFIG_ROOT", "")
	other := localProject(t)
	t.Setenv("WTC_CONFIG_ROOT", shared)
	if _, err := ResolveReceivingScope(ReceivingContext{Project: other}); err == nil {
		t.Fatal("other project inherited sharing permission")
	}
}

func TestReceivingContextCannotBorrowEnclosingProject(t *testing.T) {
	for _, key := range rootAliases {
		t.Setenv(key, "")
	}
	p := localProject(t)
	nested := filepath.Join(p.Root, "nested")
	fixtureFile(t, filepath.Join(nested, ProjectDescriptorFile), "schema_version=1\nmode='local'\n", 0644)
	if _, err := ResolveReceivingScope(ReceivingContext{Project: p, Collection: filepath.Join(nested, "task")}); err == nil {
		t.Fatal("outer project supplied nested receiver permission")
	}
	standalone := filepath.Join(p.Root, "standalone")
	fixtureFile(t, filepath.Join(standalone, AdHocConfigDir, "repos.yml"), "repos: []\n", 0644)
	if _, err := ResolveReceivingScope(ReceivingContext{Project: p, Collection: standalone}); err == nil {
		t.Fatal("project overrode standalone receiver identity")
	}
	if _, err := ResolveReceivingScope(ReceivingContext{Collection: p.Root}); err == nil {
		t.Fatal("project root accepted as a standalone receiver")
	}
}

func TestReceivingPolicyAliasTransitionRevalidatesPermission(t *testing.T) {
	c := fixture(t)
	for _, key := range rootAliases {
		t.Setenv(key, "")
	}
	// Introducing a supported config spelling does not change an owned root.
	fixtureFile(t, filepath.Join(c.Harness, "taba.toml"), "[herdr]\nsession='synthetic'\n", 0644)
	scope, err := c.ValidateControlRoot()
	if err != nil || scope.Root != c.ConfigRoot || filepath.Base(scope.PolicySource) != "taba.toml" {
		t.Fatalf("config alias transition refused: %+v %v", scope, err)
	}
	// The newly selected policy is still checked; filename changes grant nothing.
	foreign := realPath(t, t.TempDir())
	fixtureFile(t, filepath.Join(c.Harness, "wtc.toml"), "[secrets]\nroot='"+foreign+"'\nallow_external=true\n", 0644)
	if _, err := c.ValidateControlRoot(); err == nil {
		t.Fatal("config alias transition silently redirected a selected root")
	}
}

func TestReceivingConsumersUseResolvedScope(t *testing.T) {
	c := fixture(t)
	for _, key := range rootAliases {
		t.Setenv(key, "")
	}
	approved := c.ConfigRoot
	c.ConfigRoot = ""
	data, err := c.RenderEnv()
	if err != nil || !strings.Contains(string(data), "TABA_CONFIG_ROOT='"+approved+"'") {
		t.Fatalf("empty context root fell back: %s %v", data, err)
	}
	if err := c.WriteEnv([]byte("TABA_CONFIG_ROOT=/foreign\n")); err == nil {
		t.Fatal("write accepted unapproved generated root")
	}
	if _, err := os.Stat(filepath.Join(c.Collection, ".env.collection")); !os.IsNotExist(err) {
		t.Fatal("refused env write changed files")
	}
	if _, err := c.ListSecrets(""); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(approved, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(approved, "gh")); err != nil {
		t.Fatal(err)
	}
	if _, err := c.RenderEnv(); err == nil {
		t.Fatal("emitted configuration through an escaping source symlink")
	}
}

func TestReceivingEnvironmentRejectsLinkedFilesBeforeReading(t *testing.T) {
	c := fixture(t)
	for _, key := range rootAliases {
		t.Setenv(key, "")
	}
	outside := filepath.Join(t.TempDir(), "synthetic")
	fixtureFile(t, outside, "FOREIGN_TOKEN=synthetic\n", 0600)
	if err := os.Symlink(outside, filepath.Join(c.Collection, ".env.collection.local")); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ReceivingEnvironment(nil); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("linked environment accepted: %v", err)
	}
}

func TestAdHocForeignRootRefusesBeforeScaffolding(t *testing.T) {
	root := adHocFixture(t)
	t.Setenv("TABA_CONFIG_ROOT", filepath.Join(root, "foreign"))
	target := filepath.Join(root, "new")
	if _, err := InitAdHoc(target); err == nil {
		t.Fatal("foreign root accepted")
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatal("scope refusal wrote a partial scaffold")
	}
}

func TestReceivingRootSymlinkAndUseTimeRevalidation(t *testing.T) {
	c := fixture(t)
	for _, key := range rootAliases {
		t.Setenv(key, "")
	}
	foreign := realPath(t, t.TempDir())
	if err := os.Symlink(foreign, filepath.Join(c.Workspace, ".config")); err != nil {
		t.Fatal(err)
	}
	if _, err := c.RenderEnv(); err == nil {
		t.Fatal("default symlink consumed a foreign source")
	}
	if err := os.Remove(filepath.Join(c.Workspace, ".config")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(c.ConfigRoot, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(c.Workspace, "alias")
	if err := os.Symlink(c.ConfigRoot, alias); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TABA_CONFIG_ROOT", alias)
	if _, err := c.RenderEnv(); err != nil {
		t.Fatal(err)
	}
	fixtureFile(t, filepath.Join(c.ConfigRoot, "widget", "safe"), "synthetic", 0600)
	if err := os.Symlink(filepath.Join(foreign, "secret"), filepath.Join(c.ConfigRoot, "widget", "escape")); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ListSecrets(""); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("source escape accepted: %v", err)
	}
	if _, err := c.LinkSecrets(SecretLinkOptions{}); err == nil {
		t.Fatal("linking started before validating source escape")
	}
	if _, err := os.Lstat(filepath.Join(c.Collection, "widget", "safe")); !os.IsNotExist(err) {
		t.Fatal("partial link on refusal")
	}
	// Replacing policy after opening cannot retarget a previously selected context.
	fixtureFile(t, filepath.Join(c.Harness, "wtc.toml"), "[secrets]\nroot='"+foreign+"'\nallow_external=true\n", 0644)
	t.Setenv("TABA_CONFIG_ROOT", "")
	if _, err := c.ValidateControlRoot(); err == nil {
		t.Fatal("stale context silently changed control roots")
	}
}

func TestReceivingLaunchValidatesFinalRoot(t *testing.T) {
	c := fixture(t)
	for _, key := range rootAliases {
		t.Setenv(key, "")
	}
	data, err := c.RenderEnv()
	if err != nil {
		t.Fatal(err)
	}
	if err := c.WriteEnv(data); err != nil {
		t.Fatal(err)
	}
	env, err := c.ReceivingEnvironment([]string{"WTC_CONFIG_ROOT=" + c.ConfigRoot, "TABA_CONFIG_ROOT=" + c.ConfigRoot, "CUSTOM=kept"})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range rootAliases {
		count := 0
		for _, entry := range env {
			if strings.HasPrefix(entry, key+"=") {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("duplicate %s", key)
		}
	}
	out := filepath.Join(c.Collection, "child")
	cmd, err := c.ReceivingCommand("/bin/sh", "-c", `printf '%s|%s' "$TABA_CONFIG_ROOT" "$WTC_CONFIG_ROOT" > "$1"`, "child", out)
	if err != nil {
		t.Fatal(err)
	}
	if body, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("child: %s %v", body, err)
	}
	if body, err := os.ReadFile(out); err != nil || string(body) != c.ConfigRoot+"|"+c.ConfigRoot {
		t.Fatalf("child roots: %q %v", body, err)
	}
	if err := os.Remove(out); err != nil {
		t.Fatal(err)
	}
	// Indirect shell assignment is checked after evaluation, before child exec.
	fixtureFile(t, filepath.Join(c.Collection, ".env.collection.local"), "name=TABA_CONFIG_ROOT\nexport \"$name=/foreign\"\n", 0600)
	cmd, err = c.ReceivingCommand("/bin/sh", "-c", `touch "$1"`, "child", out)
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Run(); err == nil {
		t.Fatal("evaluated root redirection accepted")
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatal("child ran after root refusal")
	}
}

func TestReceivingLaunchNormalizesApprovedAliases(t *testing.T) {
	c := fixture(t)
	for _, key := range rootAliases {
		t.Setenv(key, "")
	}
	if err := os.MkdirAll(c.ConfigRoot, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(c.Workspace, "control-alias")
	if err := os.Symlink(c.ConfigRoot, alias); err != nil {
		t.Fatal(err)
	}
	if err := c.WriteEnv([]byte("TABA_CONFIG_ROOT='" + alias + "'\nWTC_CONFIG_ROOT='" + alias + "'\n")); err != nil {
		t.Fatal(err)
	}
	cmd, err := c.ReceivingCommand("/bin/sh", "-c", `printf '%s|%s' "$TABA_CONFIG_ROOT" "$WTC_CONFIG_ROOT"`)
	if err != nil {
		t.Fatal(err)
	}
	data, err := cmd.Output()
	if err != nil || string(data) != c.ConfigRoot+"|"+c.ConfigRoot {
		t.Fatalf("approved aliases not normalized: %q %v", data, err)
	}
}

func TestSnapshotConfigSelectionMatchesFilesystem(t *testing.T) {
	for _, test := range []struct {
		legacy, current string
		want            string
	}{
		{"[secrets]\nroot='legacy'\n", "[secrets]\nroot='current'\n", "wtc.toml"},
		{"", "[secrets]\nroot='current'\n", "taba.toml"},
		{"", "", "wtc.toml"},
	} {
		root := t.TempDir()
		fixtureFile(t, filepath.Join(root, "README"), "synthetic", 0644)
		if test.legacy != "" {
			fixtureFile(t, filepath.Join(root, "wtc.toml"), test.legacy, 0644)
		}
		if test.current != "" {
			fixtureFile(t, filepath.Join(root, "taba.toml"), test.current, 0644)
		}
		fixtureGit(t, "-C", root, "init", "-q", "-b", "main")
		fixtureGit(t, "-C", root, "add", "-A")
		fixtureGit(t, "-C", root, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "fixture")
		sha := fixtureGit(t, "-C", root, "rev-parse", "HEAD")
		p := &ProjectContext{Source: ProjectSource{Owner: filepath.Join(root, ".git"), Ref: sha}}
		data, name, err := p.configBytes()
		if err != nil || name != test.want {
			t.Fatalf("selection: %s %v", name, err)
		}
		local, err := configInput(root)
		if err != nil || filepath.Base(local) != name {
			t.Fatal("snapshot selection drifted from configInput")
		}
		if test.legacy != "" && string(data) != strings.TrimSpace(test.legacy) {
			t.Fatal("snapshot selected a different secrets policy")
		}
		p.Source.Ref = strings.Repeat("0", 40)
		if _, _, err := p.configBytes(); err == nil {
			t.Fatal("bad object silently treated as absent config")
		}
		p.Source.Owner = filepath.Join(root, "missing-owner")
		if _, _, err := p.configBytes(); err == nil {
			t.Fatal("bad owner silently treated as absent config")
		}
	}
}
