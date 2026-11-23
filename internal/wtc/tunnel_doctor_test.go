package wtc

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// doctorTunnelFixture declares widget/web with the given x-wtc metadata and
// writes the collection tunnel policy verbatim. Everything is synthetic.
func doctorTunnelFixture(t *testing.T, metadata, policy string) *Context {
	t.Helper()
	c := runtimeFixture(t)
	tasks := "tasks:\n web/server:\n  cmd: [echo, synthetic]\n"
	if metadata != "" {
		tasks += "  x-wtc: " + metadata + "\n"
	}
	fixtureFile(t, filepath.Join(c.Collection, "widget/.harness/dekit.tasks.yaml"), tasks, 0644)
	if policy != "" {
		fixtureFile(t, filepath.Join(c.Collection, ".config/tunnels.toml"), "schema = 1\n"+policy, 0600)
	}
	return c
}

// A short path: unix socket names are limited to about 100 bytes.
func doctorTunnelSocket(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "td")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "s.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	return path
}

func tailscaleDoctorPolicy(socket string) string {
	return `[tunnels.widget.web.tailnet]
provider = "tailscale"
mode = "serve"
access = "tailnet"
[tunnels.widget.web.tailnet.inputs]
socket = "` + socket + `"
node_id = "synthetic-node"
dns_name = "node.synthetic.ts.net"
[tunnels.widget.web.tailnet.options]
listener_port = 8443
`
}

func doctorTunnelOne(t *testing.T, c *Context) NamedTunnelDiagnostic {
	t.Helper()
	diagnostics, err := c.NamedTunnelDiagnostics()
	if err != nil || len(diagnostics) != 1 {
		t.Fatalf("diagnostics %+v, %v", diagnostics, err)
	}
	return diagnostics[0]
}

func problemText(d NamedTunnelDiagnostic) string {
	data, _ := json.Marshal(d.Problems)
	return string(data)
}

func TestNamedTunnelDiagnosticsNoneConfigured(t *testing.T) {
	c := doctorTunnelFixture(t, "{port_env: WIDGET_PORT}", "")
	diagnostics, err := c.NamedTunnelDiagnostics()
	if err != nil || len(diagnostics) != 0 {
		t.Fatal(diagnostics, err)
	}
}

// The target doctor shows is the one the tunnel action would use when the
// declared variables hold usable values, and a service without one gets a fix.
func TestNamedTunnelDiagnosticsTarget(t *testing.T) {
	socket := doctorTunnelSocket(t)
	for _, tc := range []struct {
		name, metadata, target, problem, fix string
	}{
		{"no url or port", "", "", "service widget/web declares no local URL or port", "declare x-wtc.port_env or x-wtc.url on widget/web"},
		{"port_env by name", "{port_env: WIDGET_PORT}", "http://127.0.0.1:$WIDGET_PORT", "", ""},
		{"url_env by name", "{url_env: WIDGET_URL}", "$WIDGET_URL", "", ""},
		{"url_env wins over literal", "{url: 'http://127.0.0.1:3000', url_env: WIDGET_URL}", "$WIDGET_URL", "", ""},
		{"literal url", "{url: 'http://127.0.0.1:3000'}", "http://127.0.0.1:3000", "", ""},
		{"literal url wins over port_env", "{url: 'http://127.0.0.1:3000', port_env: WIDGET_PORT}", "http://127.0.0.1:3000", "", ""},
		{"not loopback", "{url: 'http://198.51.100.7:3000'}", "", "is not a loopback URL", "point x-wtc.url on widget/web at localhost"},
		{"no port for tailscale", "{url: 'http://127.0.0.1'}", "http://127.0.0.1", "needs a target with an explicit port", "declare x-wtc.port_env"},
		{"default port for tailscale", "{url: 'http://127.0.0.1:80'}", "http://127.0.0.1:80", "needs a target with an explicit port", "declare x-wtc.port_env"},
		{"default https port for tailscale", "{url: 'https://localhost:443'}", "https://localhost:443", "needs a target with an explicit port", "declare x-wtc.port_env"},
		{"port_env wins over ready probe", "{port_env: WIDGET_PORT}\n  ready: {http: 'http://example.test/health'}", "http://127.0.0.1:$WIDGET_PORT", "", ""},
		{"ready probe when nothing else", "{mark: w}\n  ready: {http: 'http://127.0.0.1:3000/health'}", "http://127.0.0.1:3000/health", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := doctorTunnelFixture(t, tc.metadata, tailscaleDoctorPolicy(socket))
			d := doctorTunnelOne(t, c)
			if d.Tunnel != "widget/web/tailnet" || d.Provider != "tailscale/serve" || d.Scope != "collection" || d.Policy != ".config/tunnels.toml" || d.Target != tc.target {
				t.Fatalf("%+v", d)
			}
			if tc.problem == "" {
				if d.State != NamedTunnelStateOK || len(d.Problems) != 0 {
					t.Fatalf("want ok: %+v", d)
				}
				return
			}
			if d.State != NamedTunnelStateProblem || len(d.Problems) != 1 || !strings.Contains(d.Problems[0].Problem, tc.problem) || !strings.Contains(d.Problems[0].Fix, tc.fix) {
				t.Fatalf("want %q / %q: %+v", tc.problem, tc.fix, d)
			}
		})
	}
}

// Doctor is observational: unlike the explicit tunnel action, it never
// sources the collection env (receiver shell code), and it changes nothing in
// the collection.
func TestNamedTunnelDiagnosticsDoNotSourceEnvOrWrite(t *testing.T) {
	socket := doctorTunnelSocket(t)
	c := doctorTunnelFixture(t, "{port_env: WIDGET_PORT}", tailscaleDoctorPolicy(socket))
	marker := filepath.Join(t.TempDir(), "sourced")
	fixtureFile(t, filepath.Join(c.Collection, ".env.collection"), "touch '"+marker+"'\nWIDGET_PORT=4173\n", 0600)
	before := collectionSnapshot(t, c.Collection)
	d := doctorTunnelOne(t, c)
	if d.State != NamedTunnelStateOK || d.Target != "http://127.0.0.1:$WIDGET_PORT" {
		t.Fatalf("%+v", d)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("doctor sourced the collection env")
	}
	if after := collectionSnapshot(t, c.Collection); after != before {
		t.Fatalf("doctor changed the collection:\n%s\n---\n%s", before, after)
	}
}

func collectionSnapshot(t *testing.T, root string) string {
	t.Helper()
	var b strings.Builder
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		b.WriteString(rel + " " + info.Mode().String() + " " + info.ModTime().String() + "\n")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func TestNamedTunnelDiagnosticsServiceAndRepository(t *testing.T) {
	socket := doctorTunnelSocket(t)
	t.Run("undeclared service", func(t *testing.T) {
		policy := strings.ReplaceAll(tailscaleDoctorPolicy(socket), "widget.web.", "widget.api.")
		d := doctorTunnelOne(t, doctorTunnelFixture(t, "{port_env: WIDGET_PORT}", policy))
		if d.State != NamedTunnelStateProblem || !strings.Contains(problemText(d), "service widget/api is not declared") || !strings.Contains(problemText(d), "declare api in widget/.harness/dekit.tasks.yaml") {
			t.Fatalf("%+v", d)
		}
	})
	t.Run("repository not checked out", func(t *testing.T) {
		policy := strings.ReplaceAll(tailscaleDoctorPolicy(socket), "widget.web.", "gadget.web.")
		d := doctorTunnelOne(t, doctorTunnelFixture(t, "{port_env: WIDGET_PORT}", policy))
		if d.State != NamedTunnelStateProblem || !strings.Contains(problemText(d), "repository gadget is not checked out") || !strings.Contains(problemText(d), "taba add-repo gadget") {
			t.Fatalf("%+v", d)
		}
	})
	t.Run("unreadable declarations", func(t *testing.T) {
		c := doctorTunnelFixture(t, "{port_env: WIDGET_PORT}", tailscaleDoctorPolicy(socket))
		c.Config.Runtime.Backend = ""
		d := doctorTunnelOne(t, c)
		if d.State != NamedTunnelStateProblem || !strings.Contains(problemText(d), "cannot read service declarations") || !strings.Contains(problemText(d), "backend") {
			t.Fatalf("%+v", d)
		}
	})
}

func TestNamedTunnelDiagnosticsDisabledAndSelectionOnly(t *testing.T) {
	c := doctorTunnelFixture(t, "", "[tunnels.widget.web.tailnet]\nenabled = false\n[starts.widget.web]\nother = true\nstopped = false\n")
	diagnostics, err := c.NamedTunnelDiagnostics()
	if err != nil || len(diagnostics) != 2 {
		t.Fatal(diagnostics, err)
	}
	if d := diagnostics[0]; d.Tunnel != "widget/web/other" || d.State != NamedTunnelStateProblem || !strings.Contains(problemText(d), "no definition exists") {
		t.Fatalf("%+v", d)
	}
	if d := diagnostics[1]; d.Tunnel != "widget/web/tailnet" || d.State != NamedTunnelStateDisabled || len(d.Problems) != 0 {
		t.Fatalf("%+v", d)
	}
}

// Provider inputs are checked with stat alone, by field name; their values,
// paths and credential contents never appear in a diagnostic.
func TestNamedTunnelDiagnosticsProviderInputs(t *testing.T) {
	t.Run("tailscale socket missing", func(t *testing.T) {
		d := doctorTunnelOne(t, doctorTunnelFixture(t, "{port_env: WIDGET_PORT}", tailscaleDoctorPolicy("/synthetic/absent/tailscaled.sock")))
		if d.State != NamedTunnelStateProblem || !strings.Contains(problemText(d), "inputs.socket names no socket") || strings.Contains(problemText(d), "/synthetic/absent") {
			t.Fatalf("%+v", d)
		}
	})
	t.Run("tailscale socket is a plain file", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "not-a-socket")
		fixtureFile(t, file, "", 0600)
		d := doctorTunnelOne(t, doctorTunnelFixture(t, "{port_env: WIDGET_PORT}", tailscaleDoctorPolicy(file)))
		if !strings.Contains(problemText(d), "inputs.socket names no socket") {
			t.Fatalf("%+v", d)
		}
	})
	ngrok := func(inputs string) string {
		return "[tunnels.widget.web.share]\nprovider = \"ngrok\"\nmode = \"domain\"\naccess = \"public\"\n[tunnels.widget.web.share.options]\ndomain = \"preview.example.test\"\n[tunnels.widget.web.share.inputs]\n" + inputs
	}
	t.Run("required input absent", func(t *testing.T) {
		d := doctorTunnelOne(t, doctorTunnelFixture(t, "{port_env: WIDGET_PORT}", ngrok("")))
		if d.State != NamedTunnelStateProblem || !strings.Contains(problemText(d), "inputs.hosting_file is required for ngrok/domain") || !strings.Contains(problemText(d), "add inputs.hosting_file to the tunnel in .config/tunnels.toml") {
			t.Fatalf("%+v", d)
		}
	})
	t.Run("credential file absent", func(t *testing.T) {
		d := doctorTunnelOne(t, doctorTunnelFixture(t, "{port_env: WIDGET_PORT}", ngrok("hosting_file = \"tunnels/synthetic-absent.token\"\n")))
		if d.State != NamedTunnelStateProblem || !strings.Contains(problemText(d), "inputs.hosting_file names no usable credential file") || strings.Contains(problemText(d), "synthetic-absent") {
			t.Fatalf("%+v", d)
		}
	})
	t.Run("credential file present is never read", func(t *testing.T) {
		c := doctorTunnelFixture(t, "{port_env: WIDGET_PORT}", ngrok("hosting_file = \"tunnels/synthetic.token\"\n"))
		scope, err := c.ValidateControlRoot()
		if err != nil {
			t.Fatal(err)
		}
		fixtureFile(t, filepath.Join(scope.Root, "tunnels/synthetic.token"), "synthetic-secret-value\n", 0600)
		d := doctorTunnelOne(t, c)
		data, _ := json.Marshal(d)
		if d.State != NamedTunnelStateOK || strings.Contains(string(data), "synthetic-secret-value") {
			t.Fatalf("%s", data)
		}
	})
	t.Run("cloudflare executable", func(t *testing.T) {
		notExecutable := filepath.Join(t.TempDir(), "cloudflared")
		fixtureFile(t, notExecutable, "#!/bin/sh\nexit 1\n", 0600)
		policy := "[tunnels.widget.web.quick]\nprovider = \"cloudflare\"\nmode = \"quick\"\naccess = \"public\"\n[tunnels.widget.web.quick.inputs]\nexecutable = \"" + notExecutable + "\"\n"
		d := doctorTunnelOne(t, doctorTunnelFixture(t, "{port_env: WIDGET_PORT}", policy))
		if d.State != NamedTunnelStateProblem || !strings.Contains(problemText(d), "inputs.executable is not an executable file") {
			t.Fatalf("%+v", d)
		}
	})
}

func TestNamedTunnelRequiredInputsMatchDocumentedModes(t *testing.T) {
	for _, tc := range []struct {
		entry tunnelPolicyEntry
		want  string
	}{
		{tunnelPolicyEntry{Provider: "cloudflare", Mode: "named"}, "executable hosting_file management_file cleanup_file"},
		{tunnelPolicyEntry{Provider: "cloudflare", Mode: "quick"}, "executable"},
		{tunnelPolicyEntry{Provider: "devtunnel", Mode: "persistent"}, "node hosting_file management_file cleanup_file"},
		{tunnelPolicyEntry{Provider: "devtunnel", Mode: "temporary"}, "node management_file cleanup_file"},
		{tunnelPolicyEntry{Provider: "ngrok", Mode: "domain"}, "hosting_file"},
		{tunnelPolicyEntry{Provider: "ngrok", Mode: "domain", Options: tunnelProviderOptions{Reserve: true}}, "hosting_file management_file cleanup_file"},
		{tunnelPolicyEntry{Provider: "tailscale", Mode: "serve"}, "socket node_id dns_name"},
	} {
		if got := strings.Join(namedTunnelRequiredInputs(tc.entry), " "); got != tc.want {
			t.Errorf("%s/%s: %s, want %s", tc.entry.Provider, tc.entry.Mode, got, tc.want)
		}
	}
}

func TestNamedTunnelDiagnosticsUnreadablePolicyIsSanitized(t *testing.T) {
	c := doctorTunnelFixture(t, "", "")
	fixtureFile(t, filepath.Join(c.Collection, ".config/tunnels.toml"), "schema = 1\n[tunnels.widget.web.x]\nprovider = \"synthetic-secret-literal\n", 0600)
	_, err := c.NamedTunnelDiagnostics()
	if err == nil || strings.Contains(err.Error(), "synthetic-secret-literal") {
		t.Fatal(err)
	}
}

func TestNamedTunnelDiagnosticsDevtunnelTarget(t *testing.T) {
	node := filepath.Join(t.TempDir(), "node")
	fixtureFile(t, node, "#!/bin/sh\nexit 0\n", 0755)
	policy := "[tunnels.widget.web.dev]\nprovider = \"devtunnel\"\nmode = \"temporary\"\naccess = \"account\"\n[tunnels.widget.web.dev.inputs]\nnode = \"" + node + "\"\nmanagement_file = \"tunnels/m.token\"\ncleanup_file = \"tunnels/c.token\"\n"
	for _, tc := range []struct {
		name, metadata string
		ok             bool
	}{
		{"http with port", "{url: 'http://127.0.0.1:3000'}", true},
		{"root path", "{url: 'http://localhost:3000/'}", true},
		{"port_env", "{port_env: WIDGET_PORT}", true},
		{"https", "{url: 'https://127.0.0.1:3000'}", false},
		{"path", "{url: 'http://127.0.0.1:3000/app'}", false},
		{"ipv6 loopback", "{url: 'http://[::1]:3000'}", false},
		{"no port", "{url: 'http://127.0.0.1'}", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := doctorTunnelFixture(t, tc.metadata, policy)
			scope, err := c.ValidateControlRoot()
			if err != nil {
				t.Fatal(err)
			}
			fixtureFile(t, filepath.Join(scope.Root, "tunnels/m.token"), "synthetic\n", 0600)
			fixtureFile(t, filepath.Join(scope.Root, "tunnels/c.token"), "synthetic\n", 0600)
			d := doctorTunnelOne(t, c)
			if tc.ok && d.State != NamedTunnelStateOK || !tc.ok && (d.State != NamedTunnelStateProblem || !strings.Contains(problemText(d), "devtunnel needs an IPv4 or localhost HTTP target")) {
				t.Fatalf("%+v", d)
			}
		})
	}
}

func TestNamedTunnelDiagnosticsCredentialFiles(t *testing.T) {
	policy := "[tunnels.widget.web.share]\nprovider = \"ngrok\"\nmode = \"domain\"\naccess = \"public\"\n[tunnels.widget.web.share.options]\ndomain = \"preview.example.test\"\n[tunnels.widget.web.share.inputs]\nhosting_file = \"%s\"\n"
	outside := filepath.Join(t.TempDir(), "outside.token")
	fixtureFile(t, outside, "synthetic\n", 0600)
	for _, tc := range []struct {
		name, ref, body, problem string
	}{
		{"empty", "tunnels/h.token", "", "names no usable credential file"},
		{"oversized", "tunnels/h.token", strings.Repeat("x", 16385), "names no usable credential file"},
		{"outside the control root", outside, "", "is outside the control root"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := doctorTunnelFixture(t, "{port_env: WIDGET_PORT}", fmt.Sprintf(policy, tc.ref))
			scope, err := c.ValidateControlRoot()
			if err != nil {
				t.Fatal(err)
			}
			if !filepath.IsAbs(tc.ref) {
				fixtureFile(t, filepath.Join(scope.Root, tc.ref), tc.body, 0600)
			}
			d := doctorTunnelOne(t, c)
			if d.State != NamedTunnelStateProblem || !strings.Contains(problemText(d), "inputs.hosting_file "+tc.problem) || strings.Contains(problemText(d), tc.ref) {
				t.Fatalf("%+v", d)
			}
		})
	}
}

func TestNamedTunnelDiagnosticsNodeInput(t *testing.T) {
	policy := "[tunnels.widget.web.dev]\nprovider = \"devtunnel\"\nmode = \"temporary\"\naccess = \"account\"\n[tunnels.widget.web.dev.inputs]\nnode = \"/synthetic/absent/node\"\n"
	d := doctorTunnelOne(t, doctorTunnelFixture(t, "{port_env: WIDGET_PORT}", policy))
	text := problemText(d)
	for _, want := range []string{"inputs.node is not an executable file", "inputs.management_file is required for devtunnel/temporary", "inputs.cleanup_file is required for devtunnel/temporary"} {
		if !strings.Contains(text, want) {
			t.Fatalf("lacks %q: %s", want, text)
		}
	}
	if strings.Contains(text, "/synthetic/absent") {
		t.Fatal(text)
	}
}

func TestNamedTunnelDiagnosticsRepositoryAlias(t *testing.T) {
	socket := doctorTunnelSocket(t)
	c := doctorTunnelFixture(t, "{port_env: WIDGET_PORT}", strings.ReplaceAll(tailscaleDoctorPolicy(socket), "widget.web.", "gizmo.web."))
	found := false
	for i := range c.Registry.Repos {
		if c.Registry.Repos[i].Name == "widget" {
			c.Registry.Repos[i].Aliases = append(c.Registry.Repos[i].Aliases, "gizmo")
			found = true
		}
	}
	if !found {
		t.Fatal("fixture registry lacks widget")
	}
	d := doctorTunnelOne(t, c)
	if d.State != NamedTunnelStateProblem || !strings.Contains(problemText(d), "names repository gizmo by an alias") || !strings.Contains(problemText(d), "registered repository name widget") {
		t.Fatalf("%+v", d)
	}
}
