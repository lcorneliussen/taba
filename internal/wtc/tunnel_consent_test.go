package wtc

import "testing"

func testTunnelDefinition() tunnelDefinition {
	return tunnelDefinition{
		Owner: "synthetic-collection-owner",
		Entry: tunnelEffectiveEntry{
			Key:    testTunnelKey(),
			Policy: tunnelPolicyEntry{Provider: "cloudflare", Mode: "named", Access: "public", Options: tunnelProviderOptions{Hostname: "web.example.test", ZoneID: "synthetic-zone"}},
			Source: "/synthetic/project/.config/tunnels.toml", Scope: "project", Matched: true,
		},
		Target: "http://127.0.0.1:3000", ProviderScope: "synthetic-account/synthetic-zone",
	}
}

func TestTunnelConsentBindsEffectiveDefinition(t *testing.T) {
	d := testTunnelDefinition()
	c, err := newTunnelConsent(d, true, tunnelAccessEvidence{})
	if err != nil || !c.permits(d, tunnelAccessEvidence{}) {
		t.Fatalf("explicit public approval: %+v %v", c, err)
	}
	changes := map[string]func(*tunnelDefinition){
		"receiver": func(d *tunnelDefinition) { d.Owner = "other-owner" },
		"entry":    func(d *tunnelDefinition) { d.Entry.Key.Name = "other" },
		"service":  func(d *tunnelDefinition) { d.Entry.Key.Service = "api" },
		"repo":     func(d *tunnelDefinition) { d.Entry.Key.Repo = "other" },
		"provider": func(d *tunnelDefinition) {
			d.Entry.Policy = tunnelPolicyEntry{Provider: "devtunnel", Mode: "persistent", Access: "public"}
		},
		"hostname":      func(d *tunnelDefinition) { d.Entry.Policy.Options.Hostname = "other.example.test" },
		"registration":  func(d *tunnelDefinition) { d.Entry.Policy.Options.Registration = "other-registration" },
		"zone":          func(d *tunnelDefinition) { d.Entry.Policy.Options.ZoneID = "other-zone" },
		"access":        func(d *tunnelDefinition) { d.Entry.Policy.Access = "protected" },
		"target":        func(d *tunnelDefinition) { d.Target = "http://127.0.0.1:4000" },
		"scope":         func(d *tunnelDefinition) { d.ProviderScope = "other-account/synthetic-zone" },
		"hooks":         func(d *tunnelDefinition) { d.HooksDigest = tunnelDigest([]byte("changed hook")) },
		"reload":        func(d *tunnelDefinition) { d.ReloadDigest = tunnelDigest([]byte("changed reload")) },
		"autostart":     func(d *tunnelDefinition) { d.Entry.Policy.StartWithService = true },
		"policy source": func(d *tunnelDefinition) { d.Entry.Source = "/synthetic/other/.config/tunnels.toml" },
		"unmatched":     func(d *tunnelDefinition) { d.Entry.Matched = false },
		"disabled":      func(d *tunnelDefinition) { d.Entry.Disabled = true },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			changed := d
			change(&changed)
			if c.permits(changed, tunnelAccessEvidence{}) {
				t.Fatal("stale consent permitted changed definition")
			}
		})
	}
	yes := true
	d.Entry.Policy.Enabled = &yes
	if !c.permits(d, tunnelAccessEvidence{}) {
		t.Fatal("explicit enabled=true changed canonical effective definition")
	}
}

func TestTunnelConsentDoesNotMistakeDeclaredProtectionForProof(t *testing.T) {
	for _, access := range []string{"public", "protected", "unknown"} {
		d := testTunnelDefinition()
		d.Entry.Policy.Access = access
		if _, err := newTunnelConsent(d, false, tunnelAccessEvidence{}); err == nil {
			t.Fatalf("declared %s bypassed public approval", access)
		}
	}
	d := testTunnelDefinition()
	d.Entry.Policy = tunnelPolicyEntry{Provider: "devtunnel", Mode: "persistent", Access: "account"}
	digest, _ := d.digest()
	evidence := tunnelAccessEvidence{digest, "account", true}
	c, err := newTunnelConsent(d, false, evidence)
	if err != nil || !c.permits(d, evidence) {
		t.Fatalf("verified account scope: %+v %v", c, err)
	}
	for _, changed := range []tunnelAccessEvidence{{}, {digest, "public", true}, {digest, "account", false}, {tunnelDigest([]byte("old")), "account", true}} {
		if c.permits(d, changed) {
			t.Fatalf("unverified/stale access reused private consent: %+v", changed)
		}
	}
}
