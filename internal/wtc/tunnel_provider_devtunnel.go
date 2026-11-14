package wtc

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

//go:embed assets/devtunnel-host.cjs
var devTunnelHelper []byte

//go:embed assets/devtunnel-host.NOTICES.txt
var devTunnelNotices []byte

//go:embed assets/devtunnel-host.sha256
var devTunnelAssetChecksums string

// The caller supplies an already scoped transport and credential. There is no
// default client, environment lookup, login or credential-store selection.
type tunnelHTTP struct {
	CredentialContext string
	RoundTrip         http.RoundTripper
	Auth              string
}

func (c tunnelHTTP) request(ctx context.Context, method, address string, headers map[string]string, body any, result any) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if c.RoundTrip == nil || c.Auth == "" || strings.ContainsAny(c.Auth, "\r\n\x00") {
		return 0, errors.New("explicit scoped provider transport and authorization required")
	}
	var data []byte
	var err error
	if body != nil {
		data, err = json.Marshal(body)
		if err != nil {
			return 0, errors.New("invalid provider request")
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, address, bytes.NewReader(data))
	if err != nil {
		return 0, errors.New("invalid provider request")
	}
	req.Header.Set("Authorization", c.Auth)
	req.Header.Set("Content-Type", "application/json")
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	// RoundTrip does not follow redirects. Never forward credentials to a
	// Location supplied by a provider error/redirect response.
	resp, err := c.RoundTrip.RoundTrip(req)
	if err != nil {
		return 0, errors.New("provider request failed; reconcile pending operation")
	}
	if resp == nil || resp.Body == nil {
		return 0, errors.New("incomplete provider response; reconcile pending operation")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.StatusCode, fmt.Errorf("provider returned HTTP %d", resp.StatusCode)
	}
	if result == nil {
		return resp.StatusCode, nil
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 || json.Unmarshal(raw, result) != nil {
		return resp.StatusCode, errors.New("invalid provider response; reconcile pending operation")
	}
	return resp.StatusCode, nil
}

type devTunnelRegistration struct {
	TunnelID         string `json:"tunnelId"`
	ClusterID        string `json:"clusterId"`
	Port             int    `json:"port"`
	URL              string `json:"url"`
	IdentityVerified bool   `json:"-"`
	hostToken        string
}

func devTunnelID(s string) bool {
	if len(s) < 3 || len(s) > 60 || s[0] == '-' || s[len(s)-1] == '-' {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}

func devTunnelCluster(s string) bool {
	if len(s) < 1 || len(s) > 16 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

// Preserve the reported identity on partial success for reconciliation. An
// unverified provider assertion is not authority to clean up a foreign resource.
func createDevTunnel(ctx context.Context, client tunnelHTTP, id string, port int, public bool) (devTunnelRegistration, error) {
	if !devTunnelID(id) || port < 1 || port > 65535 {
		return devTunnelRegistration{}, errors.New("invalid devtunnel registration input")
	}
	body := map[string]any{"tunnelId": id, "ports": []any{map[string]any{"portNumber": port, "protocol": "http"}}}
	if public {
		body["accessControl"] = map[string]any{"entries": []any{map[string]any{"type": "anonymous", "subjects": []string{}, "scopes": []string{"connect"}}}}
	}
	var response struct {
		TunnelID     string            `json:"tunnelId"`
		ClusterID    string            `json:"clusterId"`
		AccessTokens map[string]string `json:"accessTokens"`
		Ports        []struct {
			Number int      `json:"portNumber"`
			URLs   []string `json:"portForwardingUris"`
		} `json:"ports"`
	}
	address := "https://global.rel.tunnels.api.visualstudio.com/tunnels/" + id + "?api-version=2023-09-27-preview&includePorts=true&tokenScopes=host"
	_, err := client.request(ctx, http.MethodPut, address, map[string]string{"If-None-Match": "*"}, body, &response)
	if err != nil {
		return devTunnelRegistration{}, err
	}
	r := devTunnelRegistration{TunnelID: response.TunnelID, ClusterID: response.ClusterID, Port: port}
	if response.TunnelID != id || !devTunnelCluster(response.ClusterID) {
		return r, errors.New("devtunnel identity mismatch; reconcile pending registration")
	}
	r.IdentityVerified = true
	r.hostToken = response.AccessTokens["host"]
	for _, p := range response.Ports {
		if p.Number != port {
			continue
		}
		for _, raw := range p.URLs {
			u, err := parseTunnelAddress(raw)
			if err == nil && strings.HasPrefix(u.Origin, "https://") && strings.HasSuffix(u.Hostname, ".devtunnels.ms") {
				r.URL = u.URL
				break
			}
		}
	}
	if r.hostToken == "" || r.URL == "" {
		return r, errors.New("devtunnel host capability or port URL missing; retain created registration")
	}
	return r, nil
}

// Deletion is a transport primitive for an exact owned target already checked
// by checkDevOwned in the current captured credential context. Only after that
// check may a DELETE 404 be treated as successful absence. This primitive never
// discovers accounts, selects the last-used tunnel, or deletes by prefix.
func deleteDevTunnel(ctx context.Context, client tunnelHTTP, cluster, id string) error {
	if !devTunnelID(id) || !devTunnelCluster(cluster) {
		return errors.New("invalid exact devtunnel cleanup target")
	}
	code, err := client.request(ctx, http.MethodDelete, "https://"+cluster+".rel.tunnels.api.visualstudio.com/tunnels/"+id+"?api-version=2023-09-27-preview", nil, nil, nil)
	if code == http.StatusNotFound {
		return nil
	}
	return err
}

type devTunnelHostInput struct {
	Schema     int    `json:"schema"`
	TunnelID   string `json:"tunnelId"`
	ClusterID  string `json:"clusterId"`
	PortNumber int    `json:"portNumber"`
	HostID     string `json:"hostId"`
	HostToken  string `json:"hostToken"` // private stdin frame only; never persist
}

func checkDevTunnelAssets() error {
	lines := strings.Split(strings.TrimSpace(devTunnelAssetChecksums), "\n")
	if len(lines) != 2 || lines[0] != tunnelDigest(devTunnelHelper)+"  devtunnel-host.cjs" || lines[1] != tunnelDigest(devTunnelNotices)+"  devtunnel-host.NOTICES.txt" {
		return errors.New("embedded devtunnel asset checksum mismatch")
	}
	return nil
}

func devTunnelEnvironment(env []string) error {
	for _, assignment := range env {
		key, _, ok := strings.Cut(assignment, "=")
		if !ok {
			return errors.New("unsupported devtunnel helper environment override")
		}
		// Do not pass code-loading, TLS/proxy, login or SDK controls through
		// this secret-bearing child. The caller supplies this small subset.
		switch key {
		case "HOME", "PATH", "TMPDIR", "TMP", "TEMP", "LANG", "LC_ALL", "LC_CTYPE", "TZ":
		default:
			return errors.New("unsupported devtunnel helper environment override")
		}
	}
	return nil
}

func checkDevTunnelNode(ctx context.Context, node string, env []string) error {
	if !filepath.IsAbs(node) || devTunnelEnvironment(env) != nil {
		return errors.New("explicit supported Node path and scoped environment required")
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, "--version")
	cmd.Env = append([]string{}, env...)
	out := &statusLimitedBuffer{limit: 128}
	cmd.Stdout, cmd.Stderr = out, io.Discard
	if err := cmd.Run(); err != nil {
		return errors.New("devtunnel hosting requires an installed Node 22 or newer")
	}
	version := strings.TrimSpace(out.String())
	parts := strings.Split(strings.TrimPrefix(version, "v"), ".")
	major, err := strconv.Atoi(parts[0])
	if !strings.HasPrefix(version, "v") || len(parts) != 3 || err != nil || major < 22 {
		return errors.New("devtunnel hosting requires Node 22 or newer")
	}
	return nil
}

// The path and environment are already receiving-scoped inputs. This validates
// the executable asset, probes Node --version with a scoped environment and
// three-second limit, and prepares a command; it does not start a provider.
func devTunnelHostCommand(ctx context.Context, node, helper string, env []string, input devTunnelHostInput) (*exec.Cmd, error) {
	if input.Schema != 1 || !devTunnelID(input.TunnelID) || !devTunnelCluster(input.ClusterID) || !tunnelComponent.MatchString(input.HostID) || input.PortNumber < 1 || input.PortNumber > 65535 || input.HostToken == "" || strings.ContainsAny(input.HostToken, "\r\n\x00") || len(input.HostToken) > 16384 {
		return nil, errors.New("invalid devtunnel host input")
	}
	if err := checkDevTunnelAssets(); err != nil {
		return nil, err
	}
	if !filepath.IsAbs(helper) || runtimeRegular(helper) != nil {
		return nil, errors.New("explicit regular embedded helper path required")
	}
	f, err := os.Open(helper)
	if err != nil {
		return nil, errors.New("embedded devtunnel helper unavailable")
	}
	data, err := io.ReadAll(io.LimitReader(f, int64(len(devTunnelHelper))+1))
	f.Close()
	if err != nil || !bytes.Equal(data, devTunnelHelper) {
		return nil, errors.New("devtunnel helper differs from embedded asset")
	}
	if err := checkDevTunnelNode(ctx, node, env); err != nil {
		return nil, err
	}
	frame, _ := json.Marshal(input)
	cmd := exec.CommandContext(ctx, node, helper)
	cmd.Env = append(append([]string{}, env...), "NODE_NO_WARNINGS=1")
	cmd.Stdin = bytes.NewReader(frame)
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 5 * time.Second
	return cmd, nil
}
