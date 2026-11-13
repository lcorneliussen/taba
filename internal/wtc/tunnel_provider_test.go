package wtc

import "testing"

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
		{"devtunnel", "temporary", "none", false, false},
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
}
