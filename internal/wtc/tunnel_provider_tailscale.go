package wtc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"path/filepath"
	"strconv"
	"strings"
)

// The supplied transport must open only the already-authorized Unix socket.
// No default socket/client, OS account discovery, login or CLI feature flow.
// Official protocol: client/local/serve.go at Tailscale commit
// e0d532cca9beeb8ed235bf30b9198a7c0e0afa02: GET config + ETag, POST + If-Match.
type tailscaleLocalTransport func(context.Context, string, *http.Request) (*http.Response, error)

type tailscaleServeTarget struct {
	Socket, NodeID, DNSName, Path, Target string
	Port                                  int
}

func (t tailscaleServeTarget) resource(owner string) (tunnelResourceRef, error) {
	if !filepath.IsAbs(t.Socket) || filepath.Clean(t.Socket) != t.Socket || strings.ContainsAny(t.Socket, "\r\n\x00") || !tunnelComponent.MatchString(owner) || !tunnelComponent.MatchString(t.NodeID) || !tunnelHostname(t.DNSName) {
		return tunnelResourceRef{}, errors.New("explicit scoped Tailscale socket and node identity required")
	}
	if t.Port < 1 || t.Port > 65535 || !strings.HasPrefix(t.Path, "/") || strings.ContainsAny(t.Path, "%?#\\\r\n\x00") || strings.Contains(t.Path, "..") || path.Clean(t.Path) != strings.TrimSuffix(t.Path, "/") && t.Path != "/" {
		return tunnelResourceRef{}, errors.New("explicit Tailscale listener and canonical path required")
	}
	a, err := parseTunnelAddress(t.Target)
	if err != nil || a.Hostname != "localhost" && a.Hostname != "127.0.0.1" && a.Hostname != "::1" {
		return tunnelResourceRef{}, errors.New("Tailscale target must be a loopback HTTP(S) URL")
	}
	u, _ := url.Parse(a.URL)
	if u.Port() == "" {
		return tunnelResourceRef{}, errors.New("Tailscale loopback target requires an explicit port")
	}
	data, _ := json.Marshal(t)
	r := tunnelResourceRef{Provider: "tailscale", Scope: t.Socket + "|" + t.NodeID, Kind: "serve", ID: net.JoinHostPort(t.DNSName, strconv.Itoa(t.Port)) + t.Path, Ownership: "owned", Owner: owner, Fingerprint: tunnelDigest(data)}
	return r, r.validate(owner)
}

func tailscaleLocalRequest(ctx context.Context, transport tailscaleLocalTransport, socket, method, suffix, etag string, body []byte) ([]byte, http.Header, error) {
	if transport == nil || !filepath.IsAbs(socket) || !(method == http.MethodGet && (suffix == "status?peers=false" || suffix == "serve-config") || method == http.MethodPost && suffix == "serve-config") {
		return nil, nil, errors.New("explicit authorized Tailscale socket transport required")
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://local-tailscaled.sock/localapi/v0/"+suffix, bytes.NewReader(body))
	if err != nil {
		return nil, nil, errors.New("invalid Tailscale socket request")
	}
	if method == http.MethodPost {
		if etag == "" || len(etag) > 256 || strings.ContainsAny(etag, "*,\r\n\x00") || strings.HasPrefix(etag, "W/") {
			return nil, nil, errors.New("Tailscale current configuration ETag required")
		}
		req.Header.Set("If-Match", etag)
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := transport(ctx, socket, req)
	if err != nil || resp == nil || resp.Body == nil {
		return nil, nil, errors.New("Tailscale socket request unconfirmed; reconcile")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, nil, errors.New("Tailscale configuration request refused; reconcile without retry")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil || len(data) > 1<<20 {
		return nil, nil, errors.New("Tailscale response invalid; reconcile")
	}
	return data, resp.Header, nil
}

// Reject duplicates before decoding provider JSON, preserving its documented
// uppercase keys. A duplicate AllowFunnel must never hide public exposure.
func tailscaleUniqueJSON(data []byte) bool {
	if !json.Valid(data) {
		return false
	}
	d := json.NewDecoder(bytes.NewReader(data))
	var value func(int) bool
	value = func(depth int) bool {
		if depth > 64 {
			return false
		}
		token, err := d.Token()
		if err != nil {
			return false
		}
		if delimiter, ok := token.(json.Delim); ok {
			seen := map[string]bool{}
			for d.More() {
				if delimiter == '{' {
					key, err := d.Token()
					name, ok := key.(string)
					if err != nil || !ok || seen[name] {
						return false
					}
					seen[name] = true
				}
				if !value(depth + 1) {
					return false
				}
			}
			_, err = d.Token()
			return err == nil
		}
		return true
	}
	return value(0)
}

func tailscaleObject(data json.RawMessage) (map[string]json.RawMessage, error) {
	result := map[string]json.RawMessage{}
	if len(data) == 0 || bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return result, nil
	}
	if !tailscaleUniqueJSON(data) || json.Unmarshal(data, &result) != nil || result == nil {
		return nil, errors.New("invalid Tailscale configuration object")
	}
	return result, nil
}

func tailscaleCanonical(data json.RawMessage) string {
	var value any
	if json.Unmarshal(data, &value) != nil {
		return ""
	}
	raw, _ := json.Marshal(value)
	return string(raw)
}

func tailscaleCanonicalPorts(entries map[string]json.RawMessage, hostport bool) error {
	for key := range entries {
		port := key
		if hostport {
			_, number, err := net.SplitHostPort(key)
			if err != nil {
				return errors.New("invalid Tailscale listener identity")
			}
			port = number
		}
		number, err := strconv.Atoi(port)
		if err != nil || number < 1 || number > 65535 || strconv.Itoa(number) != port {
			return errors.New("noncanonical Tailscale listener identity")
		}
	}
	return nil
}

// The controller must persist the local reservation/pending effect before this
// action. The ETag binds the entire read/modify/write, including exposure checks;
// a conflict is reconciliation, never a blind replay or listener-wide reset.
func changeTailscaleServe(ctx context.Context, transport tailscaleLocalTransport, owner string, receipt tunnelResourceRef, target tailscaleServeTarget, stop bool) error {
	expected, err := target.resource(owner)
	if err != nil || receipt != expected || transport == nil {
		return errors.New("exact owned Tailscale reservation and scoped transport required")
	}
	node, _, err := tailscaleLocalRequest(ctx, transport, target.Socket, http.MethodGet, "status?peers=false", "", nil)
	var status struct {
		BackendState string
		Self         *struct{ ID, DNSName string }
	}
	if err != nil || !tailscaleUniqueJSON(node) || json.Unmarshal(node, &status) != nil || status.BackendState != "Running" || status.Self == nil || status.Self.ID != target.NodeID || strings.TrimSuffix(status.Self.DNSName, ".") != target.DNSName {
		return errors.New("Tailscale authorized node identity unavailable or changed")
	}
	raw, headers, err := tailscaleLocalRequest(ctx, transport, target.Socket, http.MethodGet, "serve-config", "", nil)
	if err != nil {
		return err
	}
	cfg, err := tailscaleObject(raw)
	if err != nil {
		return err
	}
	for key := range cfg {
		switch key {
		case "TCP", "Web", "AllowFunnel", "Services", "Foreground":
		default:
			return errors.New("unsupported Tailscale configuration field")
		}
	}
	port := strconv.Itoa(target.Port)
	hp := net.JoinHostPort(target.DNSName, port)
	tcp, err := tailscaleObject(cfg["TCP"])
	if err != nil {
		return err
	}
	web, err := tailscaleObject(cfg["Web"])
	if err != nil {
		return err
	}
	if tailscaleCanonicalPorts(tcp, false) != nil || tailscaleCanonicalPorts(web, true) != nil {
		return errors.New("noncanonical Tailscale configuration listeners")
	}
	server, err := tailscaleObject(web[hp])
	if err != nil {
		return err
	}
	for key := range server {
		if key != "Handlers" {
			return errors.New("unsupported Tailscale web server")
		}
	}
	handlers, err := tailscaleObject(server["Handlers"])
	if err != nil {
		return err
	}
	current := handlers[target.Path]
	if stop && len(current) == 0 {
		return nil
	}
	// Foreground sessions/services are separate owners. Do not shadow one on
	// this listener even when the node-level handler map looks free.
	for _, group := range []string{"Foreground", "Services"} {
		children, err := tailscaleObject(cfg[group])
		if err != nil {
			return err
		}
		for _, child := range children {
			object, err := tailscaleObject(child)
			if err != nil {
				return err
			}
			public, err := tailscaleObject(object["AllowFunnel"])
			if err != nil || tailscaleCanonicalPorts(public, true) != nil {
				return errors.New("unknown Tailscale session/service exposure")
			}
			for key, enabled := range public {
				if strings.HasSuffix(key, ":"+port) && tailscaleCanonical(enabled) != "false" {
					return errors.New("Tailscale listener has public session/service exposure")
				}
			}
			ports, err := tailscaleObject(object["TCP"])
			if err != nil {
				return err
			}
			if tailscaleCanonicalPorts(ports, false) != nil {
				return errors.New("noncanonical Tailscale session/service listener")
			}
			if _, exists := ports[port]; exists {
				return errors.New("Tailscale listener belongs to another session/service")
			}
			servers, err := tailscaleObject(object["Web"])
			if err != nil {
				return err
			}
			if tailscaleCanonicalPorts(servers, true) != nil {
				return errors.New("noncanonical Tailscale session/service listener")
			}
			for key := range servers {
				if strings.HasSuffix(key, ":"+port) {
					return errors.New("Tailscale listener belongs to another session/service")
				}
			}
		}
	}
	funnel, err := tailscaleObject(cfg["AllowFunnel"])
	if err != nil {
		return err
	}
	if tailscaleCanonicalPorts(funnel, true) != nil {
		return errors.New("noncanonical Tailscale exposure listener")
	}
	for key, enabled := range funnel {
		if strings.HasSuffix(key, ":"+port) && tailscaleCanonical(enabled) != "false" {
			return errors.New("Tailscale listener has public or unknown exposure")
		}
	}
	for key := range web {
		if key != hp && strings.HasSuffix(key, ":"+port) {
			return errors.New("Tailscale listener hostname changed")
		}
	}
	if value, exists := tcp[port]; exists && tailscaleCanonical(value) != `{"HTTPS":true}` {
		return errors.New("Tailscale listener mode differs from owned HTTPS reservation")
	}
	wanted, _ := json.Marshal(map[string]string{"Proxy": target.Target})
	if len(current) != 0 && tailscaleCanonical(current) != string(wanted) {
		return errors.New("Tailscale owned handler changed; cleanup refused")
	}
	if !stop && len(current) != 0 {
		// A matching URL is not ownership proof for a new effect. Confirmed
		// active operations are handled idempotently by the owning controller;
		// an uncertain start must reconcile rather than adopt another handler.
		return errors.New("Tailscale handler already exists; reconcile the owned effect")
	}
	if stop {
		delete(handlers, target.Path)
		if len(handlers) == 0 {
			delete(web, hp)
			delete(tcp, port)
		} else {
			server["Handlers"], _ = json.Marshal(handlers)
			web[hp], _ = json.Marshal(server)
		}
	} else {
		for mount := range handlers {
			if strings.HasPrefix(target.Path, strings.TrimSuffix(mount, "/")+"/") || strings.TrimSuffix(mount, "/") == strings.TrimSuffix(target.Path, "/") {
				return errors.New("Tailscale path overlaps an existing handler")
			}
		}
		handlers[target.Path] = wanted
		server["Handlers"], _ = json.Marshal(handlers)
		web[hp], _ = json.Marshal(server)
		tcp[port] = json.RawMessage(`{"HTTPS":true}`)
	}
	if len(web) == 0 {
		delete(cfg, "Web")
	} else {
		cfg["Web"], _ = json.Marshal(web)
	}
	if len(tcp) == 0 {
		delete(cfg, "TCP")
	} else {
		cfg["TCP"], _ = json.Marshal(tcp)
	}
	body, _ := json.Marshal(cfg)
	_, _, err = tailscaleLocalRequest(ctx, transport, target.Socket, http.MethodPost, "serve-config", headers.Get("ETag"), body)
	return err
}
