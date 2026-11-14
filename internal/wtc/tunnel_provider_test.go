package wtc

import "testing"

func TestTunnelTemporaryDevtunnelStillNeedsManagement(t *testing.T) {
	e := tunnelPolicyEntry{Provider: "devtunnel", Mode: "temporary", Access: "public"}
	facts := tunnelExecutionFacts{SupportedBuild: true, Scoped: true, CanHost: true, CanCleanup: true}
	if checkTunnelExecution(e, facts, false) == nil {
		t.Fatal("temporary provider allocation accepted without management authority")
	}
	facts.CanManage = true
	if err := checkTunnelExecution(e, facts, false); err != nil {
		t.Fatal(err)
	}
}

func TestTunnelDocumentedModeContracts(t *testing.T) {
	// Fixtures express the primary contracts linked in tunnel_provider.go;
	// these are not captured account responses or installed-version claims.
	for _, tc := range []struct {
		provider, mode, registration string
		stable, manage               bool
	}{
		{"cloudflare", "named", "provider", true, true},
		{"cloudflare", "quick", "none", false, false},
		{"devtunnel", "persistent", "provider", true, true},
		{"devtunnel", "temporary", "none", false, true},
		{"ngrok", "domain", "provider", true, false},
		{"tailscale", "serve", "local", true, false},
	} {
		got, err := tunnelContract(tc.provider, tc.mode)
		if err != nil || got != (tunnelProviderContract{tc.registration, tc.stable, tc.manage}) {
			t.Fatalf("%s/%s: %+v %v", tc.provider, tc.mode, got, err)
		}
	}
	for _, pair := range [][2]string{{"cloudflare", "reserved"}, {"ngrok", "quick"}, {"tailscale", "funnel"}, {"unrecognized", "named"}} {
		if _, err := tunnelContract(pair[0], pair[1]); err == nil {
			t.Fatalf("unsupported generated mode accepted: %v", pair)
		}
	}
}

func TestTunnelHostingDoesNotGrantRegistrationAuthority(t *testing.T) {
	all := tunnelExecutionFacts{SupportedBuild: true, Scoped: true, CanHost: true, CanManage: true, CanCleanup: true}
	cf := testTunnelDefinition().Entry.Policy
	if err := checkTunnelExecution(cf, all, true); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*tunnelExecutionFacts){
		"unsupported version":         func(f *tunnelExecutionFacts) { f.SupportedBuild = false },
		"ambient credential fallback": func(f *tunnelExecutionFacts) { f.Scoped = false },
		"no hosting capability":       func(f *tunnelExecutionFacts) { f.CanHost = false },
		"no management capability":    func(f *tunnelExecutionFacts) { f.CanManage = false },
		"no cleanup capability":       func(f *tunnelExecutionFacts) { f.CanCleanup = false },
	} {
		t.Run(name, func(t *testing.T) {
			facts := all
			change(&facts)
			if err := checkTunnelExecution(cf, facts, true); err == nil {
				t.Fatal("registration permitted without required capability")
			}
		})
	}
	ngrok := tunnelPolicyEntry{Provider: "ngrok", Mode: "domain", Access: "public", Options: tunnelProviderOptions{Domain: "preview.example.test"}}
	hostOnly := all
	hostOnly.CanManage = false
	if err := checkTunnelExecution(ngrok, hostOnly, false); err != nil {
		t.Fatalf("borrowed domain should not require reservation permission: %v", err)
	}
	if err := checkTunnelExecution(ngrok, hostOnly, true); err == nil {
		t.Fatal("agent hosting credential was treated as reservation API credential")
	}
	quick := tunnelPolicyEntry{Provider: "cloudflare", Mode: "quick", Access: "public"}
	if err := checkTunnelExecution(quick, all, true); err == nil {
		t.Fatal("ephemeral mode pretended to create persistent registration")
	}
	serve := tunnelPolicyEntry{Provider: "tailscale", Mode: "serve", Access: "tailnet", Options: tunnelProviderOptions{ListenerPort: 8443, Path: "/preview"}}
	if err := checkTunnelExecution(serve, hostOnly, false); err != nil {
		t.Fatal(err)
	}
	hostOnly.CanCleanup = false
	if err := checkTunnelExecution(serve, hostOnly, false); err == nil {
		t.Fatal("local shared listener cannot be allocated without targeted cleanup")
	}
	no := false
	if err := checkTunnelExecution(tunnelPolicyEntry{Enabled: &no}, all, false); err == nil {
		t.Fatal("disabled entry admitted with otherwise complete capabilities")
	}
	invalid := quick
	invalid.Options.Hostname = "unexpected.example.test"
	if err := checkTunnelExecution(invalid, all, false); err == nil {
		t.Fatal("invalid entry admitted with otherwise complete capabilities")
	}
}

func TestTunnelProviderPolicyRejectsInvalidOptionsAndAccess(t *testing.T) {
	dev := tunnelPolicyEntry{Provider: "devtunnel", Mode: "persistent", Access: "account"}
	ngrok := tunnelPolicyEntry{Provider: "ngrok", Mode: "domain", Access: "public", Options: tunnelProviderOptions{Domain: "preview.example.test"}}
	serve := tunnelPolicyEntry{Provider: "tailscale", Mode: "serve", Access: "tailnet", Options: tunnelProviderOptions{ListenerPort: 8443}}
	quick := tunnelPolicyEntry{Provider: "cloudflare", Mode: "quick", Access: "public"}
	for _, tc := range []struct {
		name   string
		base   tunnelPolicyEntry
		change func(*tunnelPolicyEntry)
	}{
		{"devtunnel hostname", dev, func(e *tunnelPolicyEntry) { e.Options.Hostname = "web.example.test" }},
		{"devtunnel zone", dev, func(e *tunnelPolicyEntry) { e.Options.ZoneID = "zone" }},
		{"devtunnel domain", dev, func(e *tunnelPolicyEntry) { e.Options.Domain = "web.example.test" }},
		{"devtunnel listener", dev, func(e *tunnelPolicyEntry) { e.Options.ListenerPort = 443 }},
		{"devtunnel path", dev, func(e *tunnelPolicyEntry) { e.Options.Path = "/" }},
		{"temporary devtunnel registration", dev, func(e *tunnelPolicyEntry) { e.Mode = "temporary"; e.Options.Registration = "named" }},
		{"devtunnel access", dev, func(e *tunnelPolicyEntry) { e.Access = "protected" }},
		{"registration identity", dev, func(e *tunnelPolicyEntry) { e.Options.Registration = "../other" }},
		{"ngrok missing domain", ngrok, func(e *tunnelPolicyEntry) { e.Options.Domain = "" }},
		{"ngrok wildcard endpoint", ngrok, func(e *tunnelPolicyEntry) { e.Options.Domain = "*.example.test" }},
		{"ngrok hostname", ngrok, func(e *tunnelPolicyEntry) { e.Options.Hostname = "web.example.test" }},
		{"ngrok zone", ngrok, func(e *tunnelPolicyEntry) { e.Options.ZoneID = "zone" }},
		{"ngrok registration", ngrok, func(e *tunnelPolicyEntry) { e.Options.Registration = "named" }},
		{"ngrok listener", ngrok, func(e *tunnelPolicyEntry) { e.Options.ListenerPort = 443 }},
		{"ngrok path", ngrok, func(e *tunnelPolicyEntry) { e.Options.Path = "/" }},
		{"ngrok access", ngrok, func(e *tunnelPolicyEntry) { e.Access = "account" }},
		{"serve zero port", serve, func(e *tunnelPolicyEntry) { e.Options.ListenerPort = 0 }},
		{"serve high port", serve, func(e *tunnelPolicyEntry) { e.Options.ListenerPort = 65536 }},
		{"serve hostname", serve, func(e *tunnelPolicyEntry) { e.Options.Hostname = "web.example.test" }},
		{"serve zone", serve, func(e *tunnelPolicyEntry) { e.Options.ZoneID = "zone" }},
		{"serve domain", serve, func(e *tunnelPolicyEntry) { e.Options.Domain = "web.example.test" }},
		{"serve registration", serve, func(e *tunnelPolicyEntry) { e.Options.Registration = "named" }},
		{"serve access", serve, func(e *tunnelPolicyEntry) { e.Access = "public" }},
		{"quick access", quick, func(e *tunnelPolicyEntry) { e.Access = "account" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := tc.base
			tc.change(&e)
			if err := e.validate(); err == nil {
				t.Fatal("invalid provider option or access accepted")
			}
		})
	}
	for _, path := range []string{"relative", "/../other", "/preview?query", "/preview#fragment", "/preview\\other", "/preview\nother"} {
		e := serve
		e.Options.Path = path
		if err := e.validate(); err == nil {
			t.Fatalf("invalid Serve path accepted: %q", path)
		}
	}
}
