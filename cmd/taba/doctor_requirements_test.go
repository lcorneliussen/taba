package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"taba.sh/taba/internal/wtc"
)

func TestConfigRequirementsNativeCLI(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "taba")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %s %v", out, err)
	}
	collection := filepath.Join(root, "workspace", "demo")
	harness := filepath.Join(collection, "harness")
	repo := filepath.Join(collection, "web")
	write := func(path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(harness, ".harness-repos.yml"), "repos:\n  - name: web\n    remote: https://example.invalid/web.git\n    default_ref: origin/main\n")
	write(filepath.Join(harness, "wtc.toml"), "")
	write(filepath.Join(repo, ".gitignore"), ".env\n.env.local\n")
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatal(string(out), err)
		}
	}
	git("init", "-q")
	schema := `# taba:requirements v1
# taba:source {"files":[{"path":".env","scope":"project","format":"dotenv"},{"path":".env.local","scope":"collection","format":"dotenv"}]}
# taba:require {"kind":"config","purpose":"Choose test connection","affects":"web tests","required":"required","scope":"collection","example":"nonsecret-default","hint":"Choose local or remote","doc":"docs/testing.md","choices":["local","remote","disabled"]}
WEB_CONNECTION=remote
# taba:require {"kind":"secret","purpose":"Authenticate remote tests","affects":"remote tests","required":"conditional","scope":"project","example":"placeholder","hint":"Request development test access from the service owner","doc":"docs/testing.md","when":{"key":"WEB_CONNECTION","equals":"remote"}}
WEB_TEST_TOKEN=replace-me
# taba:file {"file":"certs/test.pem","kind":"secret","purpose":"Authenticate optional integration","affects":"full integration","required":"optional","scope":"collection","example":"placeholder","hint":"Request a development certificate","doc":"docs/testing.md"}
`
	write(filepath.Join(repo, ".env.example"), schema)
	git("add", ".env.example", ".gitignore")
	cleanEnv := []string{}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "WTC_") || strings.HasPrefix(key, "TABA_") || strings.HasPrefix(key, "OPENCLAW") || strings.HasPrefix(key, "WEB_") || key == "HOME" || strings.HasPrefix(key, "XDG_") {
			continue
		}
		cleanEnv = append(cleanEnv, entry)
	}
	cleanEnv = append(cleanEnv, "TABA_DEV_BUILD=off", "WTC_DEV_BUILD=off", "HOME="+filepath.Join(root, "home"), "XDG_CONFIG_HOME="+filepath.Join(root, "config"), "XDG_DATA_HOME="+filepath.Join(root, "data"), "WEB_TEST_TOKEN=synthetic-inherited-canary")
	forbidden := []string{"synthetic-secret-canary", "synthetic-inherited-canary", "synthetic-malformed-canary"}
	run := func(dir string, okay bool, args ...string) []byte {
		t.Helper()
		cmd := exec.Command(binary, args...)
		cmd.Dir = dir
		cmd.Env = cleanEnv
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		err := cmd.Run()
		if (err == nil) != okay {
			t.Fatalf("%v: err=%v stdout=%s stderr=%s", args, err, stdout.Bytes(), stderr.Bytes())
		}
		for _, s := range forbidden {
			if strings.Contains(stdout.String(), s) || strings.Contains(stderr.String(), s) {
				t.Fatalf("value leaked for %v", args)
			}
		}
		return stdout.Bytes()
	}
	inventory := func(data []byte) wtc.RequirementsInventory {
		t.Helper()
		var result struct{ Data wtc.RequirementsInventory }
		if err := json.Unmarshal(data, &result); err != nil {
			t.Fatal(err, string(data))
		}
		return result.Data
	}
	item := func(inv wtc.RequirementsInventory, name string) wtc.RequirementItem {
		t.Helper()
		for _, v := range inv.Items {
			if v.Name == name {
				return v
			}
		}
		t.Fatalf("item %s absent: %+v", name, inv)
		return wtc.RequirementItem{}
	}
	doctor := func(okay bool) wtc.RequirementsInventory {
		t.Helper()
		data := run(collection, okay, "doctor", "--json")
		var result struct {
			Data []struct {
				Name      string
				Inventory wtc.RequirementsInventory
			}
		}
		if err := json.Unmarshal(data, &result); err != nil {
			t.Fatal(err, string(data))
		}
		for _, c := range result.Data {
			if c.Name == "requirements" {
				return c.Inventory
			}
		}
		t.Fatal("doctor requirements absent")
		return wtc.RequirementsInventory{}
	}
	// Static CI lint needs no receiving secrets or actual configuration files.
	run(repo, true, "config", "lint", "--example", ".env.example", "--json")
	run(collection, true, "config", "lint", "--json")
	if _, err := os.Stat(filepath.Join(root, "workspace", ".config")); !os.IsNotExist(err) {
		t.Fatal("lint created a config root")
	}
	local := filepath.Join(repo, ".env.local")
	write(local, "WEB_CONNECTION=remote\n")
	before := snapshotRequirementsTree(t, collection)
	config := inventory(run(collection, true, "config", "list", "--json"))
	secrets := inventory(run(collection, true, "secrets", "list", "--requirements", "--json"))
	diagnosed := doctor(false)
	if !reflect.DeepEqual(config, secrets) || !reflect.DeepEqual(config, diagnosed) {
		t.Fatal("shared inventories diverged")
	}
	if item(config, "WEB_TEST_TOKEN").State != "missing" {
		t.Fatal("remote credential not missing")
	}
	if got := snapshotRequirementsTree(t, collection); !reflect.DeepEqual(before, got) {
		t.Fatal("read-only CLI changed collection files")
	}
	// Merely adding an API checkout cannot change explicit remote selection.
	write(filepath.Join(collection, "api", "README.md"), "synthetic API checkout\n")
	write(filepath.Join(harness, ".harness-repos.yml"), "repos:\n  - name: web\n    remote: https://example.invalid/web.git\n    default_ref: origin/main\n  - name: api\n    remote: https://example.invalid/api.git\n    default_ref: origin/main\n")
	if out, err := exec.Command("git", "-C", filepath.Join(collection, "api"), "init", "-q").CombinedOutput(); err != nil {
		t.Fatal(string(out), err)
	}

	if item(doctor(false), "WEB_TEST_TOKEN").State != "missing" {
		t.Fatal("API checkout suppressed remote credential")
	}
	write(local, "WEB_CONNECTION=local\n")
	if inv := doctor(true); item(inv, "WEB_TEST_TOKEN").State != "not-required" || inv.LocalReadiness != "unverified" {
		t.Fatal("local route became service readiness")
	}

	write(local, "WEB_CONNECTION=disabled\n")
	if item(doctor(true), "WEB_TEST_TOKEN").State != "not-required" {
		t.Fatal("disabled optional integration required credential")
	}
	write(local, "WEB_CONNECTION=garbage\n")
	if inv := doctor(false); item(inv, "WEB_TEST_TOKEN").Applicability != "unknown" || item(inv, "WEB_CONNECTION").State != "malformed" {
		t.Fatal("unknown selection passed")
	}
	// Optional file remains nonblocking, while malformed provided inputs and
	// undocumented owned keys fail the same static lint entry point.
	write(local, "WEB_CONNECTION=local\nTYPO=synthetic-secret-canary\n")
	if inv := inventory(run(collection, false, "config", "lint", "--json")); item(inv, "TYPO").State != "undocumented" {
		t.Fatal("provided unknown key not reported")
	}
	write(local, "WEB_CONNECTION='synthetic-malformed-canary\n")
	run(collection, false, "config", "lint", "--json")
	write(local, "WEB_CONNECTION=remote\n")
	project := filepath.Join(root, "workspace", ".config", "web", ".env")
	write(project, "WEB_TEST_TOKEN=synthetic-secret-canary\n")
	inv := doctor(true)
	if token := item(inv, "WEB_TEST_TOKEN"); token.State != "present" || token.Provenance != "receiving-project-file" {
		t.Fatalf("project provenance absent: %+v", token)
	}
	run(collection, true, "config", "list")
	run(collection, true, "doctor")

	processSchema := strings.Replace(schema, `}]}`, `}],"env_names":["WEB_TEST_TOKEN"]}`, 1)
	write(filepath.Join(repo, ".env.example"), processSchema)
	withProcess := inventory(run(collection, true, "config", "list", "--process-environment", "--json"))
	if token := item(withProcess, "WEB_TEST_TOKEN"); token.Provenance != "explicit-process-input" || token.Scope != "process" {
		t.Fatal("explicit process provenance lost")
	}
	run(collection, true, "doctor", "--process-environment", "--json")
	run(collection, true, "secrets", "list", "--requirements", "--process-environment", "--json")
	write(filepath.Join(repo, ".env.example"), schema)

	// Actual dotenv substitutions stay literal through all native views.
	marker := filepath.Join(root, "must-not-exist")
	literal := "$(touch " + marker + ")"
	forbidden = append(forbidden, literal)
	write(project, "WEB_TEST_TOKEN="+literal+"\n")
	doctor(true)
	run(collection, true, "config", "list")
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("shell substitution executed")
	}
	// Existing secret links are allowed only into the same receiving namespace.
	own := filepath.Join(root, "workspace", ".config", "web", "route.env")
	write(own, "WEB_CONNECTION=local\n")
	if err := os.Remove(local); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(own, local); err != nil {
		t.Fatal(err)
	}
	if got := item(doctor(true), "WEB_CONNECTION"); got.Provenance != "receiving-project-file" || got.Scope != "project" {
		t.Fatal("approved link provenance lost")
	}
	if err := os.Remove(local); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(root, "workspace", ".config", "other", "route.env")
	write(other, "WEB_CONNECTION=local\n")
	if err := os.Symlink(other, local); err != nil {
		t.Fatal(err)
	}
	run(collection, false, "config", "lint", "--json")
	doctor(false)
	if err := os.Remove(local); err != nil {
		t.Fatal(err)
	}
	write(local, "WEB_CONNECTION=remote\n")
	// Explicit nonstandard paths reuse the same contract without discovery.
	nonstandard := strings.ReplaceAll(schema, `"path":".env"`, `"path":"config/dev.env"`)
	write(filepath.Join(repo, "config", "dev.env.example"), nonstandard)
	run(collection, true, "config", "lint", "--example", "web:config/dev.env.example", "--json")
	// Default secrets list still reads only names/link state. Deliberately invalid
	// dotenv content does not make the legacy command inspect or validate it.
	write(project, "synthetic-malformed-canary\n")
	legacy := run(collection, true, "secrets", "list", "--no-tui", "--json")
	if strings.Contains(string(legacy), `"items"`) || !strings.Contains(string(legacy), `"files"`) {
		t.Fatal("default secrets inventory contract changed")
	}
	run(collection, false, "secrets", "list", "--requirements", "--json")
	run(collection, false, "secrets", "list", "--example", "web:.env.example", "--json")
}

func snapshotRequirementsTree(t *testing.T, root string) map[string][32]byte {
	t.Helper()
	result := map[string][32]byte{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		result[rel] = sha256.Sum256(body)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}
