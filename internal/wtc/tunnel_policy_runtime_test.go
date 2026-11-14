package wtc

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestTunnelPolicyProductionWriterReplacementTombstoneAndIsolation(t *testing.T) {
	spellings := []string{"direct", "symlink-spelling"}
	if runtime.GOOS == "darwin" {
		spellings = append(spellings, "mac-var-spelling")
	}
	for _, spelling := range spellings {
		t.Run(spelling, func(t *testing.T) {
			c := runtimeFixture(t)
			fixtureFile(t, filepath.Join(c.Collection, "widget/.harness/dekit.tasks.yaml"), "tasks:\n web/server:\n  cmd: [echo, synthetic]\n  x-wtc: {url: 'http://127.0.0.1:3000'}\n", 0644)
			entry := testTunnelDefinition().Entry.Policy
			if err := c.SetTunnelPolicy("widget", "web", "preview", entry, false); err != nil {
				t.Fatal(err)
			}
			project, local, err := c.tunnelPolicyPaths()
			if err != nil || local != filepath.Join(c.Collection, ".config", "tunnels.toml") {
				t.Fatal(project, local, err)
			}
			state, err := c.readTunnelJournal()
			if err != nil || state.Policy == nil || state.Policy.Path != local || tunnelJournalObligations(state) {
				t.Fatal("policy-only ownership/obligations incorrect", err)
			}
			plan, _, err := c.runtimePlan(c.Config.Runtime.Binary)
			if err != nil {
				t.Fatal(err)
			}
			entries, err := c.effectiveTunnels(plan)
			if err != nil || len(entries) != 1 || !entries[0].Matched || entries[0].Scope != "collection" {
				t.Fatal(entries, err)
			}
			no := false
			if err = c.SetTunnelPolicy("widget", "web", "preview", tunnelPolicyEntry{Enabled: &no}, false); err != nil {
				t.Fatal(err)
			}
			policy, err := readTunnelPolicyFile(local)
			if err != nil || !policy.Entries[testTunnelKey()].disabled() {
				t.Fatal("exact tombstone failed", err)
			}
			data, _ := os.ReadFile(local)
			if strings.Contains(string(data), "provider =") {
				t.Fatal("disabled entry retained provider fields")
			}
			if err = c.SetTunnelPolicy("widget", "web", "preview", entry, false); err != nil {
				t.Fatal(err)
			}
			// Explicitly change the credential grant: defaults may normally share a
			// physical root, but relocation must not relocate desired tunnel policy.
			scope, err := c.ValidateControlRoot()
			if err != nil {
				t.Fatal(err)
			}
			foreign := t.TempDir()
			if spelling == "symlink-spelling" {
				alias := filepath.Join(t.TempDir(), "approved-root-alias")
				if err = os.Symlink(foreign, alias); err != nil {
					t.Fatal(err)
				}
				foreign = alias
			}
			if spelling == "mac-var-spelling" {
				foreign, err = os.MkdirTemp("/var/tmp", "taba-tunnel-canonical-")
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.RemoveAll(foreign) })
			}
			fixtureFile(t, scope.PolicySource, fmt.Sprintf("[secrets]\nroot = %q\nallow_external = true\n", foreign), 0600)
			c, err = OpenCollection(c.Collection)
			if err != nil {
				t.Fatal(err)
			}
			newProject, newLocal, err := c.tunnelPolicyPaths()
			if err != nil {
				t.Fatalf("resolve desired paths: %v", err)
			}
			if newProject != project {
				t.Errorf("project desired policy path: expected %q; actual %q", project, newProject)
			}
			if newLocal != local {
				t.Errorf("collection desired policy path: expected %q; actual %q", local, newLocal)
			}
			canonicalForeign, canonicalErr := canonicalPath(foreign)
			if canonicalErr != nil {
				t.Fatal(canonicalErr)
			}
			if c.ConfigRoot != canonicalForeign {
				t.Errorf("canonical receiving credential root: expected %q from supplied %q; actual %q", canonicalForeign, foreign, c.ConfigRoot)
			}
			if err = c.SetTunnelPolicy("widget", "web", "preview", entry, false); err != nil {
				t.Fatal(err)
			}
			if _, err = os.Stat(filepath.Join(foreign, "tunnels.toml")); !os.IsNotExist(err) {
				t.Fatal("desired policy written into shared credential root")
			}
			if _, err = os.Stat(filepath.Join(c.ConfigRoot, "provider-store-created")); !os.IsNotExist(err) {
				t.Fatal("policy touched provider data")
			}
		})
	}
}

func TestTunnelProjectDefaultsUseVerifiedRegisteredIdentityAcrossDirectoryAliases(t *testing.T) {
	c := runtimeFixture(t)
	ctxBinary := c.Config.Runtime.Binary
	if err := os.Rename(filepath.Join(c.Collection, "widget"), filepath.Join(c.Collection, "ext.widget")); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(c.Collection, "ext.widget")
	fixtureGit(t, "-C", alias, "remote", "add", "origin", "https://github.com/example/widget.git")
	registry, err := os.ReadFile(c.registryPath())
	if err != nil {
		t.Fatal(err)
	}
	registry = []byte(strings.ReplaceAll(string(registry), "https://example.invalid/widget.git", "https://github.com/example/widget.git") + "  - name: other-owner\n    remote: https://github.com/independent/widget.git\n    default_ref: origin/main\n  - name: other-forge\n    remote: https://gitlab.com/example/widget.git\n    default_ref: origin/main\n")
	fixtureFile(t, c.registryPath(), string(registry), 0644)
	if err = c.load(); err != nil {
		t.Fatal(err)
	}
	c.Config.Runtime.Backend = "dekit"
	c.Config.Runtime.Binary = ctxBinary
	for index, repo := range []struct{ name, remote string }{{"ext.widget", ""}, {"other-owner", "https://github.com/independent/widget.git"}, {"other-forge", "https://gitlab.com/example/widget.git"}} {
		path := filepath.Join(c.Collection, repo.name)
		if repo.remote != "" {
			enlistedIdentityGit(t, path, repo.remote)
		}
		fixtureFile(t, filepath.Join(path, ".harness/dekit.tasks.yaml"), fmt.Sprintf("tasks:\n web/server:\n  cmd: [echo, synthetic]\n  x-wtc: {url: 'http://127.0.0.1:%d'}\n", 3000+index), 0644)
		fixtureFile(t, filepath.Join(path, ".harness/tunnel-event"), "#!/bin/sh\n# synthetic event from "+repo.name+"\necho '{}'\n", 0755)
	}
	entry := testTunnelDefinition().Entry.Policy
	entry.Events = map[string]tunnelEventCommand{"registered": {Command: []string{".harness/tunnel-event"}}}
	if err = c.SetTunnelPolicy("widget", "web", "preview", entry, true); err != nil {
		t.Fatal(err)
	}
	plan, _, err := c.runtimePlan(ctxBinary)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := c.effectiveTunnels(plan)
	if err != nil || len(entries) != 1 || !entries[0].Matched || entries[0].Scope != "project" {
		t.Fatal("registered project default did not match alias", entries, err)
	}
	d, err := c.tunnelDefinitionFor(entries[0], plan)
	if err != nil || d.Directory != "ext.widget" || d.Target != "http://127.0.0.1:3000" {
		t.Fatal("policy chose a colliding owner/forge target", d, err)
	}
	digest, _ := d.digest()
	matchedStatus := false
	for _, item := range c.recordedTunnelItems(c.runtimeDeclarations()) {
		if item.ManagedTunnel && item.Repo == "widget" && item.TunnelName == "preview" {
			matchedStatus = item.Dir == "ext.widget" && item.DesiredDefinitionID == digest
		}
	}
	if !matchedStatus {
		t.Fatal("pure status lost registered alias or current approval definition")
	}
	for _, task := range plan.Tasks {
		if task.Dir == "ext.widget" && task.Repo != "widget" {
			t.Fatal("runtime lost canonical registered identity")
		}
	}
	// Same basename at another owner/forge is not the registered identity.
	for _, remote := range []string{"https://github.com/independent/widget.git", "https://gitlab.com/example/widget.git"} {
		fixtureGit(t, "-C", alias, "remote", "set-url", "origin", remote)
		foreign, _, err := c.runtimePlan(ctxBinary)
		if err != nil {
			t.Fatal(err)
		}
		entries, err = c.effectiveTunnels(foreign)
		if err != nil || entries[0].Matched {
			t.Fatal("foreign checkout inherited registered default", entries, err)
		}
		if _, err = c.tunnelDefinitionFor(entries[0], foreign); err == nil {
			t.Fatal("foreign identity selected target/hook path")
		}
	}
}

func TestTunnelDefinitionSessionChoiceDoesNotRenewAuthority(t *testing.T) {
	d := testTunnelDefinition()
	before, err := d.digest()
	if err != nil {
		t.Fatal(err)
	}
	no := false
	d.Entry.Selected = &no
	after, err := d.digest()
	if err != nil || before != after {
		t.Fatal("session choice changed definition", err)
	}
	d.Entry.Policy.Inputs.HostingFile = "provider/host.token"
	changed, err := d.digest()
	if err != nil || changed == before {
		t.Fatal("credential reference did not bind definition", err)
	}
}

func TestTunnelPolicyProductionCredentialReadUsesOnlyApprovedExplicitInput(t *testing.T) {
	c := runtimeFixture(t)
	scope, err := c.ValidateControlRoot()
	if err != nil {
		t.Fatal(err)
	}
	fixtureFile(t, filepath.Join(scope.Root, "provider/host.token"), "synthetic-token\n", 0600)
	value, err := c.tunnelSecret("provider/host.token")
	if err != nil || value != "synthetic-token" {
		t.Fatal("explicit input failed", err)
	}
	outside := filepath.Join(t.TempDir(), "outside-token")
	fixtureFile(t, outside, "do-not-read", 0600)
	if _, err = c.tunnelSecret(outside); err == nil {
		t.Fatal("foreign input accepted")
	}
	if err = os.Symlink(outside, filepath.Join(scope.Root, "escape")); err != nil {
		t.Fatal(err)
	}
	if _, err = c.tunnelSecret("escape"); err == nil {
		t.Fatal("symlink escape accepted")
	}
	if _, err = c.tunnelSecret(""); err == nil {
		t.Fatal("missing input fell back to ambient auth")
	}
}
