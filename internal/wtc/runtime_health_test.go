package wtc

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRuntimeProbe(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer ok.Close()
	// A login page or a redirect answers too.
	login := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/login", http.StatusFound) }))
	defer login.Close()
	// What a tunnel says while the service behind it is down.
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusBadGateway) }))
	defer bad.Close()
	gone := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	goneURL := gone.URL
	gone.Close()

	for _, tc := range []struct {
		url    string
		want   bool
		detail string
	}{{ok.URL, true, ""}, {login.URL, true, ""}, {bad.URL, false, "HTTP 502"}, {goneURL, false, "refused"}} {
		got, detail := runtimeProbe(tc.url)
		if got != tc.want || !strings.Contains(detail, tc.detail) {
			t.Errorf("%s: %v %q, want %v %q", tc.url, got, detail, tc.want, tc.detail)
		}
	}
}

// Only ready tasks with an address are looked at; what they say is kept.
func TestRuntimeAnswering(t *testing.T) {
	items := []RuntimeItem{
		{Path: "a", State: "ready", URL: "https://a.example"},
		{Path: "b", State: "ready", URL: "https://b.example"},
		{Path: "c", State: "exited", URL: "https://c.example"},
		{Path: "d", State: "ready"},
	}
	asked := map[string]bool{}
	var askedMu sync.Mutex
	runtimeAnswering(items, func(raw string) (bool, string) {
		askedMu.Lock()
		asked[raw] = true
		askedMu.Unlock()
		return raw == "https://a.example", "HTTP 502"
	})
	if items[0].Answering != "yes" || items[1].Answering != "no" || items[1].AnswerDetail != "HTTP 502" || items[2].Answering != "" || items[3].Answering != "" {
		t.Errorf("%+v", items)
	}
	if asked["https://c.example"] {
		t.Error("an exited task was looked at")
	}
}

func TestTunnelLogins(t *testing.T) {
	items := []RuntimeItem{
		{Path: "a", Kind: "endpoint", Provider: "tailscale"},
		{Path: "b", Kind: "endpoint", Provider: "tailscale"},
		{Path: "c", Kind: "endpoint", Provider: "devtunnel"},
		{Path: "d", Kind: "service"},
	}
	asked := 0
	runtimeTunnelLogins(items, func(p string) string {
		asked++
		if p == "devtunnel" {
			return "devtunnel's login expired: devtunnel user login"
		}
		return ""
	})
	if asked != 2 || items[0].Login != "" || items[2].Login == "" || items[3].Login != "" {
		t.Errorf("asked %d: %+v", asked, items)
	}
}

func TestProviderLoginReadings(t *testing.T) {
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct{ json, want string }{
		{`{"BackendState":"Running","Self":{"KeyExpiry":"2027-03-06T07:30:21Z"}}`, ""},
		{`{"BackendState":"NeedsLogin","AuthURL":"https://login.tailscale.com/a/x"}`, "needs a login"},
		{`{"BackendState":"Stopped"}`, "stopped"},
		{`{"BackendState":"NeedsMachineAuth"}`, "approval"},
		{`{"BackendState":"Running","Self":{"KeyExpiry":"2026-10-01T00:00:00Z"}}`, "key expired"},
		{`not json`, ""},
	} {
		if got := tailscaleLogin([]byte(tc.json), now); (tc.want == "") != (got == "") || !strings.Contains(got, tc.want) {
			t.Errorf("tailscale %s: %q, want %q", tc.json, got, tc.want)
		}
	}
	for _, tc := range []struct{ out, want string }{
		{"Logged in as someone@example.com using GitHub.", ""},
		{"Login token expired.", "expired"},
		{"Not logged in.", "not logged in"},
	} {
		if got := devtunnelLogin(tc.out); (tc.want == "") != (got == "") || !strings.Contains(got, tc.want) {
			t.Errorf("devtunnel %q: %q, want %q", tc.out, got, tc.want)
		}
	}
}

// A tunnel whose provider client is not signed in is not opened.
func TestTunnelRefusesImplicitProviderSetupWithoutLoginProbe(t *testing.T) {
	c := runtimeFixture(t)
	fixtureFile(t, filepath.Join(c.Collection, ".env.collection.local"), "ECHO_PORT=43001\n", 0600)
	if _, err := c.AddService(ServiceOptions{Service: "echo", PortEnv: "ECHO_PORT", Cmd: []string{"python3", "-m", "http.server"}, NoStart: true}); err != nil {
		t.Fatal(err)
	}
	was := runtimeLoginCheck
	t.Cleanup(func() { runtimeLoginCheck = was })
	probes := 0
	runtimeLoginCheck = func(string) string { probes++; return "unexpected global login lookup" }
	_, err := c.Tunnel(TunnelOptions{Service: "echo", Provider: "tailscale"})
	if err == nil || !strings.Contains(err.Error(), "configure receiving-local tunnels.toml") || probes != 0 {
		t.Fatalf("implicit provider setup/query: %v (probes=%d)", err, probes)
	}
	if data, _ := os.ReadFile(c.runtimeLocalFragment("widget")); strings.Contains(string(data), "tailscale") {
		t.Fatal("a refused tunnel was written")
	}
}

func TestRuntimeTextSaysWhatIsWrongNow(t *testing.T) {
	out := RuntimeText([]RuntimeItem{
		{Collection: "c", Repo: "app", Group: "app/web", Path: "app/web/main", Kind: "service", State: "ready", URL: "http://127.0.0.1:43001", Answering: "no", AnswerDetail: "HTTP 502"},
		{Collection: "c", Repo: "app", Group: "app/web", Path: "app/web/tailscale", Kind: "endpoint", State: "idle", Provider: "tailscale", Login: "Tailscale needs a login: tailscale up"},
	})
	for _, want := range []string{"ready = startup probe passed, then looked at now", "not answering now: HTTP 502", "Tailscale needs a login: tailscale up"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

// Status as people see it carries the live look; up's wait loop polls
// without it, so its deadline holds.
func TestRuntimeStatusWiring(t *testing.T) {
	for _, tc := range []struct {
		name, login, answer, detail string
	}{
		{name: "healthy", answer: "yes"},
		{name: "unreachable", answer: "no", detail: "HTTP 502"},
		{name: "provider login", answer: "yes", login: "provider needs a login"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := runtimeFixture(t)
			m, err := c.renderRuntime()
			if err != nil || m == nil || len(m.Tasks) != 2 {
				t.Fatalf("prepare runtime: %v", err)
			}
			m.Tasks[0].URL = "https://service.example.invalid"
			m.Tasks[1].URL = "https://tunnel.example.invalid"
			m.Tasks[1].Provider, m.Tasks[1].Exposure = "devtunnel", ExposureAccount
			if err := runtimeSaveManifest(m); err != nil {
				t.Fatal(err)
			}
			fixtureFile(t, filepath.Join(m.Root, "running"), "", 0600)
			fixtureFile(t, filepath.Join(m.Root, "fake-tasks.json"), `{"tasks":[{"path":"widget/web/server","state":"ready"},{"path":"widget/web/tunnel","state":"ready"}]}`, 0600)
			logins := 0
			runtimeLoginCheck = func(provider string) string {
				if provider != "devtunnel" {
					t.Errorf("unexpected provider %q", provider)
				}
				logins++
				return tc.login
			}
			var mu sync.Mutex
			probes := map[string]int{}
			runtimeAnswerCheck = func(raw string) (bool, string) {
				mu.Lock()
				probes[raw]++
				mu.Unlock()
				return tc.answer == "yes", tc.detail
			}
			items := c.RuntimeStatus()
			if len(items) != 2 || logins != 0 || probes[m.Tasks[0].URL] != 1 || probes[m.Tasks[1].URL] != 0 {
				t.Fatalf("public status did not enrich the runner snapshot: %+v, probes %+v, logins %d", items, probes, logins)
			}
			for _, item := range items {
				if item.Kind == "endpoint" {
					if item.State != "ready" || item.Login != "" || item.Answering != "" || item.AnswerDetail != "" {
						t.Fatalf("status probed provider/account: %+v", item)
					}
					continue
				}
				if item.State != "ready" || item.Answering != tc.answer || item.AnswerDetail != tc.detail {
					t.Fatalf("missing reachability observation: %+v", item)
				}
			}
			logins, probes = 0, map[string]int{}
			quiet := c.runtimeStatusOnly()
			if len(quiet) != 2 || logins != 0 || len(probes) != 0 {
				t.Fatalf("runner-only status performed live checks: %+v, probes %+v, logins %d", quiet, probes, logins)
			}
			for _, item := range quiet {
				if item.State != "ready" || item.Answering != "" || item.AnswerDetail != "" || item.Login != "" {
					t.Fatalf("runner-only view carries live observations: %+v", item)
				}
			}
		})
	}
}

func TestRuntimeTextNamesEndpointExposure(t *testing.T) {
	text := RuntimeText([]RuntimeItem{
		{Collection: "fixture", Group: "widget/web", Path: "widget/web/public", Kind: "endpoint", State: "ready", URL: "https://public.example.invalid", Exposure: ExposurePublic},
		{Collection: "fixture", Group: "widget/web", Path: "widget/web/unknown", Kind: "endpoint", State: "ready", URL: "https://preview.example.invalid"},
		{Collection: "fixture", Group: "widget/web", Path: "widget/web/private", Kind: "endpoint", State: "ready", Exposure: ExposureTailnet},
	})
	for _, want := range []string{"exposure: public", "exposure: unknown", "exposure: tailnet"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q: %s", want, text)
		}
	}
}
