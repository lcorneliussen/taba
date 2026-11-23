package wtc

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"taba.sh/taba/internal/prompts"
)

func TestEjectPromptRecordsFullBaseAndOverrides(t *testing.T) {
	c := fixture(t)
	paths, err := Eject(c.Harness, []string{"prompts/launch/*"})
	if err != nil {
		t.Fatal(err)
	}
	override := filepath.Join(c.Harness, "prompts", "launch", "handoff.md")
	base := filepath.Join(c.Harness, "prompts", prompts.BaseDir, "launch", "handoff.md")
	if !strings.Contains(strings.Join(paths, "\n"), override) || !strings.Contains(strings.Join(paths, "\n"), base) {
		t.Fatalf("ejected %v", paths)
	}
	want, err := ReadDefault("prompts/launch/handoff.md")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(base); err != nil || !bytes.Equal(got, want) {
		t.Fatalf("base is not the full default text: %q (%v)", got, err)
	}
	edited := strings.Replace(string(want), "**Goal:** {{goal}}", "**Goal (project):** {{goal}}", 1)
	if err := os.WriteFile(override, []byte(edited), 0644); err != nil {
		t.Fatal(err)
	}
	text, err := prompts.Render(c.Prompts(), "launch.handoff", map[string]string{"collection": "c", "goal": "ship", "branch_note": "note."})
	if err != nil {
		t.Fatal(err)
	}
	p := text.Provenance()
	if !strings.Contains(text.String(), "**Goal (project):** ship") || p.Source != prompts.SourceProject || p.BaseFile != base || !p.BaseCurrent || p.BaseVersion != 1 {
		t.Fatalf("override not used: %q %+v", text, p)
	}
	if _, err := Eject(c.Harness, []string{"prompts/launch/handoff.md"}); err == nil {
		t.Fatal("overwrote an ejected prompt")
	}
}

func TestPublicReviewPromptsIgnoreProjectOverrides(t *testing.T) {
	c := fixture(t)
	override := filepath.Join(c.Harness, "prompts", "review", "checks-suffix.md")
	if err := os.MkdirAll(filepath.Dir(override), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(override, []byte("Private project rule.\n"), 0644); err != nil {
		t.Fatal(err)
	}
	private, err := prompts.Render(c.reviewPrompts(ReviewManifest{}), "review.checks-suffix", nil)
	if err != nil || private.String() != "Private project rule." {
		t.Fatalf("private review = %q (%v)", private, err)
	}
	public, err := prompts.Render(c.reviewPrompts(ReviewManifest{Public: true}), "review.checks-suffix", nil)
	if err != nil || strings.Contains(public.String(), "Private") || public.Provenance().Source != prompts.SourceEmbedded {
		t.Fatalf("public review = %q (%v)", public, err)
	}
}

func TestEmbeddedPromptsRenderWithDeclaredInputs(t *testing.T) {
	ctx := prompts.Context{Defaults: PromptDefaults()}
	list, err := prompts.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range list {
		data := map[string]string{}
		for _, input := range p.Inputs {
			data[input] = "<" + input + ">"
		}
		text, err := prompts.Render(ctx, p.ID, data)
		if err != nil {
			t.Errorf("%s: %v", p.ID, err)
			continue
		}
		if strings.Contains(text.String(), "{{") {
			t.Errorf("%s left a placeholder: %q", p.ID, text)
		}
	}
}
