package wtc

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strconv"
)

func (p *tunnelProviderRuntime) start(ctx context.Context, call tunnelEffectCall) (tunnelEffectResult, error) {
	result := tunnelEffectResult{}
	if call.Entry.Provider == "tailscale" {
		target, err := p.tailscaleTarget(call.Definition)
		if err != nil {
			return result, err
		}
		receipt, _ := target.resource(call.Owner)
		receipt.Lifetime = "session"
		reserved := false
		for _, r := range call.Entry.Resources {
			if r.Provider == "tailscale" && r.Kind == "reservation" && r.Scope == receipt.Scope && r.ID == receipt.ID && r.Fingerprint == receipt.Fingerprint && r.Owner == call.Owner {
				reserved = true
			}
		}
		if !reserved {
			return result, fmt.Errorf("owned exact Tailscale registration required before serving")
		}
		submitted := false
		transport := func(ctx context.Context, socket string, req *http.Request) (*http.Response, error) {
			if req.Method == http.MethodPost {
				if err = p.intent(ctx, call, "serve-cas", receipt.Scope, receipt.ID, map[string]string{"etag": req.Header.Get("If-Match"), "target": target.Target}); err != nil {
					return nil, err
				}
				submitted = true
			}
			return p.Local(ctx, socket, req)
		}
		err = changeTailscaleServe(ctx, transport, call.Owner, receipt, target, false)
		if err != nil && submitted {
			uncertain := receipt
			uncertain.Ownership = "unverified"
			uncertain.Owner = ""
			uncertain.Fingerprint = ""
			result.Add = []tunnelResourceRef{uncertain}
			return result, err
		}
		if err != nil {
			return result, err
		}
		address, _ := parseTunnelAddress("https://" + net.JoinHostPort(target.DNSName, strconv.Itoa(target.Port)) + target.Path)
		result.Address = &address
		result.Add = []tunnelResourceRef{receipt}
		return result, nil
	}
	if call.Entry.Provider == "devtunnel" && call.Entry.Mode == "temporary" {
		created, err := p.registerDev(ctx, call, "session")
		if err != nil {
			return created, err
		}
		if err = p.checkpoint(ctx, call, created.Add, nil, true); err != nil {
			return result, err
		}
	}
	return p.startHostTask(ctx, call)
}

func (p *tunnelProviderRuntime) stop(ctx context.Context, call tunnelEffectCall) (tunnelEffectResult, error) {
	result := tunnelEffectResult{}
	if call.Entry.Provider == "tailscale" {
		target, err := p.tailscaleTarget(call.Definition)
		if err != nil {
			return result, err
		}
		for _, receipt := range call.Entry.Resources {
			if receipt.Provider != "tailscale" || receipt.Kind != "serve" {
				continue
			}
			if receipt.Ownership != "owned" {
				return result, errTunnelReconcile
			}
			if err = p.intent(ctx, call, "stop-serve", receipt.Scope, receipt.ID, receipt); err != nil {
				return result, err
			}
			if err = changeTailscaleServe(ctx, p.Local, call.Owner, receipt, target, true); err != nil {
				return result, err
			}
			result.Remove = append(result.Remove, receipt)
		}
		return result, nil
	}
	if err := p.stopHostTask(ctx, call); err != nil {
		return result, err
	}
	for _, receipt := range call.Entry.Resources {
		if receipt.Provider == "devtunnel" && receipt.Kind == "tunnel" && receipt.Lifetime == "session" {
			client, err := p.client(call.Definition, true)
			if err != nil {
				return result, err
			}
			receipt, err = p.checkDevOwned(ctx, client, call, receipt)
			if err != nil {
				return result, err
			}
			if err = p.intent(ctx, call, "delete-temporary", receipt.Scope, receipt.ID, receipt); err != nil {
				return result, err
			}
			if err = deleteDevTunnel(ctx, client, receipt.Scope, receipt.ID); err != nil {
				return result, err
			}
			if err = p.checkpoint(ctx, call, nil, []tunnelResourceRef{receipt}, true); err != nil {
				return result, err
			}
		}
	}
	return result, nil
}

func (p *tunnelProviderRuntime) confirm(ctx context.Context, call tunnelEffectCall) (tunnelEffectResult, bool, error) {
	// Providers without an exact durable success observation remain pending.
	// An absent query is never permission to replay an arbitrary remote effect.
	result := tunnelEffectResult{}
	switch call.Stage {
	case "start":
		if call.Entry.Provider == "tailscale" {
			return result, false, nil
		}
		ready, receipt, err := p.readHostReady(call)
		if err != nil {
			return result, false, err
		}
		result.Address = &ready.Address
		result.Add = []tunnelResourceRef{receipt}
		return result, true, nil
	case "stop":
		if call.Entry.Provider == "tailscale" {
			return result, false, nil
		}
		for _, receipt := range call.Entry.Resources {
			if receipt.Kind != "host-task" && receipt.Lifetime == "session" && receipt.Provider != "callback" {
				return result, false, nil
			}
		}
		if err := p.confirmHostStopped(call); err != nil {
			return result, false, err
		}
		for _, receipt := range call.Entry.Resources {
			if receipt.Kind == "host-task" {
				result.Remove = append(result.Remove, receipt)
			}
		}
		return result, true, nil
	case "publish":
		_, snapshot, err := p.c.readTunnelPublication()
		if err != nil {
			return result, false, err
		}
		return result, snapshot.Revision == call.Snapshot.Revision, nil
	case "unregister":
		// A lost deletion reply is confirmable only for exact owned IDs.
		// Pin one explicit capability for the entire confirmation, including
		// compound registrations. Never silently switch between receipts.
		var client tunnelHTTP
		loaded := false
		for _, receipt := range call.Entry.Resources {
			if receipt.Provider == "callback" {
				continue
			}
			if receipt.Ownership != "owned" {
				return result, false, nil
			}
			if !loaded {
				var err error
				client, err = p.client(call.Definition, true)
				if err != nil {
					return result, false, err
				}
				loaded = true
			}
			absent, err := p.exactProviderAbsent(ctx, client, receipt)
			if err != nil || !absent {
				return result, false, err
			}
			result.Remove = append(result.Remove, receipt)
		}
		return result, true, nil
	}
	return result, false, nil
}

func (p *tunnelProviderRuntime) exactProviderAbsent(ctx context.Context, client tunnelHTTP, receipt tunnelResourceRef) (bool, error) {
	if client.CredentialContext == "" || receipt.CredentialContext != client.CredentialContext {
		return false, fmt.Errorf("provider absence requires the recorded credential context")
	}
	var code int
	var err error
	switch receipt.Provider {
	case "cloudflare":
		observation, e := p.cloudflareObservation(ctx, client, receipt)
		return observation.Absent, e
	case "ngrok":
		code, err = client.request(ctx, http.MethodGet, "https://api.ngrok.com/reserved_domains/"+receipt.ID, map[string]string{"ngrok-version": "2"}, nil, nil)
	case "devtunnel":
		code, err = client.request(ctx, http.MethodGet, "https://"+receipt.Scope+".rel.tunnels.api.visualstudio.com/tunnels/"+receipt.ID+"?api-version=2023-09-27-preview", nil, nil, nil)
	default:
		return false, fmt.Errorf("unsupported exact provider reconciliation")
	}
	if code == 404 {
		return true, nil
	}
	return false, err
}
