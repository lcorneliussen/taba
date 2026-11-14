package wtc

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func tailscaleSyntheticTarget() tailscaleServeTarget {
	return tailscaleServeTarget{Socket: "/synthetic/control/tailscaled.sock", NodeID: "synthetic-node", DNSName: "node.synthetic.ts.net", Port: 8443, Path: "/preview", Target: "http://127.0.0.1:3000"}
}

type tailscaleFakeDaemon struct {
	config     string
	nodeID     string
	etag       string
	postStatus int
	journal    []string
}

func (d *tailscaleFakeDaemon) transport(t *testing.T, target tailscaleServeTarget) tailscaleLocalTransport {
	return func(_ context.Context, socket string, r *http.Request) (*http.Response, error) {
		if socket != target.Socket || r.URL.Host != "local-tailscaled.sock" || r.Header.Get("Authorization") != "" {
			t.Fatal("LocalAPI escaped its explicit socket scope")
		}
		d.journal = append(d.journal, r.Method+" "+r.URL.RequestURI())
		switch {
		case r.Method == "GET" && r.URL.RequestURI() == "/localapi/v0/status?peers=false":
			data, _ := json.Marshal(map[string]any{"BackendState": "Running", "Self": map[string]string{"ID": d.nodeID, "DNSName": target.DNSName + "."}})
			return tunnelProviderResponse(200, string(data)), nil
		case r.Method == "GET" && r.URL.Path == "/localapi/v0/serve-config":
			resp := tunnelProviderResponse(200, d.config)
			resp.Header.Set("ETag", d.etag)
			return resp, nil
		case r.Method == "POST" && r.URL.Path == "/localapi/v0/serve-config":
			if r.Header.Get("If-Match") == "" || r.Header.Get("If-Match") != d.etag {
				t.Fatal("missing exact CAS generation")
			}
			if d.postStatus != 0 {
				return tunnelProviderResponse(d.postStatus, "synthetic-sensitive-value"), nil
			}
			data, _ := io.ReadAll(r.Body)
			if string(data) == "null" {
				t.Fatal("whole configuration reset")
			}
			d.config = string(data)
			return tunnelProviderResponse(200, ""), nil
		default:
			t.Fatal("unexpected LocalAPI mutation/query")
		}
		return nil, nil
	}
}

func tailscaleSyntheticDaemon(config string) *tailscaleFakeDaemon {
	return &tailscaleFakeDaemon{config: config, nodeID: "synthetic-node", etag: `"synthetic-generation"`}
}

func TestTunnelTailscaleCASPreservesOtherHandlersAndListeners(t *testing.T) {
	target := tailscaleSyntheticTarget()
	d := tailscaleSyntheticDaemon(`{"TCP":{"8443":{"HTTPS":true},"9443":{"TCPForward":"127.0.0.1:9000"}},"Web":{"node.synthetic.ts.net:8443":{"Handlers":{"/other":{"Text":"unrelated"}}}},"AllowFunnel":{"node.synthetic.ts.net:9443":true},"Foreground":{"other-session":{"TCP":{"7444":{"HTTPS":true}}}},"Services":{"svc:other":{"TCP":{"7445":{"HTTPS":true}}}}}`)
	before, _ := tailscaleObject([]byte(d.config))
	receipt, err := target.resource("synthetic-owner")
	if err != nil {
		t.Fatal(err)
	}
	if err := changeTailscaleServe(context.Background(), d.transport(t, target), receipt.Owner, receipt, target, false); err != nil {
		t.Fatal(err)
	}
	after, _ := tailscaleObject([]byte(d.config))
	for _, key := range []string{"AllowFunnel", "Foreground", "Services"} {
		if tailscaleCanonical(before[key]) != tailscaleCanonical(after[key]) {
			t.Fatal("unrelated state changed", key)
		}
	}
	tcp, _ := tailscaleObject(after["TCP"])
	if tailscaleCanonical(tcp["9443"]) != `{"TCPForward":"127.0.0.1:9000"}` {
		t.Fatal("other listener changed")
	}
	web, _ := tailscaleObject(after["Web"])
	server, _ := tailscaleObject(web["node.synthetic.ts.net:8443"])
	handlers, _ := tailscaleObject(server["Handlers"])
	if tailscaleCanonical(handlers["/other"]) != `{"Text":"unrelated"}` || tailscaleCanonical(handlers[target.Path]) != `{"Proxy":"http://127.0.0.1:3000"}` {
		t.Fatal("handler scope changed")
	}
	if err := changeTailscaleServe(context.Background(), d.transport(t, target), receipt.Owner, receipt, target, true); err != nil {
		t.Fatal(err)
	}
	after, _ = tailscaleObject([]byte(d.config))
	web, _ = tailscaleObject(after["Web"])
	server, _ = tailscaleObject(web["node.synthetic.ts.net:8443"])
	handlers, _ = tailscaleObject(server["Handlers"])
	if len(handlers) != 1 || tailscaleCanonical(handlers["/other"]) != `{"Text":"unrelated"}` {
		t.Fatal("targeted stop removed another handler")
	}
	if !reflect.DeepEqual(d.journal, []string{"GET /localapi/v0/status?peers=false", "GET /localapi/v0/serve-config", "GET /localapi/v0/status?peers=false", "POST /localapi/v0/serve-config", "GET /localapi/v0/status?peers=false", "GET /localapi/v0/serve-config", "GET /localapi/v0/status?peers=false", "POST /localapi/v0/serve-config"}) {
		t.Fatal("unexpected operation journal")
	}
}

func TestTunnelTailscaleRootStopIsTargetedAndAbsentCleanupDoesNotWrite(t *testing.T) {
	target := tailscaleSyntheticTarget()
	target.Path = "/"
	receipt, _ := target.resource("synthetic-owner")
	d := tailscaleSyntheticDaemon(`{"TCP":{"8443":{"HTTPS":true}},"Web":{"node.synthetic.ts.net:8443":{"Handlers":{"/":{"Proxy":"http://127.0.0.1:3000"},"/other":{"Text":"unrelated"}}}}}`)
	if err := changeTailscaleServe(context.Background(), d.transport(t, target), receipt.Owner, receipt, target, true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(d.config, "/other") || strings.Contains(d.config, `"Proxy"`) || !strings.Contains(d.config, `"8443"`) {
		t.Fatal("root stop widened to listener reset")
	}
	d.journal = nil
	before := d.config
	if err := changeTailscaleServe(context.Background(), d.transport(t, target), receipt.Owner, receipt, target, true); err != nil || d.config != before || len(d.journal) != 3 {
		t.Fatal("already-absent handler caused a write")
	}
	empty := tailscaleSyntheticDaemon("null\n")
	target.Path = "/preview"
	receipt, _ = target.resource("synthetic-owner")
	if err := changeTailscaleServe(context.Background(), empty.transport(t, target), receipt.Owner, receipt, target, false); err != nil {
		t.Fatal("fresh null configuration failed", err)
	}
}

func TestTunnelTailscaleRefusesSharedExposureAndChangedOwnership(t *testing.T) {
	target := tailscaleSyntheticTarget()
	receipt, _ := target.resource("synthetic-owner")
	for _, config := range []string{
		`{"AllowFunnel":{"node.synthetic.ts.net:8443":true}}`,
		`{"TCP":{"8443":{"TCPForward":"127.0.0.1:9000"}}}`,
		`{"Web":{"node.synthetic.ts.net:8443":{"Handlers":{"/":{"Text":"unrelated"}}}}}`,
		`{"Foreground":{"foreign":{"TCP":{"8443":{"HTTPS":true}}}}}`,
		`{"Foreground":{"foreign":{"AllowFunnel":{"node.synthetic.ts.net:8443":true}}}}`,
		`{"Foreground":{"foreign":{"AllowFunnel":{"node.synthetic.ts.net:8443":null}}}}`,
		`{"Services":{"svc:foreign":{"Web":{"other.synthetic.ts.net:8443":{"Handlers":{}}}}}}`,
		`{"TCP":{"8443":{"HTTPS":true}},"Web":{"node.synthetic.ts.net:8443":{"Handlers":{"/preview":{"Proxy":"http://127.0.0.1:9001"}}}}}`,
		`{"TCP":{"8443":{"HTTPS":true}},"Web":{"node.synthetic.ts.net:8443":{"Handlers":{"/preview":{"Proxy":"http://127.0.0.1:3000"}}}}}`,
		`{"AllowFunnel":{"node.synthetic.ts.net:8443":true},"AllowFunnel":{}}`,
		`{"allowfunnel":{}}`,
		`{"TCP":{"08443":{"HTTPS":true}}}`,
		`{"AllowFunnel":{"node.synthetic.ts.net:08443":true}}`,
	} {
		d := tailscaleSyntheticDaemon(config)
		if err := changeTailscaleServe(context.Background(), d.transport(t, target), receipt.Owner, receipt, target, false); err == nil {
			t.Fatal("unsafe shared configuration accepted")
		}
		for _, step := range d.journal {
			if strings.HasPrefix(step, "POST") {
				t.Fatal("refusal occurred after mutation")
			}
		}
	}
	d := tailscaleSyntheticDaemon(`{}`)
	foreign := receipt
	foreign.Ownership = "borrowed"
	if changeTailscaleServe(context.Background(), d.transport(t, target), receipt.Owner, foreign, target, false) == nil || len(d.journal) != 0 {
		t.Fatal("foreign reservation reached daemon")
	}
	d.nodeID = "another-node"
	if changeTailscaleServe(context.Background(), d.transport(t, target), receipt.Owner, receipt, target, false) == nil || len(d.journal) != 1 {
		t.Fatal("node identity change reached configuration mutation")
	}
}

func TestTunnelTailscaleCASConflictRemainsPendingWithoutRetry(t *testing.T) {
	target := tailscaleSyntheticTarget()
	receipt, _ := target.resource("synthetic-owner")
	d := tailscaleSyntheticDaemon(`{}`)
	d.postStatus = 412
	// A fake owner journal is already pending before the injected effect. This
	// tests adapter refusal/replay behavior, not the held production writer.
	op := tunnelOperation{ID: "synthetic-operation", Steps: []tunnelEffectStep{{Name: "serve", Status: "pending"}}}
	transport := d.transport(t, target)
	wrapped := func(ctx context.Context, socket string, r *http.Request) (*http.Response, error) {
		if r.Method == "POST" && op.Steps[0].Status != "pending" {
			t.Fatal("fake pending intent absent")
		}
		return transport(ctx, socket, r)
	}
	err := changeTailscaleServe(context.Background(), wrapped, receipt.Owner, receipt, target, false)
	if err == nil || strings.Contains(err.Error(), "synthetic-sensitive-value") || len(d.journal) != 4 || op.Steps[0].Status != "pending" {
		t.Fatal("conflict retried or pending ownership lost")
	}
	d = tailscaleSyntheticDaemon(`{}`)
	d.etag = ""
	if changeTailscaleServe(context.Background(), d.transport(t, target), receipt.Owner, receipt, target, false) == nil || len(d.journal) != 3 {
		t.Fatal("missing generation allowed blind write")
	}
}

func TestTunnelTailscaleExplicitScopeAndInputsRequired(t *testing.T) {
	for _, change := range []func(*tailscaleServeTarget){func(t *tailscaleServeTarget) { t.Socket = "relative.sock" }, func(t *tailscaleServeTarget) { t.Path = "" }, func(t *tailscaleServeTarget) { t.Path = "/../other" }, func(t *tailscaleServeTarget) { t.Target = "http://remote.example.test:3000" }, func(t *tailscaleServeTarget) { t.NodeID = "" }} {
		target := tailscaleSyntheticTarget()
		change(&target)
		if _, err := target.resource("synthetic-owner"); err == nil {
			t.Fatal("unsafe scope accepted")
		}
	}
	target := tailscaleSyntheticTarget()
	receipt, _ := target.resource("synthetic-owner")
	if changeTailscaleServe(context.Background(), nil, receipt.Owner, receipt, target, false) == nil {
		t.Fatal("implicit default socket/transport allowed")
	}
}
