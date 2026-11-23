package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"taba.sh/taba/internal/prompts"
	"taba.sh/taba/internal/wtc"
)

func runPrompts(t *testing.T, harness string, asJSON bool, args ...string) (string, error) {
	t.Helper()
	cwd := func() (*wtc.Context, error) { return nil, errors.New("not in a collection") }
	if harness != "" {
		cwd = func() (*wtc.Context, error) { return &wtc.Context{Harness: harness}, nil }
	}
	cmd := promptsCommand(&asJSON, cwd)
	cmd.SetArgs(args)
	out, err := recoveryOutput(t, cmd.Execute)
	return string(out), err
}

func TestPromptsCommandsReportEffectiveSource(t *testing.T) {
	out, err := runPrompts(t, "", false, "list")
	if err != nil || !strings.Contains(out, "launch.handoff") || !strings.Contains(out, "embedded") {
		t.Fatalf("list outside a collection = %q (%v)", out, err)
	}
	harness := t.TempDir()
	override := filepath.Join(harness, "prompts", "next", "step.md")
	if err := os.MkdirAll(filepath.Dir(override), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(override, []byte("Project step for {{dir}}.\n"), 0644); err != nil {
		t.Fatal(err)
	}
	out, err = runPrompts(t, harness, false, "show", "next.step")
	if err != nil || out != "Project step for {{dir}}.\n" {
		t.Fatalf("show = %q (%v)", out, err)
	}
	out, err = runPrompts(t, harness, true, "explain", "next.step")
	if err != nil {
		t.Fatal(err)
	}
	var reply struct {
		OK   bool               `json:"ok"`
		Data prompts.Provenance `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &reply); err != nil || !reply.OK || reply.Data.Source != prompts.SourceProject || reply.Data.File != override || reply.Data.BaseFile != "" {
		t.Fatalf("explain --json = %q (%v)", out, err)
	}
	if _, err := runPrompts(t, harness, false, "explain", "no.such"); err == nil {
		t.Fatal("explained an unknown prompt")
	}
}
