package wtc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGatewayPortsRepairAndPreserve(t *testing.T) {
	for _, explicit := range []string{"", "    port_offset: null\n", "    port_offset: 0\n", "    port_offset: 8\n"} {
		t.Run(strings.TrimSpace(explicit), func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			t.Setenv("XDG_DATA_HOME", t.TempDir())
			dir := filepath.Join(t.TempDir(), "collections", "example")
			c, err := LinkGatewayCollection(dir, nil)
			if err != nil {
				t.Fatal(err)
			}
			remote := "https://github.com/example-org/web.git"
			registry := "# Keep this comment\nschema_version: 1\nrepos:\n  - name: web\n    remote: " + remote + "\n    default_ref: origin/custom\n    custom: preserve\n" + explicit
			fixtureFile(t, c.registryPath(), registry, 0o644)
			fixtureFile(t, filepath.Join(dir, ".env.collection"), "COLLECTION_PORT_BASE=43100\n", 0o644)
			fixtureFile(t, filepath.Join(dir, ".env.collection.local"), "DOCS_PORT=49999\n", 0o600)
			if err := c.InheritGatewayPorts([]GatewayPort{{Name: "web", Remote: remote, Offset: 1, Names: []string{"docs", "legacy-docs"}}}); err != nil {
				t.Fatal(err)
			}
			env, _ := os.ReadFile(filepath.Join(dir, ".env.collection"))
			switch explicit {
			case "":
				if !strings.Contains(string(env), "DOCS_PORT=43101\n") || !strings.Contains(string(env), "LEGACY_DOCS_PORT=43101\n") || !strings.Contains(string(env), "WEB_PORT=43101\n") {
					t.Fatalf("lost project port names: %s", env)
				}
			case "    port_offset: null\n":
				if strings.Contains(string(env), "DOCS_PORT=") {
					t.Fatal("explicit null was overwritten")
				}
			default:
				want := "DOCS_PORT=43100\n"
				if strings.Contains(explicit, "8") {
					want = "DOCS_PORT=43108\n"
				}
				if !strings.Contains(string(env), want) {
					t.Fatalf("explicit port overwritten: %s", env)
				}
			}
			data, _ := os.ReadFile(c.registryPath())
			if !strings.Contains(string(data), "# Keep this comment") || !strings.Contains(string(data), "custom: preserve") || !strings.Contains(string(data), "origin/custom") {
				t.Fatalf("manual registry fields changed: %s", data)
			}
			before := string(data)
			if err := c.InheritGatewayPorts([]GatewayPort{{Name: "web", Remote: remote, Offset: 2, Names: []string{"docs", "legacy-docs"}}}); err != nil {
				t.Fatal(err)
			}
			data, _ = os.ReadFile(c.registryPath())
			if string(data) != before {
				t.Fatal("repeated refresh rewrote explicit metadata")
			}
			local, _ := os.ReadFile(filepath.Join(dir, ".env.collection.local"))
			if string(local) != "DOCS_PORT=49999\n" {
				t.Fatal("local overrides changed")
			}
		})
	}
}

func TestGatewayPortsRefuseCollisionBeforeWriting(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	c, err := LinkGatewayCollection(filepath.Join(t.TempDir(), "collections", "example"), nil)
	if err != nil {
		t.Fatal(err)
	}
	registry := "repos:\n  - name: web\n    remote: https://github.com/example-org/web.git\n  - name: docs\n    remote: https://github.com/other/docs.git\n"
	fixtureFile(t, c.registryPath(), registry, 0o644)
	env, _ := os.ReadFile(filepath.Join(c.Collection, ".env.collection"))
	err = c.InheritGatewayPorts([]GatewayPort{{Name: "web", Remote: "https://github.com/example-org/web.git", Offset: 1, Names: []string{"docs"}}})
	if err == nil {
		t.Fatal("port alias collision accepted")
	}
	data, _ := os.ReadFile(c.registryPath())
	after, _ := os.ReadFile(filepath.Join(c.Collection, ".env.collection"))
	if string(data) != registry || string(after) != string(env) {
		t.Fatal("collision changed generated files")
	}
}

func TestGatewayPortsPreserveExplicitAliasesAndForeignRows(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	c, err := LinkGatewayCollection(filepath.Join(t.TempDir(), "collections", "example"), nil)
	if err != nil {
		t.Fatal(err)
	}
	registry := "repos:\n  - name: web\n    remote: https://github.com/other/web.git\n    aliases: [manual]\n    port_offset: 3\n"
	fixtureFile(t, c.registryPath(), registry, 0o644)
	if err := c.InheritGatewayPorts([]GatewayPort{{Name: "web", Remote: "https://github.com/example-org/web.git", Offset: 1, Names: []string{"docs"}}}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(c.registryPath())
	if string(data) != registry {
		t.Fatal("foreign row changed")
	}
	if err := c.InheritGatewayPorts([]GatewayPort{{Name: "web", Remote: "https://github.com/other/web.git", Offset: 1, Names: []string{"docs"}}}); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(c.registryPath())
	if string(data) != registry {
		t.Fatal("explicit aliases or offset changed")
	}
}

func TestGatewayPortsRejectEmptyOrNonMappingRegistry(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	c, err := LinkGatewayCollection(filepath.Join(t.TempDir(), "collections", "example"), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, data := range []string{"", "[]", "null"} {
		fixtureFile(t, c.registryPath(), data, 0o644)
		if err := c.InheritGatewayPorts(nil); err == nil {
			t.Errorf("invalid registry accepted: %q", data)
		}
	}
}

func TestGatewayPortsAcceptInitialNullRepos(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	c, err := LinkGatewayCollection(filepath.Join(t.TempDir(), "collections", "example"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.InheritGatewayPorts(nil); err != nil {
		t.Fatal(err)
	}
}
