package wtc

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func testTunnelPolicy(t *testing.T, scope, data string) tunnelPolicy {
	t.Helper()
	p, err := parseTunnelPolicy(filepath.Join(t.TempDir(), scope, ".config", "tunnels.toml"), []byte(data))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestTunnelReceivingPolicyReplacementDisableAndSelection(t *testing.T) {
	project := testTunnelPolicy(t, "project", `schema = 1
[tunnels.widget.web.preview]
provider = "cloudflare"
mode = "named"
access = "public"
start_with_service = true
[tunnels.widget.web.preview.options]
hostname = "web.example.test"
zone_id = "synthetic-zone"
[tunnels.widget.web.private]
provider = "devtunnel"
mode = "persistent"
access = "account"
[tunnels.absent.web.preview]
provider = "cloudflare"
mode = "quick"
access = "public"
`)
	collection := testTunnelPolicy(t, "collection", `schema = 1
[tunnels.widget.web.preview]
provider = "ngrok"
mode = "domain"
access = "public"
[tunnels.widget.web.preview.options]
domain = "preview.example.test"
[tunnels.widget.web.private]
enabled = false
[tunnels.widget.web.second]
provider = "ngrok"
mode = "domain"
access = "public"
[tunnels.widget.web.second.options]
domain = "second.example.test"
`)
	before := project.Entries[tunnelKey{"widget", "web", "preview"}]
	entries, err := effectiveTunnelPolicy(project, collection, map[string]bool{"widget/web": true})
	if err != nil || len(entries) != 4 {
		t.Fatalf("effective policy: %+v %v", entries, err)
	}
	if entries[0].Key.Repo != "absent" || entries[0].Matched || entries[0].Scope != "project" {
		t.Fatalf("unmatched default was lost: %+v", entries[0])
	}
	preview, disabled, second := entries[1], entries[2], entries[3]
	if preview.Policy.Provider != "ngrok" || preview.Policy.Options.Hostname != "" || preview.Policy.Options.ZoneID != "" || preview.Policy.StartWithService || preview.Source != collection.Source || preview.Scope != "collection" || !preview.Matched {
		t.Fatalf("override must replace entire definition: %+v", preview)
	}
	if !disabled.Disabled || second.Policy.Provider != "ngrok" || second.Key.Name != "second" {
		t.Fatalf("named entry/disable lost: %+v", entries)
	}
	delete(collection.Entries, disabled.Key)
	restored, err := effectiveTunnelPolicy(project, collection, map[string]bool{"widget/web": true})
	if err != nil || restored[2].Disabled || restored[2].Policy.Provider != "devtunnel" || restored[2].Source != project.Source {
		t.Fatalf("removing override must restore inheritance: %+v %v", restored, err)
	}
	if !reflect.DeepEqual(before, project.Entries[testTunnelKey()]) {
		t.Fatal("merge mutated project defaults")
	}
}

func testTunnelKey() tunnelKey { return tunnelKey{"widget", "web", "preview"} }

func TestTunnelPolicyStandaloneUsesOnlyExplicitInputs(t *testing.T) {
	// This unit test covers the pure parser/merge input boundary. Receiving
	// authorization and filesystem isolation need separate integration coverage.
	for _, name := range []string{"WTC_CONFIG_ROOT", "TABA_CONFIG_ROOT"} {
		t.Setenv(name, filepath.Join(t.TempDir(), "foreign-shared-secrets"))
	}
	local := testTunnelPolicy(t, "standalone", `schema = 1
[tunnels.widget.web.preview]
provider = "cloudflare"
mode = "quick"
access = "public"
`)
	entries, err := effectiveTunnelPolicy(tunnelPolicy{}, local, map[string]bool{"widget/web": true})
	if err != nil || len(entries) != 1 || entries[0].Source != local.Source || entries[0].Scope != "collection" {
		t.Fatalf("standalone: %+v %v", entries, err)
	}
	other, err := effectiveTunnelPolicy(tunnelPolicy{}, tunnelPolicy{}, nil)
	if err != nil || len(other) != 0 {
		t.Fatalf("unrelated receiver inherited policy: %+v %v", other, err)
	}
}

func TestTunnelPolicyRejectsAmbiguousOrSensitiveInput(t *testing.T) {
	for _, data := range []string{
		`schema = 2`,
		`schema = 1` + "\nunknown = 'synthetic-secret-value'",
		"schema = 1\n[tunnels.widget.web.preview]\nprovider = 'cloudflare'\nmode = 'quick'\naccess = 'public'\ntoken = 'synthetic-secret-value'",
		"schema = 1\n[tunnels.widget.web.preview]\nenabled = false\nprovider = 'ngrok'",
		"schema = 1\n[tunnels.widget.web.preview]\nenabled = false\nstart_with_service = false",
		"schema = 1\n[tunnels.widget.web.preview]\nprovider = 'cloudflare'\nmode = 'named'\naccess = 'public'", // requires hostname and zone_id
		"schema = 1\n[tunnels.'..'.web.preview]\nenabled = false",
		"schema = 1\n[tunnels.widget.'web/server'.preview]\nenabled = false",
		"schema = 1\n[tunnels.widget.web.preview]\nenabled = false\nenabled = true",
		"schema = 'synthetic-secret-value'",
		"schema = 1\n[tunnels.widget.web.preview]\nprovider = 'cloudflare'\nmode = 'quick'\naccess = 'public'\n[tunnels.widget.web.preview.options]\nhostname = 'synthetic-secret-value'",
	} {
		_, err := parseTunnelPolicy(filepath.Join(t.TempDir(), "tunnels.toml"), []byte(data))
		if err == nil || strings.Contains(err.Error(), "synthetic-secret-value") {
			t.Fatalf("invalid policy must refuse without echoing values: %q %v", data, err)
		}
	}
	if _, err := parseTunnelPolicy("relative/tunnels.toml", []byte("schema = 1")); err == nil {
		t.Fatal("accepted unresolved policy path")
	}
}
