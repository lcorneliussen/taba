package wtc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReceivingRootRejectsForeignInputs(t *testing.T) {
	for _, source := range []string{"TABA_CONFIG_ROOT", "WTC_CONFIG_ROOT", "generated", "local"} {
		t.Run(source, func(t *testing.T) {
			for _, key := range []string{"TABA_CONFIG_ROOT", "WTC_CONFIG_ROOT"} {
				t.Setenv(key, "")
			}
			root := t.TempDir()
			receiver := filepath.Join(root, "project-b", "task")
			harness := filepath.Join(receiver, "harness")
			if err := os.MkdirAll(harness, 0755); err != nil {
				t.Fatal(err)
			}
			fixtureFile(t, filepath.Join(harness, ".harness-repos.yml"), "repos:\n  - name: widget\n    remote: https://example.invalid/widget.git\n", 0644)
			foreign := filepath.Join(root, "project-a", ".config")
			if source == "generated" || source == "local" {
				name := ".env.collection"
				if source == "local" {
					name += ".local"
				}
				fixtureFile(t, filepath.Join(receiver, name), "TABA_CONFIG_ROOT='"+foreign+"'\n", 0600)
			} else {
				t.Setenv(source, foreign)
			}
			c, err := OpenCollection(receiver)
			if err == nil {
				_, err = c.RenderEnv()
			}
			if err == nil {
				t.Fatal("foreign root accepted without receiving permission")
			}
			if strings.Contains(err.Error(), foreign) {
				t.Fatal("diagnostic disclosed rejected root")
			}
		})
	}
}

func TestReceivingRootAdHocIsCollectionLocal(t *testing.T) {
	for _, key := range []string{"TABA_CONFIG_ROOT", "WTC_CONFIG_ROOT"} {
		t.Setenv(key, "")
	}
	collection := filepath.Join(t.TempDir(), "task")
	fixtureFile(t, filepath.Join(collection, AdHocConfigDir, "repos.yml"), "repos: []\n", 0644)
	c, err := OpenCollection(collection)
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(collection)
	if err != nil {
		t.Fatal(err)
	}
	if c.ConfigRoot != filepath.Join(want, ".config") {
		t.Fatal("ad-hoc root is not collection-local")
	}
}
