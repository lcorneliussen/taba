package wtc

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// acceptanceDaemon is the synthetic Tailscale LocalAPI boundary. Requests reach
// it through the production permission checks, transport and provider adapter.
type acceptanceDaemon struct {
	mu     sync.Mutex
	config string
	calls  int
}

func acceptanceSocket(t *testing.T) (string, *acceptanceDaemon) {
	t.Helper()
	// Keep Unix socket paths short on macOS as well as Linux.
	dir, err := os.MkdirTemp("", "taba-api-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(realPath(t, dir), "api.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	d := &acceptanceDaemon{config: `{}`}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		defer d.mu.Unlock()
		d.calls++
		if r.Host != "local-tailscaled.sock" || r.Header.Get("Authorization") != "" {
			http.Error(w, "scope", 400)
			return
		}
		switch r.Method + " " + r.URL.RequestURI() {
		case "GET /localapi/v0/status?peers=false":
			io.WriteString(w, `{"BackendState":"Running","Self":{"ID":"synthetic-node","DNSName":"node.synthetic.ts.net."}}`)
		case "GET /localapi/v0/serve-config":
			w.Header().Set("ETag", `"synthetic-generation"`)
			io.WriteString(w, d.config)
		case "POST /localapi/v0/serve-config":
			if r.Header.Get("If-Match") != `"synthetic-generation"` {
				http.Error(w, "generation", 412)
				return
			}
			data, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
			if err != nil || !json.Valid(data) || string(data) == "null" {
				http.Error(w, "config", 400)
				return
			}
			d.config = string(data)
		default:
			http.Error(w, "unexpected request", 400)
		}
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	return socket, d
}

func (d *acceptanceDaemon) snapshot() (string, int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.config, d.calls
}

func acceptancePolicy(socket, path string) tunnelPolicyEntry {
	return tunnelPolicyEntry{Provider: "tailscale", Mode: "serve", Access: "tailnet", StartWithService: true,
		Options: tunnelProviderOptions{ListenerPort: 8443, Path: path},
		Inputs:  tunnelInputRefs{Socket: socket, NodeID: "synthetic-node", DNSName: "node.synthetic.ts.net"}}
}

// The synthetic runner models app startup and reports app readiness, including
// what config it sees. It never invents tunnel readiness or writes the journal.
func acceptanceRunner(t *testing.T, c *Context) {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "dekit")
	app := filepath.Join(c.Collection, "widget")
	fixtureFile(t, binary, `#!/bin/sh
if [ "$1" = --version ]; then echo 'dekit 0.10.0'; exit; fi
root=$2
shift 3
printf '%s\n' "$*" >> "$root/calls"
case "$1 $2" in
 'runner status') if [ -f "$root/running" ]; then echo '{"status":"running"}'; else echo '{"status":"absent"}'; fi ;;
 'ls ') if [ -f "$root/app" ]; then echo '{"tasks":[{"path":"widget/web/server","state":"ready"}]}'; else echo '{"tasks":[]}'; fi ;;
 'start widget/web/server'|'restart widget/web/server')
  if [ -f `+shellQuote(filepath.Join(app, ".app-tunnels.json"))+` ]; then
   if [ ! -f "$root/config-at-start.json" ]; then cp `+shellQuote(filepath.Join(app, ".app-tunnels.json"))+` "$root/config-at-start.json"; fi
  fi
  touch "$root/app" "$root/running"; echo '{"matched":1}' ;;
 'veto widget/web/**'|'veto widget/web/server'|'down '|'runner stop') rm -f "$root/app" "$root/running"; echo '{}' ;;
 *) touch "$root/running"; echo '{"matched":1}' ;;
esac
`, 0755)
	c.Config.Runtime.Backend, c.Config.Runtime.Binary = "dekit", binary
}

func acceptanceEntry(t *testing.T, c *Context, name string) tunnelEntryState {
	t.Helper()
	s, err := c.readTunnelJournal()
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range s.Entries {
		if e.Key == (tunnelKey{"widget", "web", name}) {
			return e
		}
	}
	t.Fatalf("missing journal entry %s", name)
	return tunnelEntryState{}
}

func acceptanceAction(t *testing.T, c *Context, action string) {
	t.Helper()
	if _, err := c.RuntimeAction(action, "widget/web", 0); err != nil {
		t.Fatal(action, err)
	}
}

func TestTunnelAcceptanceNewReceiverNamedPolicyServiceLifecycle(t *testing.T) {
	for _, name := range []string{"WTC_CONFIG_ROOT", "TABA_CONFIG_ROOT", "WTC_PROJECT_ROOT", "TABA_PROJECT_ROOT"} {
		t.Setenv(name, "")
	}
	owner := newWorkspaceFixture(t)
	socket, daemon := acceptanceSocket(t)
	policy := acceptancePolicy(socket, "/preview")
	for _, name := range []string{"preview", "selected", "stopped", "disabled"} {
		entry := policy
		entry.Options.Path = "/" + name
		if name == "selected" {
			entry.StartWithService = false
		}
		if err := owner.SetTunnelPolicy("widget", "web", name, entry, true); err != nil {
			t.Fatal(err)
		}
	}
	if err := owner.SetTunnelPolicy("foreign", "web", "foreign", policy, true); err != nil {
		t.Fatal(err)
	}
	publicPolicy := tunnelPolicyEntry{Provider: "ngrok", Mode: "domain", Access: "public", Options: tunnelProviderOptions{Domain: "public.example.test"}}
	if err := owner.SetTunnelPolicy("widget", "web", "public-approval", publicPolicy, true); err != nil {
		t.Fatal(err)
	}
	// Commit the app task declaration before NewCollection checks out widget.
	source := filepath.Join(owner.Workspace, "source-widget")
	fixtureFile(t, filepath.Join(source, ".harness/dekit.tasks.yaml"), "tasks:\n web/server:\n  cmd: [echo, synthetic]\n  x-wtc: {url: 'http://127.0.0.1:3000'}\n", 0644)
	fixtureGit(t, "-C", source, "add", "-A")
	fixtureGit(t, "-C", source, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "synthetic app")
	made, err := owner.NewCollection(NewOptions{Slug: "receiving", Repos: []string{"widget"}})
	if err != nil {
		t.Fatal(err)
	}
	c, err := OpenCollection(made.Collection)
	if err != nil {
		t.Fatal(err)
	}
	acceptanceRunner(t, c)
	project, local, err := c.tunnelPolicyPaths()
	if err != nil || project != filepath.Join(realPath(t, owner.Workspace), ".config/tunnels.toml") {
		t.Fatal("receiver lost project defaults", project, err)
	}
	if _, err = os.Stat(local); !os.IsNotExist(err) {
		t.Fatal("creation copied project defaults into collection policy", err)
	}
	if state, err := c.readTunnelJournal(); err != nil || len(state.Entries) != 0 {
		t.Fatal("creation granted provider authority", err)
	}
	if _, calls := daemon.snapshot(); calls != 0 {
		t.Fatal("creation contacted daemon")
	}
	if _, err := c.Tunnel(TunnelOptions{Service: "web", Name: "public-approval", Action: "register"}); err == nil || !strings.Contains(err.Error(), "explicit approval") {
		t.Fatal("project default granted implicit public/account authority", err)
	}
	if _, calls := daemon.snapshot(); calls != 0 {
		t.Fatal("public approval refusal reached daemon")
	}
	if _, err := c.RenderRuntime(false); err != nil {
		t.Fatal("prepare runtime", err)
	}
	// Borrowed-domain registration performs no remote allocation or hosting.
	if _, err = c.Tunnel(TunnelOptions{Service: "web", Name: "public-approval", Action: "register", Public: true}); err != nil {
		t.Fatal(err)
	}
	approved := acceptanceEntry(t, c, "public-approval")
	if approved.Consent == nil || !approved.Consent.Public || approved.Consent.Definition != approved.Definition {
		t.Fatal("explicit public registration approval missing")
	}
	publicPolicy.Options.Domain = "changed.example.test"
	if err = c.SetTunnelPolicy("widget", "web", "public-approval", publicPolicy, true); err != nil {
		t.Fatal(err)
	}
	if _, err = c.Tunnel(TunnelOptions{Service: "web", Name: "public-approval", Action: "register"}); err == nil || !strings.Contains(err.Error(), "explicit approval") {
		t.Fatal("changed definition reused old public approval", err)
	}
	if _, err = c.Tunnel(TunnelOptions{Service: "web", Name: "public-approval", Action: "register", Public: true, ExpectedDefinition: approved.Definition}); err == nil || !strings.Contains(err.Error(), "definition changed") {
		t.Fatal("stale observed public definition accepted", err)
	}
	if _, err = c.Tunnel(TunnelOptions{Service: "web", Name: "disabled", Action: "disable"}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"preview", "selected", "stopped"} {
		if _, err = c.Tunnel(TunnelOptions{Service: "web", Name: name, Action: "register", Public: true}); err != nil {
			t.Fatal(name, err)
		}
		e := acceptanceEntry(t, c, name)
		if e.Consent == nil || !e.Consent.Public || e.Consent.Definition != e.Definition {
			t.Fatal("explicit registration approval missing", name)
		}
	}
	// Explicit selection exercises a non-default tunnel; explicit stop overrides autostart.
	if _, err = c.Tunnel(TunnelOptions{Service: "web", Name: "selected", Public: true}); err != nil {
		t.Fatal(err)
	}
	if _, err = c.Tunnel(TunnelOptions{Service: "web", Name: "stopped", Stop: true}); err != nil {
		t.Fatal(err)
	}
	acceptanceAction(t, c, "up")
	// Service actions refresh verified private evidence for the current definition.
	addresses := map[string]string{}
	for _, name := range []string{"preview", "selected"} {
		e := acceptanceEntry(t, c, name)
		if e.Active == nil || e.Consent == nil || e.Consent.Definition != e.Definition || e.Operation.Lifecycle.Definition.Entry.Scope != "project" {
			t.Fatal("missing current approval/inherited active definition", name)
		}
		addresses[name] = e.Active.URL
	}
	acceptanceAction(t, c, "down")
	for _, name := range []string{"preview", "selected"} {
		e := acceptanceEntry(t, c, name)
		if e.Active != nil || !e.Registered || e.Known == nil || e.Known.URL != addresses[name] {
			t.Fatal("down discarded registration/address", name)
		}
	}
	acceptanceAction(t, c, "up")
	for _, name := range []string{"preview", "selected"} {
		e := acceptanceEntry(t, c, name)
		if e.Active == nil || e.Active.URL != addresses[name] || e.Consent == nil || e.Consent.Definition != e.Definition {
			t.Fatal("up failed stable restoration", name)
		}
	}
	if e := acceptanceEntry(t, c, "stopped"); e.Active != nil {
		t.Fatal("service up reopened explicit stop")
	}
	state, err := c.readTunnelJournal()
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range state.Entries {
		if e.Key.Repo == "foreign" || e.Key.Name == "disabled" {
			t.Fatal("disabled/foreign default acquired authority")
		}
	}
	config, calls := daemon.snapshot()
	if strings.Contains(config, "/stopped") || strings.Contains(config, "/disabled") || strings.Contains(config, "/foreign") {
		t.Fatal("control handler appeared")
	}
	// A stale observed definition cannot grant a new operation, even explicitly.
	if _, err = c.Tunnel(TunnelOptions{Service: "web", Name: "preview", Public: true, ExpectedDefinition: tunnelDigest([]byte("stale"))}); err == nil || !strings.Contains(err.Error(), "definition changed") {
		t.Fatal("stale approval accepted")
	}
	if after, n := daemon.snapshot(); after != config || n != calls {
		t.Fatal("stale approval reached provider")
	}
	acceptanceAction(t, c, "down")
	for _, name := range []string{"preview", "selected", "stopped"} {
		if _, err = c.Tunnel(TunnelOptions{Service: "web", Name: name, Action: "unregister"}); err != nil {
			t.Fatal(err)
		}
	}
}

// This subprocess is the synthetic receiving application callback. The parent
// uses the actual dispatcher; no event effect or callback receipt is injected.
func TestTunnelAcceptanceCallbackProcess(t *testing.T) {
	if os.Getenv("TUNNEL_ACCEPTANCE_CALLBACK") != "1" {
		return
	}
	fail := func(err error) { fmt.Fprintln(os.Stderr, err); os.Exit(2) }
	var event tunnelEvent
	if err := json.NewDecoder(os.Stdin).Decode(&event); err != nil {
		fail(err)
	}
	if err := event.validate(); err != nil {
		fail(err)
	}
	registry := map[string]string{}
	if data, err := os.ReadFile(".app-tunnels.json"); err == nil {
		if err = json.Unmarshal(data, &registry); err != nil {
			fail(err)
		}
	}
	lifetime := "registration"
	kind := "webhook"
	if event.Type == "started" || event.Type == "stopped" {
		lifetime = "session"
		kind = "redirect"
	}
	id := event.Key.Name + "-" + lifetime
	identity := tunnelCallbackIdentity{Kind: kind, Scope: "synthetic-app", ID: id, Fingerprint: tunnelDigest([]byte(id + "/https://callback.example.test"))}
	reply := map[string]any{}
	switch event.Type {
	case "registered", "started":
		if event.Current == nil {
			fail(fmt.Errorf("missing callback address"))
		}
		registry[id] = event.Current.URL
		reply["resources"] = []tunnelCallbackIdentity{identity}
	case "stopped", "unregistered":
		found := false
		for _, r := range event.Resources {
			if r.Provider == "callback" && r.ID == id && r.Fingerprint == identity.Fingerprint && r.Lifetime == lifetime {
				found = true
			}
		}
		if !found {
			fail(fmt.Errorf("cleanup lacks original receipt"))
		}
		delete(registry, id)
		reply["released"] = []tunnelCallbackIdentity{identity}
	}
	data, err := json.Marshal(registry)
	if err != nil {
		fail(err)
	}
	if err = os.WriteFile(".app-tunnels.json", data, 0600); err != nil {
		fail(err)
	}
	file, err := os.OpenFile(".callback-events.jsonl", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		fail(err)
	}
	if err = json.NewEncoder(file).Encode(event); err != nil {
		fail(err)
	}
	if err = file.Close(); err != nil {
		fail(err)
	}
	if err = json.NewEncoder(os.Stdout).Encode(reply); err != nil {
		fail(err)
	}
	os.Exit(0)
}

func TestTunnelAcceptanceDispatcherReceiptsConfigAndIsolatedCleanup(t *testing.T) {
	for _, name := range []string{"WTC_CONFIG_ROOT", "TABA_CONFIG_ROOT", "WTC_PROJECT_ROOT", "TABA_PROJECT_ROOT"} {
		t.Setenv(name, "")
	}
	c := runtimeFixture(t)
	acceptanceRunner(t, c)
	socket, daemon := acceptanceSocket(t)
	app := filepath.Join(c.Collection, "widget")
	fixtureFile(t, filepath.Join(app, ".harness/dekit.tasks.yaml"), "tasks:\n web/server:\n  cmd: [echo, synthetic]\n  x-wtc: {url: 'http://127.0.0.1:3000'}\n", 0644)
	fixtureFile(t, filepath.Join(app, ".gitignore"), ".app-tunnels.json\n.callback-events.jsonl\n", 0644)
	fixtureFile(t, filepath.Join(app, "callback"), "#!/bin/sh\nexport TUNNEL_ACCEPTANCE_CALLBACK=1\nexec "+shellQuote(os.Args[0])+" -test.run '^TestTunnelAcceptanceCallbackProcess$'\n", 0755)
	fixtureGit(t, "-C", app, "add", "-A")
	fixtureGit(t, "-C", app, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "synthetic callback app")
	for _, name := range []string{"preview", "other"} {
		entry := acceptancePolicy(socket, "/"+name)
		entry.Events = map[string]tunnelEventCommand{}
		for _, event := range []string{"registered", "started", "stopped", "unregistered"} {
			entry.Events[event] = tunnelEventCommand{Command: []string{"callback"}}
		}
		if err := c.SetTunnelPolicy("widget", "web", name, entry, false); err != nil {
			t.Fatal(err)
		}
		if _, err := c.Tunnel(TunnelOptions{Service: "web", Name: name, Action: "register", Public: true}); err != nil {
			t.Fatal(name, err)
		}
		e := acceptanceEntry(t, c, name)
		found := false
		for _, r := range e.Resources {
			if r.Provider == "callback" && r.Kind == "callback-webhook" && r.ID == name+"-registration" && r.Lifetime == "registration" && r.Ownership == "owned" && r.Owner == c.tunnelOwner() {
				found = true
			}
		}
		if !found {
			t.Fatal("dispatcher output was not durably recorded", name)
		}
	}
	acceptanceAction(t, c, "up")
	data, err := os.ReadFile(filepath.Join(c.runtimeRoot(), "config-at-start.json"))
	if err != nil {
		t.Fatal(err)
	}
	var atStart map[string]string
	if err = json.Unmarshal(data, &atStart); err != nil || len(atStart) != 2 || atStart["preview-registration"] != "https://node.synthetic.ts.net:8443/preview" || atStart["other-registration"] != "https://node.synthetic.ts.net:8443/other" {
		t.Fatal("registered config was unavailable before app startup", string(data), err)
	}
	for _, name := range []string{"preview", "other"} {
		e := acceptanceEntry(t, c, name)
		found := false
		for _, r := range e.Resources {
			if r.Provider == "callback" && r.Kind == "callback-redirect" && r.ID == name+"-session" && r.Lifetime == "session" {
				found = true
			}
		}
		if !found {
			t.Fatal("started receipt was not persisted", name)
		}
	}
	other := acceptanceEntry(t, c, "other")
	for _, action := range []string{"stop", "unregister"} {
		if _, err = c.Tunnel(TunnelOptions{Service: "web", Name: "preview", Action: action}); err != nil {
			t.Fatal(action, err)
		}
		e := acceptanceEntry(t, c, "preview")
		for _, r := range e.Resources {
			if action == "unregister" || r.Lifetime == "session" {
				t.Fatal("released callback receipt retained", action, r.Kind)
			}
		}
		if !reflect.DeepEqual(other, acceptanceEntry(t, c, "other")) {
			t.Fatal("cleanup changed another active entry")
		}
	}
	config, _ := daemon.snapshot()
	if !strings.Contains(config, "/other") || strings.Contains(config, "/preview") {
		t.Fatal("cleanup changed another active daemon handler", config)
	}
	e := acceptanceEntry(t, c, "preview")
	if e.Active != nil || e.Registered || e.needsCleanup() {
		t.Fatal("unregister left obligations")
	}
	data, err = os.ReadFile(filepath.Join(app, ".app-tunnels.json"))
	if err != nil {
		t.Fatal(err)
	}
	var remaining map[string]string
	if err = json.Unmarshal(data, &remaining); err != nil || len(remaining) != 2 || remaining["other-registration"] == "" || remaining["other-session"] == "" {
		t.Fatal("callback cleanup removed unrelated config", string(data), err)
	}
	if got := fixtureGit(t, "-C", app, "status", "--porcelain"); got != "" {
		t.Fatal("callback changed tracked application files", got)
	}
	fixtureGit(t, "-C", app, "check-ignore", ".app-tunnels.json", ".callback-events.jsonl")
	file, err := os.Open(filepath.Join(app, ".callback-events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	types := []string{}
	ids := map[string]bool{}
	for {
		var event tunnelEvent
		err := decoder.Decode(&event)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if ids[event.ID] {
			t.Fatal("event replayed")
		}
		ids[event.ID] = true
		if event.Key.Name == "preview" {
			types = append(types, event.Type)
		}
	}
	if !reflect.DeepEqual(types, []string{"registered", "started", "stopped", "unregistered"}) {
		t.Fatal("actual dispatcher did not deliver all lifecycle events", types)
	}
	if _, err = c.Tunnel(TunnelOptions{Service: "web", Name: "other", Action: "unregister"}); err != nil {
		t.Fatal(err)
	}
	acceptanceAction(t, c, "down")
}
