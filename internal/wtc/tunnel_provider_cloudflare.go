package wtc

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
)

// Contracts: Cloudflare's remote-tunnel API and cloudflared's supplied-token
// run branch. No login, tunnel-name lookup, DNS discovery or default transport.
// https://developers.cloudflare.com/tunnel/get-started/
// https://github.com/cloudflare/cloudflared/blob/b0297f0b24c60a4cef743b7b878e2da3798cb839/cmd/cloudflared/tunnel/subcommands.go
var cloudflareID = regexp.MustCompile(`^[a-f0-9]{32}$`)
var cloudflareTunnelID = regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`)

func cloudflareRequest(ctx context.Context, client tunnelHTTP, method, path string, body, result any) (int, error) {
	var envelope struct {
		Success bool              `json:"success"`
		Errors  []json.RawMessage `json:"errors"`
		Result  json.RawMessage   `json:"result"`
	}
	code, err := client.request(ctx, method, "https://api.cloudflare.com/client/v4/"+path, nil, body, &envelope)
	if err != nil {
		return code, err
	}
	if !envelope.Success || len(envelope.Errors) != 0 {
		return code, errors.New("Cloudflare operation unconfirmed; reconcile pending operation")
	}
	if result != nil && (len(envelope.Result) == 0 || string(envelope.Result) == "null" || json.Unmarshal(envelope.Result, result) != nil) {
		return code, errors.New("Cloudflare response incomplete; reconcile pending operation")
	}
	return code, nil
}

type cloudflareTunnelRegistration struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	ConfigSource     string `json:"config_src"`
	AccountTag       string `json:"account_tag,omitempty"`
	IdentityVerified bool   `json:"-"`
}

func createCloudflareTunnel(ctx context.Context, client tunnelHTTP, account, name string) (cloudflareTunnelRegistration, error) {
	if !cloudflareID.MatchString(account) || !tunnelComponent.MatchString(name) {
		return cloudflareTunnelRegistration{}, errors.New("invalid Cloudflare tunnel registration")
	}
	var result cloudflareTunnelRegistration
	_, err := cloudflareRequest(ctx, client, http.MethodPost, "accounts/"+account+"/cfd_tunnel", map[string]string{"name": name, "config_src": "cloudflare"}, &result)
	// IdentityVerified is local evidence, never a provider-supplied assertion.
	result.IdentityVerified = false
	if err != nil {
		return result, err
	}
	if !cloudflareTunnelID.MatchString(result.ID) || result.Name != name || result.ConfigSource != "cloudflare" || result.AccountTag != "" && result.AccountTag != account {
		return result, errors.New("Cloudflare identity mismatch; reconcile registration")
	}
	result.IdentityVerified = true
	return result, nil
}

// The controller must own the dedicated tunnel/config/DNS record and provide a
// current exact observation under its lock. This reuses the common receipt
// check; it never turns a borrowed or changed resource into a mutation target.
func cloudflareOwnedTarget(owner, kind string, receipt tunnelResourceRef, current tunnelResourceObservation) error {
	if !tunnelComponent.MatchString(owner) || receipt.Provider != "cloudflare" || receipt.Kind != kind || receipt.Ownership != "owned" || !cloudflareID.MatchString(receipt.Scope) {
		return errors.New("exact owned Cloudflare target required")
	}
	validID := cloudflareID.MatchString(receipt.ID)
	if kind == "tunnel" || kind == "configuration" {
		validID = cloudflareTunnelID.MatchString(receipt.ID)
	}
	if !validID {
		return errors.New("invalid Cloudflare resource ID")
	}
	targets, err := tunnelCleanupTargets(owner, []tunnelResourceRef{receipt}, []tunnelResourceObservation{current})
	if err != nil || len(targets) != 1 {
		return errors.New("Cloudflare ownership/current content unconfirmed; reconcile")
	}
	return nil
}

func putCloudflareConfiguration(ctx context.Context, client tunnelHTTP, owner string, receipt tunnelResourceRef, current tunnelResourceObservation, hostname, target string) error {
	if err := cloudflareOwnedTarget(owner, "configuration", receipt, current); err != nil {
		return err
	}
	u, err := parseTunnelAddress(target)
	if err != nil || !tunnelHostname(hostname) || u.Hostname != "localhost" && u.Hostname != "127.0.0.1" && u.Hostname != "::1" {
		return errors.New("Cloudflare ingress requires a concrete hostname and loopback HTTP(S) target")
	}
	body := map[string]any{"config": map[string]any{"ingress": []any{map[string]string{"hostname": hostname, "service": u.URL}, map[string]string{"service": "http_status:404"}}}}
	_, err = cloudflareRequest(ctx, client, http.MethodPut, "accounts/"+receipt.Scope+"/cfd_tunnel/"+receipt.ID+"/configurations", body, nil)
	return err
}

type cloudflareDNSRegistration struct {
	ID      string             `json:"id"`
	Receipt *tunnelResourceRef `json:"-"`
}

func createCloudflareDNS(ctx context.Context, client tunnelHTTP, zone, hostname, tunnelID, owner string) (cloudflareDNSRegistration, error) {
	if !cloudflareID.MatchString(zone) || !cloudflareTunnelID.MatchString(tunnelID) || !tunnelHostname(hostname) || !tunnelComponent.MatchString(owner) {
		return cloudflareDNSRegistration{}, errors.New("invalid Cloudflare DNS request")
	}
	body := map[string]any{"type": "CNAME", "name": hostname, "content": tunnelID + ".cfargotunnel.com", "proxied": true, "comment": "taba-owner:" + owner}
	var result struct {
		ID, Type, Name, Content, Comment string
		Proxied                          bool
	}
	var wire json.RawMessage
	_, err := cloudflareRequest(ctx, client, http.MethodPost, "zones/"+zone+"/dns_records", body, &wire)
	if err == nil && json.Unmarshal(wire, &result) != nil {
		return cloudflareDNSRegistration{}, errors.New("invalid Cloudflare DNS response; reconcile")
	}
	registration := cloudflareDNSRegistration{ID: result.ID}
	if err != nil {
		return registration, err
	}
	// The caller must retain a mismatched assertion for reconciliation. Do not
	// mint a trusted cleanup receipt or replace an existing record by name.
	if !cloudflareID.MatchString(result.ID) || result.Type != "CNAME" || result.Name != hostname || result.Content != body["content"] || !result.Proxied || result.Comment != body["comment"] {
		return registration, errors.New("Cloudflare DNS identity mismatch; reconcile pending creation")
	}
	// Retain all returned record fields in the fingerprint so a later exact-ID
	// observation detects tags/TTL/settings changes as well as routing changes.
	decoder := json.NewDecoder(bytes.NewReader(wire))
	decoder.UseNumber()
	var fields any
	if decoder.Decode(&fields) != nil {
		return registration, errors.New("invalid Cloudflare DNS record; reconcile")
	}
	data, _ := json.Marshal(fields)
	registration.Receipt = &tunnelResourceRef{Provider: "cloudflare", Scope: zone, Kind: "dns", ID: result.ID, Ownership: "owned", Owner: owner, Fingerprint: tunnelDigest(data)}
	return registration, nil
}

func deleteCloudflareResource(ctx context.Context, client tunnelHTTP, owner string, receipt tunnelResourceRef, current tunnelResourceObservation, connectorStopped bool) error {
	if receipt.Kind != "dns" && receipt.Kind != "tunnel" {
		return errors.New("Cloudflare configuration is retired with its dedicated tunnel")
	}
	validID := cloudflareID.MatchString(receipt.ID)
	if receipt.Kind == "tunnel" {
		validID = cloudflareTunnelID.MatchString(receipt.ID)
	}
	if !connectorStopped || !tunnelComponent.MatchString(owner) || receipt.Provider != "cloudflare" || !cloudflareID.MatchString(receipt.Scope) || !validID || receipt.validate(owner) != nil || receipt.Ownership != "owned" {
		return errors.New("stopped owned connector and exact Cloudflare receipt required")
	}
	if current.Absent && current.Resource == receipt {
		_, err := tunnelCleanupTargets(owner, []tunnelResourceRef{receipt}, []tunnelResourceObservation{current})
		return err
	}
	if err := cloudflareOwnedTarget(owner, receipt.Kind, receipt, current); err != nil {
		return err
	}
	path := "accounts/" + receipt.Scope + "/cfd_tunnel/" + receipt.ID
	if receipt.Kind == "dns" {
		path = "zones/" + receipt.Scope + "/dns_records/" + receipt.ID
	}
	code, err := cloudflareRequest(ctx, client, http.MethodDelete, path, nil, nil)
	if code == http.StatusNotFound {
		return nil
	}
	return err
}

// The supplied token binds this child to one tunnel. Never put it in argv or
// inherit ambient auth/proxy/SDK controls. No command is started here; runtime
// must redact child output and journal uncertain connector shutdown separately.
func cloudflareHostCommand(ctx context.Context, binary, config, account, id, token string, env []string) (*exec.Cmd, error) {
	if !filepath.IsAbs(binary) || !filepath.IsAbs(config) || !cloudflareID.MatchString(account) || !cloudflareTunnelID.MatchString(id) || devTunnelEnvironment(env) != nil || len(token) > 16384 {
		return nil, errors.New("explicit scoped Cloudflare host inputs required")
	}
	if !cloudflareTokenBound(token, account, id) {
		return nil, errors.New("Cloudflare supplied host capability does not bind the exact tunnel")
	}
	if runtimeRegular(config) != nil {
		return nil, errors.New("dedicated Cloudflare configuration required")
	}
	// Remotely managed ingress comes only from the owned configuration API.
	f, err := os.Open(config)
	if err != nil {
		return nil, errors.New("dedicated Cloudflare configuration unavailable")
	}
	data, err := io.ReadAll(io.LimitReader(f, 65))
	f.Close()
	if err != nil || len(data) > 64 || !bytes.Equal(bytes.TrimSpace(data), []byte("{}")) {
		return nil, errors.New("Cloudflare host requires an empty dedicated local configuration")
	}
	cmd := exec.CommandContext(ctx, binary, "tunnel", "--no-autoupdate", "--config", config, "run", id)
	cmd.Env = append(append([]string{}, env...), "TUNNEL_TOKEN="+token)
	return cmd, nil
}

func cloudflareTokenBound(token, account, id string) bool {
	if len(token) > 16384 {
		return false
	}
	var capability struct {
		Account  string `json:"a"`
		Tunnel   string `json:"t"`
		Secret   []byte `json:"s"`
		Endpoint string `json:"e"`
	}
	raw, err := base64.StdEncoding.DecodeString(token)
	return err == nil && json.Unmarshal(raw, &capability) == nil && capability.Account == account && capability.Tunnel == id && len(capability.Secret) >= 32 && capability.Endpoint == ""
}

func getCloudflareHostToken(ctx context.Context, client tunnelHTTP, owner string, receipt tunnelResourceRef, current tunnelResourceObservation) (string, error) {
	if err := cloudflareOwnedTarget(owner, "tunnel", receipt, current); err != nil {
		return "", err
	}
	var token string
	_, err := cloudflareRequest(ctx, client, http.MethodGet, "accounts/"+receipt.Scope+"/cfd_tunnel/"+receipt.ID+"/token", nil, &token)
	if err != nil || !cloudflareTokenBound(token, receipt.Scope, receipt.ID) {
		return "", errors.New("Cloudflare exact host capability unavailable; retain registration")
	}
	return token, nil
}
