package wtc

import (
	"encoding/json"
	"net"
	"net/url"
	"regexp"
	"strings"
)

// How far a tunnel reaches. Anything wtc cannot establish is unknown, and
// status treats unknown like public.
const (
	ExposureTailnet   = "tailnet"   // devices on the owner's tailnet
	ExposureAccount   = "account"   // accounts the tunnel owner allows
	ExposureProtected = "protected" // declared protected by the task
	ExposurePublic    = "public"    // anyone on the internet
	ExposureUnknown   = "unknown"
)

// Where an exposure comes from, strongest first.
const (
	ExposureVerified = "verified" // asked the provider
	ExposureDeclared = "declared" // x-wtc public
	ExposureCommand  = "command"  // the tunnel's command line
	ExposureDefault  = "default"  // how the provider behaves unless told otherwise
)

const runtimeTailscaleApp = "/Applications/Tailscale.app/Contents/MacOS/Tailscale"

type runtimeTunnelProvider struct {
	name     string
	program  string
	suffixes []string
	// urlLog finds the address in the client's output when the task sets none.
	urlLog string
	// command reads the exposure from the command line; "" when it does not say.
	command func(cmd string) string
	// fallback is the exposure of an address when nothing else is known.
	fallback func(host string) string
}

var runtimeTunnelProviders = []runtimeTunnelProvider{
	{name: "tailscale", program: "tailscale", suffixes: []string{".ts.net"},
		urlLog: `(https://[A-Za-z0-9.-]+\.ts\.net(?::\d+)?)`,
		command: func(cmd string) string {
			switch {
			case runtimeWord(cmd, "funnel"):
				return ExposurePublic
			case runtimeWord(cmd, "serve"):
				return ExposureTailnet
			}
			return ""
		},
		// serve and funnel share the address; only the live check tells.
		fallback: func(string) string { return ExposureUnknown }},
	{name: "cloudflare", program: "cloudflared", suffixes: []string{".trycloudflare.com"},
		urlLog: `(https://[a-z0-9-]+\.trycloudflare\.com)`,
		command: func(cmd string) string {
			// A quick tunnel (--url, no named tunnel) has no access control.
			if strings.Contains(cmd, "--url") && !runtimeWord(cmd, "run") {
				return ExposurePublic
			}
			return ""
		},
		// A named tunnel on an own domain may sit behind Cloudflare Access.
		fallback: func(host string) string {
			if strings.HasSuffix(host, ".trycloudflare.com") {
				return ExposurePublic
			}
			return ExposureUnknown
		}},
	{name: "ngrok", program: "ngrok", suffixes: []string{".ngrok-free.app", ".ngrok-free.dev", ".ngrok.app", ".ngrok.dev", ".ngrok.io"},
		urlLog: `(https://[A-Za-z0-9.-]+\.ngrok(?:-free)?\.(?:app|dev|io))`,
		command: func(cmd string) string {
			for _, flag := range []string{"--basic-auth", "--oauth", "--oidc"} {
				if strings.Contains(cmd, flag) {
					return ExposureProtected
				}
			}
			return ExposurePublic
		},
		fallback: func(string) string { return ExposurePublic }},
	{name: "devtunnel", program: "devtunnel", suffixes: []string{".devtunnels.ms"},
		urlLog: `(https://[A-Za-z0-9.-]+\.devtunnels\.ms(?::\d+)?)`,
		command: func(cmd string) string {
			if strings.Contains(cmd, "--allow-anonymous") || runtimeWord(cmd, "-a") {
				return ExposurePublic
			}
			return ExposureAccount
		},
		// Dev tunnels admit only the owner's account unless made anonymous.
		fallback: func(string) string { return ExposureAccount }},
}

func runtimeWord(cmd, word string) bool {
	for _, field := range strings.Fields(strings.NewReplacer("'", " ", `"`, " ", ";", " ").Replace(cmd)) {
		if field == word {
			return true
		}
	}
	return false
}

func runtimeTunnelProviderNamed(name string) (runtimeTunnelProvider, bool) {
	for _, p := range runtimeTunnelProviders {
		if p.name == name {
			return p, true
		}
	}
	return runtimeTunnelProvider{}, false
}

// runtimeTunnelProviderOfCommand recognises the client a task runs.
func runtimeTunnelProviderOfCommand(cmd string) (runtimeTunnelProvider, bool) {
	for _, p := range runtimeTunnelProviders {
		if runtimeWord(cmd, p.program) {
			return p, true
		}
	}
	return runtimeTunnelProvider{}, false
}

// RuntimeTunnelProvider names the provider of a tunnel address, or "".
func RuntimeTunnelProvider(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	for _, p := range runtimeTunnelProviders {
		for _, suffix := range p.suffixes {
			if strings.HasSuffix(u.Hostname(), suffix) {
				return p.name
			}
		}
	}
	return ""
}

// runtimeTunnelExposure settles an endpoint's exposure once its address is
// known: the live provider check wins, then what the task declared or its
// command said, then how the provider behaves by default.
func runtimeTunnelExposure(item *RuntimeItem, tailscale func(string) string) {
	if item.Kind != "endpoint" {
		return
	}
	if item.Provider == "" {
		item.Provider = RuntimeTunnelProvider(item.URL)
	}
	if item.Provider == "tailscale" && item.URL != "" && tailscale != nil {
		if exposure := tailscale(item.URL); exposure != "" {
			item.Exposure, item.ExposureBasis = exposure, ExposureVerified
			return
		}
	}
	if item.Exposure != "" && item.Exposure != ExposureUnknown {
		return
	}
	item.Exposure, item.ExposureBasis = ExposureUnknown, ""
	if p, ok := runtimeTunnelProviderNamed(item.Provider); ok && item.URL != "" {
		if u, err := url.Parse(item.URL); err == nil {
			if exposure := p.fallback(u.Hostname()); exposure != ExposureUnknown {
				item.Exposure, item.ExposureBasis = exposure, ExposureDefault
			}
		}
	}
}

// runtimeTailscaleExposure reads `tailscale serve status --json`: an address
// listed under AllowFunnel is public, one only served is tailnet-only.
func runtimeTailscaleExposure(data []byte, raw string) string {
	type config struct {
		Web         map[string]json.RawMessage `json:"Web"`
		AllowFunnel map[string]bool            `json:"AllowFunnel"`
	}
	var status struct {
		config
		Foreground map[string]config `json:"Foreground"`
	}
	if len(data) == 0 || json.Unmarshal(data, &status) != nil {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return ""
	}
	port := u.Port()
	if port == "" {
		port = "443"
	}
	key := net.JoinHostPort(u.Hostname(), port)
	served := false
	configs := []config{status.config}
	for _, c := range status.Foreground {
		configs = append(configs, c)
	}
	for _, c := range configs {
		if c.AllowFunnel[key] {
			return ExposurePublic
		}
		if _, ok := c.Web[key]; ok {
			served = true
		}
	}
	if served {
		return ExposureTailnet
	}
	return ""
}

var runtimeTunnelNames = func() *regexp.Regexp {
	var names []string
	for _, p := range runtimeTunnelProviders {
		names = append(names, p.name)
	}
	return regexp.MustCompile(`^(` + strings.Join(names, "|") + `)$`)
}()
