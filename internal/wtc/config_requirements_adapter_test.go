package wtc

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func requirementsCollection(t *testing.T) (*Context, string, string) {
	t.Helper()
	for _, name := range rootAliases {
		t.Setenv(name, "")
	}
	c := fixture(t)
	repo := filepath.Join(c.Collection, "widget")
	if err := os.MkdirAll(repo, 0755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", repo, "init", "-q").CombinedOutput(); err != nil {
		t.Fatal(string(out), err)
	}
	_, body := requirementsFixture(t)
	body = strings.ReplaceAll(body, "web.env", ".env")
	fixtureFile(t, filepath.Join(repo, ".env.example"), body, 0644)
	if out, err := exec.Command("git", "-C", repo, "add", ".env.example").CombinedOutput(); err != nil {
		t.Fatal(string(out), err)
	}
	return c, repo, body
}
func TestConfigRequirementsDiscoveryAndStaticLint(t *testing.T) {
	c, repo, body := requirementsCollection(t)
	inv, err := c.ListConfigRequirements(RequirementsOptions{Lint: true})
	if err != nil || !inv.LintOK || inv.ReadinessOK || len(inv.Items) != 4 {
		t.Fatalf("credential-free lint: %+v %v", inv, err)
	}
	standalone, err := LintRepositoryRequirements(repo, nil)
	if err != nil || !standalone.LintOK || len(standalone.Items) != 4 {
		t.Fatalf("standalone CI lint: %+v %v", standalone, err)
	}
	// Neither untracked root files nor arbitrarily nested dotfiles are discovered.
	fixtureFile(t, filepath.Join(repo, ".env.local.example"), "# taba:requirements v1\ninvalid secret-canary\n", 0644)
	fixtureFile(t, filepath.Join(repo, "hidden", ".env.example"), body, 0644)
	inv, err = c.ListConfigRequirements(RequirementsOptions{})
	if err != nil || len(inv.Items) != 4 || !inv.LintOK {
		t.Fatalf("unbounded discovery: %+v %v", inv, err)
	}
	inv, err = c.ListConfigRequirements(RequirementsOptions{Examples: []string{"widget:hidden/.env.example"}})
	if err != nil || inv.LintOK {
		t.Fatalf("explicit mismatched canonical mapping should fail: %+v %v", inv, err)
	}
	// Legacy examples remain hints, even when tracked.
	fixtureFile(t, filepath.Join(repo, ".env.example"), "OLD=not-mandatory\n", 0644)
	inv, err = c.ListConfigRequirements(RequirementsOptions{})
	if err != nil || !inv.LintOK || !inv.ReadinessOK || len(inv.Items) != 0 {
		t.Fatalf("legacy became mandatory: %+v %v", inv, err)
	}
	inv, err = c.ListConfigRequirements(RequirementsOptions{Examples: []string{"widget:.env.example"}})
	if err != nil || !inv.LintOK || len(inv.Diagnostics) != 1 || inv.Diagnostics[0].Code != "legacy-example-hint" {
		t.Fatalf("legacy hint missing: %+v %v", inv, err)
	}
}
func TestConfigRequirementsReceivingPrecedenceAndOptInProcess(t *testing.T) {
	c, repo, _ := requirementsCollection(t)
	project := filepath.Join(c.ConfigRoot, "widget", ".env")
	fixtureFile(t, project, "WEB_CONNECTION=remote\nWEB_TEST_TOKEN=synthetic-project-canary\n", 0600)
	local := filepath.Join(repo, "web.local.env")
	fixtureFile(t, local, "WEB_CONNECTION=local\n", 0600)
	t.Setenv("WEB_CONNECTION", "remote")
	t.Setenv("WEB_TEST_TOKEN", "synthetic-process-canary")
	t.Setenv("UNRELATED_SECRET", "synthetic-unrelated-canary")
	inv, err := c.ListConfigRequirements(RequirementsOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// The selector is owned by the process but was not inspected: do not silently
	// use either inherited values or the lower-precedence file as final proof.
	if item := requirementItem(t, inv.Items, "WEB_TEST_TOKEN"); item.State != "unverified" {
		t.Fatalf("process input inferred: %+v", item)
	}
	inv, err = c.ListConfigRequirements(RequirementsOptions{ProcessEnvironment: true})
	if err != nil {
		t.Fatal(err)
	}
	item := requirementItem(t, inv.Items, "WEB_TEST_TOKEN")
	if item.State != "present" || item.Provenance != "explicit-process-input" || item.Declaration != ".env.example" || item.Repository != "widget" {
		t.Fatalf("process provenance: %+v", item)
	}
	t.Setenv("WEB_CONNECTION", "local")
	inv, err = c.ListConfigRequirements(RequirementsOptions{ProcessEnvironment: true})
	if err != nil || requirementItem(t, inv.Items, "WEB_TEST_TOKEN").State != "not-required" || inv.LocalReadiness != "unverified" {
		t.Fatalf("local selection/readiness conflated: %+v %v", inv, err)
	}
	data, _ := json.Marshal(inv)
	for _, canary := range []string{"synthetic-project-canary", "synthetic-process-canary", "synthetic-unrelated-canary"} {
		if strings.Contains(string(data), canary) {
			t.Fatal("inventory leaked value")
		}
	}
	for path, want := range map[string]string{project: "WEB_CONNECTION=remote\nWEB_TEST_TOKEN=synthetic-project-canary\n", local: "WEB_CONNECTION=local\n"} {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != want {
			t.Fatal("read-only inventory changed inputs")
		}
	}
}
func TestConfigRequirementsRejectLinksOutsideOwnReceivingNamespace(t *testing.T) {
	for _, kind := range []string{"own-project", "other-repo", "outside-project", "example-link", "directory", "writable"} {
		t.Run(kind, func(t *testing.T) {
			c, repo, body := requirementsCollection(t)
			actual := filepath.Join(repo, "web.local.env")
			target := filepath.Join(c.ConfigRoot, "widget", "linked.env")
			switch kind {
			case "other-repo":
				target = filepath.Join(c.ConfigRoot, "other", "linked.env")
			case "outside-project":
				target = filepath.Join(t.TempDir(), "linked.env")
			}
			fixtureFile(t, target, "WEB_CONNECTION=local\n", 0600)
			switch kind {
			case "example-link":
				if err := os.Remove(filepath.Join(repo, ".env.example")); err != nil {
					t.Fatal(err)
				}
				fixtureFile(t, target, body, 0600)
				if err := os.Symlink(target, filepath.Join(repo, ".env.example")); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.MkdirAll(actual, 0755); err != nil {
					t.Fatal(err)
				}
			case "writable":
				fixtureFile(t, actual, "WEB_CONNECTION=local\n", 0666)
				if err := os.Chmod(actual, 0666); err != nil {
					t.Fatal(err)
				}
			default:
				if err := os.Symlink(target, actual); err != nil {
					t.Fatal(err)
				}
			}
			inv, err := c.ListConfigRequirements(RequirementsOptions{ProcessEnvironment: true})
			if err != nil {
				t.Fatal(err)
			}
			if kind == "own-project" {
				if !inv.LintOK || (requirementItem(t, inv.Items, "WEB_CONNECTION").Provenance != "receiving-project-file" || requirementItem(t, inv.Items, "WEB_CONNECTION").Scope != "project") {
					t.Fatalf("approved secret link rejected: %+v", inv)
				}
			} else if inv.LintOK || inv.ReadinessOK {
				t.Fatalf("unsafe %s accepted: %+v", kind, inv)
			}
		})
	}
}
func TestConfigRequirementsUndocumentedAndConflictingSchemas(t *testing.T) {
	c, repo, body := requirementsCollection(t)
	fixtureFile(t, filepath.Join(repo, "web.local.env"), "WEB_CONNECTION=local\nTYPO=synthetic-canary\n", 0600)
	inv, err := c.ListConfigRequirements(RequirementsOptions{Lint: true})
	if err != nil || inv.LintOK || requirementItem(t, inv.Items, "TYPO").State != "undocumented" {
		t.Fatalf("reverse key check: %+v %v", inv, err)
	}
	// Two explicitly selected schemas cannot claim the same actual source/key.
	fixtureFile(t, filepath.Join(repo, ".env.local.example"), strings.ReplaceAll(body, `"path":"web.local.env"`, `"path":".env.local"`), 0644)
	inv, err = c.ListConfigRequirements(RequirementsOptions{Examples: []string{"widget:.env.example", "widget:.env.local.example"}, Lint: true})
	if err != nil || inv.LintOK {
		t.Fatalf("conflicting canonical ownership accepted: %+v %v", inv, err)
	}
	// Receiving identity conflicts remain a fixed diagnostic; no unrelated root
	// is opened and no control-root value is printed.
	t.Setenv("WTC_CONFIG_ROOT", filepath.Join(t.TempDir(), "synthetic-foreign-root"))
	inv, err = c.ListConfigRequirements(RequirementsOptions{})
	if err != nil || inv.LintOK || len(inv.Items) > 0 {
		t.Fatalf("foreign receiving root accepted: %+v %v", inv, err)
	}
	data, _ := json.Marshal(inv)
	if strings.Contains(string(data), "synthetic-foreign-root") {
		t.Fatal("root value leaked")
	}
}

func TestConfigRequirementsTrackedLocalExampleAndActualAliases(t *testing.T) {
	c, repo, body := requirementsCollection(t)
	second := strings.ReplaceAll(body, "WEB_", "SECOND_")
	second = strings.ReplaceAll(second, `"path":".env"`, `"path":".env.local"`)
	second = strings.ReplaceAll(second, "web.local.env", "second.local.env")
	second = strings.ReplaceAll(second, "certs/test.pem", "certs/second.pem")
	fixtureFile(t, filepath.Join(repo, ".env.local.example"), second, 0644)
	if out, err := exec.Command("git", "-C", repo, "add", ".env.local.example").CombinedOutput(); err != nil {
		t.Fatal(string(out), err)
	}
	inv, err := c.ListConfigRequirements(RequirementsOptions{Lint: true})
	if err != nil || !inv.LintOK || len(inv.Items) != 8 {
		t.Fatalf("bounded tracked local example missing: %+v %v", inv, err)
	}
	// Disjoint key lists still conflict when distinct source paths resolve to
	// the same actual file through an owned symlink.
	fixtureFile(t, filepath.Join(repo, "web.local.env"), "WEB_CONNECTION=local\n", 0600)
	alias := strings.ReplaceAll(second, `.env.local`, `alias.env`)
	alias = strings.ReplaceAll(alias, "second.local.env", "alias.env")
	fixtureFile(t, filepath.Join(repo, "alias.env.example"), alias, 0644)
	if err := os.Symlink("web.local.env", filepath.Join(repo, "alias.env")); err != nil {
		t.Fatal(err)
	}
	inv, err = c.ListConfigRequirements(RequirementsOptions{Examples: []string{"widget:.env.example", "widget:alias.env.example"}, Lint: true})
	if err != nil || inv.LintOK {
		t.Fatalf("actual source aliases accepted: %+v %v", inv, err)
	}
	found := false
	for _, d := range inv.Diagnostics {
		if d.Code == "duplicate-actual-source-ownership" {
			found = true
		}
	}
	if !found {
		t.Fatalf("actual alias conflict not identified: %+v", inv.Diagnostics)
	}
}

func TestConfigRequirementsMandatoryFilePresenceSeparateFromLocalReadiness(t *testing.T) {
	c, repo, body := requirementsCollection(t)
	body = strings.Replace(body, `"purpose":"Authenticate a separate integration","affects":"full integration","required":"optional"`, `"purpose":"Authenticate a separate integration","affects":"full integration","required":"required"`, 1)
	fixtureFile(t, filepath.Join(repo, ".env.example"), body, 0644)
	fixtureFile(t, filepath.Join(repo, "web.local.env"), "WEB_CONNECTION=local\n", 0600)
	inv, err := c.ListConfigRequirements(RequirementsOptions{ProcessEnvironment: true})
	if err != nil || inv.ReadinessOK || requirementItem(t, inv.Items, "certs/test.pem").State != "missing" {
		t.Fatalf("mandatory file absence passed: %+v %v", inv, err)
	}
	fixtureFile(t, filepath.Join(repo, "certs", "test.pem"), "synthetic-file-canary", 0600)
	inv, err = c.ListConfigRequirements(RequirementsOptions{ProcessEnvironment: true})
	if err != nil || !inv.ReadinessOK || inv.LocalReadiness != "unverified" {
		t.Fatalf("file presence conflated with local readiness: %+v %v", inv, err)
	}
	data, _ := json.Marshal(inv)
	if strings.Contains(string(data), "synthetic-file-canary") {
		t.Fatal("opaque file contents leaked")
	}
}

func TestConfigRequirementsInapplicableOpaqueFileIsNotLintDependency(t *testing.T) {
	c, repo, body := requirementsCollection(t)
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, "# taba:file ") {
			var fields map[string]any
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "# taba:file ")), &fields); err != nil {
				t.Fatal(err)
			}
			fields["required"] = "conditional"
			fields["when"] = map[string]string{"key": "WEB_CONNECTION", "equals": "remote"}
			encoded, err := json.Marshal(fields)
			if err != nil {
				t.Fatal(err)
			}
			lines[i] = "# taba:file " + string(encoded)
		}
	}
	fixtureFile(t, filepath.Join(repo, ".env.example"), strings.Join(lines, "\n"), 0644)
	fixtureFile(t, filepath.Join(repo, "web.local.env"), "WEB_CONNECTION=local\n", 0600)
	outside := filepath.Join(t.TempDir(), "opaque.pem")
	fixtureFile(t, outside, "synthetic-opaque-canary", 0600)
	if err := os.MkdirAll(filepath.Join(repo, "certs"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(repo, "certs", "test.pem")); err != nil {
		t.Fatal(err)
	}
	inv, err := c.ListConfigRequirements(RequirementsOptions{ProcessEnvironment: true, Lint: true})
	if err != nil || !inv.LintOK || !inv.ReadinessOK || requirementItem(t, inv.Items, "certs/test.pem").State != "not-required" {
		t.Fatalf("disabled opaque integration became a dependency: %+v %v", inv, err)
	}
	// Enabling the integration cannot authorize the external file. The same
	// observation becomes unverified readiness, while static key lint stays free
	// of a dependency on an actual integration certificate.
	fixtureFile(t, filepath.Join(repo, "web.local.env"), "WEB_CONNECTION=remote\n", 0600)
	inv, err = c.ListConfigRequirements(RequirementsOptions{ProcessEnvironment: true, Lint: true})
	if err != nil || !inv.LintOK || inv.ReadinessOK || requirementItem(t, inv.Items, "certs/test.pem").State != "unverified" {
		t.Fatalf("external file became ready: %+v %v", inv, err)
	}
}
