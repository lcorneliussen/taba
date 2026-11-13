package wtc

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type tunnelProviderTransport func(*http.Request) (*http.Response, error)

func (f tunnelProviderTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func tunnelProviderResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}
}

func TestTunnelDevRegistrationAndPartialCapabilities(t *testing.T) {
	for _, missing := range []bool{false, true} {
		calls := 0
		client := tunnelHTTP{Auth: "Bearer synthetic-user-capability", RoundTrip: tunnelProviderTransport(func(r *http.Request) (*http.Response, error) {
			calls++
			if r.Method != "PUT" || r.URL.Host != "global.rel.tunnels.api.visualstudio.com" || r.URL.Path != "/tunnels/synthetic-tunnel" || r.Header.Get("If-None-Match") != "*" || r.Header.Get("Authorization") != "Bearer synthetic-user-capability" || r.URL.Query().Get("tokenScopes") != "host" || r.URL.Query().Get("api-version") != "2023-09-27-preview" {
				t.Fatal("unexpected management request")
			}
			var input map[string]any
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil || input["accessControl"] == nil {
				t.Fatal("public policy missing from registration")
			}
			tokens := `"accessTokens":{"host":"synthetic-host-capability"},`
			if missing {
				tokens = ""
			}
			return tunnelProviderResponse(200, `{"tunnelId":"synthetic-tunnel","clusterId":"use2",`+tokens+`"ports":[{"portNumber":3000,"portForwardingUris":["https://synthetic-3000.use2.devtunnels.ms/path"]}]}`), nil
		})}
		registration, err := createDevTunnel(context.Background(), client, "synthetic-tunnel", 3000, true)
		if calls != 1 || !registration.IdentityVerified || registration.TunnelID != "synthetic-tunnel" || registration.ClusterID != "use2" || (err != nil) != missing {
			t.Fatalf("registration/partial receipt: calls=%d, error=%v", calls, err)
		}
		if !missing && (registration.hostToken == "" || registration.URL != "https://synthetic-3000.use2.devtunnels.ms/path") {
			t.Fatal("usable host capability/address missing")
		}
		persisted, _ := json.Marshal(registration)
		if strings.Contains(string(persisted), "synthetic-host-capability") {
			t.Fatal("credential entered registration JSON")
		}
	}
}

func TestTunnelDevMismatchedIdentityRetainedWithoutCleanupAuthority(t *testing.T) {
	for _, identity := range [][2]string{{"other-tunnel", "use2"}, {"synthetic-tunnel", ""}, {"synthetic-tunnel", "../unexpected"}} {
		calls := 0
		client := tunnelHTTP{Auth: "Bearer synthetic-capability", RoundTrip: tunnelProviderTransport(func(r *http.Request) (*http.Response, error) {
			calls++
			if r.Method != "PUT" {
				t.Fatal("unverified provider assertion triggered cleanup")
			}
			body, _ := json.Marshal(map[string]any{"tunnelId": identity[0], "clusterId": identity[1], "accessTokens": map[string]string{"host": "synthetic-host-capability"}})
			return tunnelProviderResponse(200, string(body)), nil
		})}
		receipt, err := createDevTunnel(context.Background(), client, "synthetic-tunnel", 3000, false)
		if err == nil || receipt.TunnelID != identity[0] || receipt.ClusterID != identity[1] || receipt.IdentityVerified || receipt.hostToken != "" || calls != 1 {
			t.Fatal("mismatched identity was discarded or trusted")
		}
	}
}

func TestTunnelProviderHTTPRefusesFallbackRedirectsAndSecretDiagnostics(t *testing.T) {
	for _, status := range []int{302, 401, 403, 429, 500} {
		calls := 0
		client := tunnelHTTP{Auth: "tunnel synthetic-capability", RoundTrip: tunnelProviderTransport(func(r *http.Request) (*http.Response, error) {
			calls++
			if r.Method != "DELETE" || r.URL.Host != "use2.rel.tunnels.api.visualstudio.com" || r.URL.Path != "/tunnels/synthetic-tunnel" {
				t.Fatal("wrong cleanup target")
			}
			response := tunnelProviderResponse(status, `{"message":"synthetic-sensitive-value"}`)
			response.Header.Set("Location", "https://foreign.example.test")
			return response, nil
		})}
		err := deleteDevTunnel(context.Background(), client, "use2", "synthetic-tunnel")
		if calls != 1 || err == nil || strings.Contains(err.Error(), "synthetic-sensitive-value") {
			t.Fatal("unsafe redirect/error handling")
		}
	}
	client := tunnelHTTP{Auth: "tunnel synthetic-capability", RoundTrip: tunnelProviderTransport(func(*http.Request) (*http.Response, error) { return tunnelProviderResponse(404, ""), nil })}
	if err := deleteDevTunnel(context.Background(), client, "use2", "synthetic-tunnel"); err != nil {
		t.Fatal(err)
	}
	for _, target := range [][2]string{{"../foreign", "synthetic-tunnel"}, {"use2", "../foreign"}} {
		if err := deleteDevTunnel(context.Background(), client, target[0], target[1]); err == nil {
			t.Fatal("unsafe ID accepted")
		}
	}
	for _, client := range []tunnelHTTP{{}, {Auth: "Bearer synthetic"}, {Auth: "Bearer bad\nheader", RoundTrip: tunnelProviderTransport(func(*http.Request) (*http.Response, error) {
		t.Fatal("invalid authorization reached transport")
		return nil, nil
	})}} {
		if err := deleteDevTunnel(context.Background(), client, "use2", "synthetic-tunnel"); err == nil {
			t.Fatal("implicit transport/auth permitted")
		}
	}
	client.RoundTrip = tunnelProviderTransport(func(*http.Request) (*http.Response, error) { return nil, errors.New("synthetic-sensitive-value") })
	if err := deleteDevTunnel(context.Background(), client, "use2", "synthetic-tunnel"); err == nil || strings.Contains(err.Error(), "synthetic-sensitive-value") {
		t.Fatal("transport diagnostic leaked")
	}
}

func TestTunnelDevHelperInputsAndAsset(t *testing.T) {
	if err := checkDevTunnelAssets(); err != nil {
		t.Fatal(err)
	}
	for _, env := range [][]string{{"NODE_OPTIONS=--require=foreign"}, {"NODE_PATH=/foreign"}, {"DEVTUNNELS_PROTOCOL_VERSION=1"}, {"DEBUG=*"}, {"NODE_TLS_REJECT_UNAUTHORIZED=0"}, {"NODE_REDIRECT_WARNINGS=output"}, {"HTTPS_PROXY=http://proxy.example.test"}, {"https_proxy=http://proxy.example.test"}, {"HTTP_PROXY=http://proxy.example.test"}, {"ALL_PROXY=http://proxy.example.test"}, {"NO_PROXY=*"}, {"npm_config_proxy=http://proxy.example.test"}, {"UNRECOGNIZED_SETTING=value"}} {
		if devTunnelEnvironment(env) == nil {
			t.Fatal("unsafe helper override permitted")
		}
	}
	if err := devTunnelEnvironment([]string{"HOME=/synthetic/home", "PATH=/synthetic/bin", "TMPDIR=/synthetic/tmp", "LANG=C", "LC_ALL=C", "TZ=UTC"}); err != nil {
		t.Fatal("basic scoped child environment rejected")
	}
	dir := t.TempDir()
	old := filepath.Join(dir, "old-node")
	fixtureFile(t, old, "#!/bin/sh\nprintf 'v20.19.0\\n'\n", 0700)
	if err := checkDevTunnelNode(context.Background(), old, []string{}); err == nil {
		t.Fatal("old Node accepted")
	}
	if err := checkDevTunnelNode(context.Background(), filepath.Join(dir, "missing-node"), []string{}); err == nil {
		t.Fatal("missing Node accepted")
	}
	if err := checkDevTunnelNode(context.Background(), "node", []string{}); err == nil {
		t.Fatal("ambient Node selected")
	}
}

func TestTunnelDevBundledCommandKeepsCredentialInStdin(t *testing.T) {
	node := os.Getenv("TABA_TEST_NODE")
	if node == "" {
		t.Skip("set TABA_TEST_NODE for the Node22 embedded-helper subprocess check")
	}
	t.Setenv("NODE_OPTIONS", "--require=/synthetic/missing-module")
	t.Setenv("NODE_TLS_REJECT_UNAUTHORIZED", "0")
	t.Setenv("HTTPS_PROXY", "http://proxy.example.test")
	dir := t.TempDir()
	helper := filepath.Join(dir, "host.cjs")
	if err := os.WriteFile(helper, devTunnelHelper, 0600); err != nil {
		t.Fatal(err)
	}
	env := []string{"HOME=" + dir, "PATH=" + filepath.Dir(node)}
	input := devTunnelHostInput{1, "synthetic-tunnel", "use2", 3000, "synthetic-owner", "synthetic-host-capability"}
	cmd, err := devTunnelHostCommand(context.Background(), node, helper, env, input)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(append(cmd.Args, cmd.Env...), " "), input.HostToken) {
		t.Fatal("credential entered argv/environment")
	}
	for _, assignment := range cmd.Env {
		if strings.HasPrefix(assignment, "NODE_OPTIONS=") || strings.HasPrefix(assignment, "NODE_TLS_REJECT_UNAUTHORIZED=") || strings.HasPrefix(assignment, "HTTPS_PROXY=") {
			t.Fatal("ambient child environment was inherited")
		}
	}
	frame, _ := io.ReadAll(cmd.Stdin)
	if !strings.Contains(string(frame), input.HostToken) {
		t.Fatal("missing private capability frame")
	}
	check := exec.Command(node, helper, "--check")
	check.Env = env
	out, err := check.CombinedOutput()
	if err != nil || !strings.Contains(string(out), `"sdk":"1.3.62"`) {
		t.Fatalf("embedded helper check failed: %v", err)
	}
	if err := os.WriteFile(helper, []byte("foreign bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := devTunnelHostCommand(context.Background(), node, helper, env, input); err == nil {
		t.Fatal("modified helper accepted")
	}
}
