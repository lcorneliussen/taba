package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestDoctorReportsNamedTunnelsAsAdvisory runs the real doctor in a synthetic
// collection whose named tunnel cannot resolve a local target: the row says
// so with a fix, names no input path, and does not fail doctor.
func TestDoctorReportsNamedTunnelsAsAdvisory(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "taba")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %s: %v", out, err)
	}
	workspace := filepath.Join(root, "workspace")
	collection := filepath.Join(workspace, "sample")
	write := func(path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(collection, "harness", ".harness-repos.yml"), "repos:\n  - name: harness\n    remote: https://example.invalid/harness.git\n    default_ref: origin/main\n  - name: widget\n    remote: https://example.invalid/widget.git\n    default_ref: origin/main\n")
	write(filepath.Join(collection, "harness", "taba.toml"), "[runtime]\nbackend = \"dekit\"\n")
	write(filepath.Join(collection, "SCOPE.md"), "task\n")
	write(filepath.Join(collection, "widget", ".harness", "dekit.tasks.yaml"), "tasks:\n web/server:\n  cmd: [echo, synthetic]\n")
	for _, dir := range []string{"harness", "widget"} {
		if out, err := exec.Command("git", "-C", filepath.Join(collection, dir), "init", "-q", "-b", "main").CombinedOutput(); err != nil {
			t.Fatalf("git init: %s: %v", out, err)
		}
		if out, err := exec.Command("git", "-C", filepath.Join(collection, dir), "remote", "add", "origin", "https://example.invalid/"+dir+".git").CombinedOutput(); err != nil {
			t.Fatalf("git remote: %s: %v", out, err)
		}
	}
	run := func(args ...string) (string, error) {
		cmd := exec.Command(binary, args...)
		cmd.Dir = collection
		env := []string{}
		for _, kv := range os.Environ() {
			if !strings.HasPrefix(kv, "TABA_") && !strings.HasPrefix(kv, "WTC_") {
				env = append(env, kv)
			}
		}
		cmd.Env = append(env, "TABA_DEV_BUILD=off", "WTC_DEV_BUILD=off", "XDG_CONFIG_HOME="+filepath.Join(root, "xdg-config"), "XDG_DATA_HOME="+filepath.Join(root, "xdg-data"), "WTC_CONFIG_ROOT="+filepath.Join(workspace, ".config"), "TABA_CONFIG_ROOT="+filepath.Join(workspace, ".config"))
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	row := func() map[string]any {
		t.Helper()
		out, err := run("doctor", "--json")
		var result struct {
			OK   bool             `json:"ok"`
			Data []map[string]any `json:"data"`
		}
		if jsonErr := json.Unmarshal([]byte(out), &result); jsonErr != nil {
			t.Fatalf("doctor --json: %v %v\n%s", err, jsonErr, out)
		}
		if err != nil || !result.OK {
			t.Fatalf("named tunnels must not fail doctor: %v\n%s", err, out)
		}
		for _, check := range result.Data {
			if check["name"] == "named-tunnels" {
				return check
			}
		}
		t.Fatalf("no named-tunnels row:\n%s", out)
		return nil
	}

	if r := row(); r["ok"] != true || r["detail"] != "none configured" {
		t.Fatalf("without policy: %v", r)
	}

	socket := "/synthetic/absent/tailscaled.sock"
	write(filepath.Join(collection, ".config", "tunnels.toml"), `schema = 1
[tunnels.widget.web.tailnet]
provider = "tailscale"
mode = "serve"
access = "tailnet"
[tunnels.widget.web.tailnet.inputs]
socket = "`+socket+`"
node_id = "synthetic-node"
dns_name = "node.synthetic.ts.net"
[tunnels.widget.web.tailnet.options]
listener_port = 8443
`)
	r := row()
	data, _ := json.Marshal(r)
	if r["ok"] != false || r["detail"] != "1 configured, 1 with problems" || !strings.Contains(string(data), "declare x-wtc.port_env or x-wtc.url on widget/web") {
		t.Fatalf("broken tunnel: %s", data)
	}
	for _, private := range []string{socket, "synthetic-node", "node.synthetic.ts.net"} {
		if strings.Contains(string(data), private) {
			t.Fatalf("diagnostic echoes input %q: %s", private, data)
		}
	}
	text, err := run("doctor")
	if err != nil {
		t.Fatalf("doctor: %v\n%s", err, text)
	}
	for _, want := range []string{"named-tunnels   problem  1 configured, 1 with problems", "widget/web/tailnet", "service widget/web declares no local URL or port", "fix: declare x-wtc.port_env or x-wtc.url on widget/web"} {
		if !strings.Contains(text, want) {
			t.Fatalf("doctor text lacks %q:\n%s", want, text)
		}
	}
}
