package wtc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeRenameFixture(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
}

func readRendered(t *testing.T, c *Context, root, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(c.Collection, root, "skills", name, "SKILL.md"))
	if err != nil {
		t.Fatalf("%s/skills/%s: %v", root, name, err)
	}
	return string(data)
}

func TestRenderSkillsUsesTabaNamesWithWtcShims(t *testing.T) {
	c := fixture(t)
	// A collection rendered by an earlier release has the defaults under the
	// old names; the new render turns them into shims in place.
	writeRenameFixture(t, filepath.Join(c.Collection, ".wtc", "skills", "wtc-pr", "SKILL.md"), "---\nname: wtc-pr\n---\nold default\n")
	if err := os.MkdirAll(filepath.Join(c.Collection, ".claude", "skills"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../../.wtc/skills/wtc-pr", filepath.Join(c.Collection, ".claude", "skills", "wtc-pr")); err != nil {
		t.Fatal(err)
	}
	if _, err := c.RenderSkills(SkillRenderOptions{}); err != nil {
		t.Fatal(err)
	}
	for _, root := range []string{".claude", ".agents"} {
		pr := readRendered(t, c, root, "taba-pr")
		if !strings.Contains(pr, "name: taba-pr") || strings.Contains(pr, "`wtc pr") {
			t.Fatalf("%s taba-pr is not the renamed default:\n%s", root, pr)
		}
		shim := readRendered(t, c, root, "wtc-pr")
		if !strings.Contains(shim, "name: wtc-pr") || !strings.Contains(shim, "Use taba-pr instead") || !strings.Contains(shim, "`/taba-pr`") {
			t.Fatalf("%s wtc-pr is not a shim to taba-pr:\n%s", root, shim)
		}
	}
	// Services are offered only with the backend, under either name.
	for _, name := range []string{"taba-svc", "wtc-svc"} {
		if _, err := os.Lstat(filepath.Join(c.Collection, ".claude", "skills", name)); !os.IsNotExist(err) {
			t.Fatalf("%s rendered without the services backend: %v", name, err)
		}
	}
}

func TestRenderSkillsKeepsHarnessOverrideUnderTheOldName(t *testing.T) {
	c := fixture(t)
	writeRenameFixture(t, filepath.Join(c.Harness, "skills", "wtc-pr", "SKILL.md"), "---\nname: wtc-pr\n---\nharness pr procedure\n")
	if _, err := c.RenderSkills(SkillRenderOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := readRendered(t, c, ".claude", "wtc-pr"); !strings.Contains(got, "harness pr procedure") {
		t.Fatalf("wtc-pr is not the harness override:\n%s", got)
	}
	if target, err := os.Readlink(filepath.Join(c.Collection, ".claude", "skills", "wtc-pr")); err != nil || target != "../../harness/skills/wtc-pr" {
		t.Fatalf("wtc-pr link = %q, %v", target, err)
	}
	pointer := readRendered(t, c, ".claude", "taba-pr")
	if !strings.Contains(pointer, "name: taba-pr") || !strings.Contains(pointer, "Use wtc-pr") || strings.Contains(pointer, "## ") {
		t.Fatalf("taba-pr should point at the harness override, not offer the default:\n%s", pointer)
	}
	// Other skills are unaffected.
	if got := readRendered(t, c, ".claude", "wtc-start"); !strings.Contains(got, "Use taba-start instead") {
		t.Fatalf("wtc-start shim missing:\n%s", got)
	}

	diffs, err := c.DiffSkills()
	if err != nil {
		t.Fatal(err)
	}
	if len(diffs) != 1 || diffs[0].Name != "wtc-pr" || diffs[0].Status == "custom" || diffs[0].DefaultHash == "" {
		t.Fatalf("an old-name override should be compared with the renamed default: %+v", diffs)
	}
}

func TestRenderSkillsAppliesOldNameSectionPatchesToTheRenamedSkill(t *testing.T) {
	c := fixture(t)
	base, err := ReadDefault("skills/taba-pr/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	var heading string
	for _, line := range strings.Split(string(base), "\n") {
		if strings.HasPrefix(line, "## ") {
			heading = line
			break
		}
	}
	if heading == "" {
		t.Fatal("taba-pr has no H2 section to patch")
	}
	patchDir := filepath.Join(c.Harness, "overlays", "skills", "wtc-pr")
	writeRenameFixture(t, filepath.Join(patchDir, "sections", "first.md"), heading+"\n\nharness section\n")
	writeRenameFixture(t, filepath.Join(patchDir, ".taba-base.sha256"), skillDigest(base)+"\n")
	if _, err := c.RenderSkills(SkillRenderOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := readRendered(t, c, ".claude", "taba-pr"); !strings.Contains(got, "harness section") || !strings.Contains(got, "name: taba-pr") {
		t.Fatalf("the old-name patch was not applied to taba-pr:\n%s", got)
	}
	if got := readRendered(t, c, ".claude", "wtc-pr"); !strings.Contains(got, "Use taba-pr instead") {
		t.Fatalf("wtc-pr shim missing:\n%s", got)
	}

	// The recorded base is read from the old-name folder: a stale one stops
	// the render instead of applying the patches to a changed default.
	writeRenameFixture(t, filepath.Join(patchDir, ".taba-base.sha256"), skillDigest([]byte("an older default\n"))+"\n")
	if _, err := c.RenderSkills(SkillRenderOptions{}); err == nil || !strings.Contains(err.Error(), "drifted") {
		t.Fatalf("a stale base under overlays/skills/wtc-pr did not stop the render: %v", err)
	}
}

func TestSeedScopeUsesTheHarnessNaming(t *testing.T) {
	for _, tc := range []struct {
		name, pinFile, want, absent string
	}{
		{"legacy harness", ".wtc-cli-version", "WTC-SCOPE.md", "SCOPE.md"},
		{"taba-native harness", ".taba-version", "SCOPE.md", "WTC-SCOPE.md"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := fixture(t)
			writeRenameFixture(t, filepath.Join(c.Harness, tc.pinFile), "0.2.1\n")
			r, err := c.RenderSkills(SkillRenderOptions{SeedScope: true})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(c.Collection, tc.want)); err != nil {
				t.Fatalf("%s not seeded: %v (%v)", tc.want, err, r.Actions)
			}
			if _, err := os.Lstat(filepath.Join(c.Collection, tc.absent)); !os.IsNotExist(err) {
				t.Fatalf("%s seeded too: %v", tc.absent, err)
			}
			if !strings.Contains(strings.Join(r.Actions, "\n"), "seeded: "+tc.want) {
				t.Fatalf("actions do not name %s: %v", tc.want, r.Actions)
			}
		})
	}
	t.Run("existing legacy scope is kept in a taba-native harness", func(t *testing.T) {
		c := fixture(t)
		writeRenameFixture(t, filepath.Join(c.Harness, ".taba-version"), "0.2.1\n")
		writeRenameFixture(t, filepath.Join(c.Collection, "WTC-SCOPE.md"), "task\n")
		if _, err := c.RenderSkills(SkillRenderOptions{SeedScope: true}); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Lstat(filepath.Join(c.Collection, "SCOPE.md")); !os.IsNotExist(err) {
			t.Fatalf("SCOPE.md created beside WTC-SCOPE.md: %v", err)
		}
	})
}

func TestCommandNameFollowsThePin(t *testing.T) {
	for pin, want := range map[string]string{"0.1.64": "wtc", "0.2.0-rc.1": "taba", "0.2.0": "taba", "0.2.1": "taba", "": "taba"} {
		c := fixture(t)
		if pin != "" {
			writeRenameFixture(t, filepath.Join(c.Harness, ".wtc-cli-version"), pin+"\n")
		}
		if got := c.CommandName(); got != want {
			t.Errorf("pin %q: CommandName() = %q, want %q", pin, got, want)
		}
	}
	c := fixture(t)
	writeRenameFixture(t, filepath.Join(c.Harness, ".taba-version"), "0.2.1\n")
	if got := c.CommandName(); got != "taba" {
		t.Errorf("new pin file: CommandName() = %q", got)
	}
	if !TabaNative(c.Harness) {
		t.Error("a harness with only .taba-version is taba-native")
	}
	writeRenameFixture(t, filepath.Join(c.Harness, ".wtc-cli-version"), "0.2.1\n")
	if TabaNative(c.Harness) {
		t.Error("a harness with .wtc-cli-version is not taba-native")
	}
}

func TestSkillsDiffPatchesTheRenamedHarnessOverride(t *testing.T) {
	c := fixture(t)
	override := "---\nname: taba-pr\n---\n# PR\n\n## Harness only\n\nlocal text\n"
	writeRenameFixture(t, filepath.Join(c.Harness, "skills", "taba-pr", "SKILL.md"), override)
	patchDir := filepath.Join(c.Harness, "overlays", "skills", "wtc-pr")
	writeRenameFixture(t, filepath.Join(patchDir, "sections", "harness.md"), "## Harness only\n\npatched text\n")
	writeRenameFixture(t, filepath.Join(patchDir, ".wtc-base.sha256"), skillDigest([]byte(override))+"\n")
	if _, err := c.RenderSkills(SkillRenderOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := readRendered(t, c, ".claude", "taba-pr"); !strings.Contains(got, "patched text") {
		t.Fatalf("render did not patch the harness override:\n%s", got)
	}
	diffs, err := c.DiffSkills()
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range diffs {
		if d.Name != "wtc-pr" {
			continue
		}
		if d.Status != "reviewed" || d.Error != "" || d.DefaultHash != skillDigest([]byte(override)) {
			t.Fatalf("diff does not patch what render patches: %+v", d)
		}
		return
	}
	t.Fatalf("no diff entry for the wtc-pr patches: %+v", diffs)
}
