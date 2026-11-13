package wtc

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestInventoriesExposeNamesScopesAndLinkStateWithoutValues(t *testing.T) {
	root := t.TempDir()
	collection := filepath.Join(root, "sample")
	control := filepath.Join(root, ".config")
	harness := filepath.Join(collection, "harness")
	app := filepath.Join(collection, "app")
	for _, dir := range []string{harness, app, filepath.Join(control, "app"), filepath.Join(control, "gh")} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(path, value string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(harness, ".harness-repos.yml"), "repos:\n  - name: app\n    remote: https://github.com/example/app.git\n")
	write(filepath.Join(harness, "wtc.toml"), "[secrets]\nprod_paths = ['app/.env.prod']\n")
	write(filepath.Join(app, ".gitignore"), ".env*\n")
	if out, err := exec.Command("git", "-C", app, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %s %v", out, err)
	}
	write(filepath.Join(control, "app", ".env"), "API_TOKEN=control-secret-value\n")
	write(filepath.Join(control, "app", ".env.prod"), "PROD_TOKEN=production-secret-value\n")
	write(filepath.Join(control, "app", "visible.txt"), "unignored-secret-value\n")
	write(filepath.Join(control, "gh", "hosts.yml"), "token: gh-secret-value\n")
	write(filepath.Join(control, "wtc.env"), "# comment\nWTC_STATUS_WATCH=33\n")
	write(filepath.Join(collection, ".env.collection"), "WTC_COLLECTION='sample'\nSHARED_NAME=generated-value\n")
	write(filepath.Join(collection, ".env.collection.local"), "export SHARED_NAME=local-secret-value\nLOCAL_KEY=other-secret-value\n")
	t.Setenv("WTC_CONFIG_ROOT", control)
	c, err := OpenCollection(collection)
	if err != nil {
		t.Fatal(err)
	}
	files, err := c.ListSecrets("")
	if err != nil {
		t.Fatal(err)
	}
	byPath := map[string]SecretInventoryFile{}
	for _, item := range files.Files {
		byPath[item.Path] = item
	}
	if got := byPath["app/.env"]; got.State != "available" || got.Target != "app/.env" || got.Ignored == nil || !*got.Ignored {
		t.Fatalf("available shared file: %+v", got)
	}
	if got := byPath["app/.env.prod"]; !got.Production || got.State != "prod-excluded" {
		t.Fatalf("production file: %+v", got)
	}
	if got := byPath["app/visible.txt"]; got.State != "not-ignored" || got.Ignored == nil || *got.Ignored {
		t.Fatalf("unignored file: %+v", got)
	}
	if got := byPath["gh/hosts.yml"]; got.Scope != "all collections using this control root (workspace config)" || got.State != "control-only" {
		t.Fatalf("workspace config: %+v", got)
	}
	if got := byPath["wtc.env"]; got.Scope != "all collections using this control root (workspace config)" {
		t.Fatalf("workspace defaults file: %+v", got)
	}
	if _, err := c.LinkSecrets(SecretLinkOptions{}); err == nil { // visible.txt is deliberately not ignored.
		t.Fatal("expected link refusal for an unignored file")
	}
	files, err = c.ListSecrets("app")
	if err != nil {
		t.Fatal(err)
	}
	if len(files.Files) != 3 {
		t.Fatalf("repo filter: %+v", files.Files)
	}
	for _, item := range files.Files {
		if item.Path == "app/.env" && item.State != "linked" {
			t.Fatalf("linked state: %+v", item)
		}
	}
	write(filepath.Join(app, ".env.prod"), "local-production-override\n")
	files, err = c.ListSecrets("app")
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range files.Files {
		if item.Path == "app/.env.prod" && item.State != "local-override" {
			t.Fatalf("local file override: %+v", item)
		}
	}
	env, err := c.ListEnv()
	if err != nil {
		t.Fatal(err)
	}
	if len(env.Files) != 3 || len(env.Variables) != 5 {
		t.Fatalf("env inventory: %+v", env)
	}
	if env.Files[0].Path != "wtc.env" || env.Files[0].Scope != "all collections using this control root (workspace defaults)" {
		t.Fatalf("workspace defaults file scope: %+v", env.Files[0])
	}
	for _, variable := range env.Variables {
		if variable.Source == "wtc.env" && variable.Scope != "all collections using this control root (workspace defaults)" {
			t.Fatalf("workspace defaults scope: %+v", variable)
		}
		if variable.Name == "SHARED_NAME" && variable.Source == ".env.collection.local" && !variable.Overrides {
			t.Fatalf("missing local override marker: %+v", variable)
		}
	}
	encoded, err := json.Marshal(struct {
		Files SecretInventory `json:"secrets"`
		Env   EnvInventory    `json:"env"`
	}{files, env})
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"control-secret-value", "production-secret-value", "gh-secret-value", "local-secret-value", "other-secret-value"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("inventory exposed a value: %s", secret)
		}
	}
}
