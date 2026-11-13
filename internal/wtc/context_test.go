package wtc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRegistryShapesAndMetadata(t *testing.T) {
	cases := []struct {
		name, registry, field string
		count                 int
	}{
		{"repos", "repos:\n  - name: widget\n    remote: https://example.invalid/widget.git\n    default_ref: origin/main\n    port_offset: 2\n    deploy_workflow: release.yml\n", "deploy_workflow", 1},
		{"repositories", "repositories:\n  - name: widget\n    remote: https://example.invalid/widget.git\n    default_ref: origin/main\n    beans_prefix: widget-\n", "beans_prefix", 1},
		{"selected", "selected:\n  - name: widget\n    remote: https://example.invalid/widget.git\n    default_ref: origin/main\n    reason: active\nnon_default:\n  excluded:\n    - name: archive\n      remote: https://example.invalid/archive.git\n      default_ref: origin/main\n      reason: historical\n", "reason", 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := fixture(t)
			path := filepath.Join(c.Harness, ".harness-repos.yml")
			if err := os.WriteFile(path, []byte(tc.registry), 0644); err != nil {
				t.Fatal(err)
			}
			c, err := OpenCollection(c.Collection)
			if err != nil {
				t.Fatal(err)
			}
			if len(c.Registry.Repos) != tc.count {
				t.Fatalf("got %d repositories, want %d", len(c.Registry.Repos), tc.count)
			}
			if _, ok := c.Registry.Repos[0].Extra[tc.field]; !ok {
				t.Fatalf("lost %s metadata", tc.field)
			}
		})
	}
}

func TestRegistrySectionsRejectDuplicateNames(t *testing.T) {
	c := fixture(t)
	registry := "selected:\n  - name: widget\n    remote: https://example.invalid/widget.git\nnon_default:\n  excluded:\n    - name: widget\n      remote: https://example.invalid/other.git\n"
	if err := os.WriteFile(filepath.Join(c.Harness, ".harness-repos.yml"), []byte(registry), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenCollection(c.Collection); err == nil || !strings.Contains(err.Error(), "repeated repository name") {
		t.Fatalf("accepted duplicate names across sections: %v", err)
	}
}

func TestConfigRootDefaultsToWorkspaceConfigDirectory(t *testing.T) {
	c := newWorkspaceFixture(t)
	t.Setenv("WTC_CONFIG_ROOT", "")
	if err := os.Remove(filepath.Join(c.Collection, ".env.collection")); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	opened, err := OpenCollection(c.Collection)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(realPath(t, c.Workspace), ".config"); opened.ConfigRoot != want {
		t.Fatalf("default control root %q, want %q", opened.ConfigRoot, want)
	}
	fixtureFile(t, filepath.Join(c.Collection, ".env.collection"), "WTC_CONFIG_ROOT=/generated/root\n", 0644)
	if _, err := OpenCollection(c.Collection); err == nil {
		t.Fatal("generated root granted permission")
	}
	t.Setenv("WTC_CONFIG_ROOT", "/explicit/root")
	if _, err := OpenCollection(c.Collection); err == nil {
		t.Fatal("inherited root granted permission")
	}
}
