package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRenderedSkillUsesTheNameTheCollectionHas(t *testing.T) {
	collection := t.TempDir()
	render := func(root, name string) {
		t.Helper()
		dir := filepath.Join(collection, root, "skills", name)
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: "+name+"\n---\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if got := renderedSkill(collection, "taba-catch-up"); got != "taba-catch-up" {
		t.Fatalf("nothing rendered: %s", got)
	}
	render(".agents", "wtc-catch-up")
	if got := renderedSkill(collection, "taba-catch-up"); got != "wtc-catch-up" {
		t.Fatalf("collection rendered before the rename: %s", got)
	}
	render(".claude", "taba-catch-up")
	if got := renderedSkill(collection, "taba-catch-up"); got != "taba-catch-up" {
		t.Fatalf("collection with both names: %s", got)
	}
	if got := renderedSkill(collection, "wtc-follow"); got != "wtc-follow" {
		t.Fatalf("an old name is kept as given: %s", got)
	}
}
