package wtc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// runtimeProbe looks at an address now: any HTTP answer below 500 counts,
// so a login page or a redirect is an answer; a 5xx, a refusal or no answer
// within two seconds is not (a tunnel answers 502 while the service behind
// it is down).
func runtimeProbe(raw string) (bool, string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return false, err.Error()
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return false, "no answer within 2s"
		}
		var u *url.Error
		if errors.As(err, &u) {
			return false, u.Err.Error()
		}
		return false, err.Error()
	}
	resp.Body.Close()
	if resp.StatusCode >= 500 {
		return false, fmt.Sprintf("HTTP %d", resp.StatusCode)
	}
	return true, ""
}

// runtimeAnswering looks at every ready task's address at once.
func runtimeAnswering(items []RuntimeItem, probe func(string) (bool, string)) {
	var wg sync.WaitGroup
	for i := range items {
		item := &items[i]
		if item.Kind == "endpoint" || item.State != "ready" || !(strings.HasPrefix(item.URL, "http://") || strings.HasPrefix(item.URL, "https://")) {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			if ok, detail := probe(item.URL); ok {
				item.Answering = "yes"
			} else {
				item.Answering, item.AnswerDetail = "no", detail
			}
		}()
	}
	wg.Wait()
}

// runtimeTunnelLogins marks the endpoints whose provider client cannot serve
// (logged out, stopped, awaiting approval, an expired key), asking each
// provider once. Closed endpoints are marked too: opening them would fail.
func runtimeTunnelLogins(items []RuntimeItem, login func(string) string) {
	asked := map[string]string{}
	for i := range items {
		item := &items[i]
		if item.Kind != "endpoint" || item.Provider == "" {
			continue
		}
		missing, ok := asked[item.Provider]
		if !ok {
			missing = login(item.Provider)
			asked[item.Provider] = missing
		}
		item.Login = missing
	}
}

// runtimeProviderLogin says how a provider's client is not signed in, or ""
// when it is, or when wtc cannot tell.
func runtimeProviderLogin(provider string) string {
	run := func(program string, args ...string) ([]byte, bool) {
		binary, err := exec.LookPath(program)
		if err != nil && program == "tailscale" {
			binary, err = runtimeTailscaleApp, nil
		}
		if err != nil {
			return nil, false
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, binary, args...).CombinedOutput()
		return out, err == nil || len(out) > 0
	}
	switch provider {
	case "tailscale":
		out, ok := run("tailscale", "status", "--json")
		if !ok {
			return ""
		}
		return tailscaleLogin(out, time.Now())
	case "devtunnel":
		out, ok := run("devtunnel", "user", "show")
		if !ok {
			return ""
		}
		return devtunnelLogin(string(out))
	}
	return ""
}

// tailscaleLogin reads `tailscale status --json`.
func tailscaleLogin(data []byte, now time.Time) string {
	var s struct {
		BackendState string `json:"BackendState"`
		AuthURL      string `json:"AuthURL"`
		Self         struct {
			KeyExpiry *time.Time `json:"KeyExpiry"`
		} `json:"Self"`
	}
	if json.Unmarshal(data, &s) != nil {
		return ""
	}
	switch {
	case s.BackendState == "NeedsLogin" || s.AuthURL != "":
		return "Tailscale needs a login: tailscale up"
	case s.BackendState == "NeedsMachineAuth":
		return "Tailscale waits for this machine's approval in the admin console"
	case s.BackendState == "Stopped":
		return "Tailscale is stopped: tailscale up"
	case s.Self.KeyExpiry != nil && s.Self.KeyExpiry.Before(now):
		return "Tailscale's key expired: tailscale up --force-reauth"
	}
	return ""
}

// devtunnelLogin reads `devtunnel user show`.
func devtunnelLogin(out string) string {
	lower := strings.ToLower(out)
	switch {
	case strings.Contains(lower, "expired"):
		return "devtunnel's login expired: devtunnel user login"
	case strings.Contains(lower, "not logged in"), strings.Contains(lower, "no user"), strings.Contains(lower, "login required"):
		return "devtunnel is not logged in: devtunnel user login"
	}
	return ""
}

// runtimeLoginCheck and runtimeAnswerCheck are runtimeProviderLogin and
// runtimeProbe; tests replace them.
var (
	runtimeLoginCheck  = runtimeProviderLogin
	runtimeAnswerCheck = runtimeProbe
)
