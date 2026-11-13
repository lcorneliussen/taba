package wtc

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestConventionsDocMatchesTheList(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "docs", "conventions.md"))
	if err != nil {
		t.Fatal(err)
	}
	doc := string(data)
	start := strings.Index(doc, "<!-- conventions:begin")
	end := strings.Index(doc, "<!-- conventions:end -->")
	if start < 0 || end < start {
		t.Fatal("docs/conventions.md has no generated section")
	}
	start += strings.Index(doc[start:], "\n") + 1
	if got, want := strings.TrimSpace(doc[start:end]), strings.TrimSpace(ConventionsMarkdown()); got != want {
		t.Fatal("docs/conventions.md is out of date; regenerate its section with `go run ./cmd/taba conventions`")
	}
}

func TestConventionsAreWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range Conventions {
		if c.ID == "" || seen[c.ID] {
			t.Fatalf("missing or duplicate id: %+v", c)
		}
		seen[c.ID] = true
		switch c.Support {
		case SupportBoth, SupportBothNewWins, SupportOldOnly, SupportNew:
		default:
			t.Fatalf("%s: unknown support %q", c.ID, c.Support)
		}
		if c.Owner != "you" && c.Owner != "taba" {
			t.Fatalf("%s: owner %q", c.ID, c.Owner)
		}
		if c.Old == "" || c.Migrate == "" || strings.Contains(c.Migrate, "|") {
			t.Fatalf("%s: needs old name and a migration step without table pipes", c.ID)
		}
	}
}

func findingIDs(findings []ConventionFinding) []string {
	var ids []string
	for _, f := range findings {
		ids = append(ids, f.ID)
	}
	sort.Strings(ids)
	return ids
}

func TestConventionFindingsInAnUnmigratedProject(t *testing.T) {
	c := fixture(t)
	writeConventionFixture(t, filepath.Join(c.Harness, "wtc.toml"), "[mise.tools]\n\"github:lcorneliussen/wtc-cli\" = \"0.2.0\"\n")
	writeConventionFixture(t, filepath.Join(c.Harness, ".wtc-cli-version"), "0.2.0\n")
	writeConventionFixture(t, filepath.Join(c.Harness, "overlays", "skills", "wtc-pr", ".wtc-base.sha256"), "x\n")
	writeConventionFixture(t, filepath.Join(c.Harness, "hooks", "wtc", "env.post.sh"), "echo \"$WTC_COLLECTION\"\nwtc status --json\n# a wtc collection is a task\n")
	writeConventionFixture(t, filepath.Join(c.Harness, "release-notes", "old.md"), "wtc status\n")
	writeConventionFixture(t, filepath.Join(c.Collection, "WTC-SCOPE.md"), "task\n")
	writeConventionFixture(t, filepath.Join(c.Collection, ".env.collection.local"), "WTC_PANES=full\n")
	writeConventionFixture(t, filepath.Join(c.Collection, ".wtc", "skills", "x"), "")
	writeConventionFixture(t, filepath.Join(c.Collection, "widget", ".harness", "dekit.tasks.yaml"), "tasks:\n  web:\n    x-wtc: {}\n")

	got, err := c.ConventionFindings()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"generated-state", "harness-config", "harness-hooks", "harness-pin", "harness-references", "harness-skills", "local-references", "mise-tool-key", "scope-file", "skill-base-digest", "task-metadata"}
	if ids := findingIDs(got); strings.Join(ids, ",") != strings.Join(want, ",") {
		t.Fatalf("findings = %v, want %v", ids, want)
	}
	for _, f := range got {
		switch f.ID {
		case "harness-references":
			if f.Path != "harness/hooks/wtc/env.post.sh" || f.Detail != "1 WTC_ variable(s), 1 wtc command(s)" {
				t.Fatalf("references counted the noun or release notes: %+v", f)
			}
		case "harness-hooks", "generated-state", "task-metadata":
			if !f.Blocked {
				t.Fatalf("%s has no taba name yet and should be waiting", f.ID)
			}
		case "harness-config", "harness-pin", "scope-file", "skill-base-digest":
			if f.Blocked {
				t.Fatalf("%s can be migrated now", f.ID)
			}
		case "harness-skills":
			if f.Blocked == tabaSkillNames() {
				t.Fatalf("harness-skills waits exactly until the release names skills taba-*: %+v", f)
			}
		}
	}
}

func TestConventionFindingsInAMigratedProject(t *testing.T) {
	c := fixture(t)
	writeConventionFixture(t, filepath.Join(c.Harness, "taba.toml"), "[mise.tools]\n\"github:lcorneliussen/taba\" = \"0.2.0\"\n")
	writeConventionFixture(t, filepath.Join(c.Harness, ".taba-version"), "0.2.0\n")
	writeConventionFixture(t, filepath.Join(c.Harness, "hooks", "env.post.sh"), "taba status --json\necho \"$TABA_COLLECTION\"\n")
	writeConventionFixture(t, filepath.Join(c.Collection, "SCOPE.md"), "task\n")
	got, err := c.ConventionFindings()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("a migrated project has findings: %+v", got)
	}
}

func TestUserConventionFindings(t *testing.T) {
	root := t.TempDir()
	config, data, control := filepath.Join(root, "config"), filepath.Join(root, "data"), filepath.Join(root, "control")
	t.Setenv("XDG_CONFIG_HOME", config)
	t.Setenv("XDG_DATA_HOME", data)
	if got := UserConventionFindings(control); len(got) != 0 {
		t.Fatalf("nothing on disk: %+v", got)
	}
	for _, dir := range []string{filepath.Join(config, "wtc"), filepath.Join(data, "wtc")} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	writeConventionFixture(t, filepath.Join(control, "wtc.env"), "")
	got := UserConventionFindings(control)
	if ids := strings.Join(findingIDs(got), ","); ids != "control-root-env,user-config-dir,user-data-dir" {
		t.Fatalf("findings = %s", ids)
	}
}

func writeConventionFixture(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
}
