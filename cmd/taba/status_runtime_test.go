package main

import (
	"github.com/charmbracelet/x/ansi"
	"os"
	"path/filepath"
	"strings"
	"taba.sh/taba/internal/wtc"
	"testing"
	"time"
)

func TestStatusRuntimeRowsPreservePRClicksAndWidth(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	url := "https://github.com/example/widget/pull/7"
	snapshot := wtc.StatusSnapshot{Collection: "fixture", Repos: []wtc.StatusRepo{{Collection: "fixture", Repo: "widget", Dir: "widget", Runtime: "2 ready", BranchDisplay: "feature", Tree: "clean"}},
		Runtime: []wtc.RuntimeItem{{Collection: "fixture", Repo: "widget", Dir: "widget", Path: "widget/web/server", Group: "widget/web", Kind: "service", State: "ready"}, {Collection: "fixture", Repo: "widget", Dir: "widget", Path: "widget/web/tunnel", Group: "widget/web", Kind: "endpoint", State: "ready", URL: "https://widget.example.test", Exposure: wtc.ExposureProtected, ExposureBasis: wtc.ExposureDeclared}},
		PRs:     []wtc.StatusPRRow{{Repo: "widget", Number: "7", Title: "Change widget", URL: &url}}}
	for _, width := range []int{40, 80, 120} {
		model := statusTUIModel{width: width, height: 60, snapshot: snapshot}
		lines := model.contentLines()
		found := false
		for y, line := range lines {
			plain := ansi.Strip(line)
			if ansi.StringWidth(line) > width {
				t.Fatalf("%d: line exceeded width: %q", width, plain)
			}
			if strings.HasPrefix(strings.TrimSpace(plain), "#7") {
				found = true
				if got := model.buildClickTarget(1, y); got != url {
					t.Fatalf("runtime rows shifted PR click: %q", got)
				}
			}
			if strings.Contains(plain, "endpoint") && model.buildClickTarget(1, y) != "" {
				t.Fatal("runtime row opened a PR")
			}
		}
		if !found {
			t.Fatal("PR missing")
		}
		if width < 60 {
			continue
		}
		// One letter per task under LOCAL: w for the web service, / then t for its tunnel.
		header, row := ansi.Strip(lines[2]), ansi.Strip(lines[3])
		start := len([]rune(header[:max(0, strings.Index(header, "LOCAL"))]))
		if !strings.Contains(header, "LOCAL") || !strings.HasPrefix(string([]rune(row)[start:]), "w/t") {
			t.Fatalf("%d: marks missing under LOCAL: %q / %q", width, header, row)
		}
		if !strings.Contains(lines[3], "\x1b[32mw") {
			t.Fatalf("ready mark is not toned: %q", lines[3])
		}
		if !strings.Contains(lines[3], "\x1b]8;;https://widget.example.test\a\x1b[32mt") {
			t.Fatalf("tunnel mark is not a link to its URL: %q", lines[3])
		}
		if item, ok := model.runtimeItemAt(start+2, 3); !ok || item.Path != "widget/web/tunnel" {
			t.Fatalf("wrong mark under pointer: %+v", item)
		}
		for _, x := range []int{start + 1, start + 3} {
			if _, ok := model.runtimeItemAt(x, 3); ok {
				t.Fatalf("separator or empty cell at %d resolved to a task", x)
			}
		}
		if got := model.buildClickTarget(start+2, 3); got != "https://widget.example.test" {
			t.Fatalf("tunnel mark did not open its URL: %q", got)
		}
		if got := model.buildClickTarget(start, 3); got != "" {
			t.Fatalf("service mark without a URL opened %q", got)
		}
		// The detail table has one line for the service and its tunnel.
		detail := -1
		for y, line := range lines {
			if plain := ansi.Strip(line); strings.HasPrefix(plain, "w/t widget") && strings.Contains(plain, " web ") && strings.Contains(plain, "ready") {
				detail = y
			}
			if plain := ansi.Strip(line); strings.Contains(plain, "web/tunnel") || strings.Contains(plain, "web/server") {
				t.Fatalf("service and tunnel are on separate lines: %q", plain)
			}
		}
		if detail < 0 || !strings.Contains(lines[detail], "\x1b]8;;https://widget.example.test") {
			t.Fatalf("%d: service line or its tunnel link missing: %q", width, lines)
		}
		if plain := ansi.Strip(lines[detail]); strings.Contains(plain, "https://") || !strings.Contains(plain, "widget.example.test") {
			t.Fatalf("URL not shortened to its host: %q", plain)
		}
		layout := statusTUIRuntimeLayout(snapshot, width)
		if got := model.buildClickTarget(layout.tunnelStart("")+2, detail); got != "https://widget.example.test" {
			t.Fatalf("tunnel URL opened %q", got)
		}
		if got := model.buildClickTarget(layout.localStart(), detail); got != "" {
			t.Fatalf("empty local URL cell opened %q", got)
		}
		hover := statusRuntimeHover(snapshot.Runtime[1])
		model.hover = hover
		if view := ansi.Strip(model.View().Content); !strings.Contains(view, "widget/web/tunnel · tunnel, protected (declared)") {
			t.Fatalf("hover text missing from footer: %q", hover)
		}
	}
}

func TestStatusRuntimeURLLabels(t *testing.T) {
	for raw, want := range map[string]string{
		"http://127.0.0.1:42431":                   ":42431",
		"http://localhost:3000/":                   ":3000",
		"https://workstation.example.ts.net:42433": "tailscale",
		"https://calm-river.trycloudflare.com":     "cloudflare",
		"https://1a2b.ngrok-free.app":              "ngrok",
		"https://abc-3000.euw.devtunnels.ms":       "devtunnel",
		"https://preview.example.test/path":        "preview.example.test",
	} {
		if got := statusRuntimeURLLabel(raw); got != want {
			t.Errorf("%s: got %q, want %q", raw, got, want)
		}
	}
}

func TestStatusRuntimeHasAColumnPerTunnelProvider(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "")
	item := func(name, kind, provider, url string, public bool) wtc.RuntimeItem {
		return wtc.RuntimeItem{Collection: "fixture", Repo: "widget", Dir: "widget", Path: "widget/web/" + name, Group: "widget/web", Kind: kind, State: "ready", URL: url, Provider: provider, Exposure: map[bool]string{true: wtc.ExposurePublic, false: wtc.ExposureTailnet}[public]}
	}
	snapshot := wtc.StatusSnapshot{Collection: "fixture", Repos: []wtc.StatusRepo{{Collection: "fixture", Repo: "widget", Dir: "widget", BranchDisplay: "feature", Tree: "clean"}},
		Runtime: []wtc.RuntimeItem{item("server", "service", "", "http://127.0.0.1:3000", false),
			item("tailnet", "endpoint", "tailscale", "https://workstation.example.ts.net:8443", false),
			item("preview", "endpoint", "cloudflare", "https://calm-river.trycloudflare.com", true)}}
	model := statusTUIModel{width: 120, height: 40, snapshot: snapshot}
	lines := model.contentLines()
	header, row, y := "", "", -1
	for i, line := range lines {
		if plain := ansi.Strip(line); strings.HasPrefix(plain, "w/t/P widget") {
			header, row, y = ansi.Strip(lines[i-1]), plain, i
		}
	}
	if y < 0 {
		t.Fatalf("service line missing: %q", lines)
	}
	column := func(name string) int { return len([]rune(header[:strings.Index(header, name)])) }
	cell := func(name string) string { return strings.Fields(string([]rune(row)[column(name):]))[0] }
	if cell("URL") != ":3000" || cell("TAILSCALE") != ":8443" || cell("CLOUDFLARE") != "calm-river" {
		t.Fatalf("columns wrong: %q / %q", header, row)
	}
	for name, want := range map[string]string{"URL": "http://127.0.0.1:3000", "TAILSCALE": "https://workstation.example.ts.net:8443", "CLOUDFLARE": "https://calm-river.trycloudflare.com"} {
		if got := model.buildClickTarget(column(name), y); got != want {
			t.Errorf("%s opened %q", name, got)
		}
	}
	if !strings.Contains(lines[y], "\x1b[32mcalm-river [public]") || !strings.Contains(lines[y], "\x1b[32m:8443") {
		t.Errorf("healthy tunnels are not green or public exposure is unlabeled: %q", lines[y])
	}
	if hover := statusRuntimeHover(snapshot.Runtime[2]); !strings.Contains(hover, "public, reachable from the internet") {
		t.Errorf("hover does not say public: %q", hover)
	}
	if hover := statusRuntimeHover(snapshot.Runtime[1]); !strings.Contains(hover, "tailnet only") {
		t.Errorf("hover does not say protected: %q", hover)
	}
	// A configured tunnel nobody started has no column; one that starts by
	// default shows its state until it has an address.
	snapshot.Runtime[2].State, snapshot.Runtime[2].URL = "idle", ""
	render := func() string {
		return ansi.Strip(strings.Join(statusTUIModel{width: 120, height: 40, snapshot: snapshot}.contentLines(), "\n"))
	}
	if plain := render(); strings.Contains(plain, "CLOUDFLARE") || !strings.Contains(plain, "TAILSCALE") {
		t.Fatalf("idle tunnel shown or running one missing: %s", plain)
	}
	snapshot.Runtime[2].Autostart = true
	if plain := render(); !strings.Contains(plain, "CLOUDFLARE") || !strings.Contains(plain, ":8443     stopped") {
		t.Fatalf("default tunnel not shown with its state: %s", plain)
	}
}

func TestStatusRuntimeTunnelExposureLabelsAndHealthTone(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "")
	for _, tc := range []struct {
		name, exposure, state, answering, login, label, tone string
		noURL                                                bool
	}{
		{name: "healthy public", exposure: wtc.ExposurePublic, state: "ready", label: "preview [public]", tone: statusToneSuccess},
		{name: "public starting", exposure: wtc.ExposurePublic, state: "starting", label: "preview [public]", tone: statusToneWarning},
		{name: "public failed", exposure: wtc.ExposurePublic, state: "failed", label: "preview [public]", tone: statusToneFailure},
		{name: "public unreachable", exposure: wtc.ExposurePublic, state: "ready", answering: "no", label: "preview [public]", tone: statusToneFailure},
		{name: "public needs login", exposure: wtc.ExposurePublic, state: "ready", login: "login expired", label: "preview [public]", tone: statusToneFailure},
		{name: "unknown exposure", state: "ready", label: "preview [exposure unknown]", tone: statusToneWarning},
		{name: "protected", exposure: wtc.ExposureProtected, state: "ready", label: "preview", tone: statusToneSuccess},
		{name: "public stopped", exposure: wtc.ExposurePublic, state: "idle", label: "stopped [public]", tone: statusToneDim, noURL: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			item := wtc.RuntimeItem{Collection: "fixture", Repo: "widget", Dir: "widget", Group: "widget/web", Path: "widget/web/tunnel", Kind: "endpoint", Provider: "cloudflare", URL: "https://preview.trycloudflare.com", State: tc.state, Exposure: tc.exposure, Answering: tc.answering, Login: tc.login}
			if tc.noURL {
				item.URL, item.Autostart = "", true
			}
			lines := statusTUIRuntimeLines(wtc.StatusSnapshot{Runtime: []wtc.RuntimeItem{item}}, 120, true)
			text := strings.Join(lines, "\n")
			if !strings.Contains(ansi.Strip(text), tc.label) || !strings.Contains(text, "\x1b["+tc.tone+"m"+tc.label) {
				t.Fatalf("expected %q with tone %s: %q", tc.label, tc.tone, text)
			}
			if tc.exposure != wtc.ExposurePublic && strings.Contains(ansi.Strip(text), "[public]") {
				t.Fatal("unconfirmed exposure labeled public")
			}
		})
	}
	t.Setenv("NO_COLOR", "1")
	item := wtc.RuntimeItem{Collection: "fixture", Repo: "widget", Dir: "widget", Group: "widget/web", Path: "widget/web/tunnel", Kind: "endpoint", State: "ready", URL: "https://preview.trycloudflare.com", Exposure: wtc.ExposurePublic}
	text := strings.Join(statusTUIRuntimeLines(wtc.StatusSnapshot{Runtime: []wtc.RuntimeItem{item}}, 120, true), "\n")
	if !strings.Contains(ansi.Strip(text), "preview [public]") || strings.Contains(text, "\x1b[32m") {
		t.Fatalf("exposure should remain visible without color: %q", text)
	}
}

func TestStatusRuntimeSlugs(t *testing.T) {
	for raw, want := range map[string]string{
		"https://workstation.example.ts.net:42433": ":42433",
		"https://workstation.example.ts.net":       "workstation",
		"https://calm-river.trycloudflare.com":     "calm-river",
		"https://1a2b.ngrok-free.app":              "1a2b",
		"https://abc-3000.euw.devtunnels.ms":       "abc-3000",
		"https://preview.example.test/path":        "preview.example.test",
	} {
		if got := statusRuntimeSlug(raw); got != want {
			t.Errorf("%s: got %q, want %q", raw, got, want)
		}
	}
}

func TestStatusRuntimeMarkDefaultsAndOverride(t *testing.T) {
	for want, item := range map[string]wtc.RuntimeItem{
		"w": {Path: "app/web/server", Group: "app/web", Kind: "service"},
		"d": {Path: "app/database", Group: "app/database", Kind: "service"},
		"t": {Path: "app/web/tunnel", Group: "app/web", Kind: "endpoint", Exposure: wtc.ExposureTailnet},
		"T": {Path: "app/web/tunnel", Group: "app/web", Kind: "endpoint", Exposure: wtc.ExposurePublic},
		"r": {Path: "app/@resources", Group: "app/resources", Kind: "resources"},
		"x": {Path: "app/web/server", Group: "app/web", Kind: "service", Mark: "x"},
	} {
		if got, _ := statusRuntimeMark(item); got != want {
			t.Errorf("%+v: got %q, want %q", item, got, want)
		}
	}
}

func TestStatusRuntimeCellSeparatesServices(t *testing.T) {
	items := []wtc.RuntimeItem{
		{Collection: "c", Group: "widget/web", Path: "widget/web/server", Kind: "service", State: "ready"},
		{Collection: "c", Group: "widget/resources", Path: "widget/resources", Kind: "resources", State: "provision-hook-completed"},
		{Collection: "c", Group: "widget/web", Path: "widget/web/tunnel", Kind: "endpoint", State: "ready", Exposure: wtc.ExposureTailnet},
	}
	if got := statusRuntimeCell(items, 6, false); got != "w/t r " {
		t.Fatalf("cell = %q, want a service joined to its tunnel and apart from resources", got)
	}
}

func TestStatusRuntimeTreatsUnknownExposureAsPublic(t *testing.T) {
	item := wtc.RuntimeItem{Collection: "fixture", Repo: "widget", Dir: "widget", Path: "widget/web/tunnel", Group: "widget/web", Kind: "endpoint", State: "ready", URL: "https://preview.example.test"}
	if !statusRuntimeExposed(item) {
		t.Fatal("undeclared tunnel not treated as public")
	}
	if letter, _ := statusRuntimeMark(item); letter != "T" {
		t.Fatalf("mark = %q, want upper case for an unknown exposure", letter)
	}
	if hover := statusRuntimeHover(item); !strings.Contains(hover, "exposure unknown") {
		t.Fatalf("hover does not say unknown: %q", hover)
	}
	item.Provider, item.Exposure, item.ExposureBasis = "tailscale", wtc.ExposureTailnet, wtc.ExposureVerified
	if hover := statusRuntimeHover(item); !strings.Contains(hover, "tailscale tunnel, tailnet only (checked with the provider)") {
		t.Fatalf("hover = %q", hover)
	}
}

func TestStatusTUIRuntimeMessageReplacesEnlistedStates(t *testing.T) {
	enlisted := wtc.RuntimeItem{Collection: "fixture", Repo: "widget", Dir: "widget", Path: "widget/web/server", Group: "widget/web", Kind: "service", State: "checking"}
	model := statusTUIModel{width: 100, height: 30, snapshot: wtc.StatusSnapshot{Collection: "fixture", Runtime: []wtc.RuntimeItem{enlisted}}}
	if plain := ansi.Strip(strings.Join(model.contentLines(), "\n")); !strings.Contains(plain, "checking") {
		t.Fatalf("enlisted service not shown before its state: %s", plain)
	}
	ready := enlisted
	ready.State, ready.URL = "ready", "http://127.0.0.1:3000"
	next, _ := model.Update(statusRuntimeMsg{items: []wtc.RuntimeItem{ready}})
	if plain := ansi.Strip(strings.Join(next.(statusTUIModel).contentLines(), "\n")); !strings.Contains(plain, "ready") || !strings.Contains(plain, ":3000") {
		t.Fatalf("runtime state not applied: %s", plain)
	}
}

func TestStatusRuntimePortColourFollowsState(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	for state, want := range map[string]string{"ready": "\x1b[32m", "starting": "\x1b[33m", "absent": "\x1b[2m", "exited": "\x1b[31m"} {
		item := wtc.RuntimeItem{Collection: "fixture", Repo: "widget", Dir: "widget", Path: "widget/web/server", Group: "widget/web", Kind: "service", State: state, URL: "http://127.0.0.1:3000"}
		model := statusTUIModel{width: 100, height: 30, snapshot: wtc.StatusSnapshot{Collection: "fixture", Runtime: []wtc.RuntimeItem{item}}}
		lines := strings.Join(model.contentLines(), "\n")
		if !strings.Contains(lines, want+":3000") {
			t.Errorf("%s: port not toned %q: %q", state, want, lines)
		}
		if linked := strings.Contains(lines, "\x1b]8;;http://127.0.0.1:3000"); linked != (state == "ready" || state == "starting") {
			t.Errorf("%s: port linked = %v", state, linked)
		}
	}
	if got := statusRuntimeState(wtc.RuntimeItem{State: "absent"}); got != "stopped" {
		t.Errorf("absent reads %q", got)
	}
	if _, tone := statusRuntimeMark(wtc.RuntimeItem{Kind: "resources", Group: "w/database", State: "provision-hook-completed"}); tone == statusToneSuccess {
		t.Error("a provisioned resource looks like a running one")
	}
}

func TestStatusTUIServiceStatesRefreshOnTheirOwnCycle(t *testing.T) {
	running := wtc.RuntimeItem{Collection: "fixture", Repo: "widget", Dir: "widget", Path: "widget/web/server", Group: "widget/web", Kind: "service", State: "ready"}
	stopped := running
	stopped.State = "absent"
	model := statusTUIModel{width: 100, height: 30, context: &wtc.Context{}, focused: true, snapshot: wtc.StatusSnapshot{Collection: "fixture"}}
	next, cmd := model.Update(statusRuntimeMsg{items: []wtc.RuntimeItem{running}})
	if cmd == nil {
		t.Fatal("no next service refresh scheduled")
	}
	if _, ok := cmd().(statusRuntimeTickMsg); !ok {
		t.Fatal("the next service refresh is not a tick")
	}
	// A full refresh that read the states earlier does not undo newer ones.
	next, _ = next.(statusTUIModel).Update(statusRuntimeMsg{items: []wtc.RuntimeItem{stopped}})
	next, _ = next.(statusTUIModel).Update(statusLoadedMsg{snapshot: wtc.StatusSnapshot{Collection: "fixture", Runtime: []wtc.RuntimeItem{running}}, at: time.Now()})
	if got := next.(statusTUIModel).snapshot.Runtime[0].State; got != "absent" {
		t.Fatalf("full refresh replaced a newer service state with %q", got)
	}
}

func TestStatusTUIReadsServicesWhenSignalledOrDue(t *testing.T) {
	collection := t.TempDir()
	c := &wtc.Context{Collection: collection}
	if err := os.MkdirAll(filepath.Join(collection, ".wtc", "runtime"), 0755); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	model := statusTUIModel{context: c, focused: true, runtimeReadAt: now}
	if model.runtimeDue(now.Add(time.Second)) {
		t.Fatal("read again before the interval without a signal")
	}
	if !model.runtimeDue(now.Add(3 * time.Second)) {
		t.Fatal("not read after the interval")
	}
	signal := filepath.Join(collection, ".wtc", "runtime", "services-changed")
	if err := os.WriteFile(signal, nil, 0644); err != nil {
		t.Fatal(err)
	}
	later := now.Add(time.Second)
	if err := os.Chtimes(signal, later, later); err != nil {
		t.Fatal(err)
	}
	if !model.runtimeDue(now.Add(time.Second)) {
		t.Fatal("a wtc svc signal did not trigger a read")
	}
	model.runtimeReading = true
	if model.runtimeDue(now.Add(10 * time.Second)) {
		t.Fatal("started a second read while one is in flight")
	}
}

// Ready is only a startup probe: a live task that doesn't answer now, or a
// live tunnel whose provider can't serve, is red, and its hover says why.
func TestStatusRuntimeShowsTroubleNow(t *testing.T) {
	ok := wtc.RuntimeItem{Path: "app/web/server", Group: "app/web", Kind: "service", State: "ready", URL: "http://127.0.0.1:1", Answering: "yes"}
	silent := ok
	silent.Answering, silent.AnswerDetail = "no", "HTTP 502"
	login := wtc.RuntimeItem{Path: "app/web/tailscale", Group: "app/web", Kind: "endpoint", State: "ready", Login: "Tailscale needs a login: tailscale up"}
	closed := login
	closed.State = "idle"
	starting := login
	starting.State = "running"
	for _, tc := range []struct {
		item        wtc.RuntimeItem
		state, tone string
	}{
		{ok, "ready", statusToneSuccess},
		{silent, "no answer", statusToneFailure},
		{login, "login", statusToneFailure},
		{starting, "login", statusToneFailure},
		// A closed tunnel's login waits until it is opened.
		{closed, "stopped", statusToneDim},
	} {
		if _, tone := statusRuntimeMark(tc.item); statusRuntimeState(tc.item) != tc.state || tone != tc.tone {
			t.Errorf("%s: %q %q, want %q %q", tc.item.Path, statusRuntimeState(tc.item), tone, tc.state, tc.tone)
		}
	}
	if h := statusRuntimeHover(silent); !strings.Contains(h, "not answering now: HTTP 502") {
		t.Errorf("hover: %s", h)
	}
	if h := statusRuntimeHover(login); !strings.Contains(h, "tailscale up") {
		t.Errorf("hover: %s", h)
	}
}

func TestRuntimeMissingIntendedTunnelIsVisible(t *testing.T) {
	item := wtc.RuntimeItem{Collection: "sample", Dir: "widget", Repo: "widget", Group: "widget/web", Path: "widget/web/tunnel", Kind: "endpoint", State: "idle", TunnelIntent: "on", Lifecycle: "tunnel intended on but not running", Login: "synthetic login expired", Exposure: wtc.ExposureProtected}
	if !statusRuntimeTunnelShown(item) {
		t.Fatal("missing intended tunnel hidden")
	}
	if state := statusRuntimeState(item); state != "tunnel missing" {
		t.Fatalf("state: %s", state)
	}
	if _, tone := statusRuntimeMark(item); tone != statusToneFailure {
		t.Fatalf("tone: %s", tone)
	}
	hover := statusRuntimeHover(item)
	if !strings.Contains(hover, item.Lifecycle) || !strings.Contains(hover, item.Login) {
		t.Fatalf("diagnostics missing: %s", hover)
	}
}
