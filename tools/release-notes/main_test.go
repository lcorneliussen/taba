package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckFailsWhileFragmentsRemain(t *testing.T) {
	dir := t.TempDir()
	unreleased := filepath.Join(dir, "unreleased")
	if err := os.MkdirAll(unreleased, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(unreleased, "README.md"), []byte("# Unreleased\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run(dir, []string{"check"}); err != nil {
		t.Fatalf("README alone must pass: %v", err)
	}
	if err := os.WriteFile(filepath.Join(unreleased, "1-x.md"), []byte("## Status\n\n- x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run(dir, []string{"check"}); err == nil || !strings.Contains(err.Error(), "1 unreleased fragment") {
		t.Fatalf("expected leftover fragments to fail, got %v", err)
	}
	if err := run(dir, []string{"cut", "1.0.0-rc.1"}); err != nil {
		t.Fatal(err)
	}
	if err := run(dir, []string{"check", "1.0.0-rc.1"}); err != nil {
		t.Fatalf("candidate check with retained fragments: %v", err)
	}
	if err := run(dir, []string{"check", "1.0.0"}); err == nil {
		t.Fatal("candidate cut must leave fragments for stable")
	}
	if err := run(dir, []string{"cut", "1.0.0"}); err != nil {
		t.Fatal(err)
	}
	if err := run(dir, []string{"check"}); err != nil {
		t.Fatalf("check after cut: %v", err)
	}
	if err := run(dir, []string{"publish"}); err == nil {
		t.Fatal("expected usage error")
	}
}
