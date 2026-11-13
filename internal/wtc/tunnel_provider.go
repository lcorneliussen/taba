package wtc

import (
	"fmt"
	"net"
	"strings"
)

// These are documented mode contracts, not probes of an installed provider,
// account, credentials or enforced access policy. Runtime adapters must receive
// a validated execution scope from the receiving permission boundary.
// Primary contracts:
// https://developers.cloudflare.com/tunnel/features/locally-managed-tunnels/tunnel-permissions/
// https://developers.cloudflare.com/tunnel/concepts/routing/
// https://learn.microsoft.com/azure/developer/dev-tunnels/cli-commands
// https://learn.microsoft.com/azure/developer/dev-tunnels/faq
// https://ngrok.com/docs/gateway/domains
// https://ngrok.com/docs/start
// https://tailscale.com/docs/reference/tailscale-cli/serve
type tunnelProviderContract struct {
	Registration string // provider, local, or none
	Stable       bool   // conditional on provider retention and identity
	Manage       bool   // separate registration/cleanup capability required
}

func tunnelContract(provider, mode string) (tunnelProviderContract, error) {
	switch provider + "/" + mode {
	case "cloudflare/named", "devtunnel/persistent":
		return tunnelProviderContract{"provider", true, true}, nil
	case "ngrok/domain":
		// The domain is borrowed by default. New reservation requires Manage.
		return tunnelProviderContract{"provider", true, false}, nil
	case "tailscale/serve":
		return tunnelProviderContract{"local", true, false}, nil
	case "devtunnel/temporary":
		// Temporary lifetime still allocates a provider tunnel and port. It
		// needs explicit management/deletion authority without registration events.
		return tunnelProviderContract{"none", false, true}, nil
	case "cloudflare/quick":
		return tunnelProviderContract{"none", false, false}, nil
	}
	return tunnelProviderContract{}, fmt.Errorf("unsupported tunnel provider or mode")
}

func tunnelHostname(s string) bool {
	if len(s) == 0 || len(s) > 253 || strings.ToLower(s) != s || net.ParseIP(s) != nil {
		return false
	}
	for _, label := range strings.Split(s, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}

func validateTunnelProviderPolicy(e tunnelPolicyEntry) error {
	if _, err := tunnelContract(e.Provider, e.Mode); err != nil {
		return err
	}
	o := e.Options
	allowedAccess := "public protected unknown"
	switch e.Provider {
	case "cloudflare":
		if e.Mode == "named" {
			if !tunnelHostname(o.Hostname) || !tunnelComponent.MatchString(o.ZoneID) {
				return fmt.Errorf("named Cloudflare tunnel requires hostname and zone_id")
			}
		} else {
			allowedAccess = "public"
		}
		if o.Domain != "" || o.ListenerPort != 0 || o.Path != "" || e.Mode == "quick" && o != (tunnelProviderOptions{}) {
			return fmt.Errorf("unsupported Cloudflare tunnel option")
		}
	case "devtunnel":
		allowedAccess = "account public"
		if o.Hostname != "" || o.ZoneID != "" || o.Domain != "" || o.ListenerPort != 0 || o.Path != "" || e.Mode == "temporary" && o.Registration != "" {
			return fmt.Errorf("unsupported devtunnel option")
		}
	case "ngrok":
		if !tunnelHostname(o.Domain) || o.Hostname != "" || o.ZoneID != "" || o.Registration != "" || o.ListenerPort != 0 || o.Path != "" {
			return fmt.Errorf("ngrok requires a concrete domain and no unrelated options")
		}
	case "tailscale":
		allowedAccess = "tailnet"
		if o.ListenerPort < 1 || o.ListenerPort > 65535 || o.Hostname != "" || o.ZoneID != "" || o.Domain != "" || o.Registration != "" {
			return fmt.Errorf("Tailscale Serve requires an explicit listener port")
		}
		if o.Path != "" && (!strings.HasPrefix(o.Path, "/") || strings.ContainsAny(o.Path, "?#\\\r\n") || strings.Contains(o.Path, "..")) {
			return fmt.Errorf("invalid Tailscale listener path")
		}
	}
	if o.Registration != "" && !tunnelComponent.MatchString(o.Registration) {
		return fmt.Errorf("invalid tunnel registration name")
	}
	for _, access := range strings.Fields(allowedAccess) {
		if e.Access == access {
			return nil
		}
	}
	return fmt.Errorf("unsupported desired tunnel access")
}

// Facts are supplied by the trusted scoped-execution adapter at use time. They
// are not a new permission resolver, a persisted grant, or user configuration.
type tunnelExecutionFacts struct {
	SupportedBuild bool
	Scoped         bool
	CanHost        bool
	CanManage      bool
	CanCleanup     bool
}

func checkTunnelExecution(e tunnelPolicyEntry, facts tunnelExecutionFacts, createReservation bool) error {
	if e.disabled() {
		return fmt.Errorf("tunnel is disabled")
	}
	if err := e.validate(); err != nil {
		return err
	}
	c, _ := tunnelContract(e.Provider, e.Mode)
	if !facts.SupportedBuild || !facts.Scoped || !facts.CanHost || !facts.CanCleanup {
		return fmt.Errorf("supported scoped tunnel hosting capability is required")
	}
	if (c.Manage || createReservation) && !facts.CanManage {
		return fmt.Errorf("registration requires scoped management and cleanup capabilities")
	}
	if createReservation && c.Registration != "provider" {
		return fmt.Errorf("this mode does not create a provider registration")
	}
	return nil
}
