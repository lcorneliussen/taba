package wtc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiffSkillsReportsPrecedenceAndBaseDrift(t *testing.T) {
	c := fixture(t)
	base, err := ReadDefault("skills/taba-customize/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	local := filepath.Join(c.Harness, "skills", "taba-customize")
	overlay := filepath.Join(c.Harness, "overlays", "skills", "taba-customize")
	for _, dir := range []string{local, overlay} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(local, "SKILL.md"), []byte("unused override\n"), 0644); err != nil {
		t.Fatal(err)
	}
	modified := strings.Replace(string(base), "# Customize wtc behavior", "# Customize local behavior", 1)
	if err := os.WriteFile(filepath.Join(overlay, "SKILL.md"), []byte(modified), 0644); err != nil {
		t.Fatal(err)
	}
	check := func(want string) SkillDiff {
		t.Helper()
		rows, err := c.DiffSkills()
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 1 || rows[0].Status != want || rows[0].Source != "overlays/skills/taba-customize/SKILL.md" {
			t.Fatalf("rows = %+v, want %s overlay", rows, want)
		}
		return rows[0]
	}
	row := check("untracked")
	if row.DefaultHash != skillDigest(base) || !strings.Contains(strings.Join(row.Changes, "\n"), "-# Customize wtc behavior\n+# Customize local behavior") {
		t.Fatalf("missing digest or line changes: %+v", row)
	}
	digest := filepath.Join(overlay, ".wtc-base.sha256")
	if err := os.WriteFile(digest, []byte(row.DefaultHash+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	check("reviewed")
	if err := os.WriteFile(digest, []byte(strings.Repeat("0", 64)+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	check("drifted")
	if err := os.WriteFile(digest, []byte("invalid\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := c.DiffSkills(); err == nil {
		t.Fatal("invalid base digest accepted")
	}
}

func TestDiffSkillsCustomAndUnchanged(t *testing.T) {
	c := fixture(t)
	base, err := ReadDefault("skills/taba-customize/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string][]byte{"taba-customize": base, "wtc-extra": []byte("custom\n")} {
		dir := filepath.Join(c.Harness, "skills", name)
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), body, 0644); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := c.DiffSkills()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Name != "taba-customize" || rows[0].Status != "current" || rows[1].Name != "wtc-extra" || rows[1].Status != "custom" {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestTabaSkillDigestInputs(t *testing.T) {
	for _, sections := range []bool{false, true} {
		t.Run(map[bool]string{false: "full-overlay", true: "sections"}[sections], func(t *testing.T) {
			c := fixture(t)
			base, err := ReadDefault("skills/taba-customize/SKILL.md")
			if err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(c.Harness, "overlays", "skills", "taba-customize")
			if sections {
				base = append(base, []byte("\n## Setup\n\nOriginal setup.\n")...)
				fixtureFile(t, filepath.Join(c.Harness, "skills", "taba-customize", "SKILL.md"), string(base), 0644)
				fixtureFile(t, filepath.Join(dir, "sections", "setup.md"), "## Setup\n\nNew setup.\n", 0644)
			} else {
				fixtureFile(t, filepath.Join(dir, "SKILL.md"), string(base)+"\nLocal details.\n", 0644)
			}
			check := func(want string) {
				t.Helper()
				rows, err := c.DiffSkills()
				if err != nil || len(rows) == 0 || rows[0].Status != want {
					t.Fatalf("digest status = %+v, %v; want %s", rows, err, want)
				}
			}
			alias := filepath.Join(dir, ".taba-base.sha256")
			fixtureFile(t, alias, skillDigest(base)+"\n", 0644)
			check("reviewed")
			if _, err := c.RenderSkills(SkillRenderOptions{DryRun: true}); err != nil {
				t.Fatal(err)
			}
			fixtureFile(t, alias, strings.Repeat("0", 64)+"\n", 0644)
			check("drifted")
			if sections {
				if _, err := c.RenderSkills(SkillRenderOptions{DryRun: true}); err == nil || !strings.Contains(err.Error(), ".taba-base.sha256") {
					t.Fatalf("alias drift was not enforced: %v", err)
				}
			}
			fixtureFile(t, filepath.Join(dir, ".wtc-base.sha256"), skillDigest(base)+"\n", 0644)
			check("reviewed")
			if _, err := c.RenderSkills(SkillRenderOptions{DryRun: true}); err != nil {
				t.Fatalf("legacy digest lost authority: %v", err)
			}
		})
	}
}

func TestDiffSkillsSectionPatchDrift(t *testing.T) {
	c := fixture(t)
	dir := filepath.Join(c.Harness, "overlays", "skills", "taba-customize")
	if err := os.MkdirAll(filepath.Join(dir, "sections"), 0755); err != nil {
		t.Fatal(err)
	}
	base, err := ReadDefault("skills/taba-customize/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	// The default skill has no H2 headings, so use a harness full skill as
	// the patch base and verify the patch is reported separately.
	local := filepath.Join(c.Harness, "skills", "taba-customize")
	if err := os.MkdirAll(local, 0755); err != nil {
		t.Fatal(err)
	}
	localBase := string(base) + "\n## Project setup\n\nOld setup.\n"
	if err := os.WriteFile(filepath.Join(local, "SKILL.md"), []byte(localBase), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sections", "setup.md"), []byte("## Project setup\n\nNew setup.\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".wtc-base.sha256"), []byte(skillDigest([]byte(localBase))+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	rows, err := c.DiffSkills()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Status != "reviewed" || rows[0].Source != "overlays/skills/taba-customize/sections" || !strings.Contains(strings.Join(rows[0].Changes, "\n"), "-Old setup.\n+New setup.") {
		t.Fatalf("patch rows = %+v", rows)
	}
	if err := os.WriteFile(filepath.Join(dir, ".wtc-base.sha256"), []byte(strings.Repeat("0", 64)+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	rows, err = c.DiffSkills()
	if err != nil || rows[0].Status != "drifted" {
		t.Fatalf("stale patch base = %+v, %v", rows, err)
	}
	if err := os.WriteFile(filepath.Join(local, "SKILL.md"), []byte(strings.Replace(localBase, "## Project setup", "## Renamed setup", 1)), 0644); err != nil {
		t.Fatal(err)
	}
	rows, err = c.DiffSkills()
	if err != nil || len(rows) != 2 || rows[0].Status != "drifted" || rows[0].Error == "" || rows[0].DefaultHash != skillDigest([]byte(strings.Replace(localBase, "## Project setup", "## Renamed setup", 1))) {
		t.Fatalf("renamed heading hides drift: %+v, %v", rows, err)
	}
}
