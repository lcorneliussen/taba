package wtc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type tunnelProviderIntent struct {
	Kind    string `json:"kind"`
	Scope   string `json:"scope"`
	ID      string `json:"id"`
	Request string `json:"request"`
	Outcome string `json:"outcome,omitempty"`
}

func (i tunnelProviderIntent) validate() error {
	if !tunnelComponent.MatchString(i.Kind) || i.Scope == "" || len(i.Scope) > 512 || len(i.ID) > 512 || strings.ContainsAny(i.Scope+i.ID, "\r\n\x00") || !validTunnelDigest(i.Request) || i.Outcome != "" && i.Outcome != "not-applied" {
		return fmt.Errorf("invalid exact provider intent")
	}
	return nil
}

type tunnelProviderRuntime struct {
	c     *Context
	HTTP  http.RoundTripper
	Local tailscaleLocalTransport
}

func (c *Context) tunnelProviders() *tunnelProviderRuntime {
	transport := &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 10 * time.Second}).DialContext, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 15 * time.Second}
	return &tunnelProviderRuntime{c: c, HTTP: transport, Local: func(ctx context.Context, socket string, req *http.Request) (*http.Response, error) {
		t := &http.Transport{Proxy: nil, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "unix", socket)
		}}
		defer t.CloseIdleConnections()
		return t.RoundTrip(req)
	}}
}

func (p *tunnelProviderRuntime) effects() tunnelLifecycleEffects {
	return tunnelLifecycleEffects{Authorize: p.authorize, Access: p.access, Provider: p.effect, Event: p.c.dispatchTunnelEvent, Publish: func(_ context.Context, s tunnelHostSnapshot) error { return p.c.publishTunnelSnapshot(s) }, Reload: p.c.reloadTunnelApps, Confirm: p.confirm}
}

func (p *tunnelProviderRuntime) authorize(ctx context.Context, call tunnelEffectCall) error {
	scope, err := p.c.ValidateControlRoot()
	if err != nil {
		return err
	}
	data, _ := json.Marshal([2]string{scope.OwnerRoot, scope.Root})
	if call.Definition.ControlScope == "" || tunnelDigest(data) != call.Definition.ControlScope {
		return fmt.Errorf("receiving credential permission changed; restore/approve retained scope before cleanup")
	}
	if call.Entry.Operation.Action == "start" && (call.Stage == "register" || call.Stage == "start") {
		refs := call.Definition.Entry.Policy.Inputs
		if call.Entry.Provider == "devtunnel" {
			if err = checkDevTunnelNode(ctx, refs.Node, minimalTunnelEnvironment(p.c.Collection)); err != nil {
				return err
			}
		}
		if call.Stage == "register" && (call.Entry.Provider == "cloudflare" || call.Entry.Provider == "devtunnel" || call.Entry.Provider == "ngrok") {
			if _, err = p.c.tunnelSecret(refs.HostingFile); err != nil {
				return err
			}
		}
		if call.Entry.Provider == "cloudflare" {
			if !filepath.IsAbs(refs.Executable) {
				return fmt.Errorf("explicit scoped cloudflared executable required")
			}
			info, err := os.Stat(refs.Executable)
			if err != nil || !info.Mode().IsRegular() || info.Mode()&0111 == 0 {
				return fmt.Errorf("explicit cloudflared executable unavailable")
			}
		}
	}
	if call.Stage == "register" || call.Stage == "start" {
		plan, _, err := p.c.runtimePlanWithTunnels("dekit", false)
		if err != nil {
			return err
		}
		entries, err := p.c.effectiveTunnels(plan)
		if err != nil {
			return err
		}
		found := false
		for _, entry := range entries {
			if entry.Key != call.Entry.Key {
				continue
			}
			if entry.Disabled || !entry.Matched || call.Stage == "start" && entry.Selected != nil && !*entry.Selected {
				return fmt.Errorf("tunnel selection no longer permits this effect")
			}
			d, err := p.c.tunnelDefinitionFor(entry, plan)
			if err != nil {
				return err
			}
			digest, _ := d.digest()
			if digest != call.Entry.Definition {
				return fmt.Errorf("tunnel definition changed before provider use")
			}
			found = true
			break
		}
		if !found {
			return fmt.Errorf("tunnel definition is no longer selected")
		}
	}
	refs := call.Definition.Entry.Policy.Inputs
	switch call.Stage {
	case "register":
		if call.Entry.Provider == "cloudflare" || call.Entry.Provider == "devtunnel" || call.Entry.Provider == "ngrok" && call.Definition.Entry.Policy.Options.Reserve {
			if _, err = p.c.tunnelSecret(refs.ManagementFile); err != nil {
				return err
			}
			if _, err = p.c.tunnelSecret(refs.CleanupFile); err != nil {
				return err
			}
		}
	case "start":
		if call.Entry.Provider == "devtunnel" && call.Entry.Mode == "temporary" {
			if _, err = p.c.tunnelSecret(refs.ManagementFile); err != nil {
				return err
			}
			if _, err = p.c.tunnelSecret(refs.CleanupFile); err != nil {
				return err
			}
		} else if call.Entry.Provider == "ngrok" || call.Entry.Provider == "devtunnel" || call.Entry.Provider == "cloudflare" && call.Entry.Mode == "named" {
			if _, err = p.c.tunnelSecret(refs.HostingFile); err != nil {
				return err
			}
		}
	case "unregister":
		for _, r := range call.Entry.Resources {
			if r.Ownership == "owned" && r.Provider != "callback" && r.Provider != "tailscale" && r.Kind != "host-task" {
				if _, err = p.c.tunnelSecret(refs.CleanupFile); err != nil {
					return err
				}
				break
			}
		}
	}
	if call.Entry.Provider == "tailscale" {
		_, err = p.tailscaleTarget(call.Definition)
		if err != nil {
			return err
		}
	}
	return nil
}

func (p *tunnelProviderRuntime) access(ctx context.Context, d tunnelDefinition) (tunnelAccessEvidence, error) {
	// Account/protected declarations alone are not enforcement evidence.
	evidence := tunnelAccessEvidence{}
	if d.Entry.Policy.Provider != "tailscale" {
		return evidence, nil
	}
	target, err := p.tailscaleTarget(d)
	if err != nil {
		return evidence, err
	}
	if err = p.authorizeScope(d); err != nil {
		return evidence, err
	}
	data, _, err := tailscaleLocalRequest(ctx, p.Local, target.Socket, http.MethodGet, "status?peers=false", "", nil)
	if err != nil {
		return evidence, err
	}
	var status struct {
		BackendState string
		Self         struct{ ID, DNSName string }
	}
	if !tailscaleUniqueJSON(data) || json.Unmarshal(data, &status) != nil || status.BackendState != "Running" || status.Self.ID != target.NodeID || strings.TrimSuffix(status.Self.DNSName, ".") != target.DNSName {
		return evidence, fmt.Errorf("authorized daemon identity not verified")
	}
	data, _, err = tailscaleLocalRequest(ctx, p.Local, target.Socket, http.MethodGet, "serve-config", "", nil)
	if err != nil {
		return evidence, err
	}
	if !tailscaleUniqueJSON(data) {
		return evidence, fmt.Errorf("invalid daemon exposure configuration")
	}
	if tailscaleSelectedPublic(data, target.Port) {
		return evidence, fmt.Errorf("selected listener has public exposure; shared widening is refused")
	}
	digest, _ := d.digest()
	return tunnelAccessEvidence{Definition: digest, Exposure: "tailnet", Verified: true}, nil
}

func (p *tunnelProviderRuntime) authorizeScope(d tunnelDefinition) error {
	scope, err := p.c.ValidateControlRoot()
	if err != nil {
		return err
	}
	data, _ := json.Marshal([2]string{scope.OwnerRoot, scope.Root})
	if tunnelDigest(data) != d.ControlScope {
		return fmt.Errorf("receiving scope changed")
	}
	return nil
}

func tailscaleSelectedPublic(data []byte, port int) bool {
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return false
	}
	var root map[string]json.RawMessage
	if json.Unmarshal(data, &root) != nil {
		return true
	}
	var inspect func(map[string]json.RawMessage) bool
	inspect = func(object map[string]json.RawMessage) bool {
		for key := range object {
			switch key {
			case "TCP", "Web", "AllowFunnel", "Foreground", "Services":
			default:
				return true
			}
		}
		for _, field := range []struct {
			name string
			host bool
		}{{"TCP", false}, {"Web", true}, {"AllowFunnel", true}} {
			values, err := tailscaleObject(object[field.name])
			if err != nil || tailscaleCanonicalPorts(values, field.host) != nil {
				return true
			}
		}
		if value, exists := object["AllowFunnel"]; exists {
			var public map[string]json.RawMessage
			if json.Unmarshal(value, &public) != nil {
				return true
			}
			for key, on := range public {
				if strings.HasSuffix(key, ":"+strconv.Itoa(port)) && string(bytes.TrimSpace(on)) != "false" {
					return true
				}
			}
		}
		for _, section := range []string{"Foreground", "Services"} {
			if value, exists := object[section]; exists {
				var children map[string]map[string]json.RawMessage
				if json.Unmarshal(value, &children) != nil {
					return true
				}
				for _, child := range children {
					if inspect(child) {
						return true
					}
				}
			}
		}
		return false
	}
	return inspect(root)
}

func (p *tunnelProviderRuntime) tailscaleTarget(d tunnelDefinition) (tailscaleServeTarget, error) {
	r := d.Entry.Policy.Inputs
	o := d.Entry.Policy.Options
	path := o.Path
	if path == "" {
		path = "/"
	}
	t := tailscaleServeTarget{Socket: r.Socket, NodeID: r.NodeID, DNSName: r.DNSName, Path: path, Target: d.Target, Port: o.ListenerPort}
	_, err := t.resource(d.Owner)
	return t, err
}

func (p *tunnelProviderRuntime) client(d tunnelDefinition, cleanup bool) (tunnelHTTP, error) {
	ref := d.Entry.Policy.Inputs.ManagementFile
	if cleanup {
		ref = d.Entry.Policy.Inputs.CleanupFile
	}
	return p.explicitClient(d, ref)
}

func tunnelAuthorization(token string) string {
	if !strings.HasPrefix(token, "Bearer ") && !strings.HasPrefix(token, "tunnel ") {
		return "Bearer " + token
	}
	return token
}
func tunnelCredentialContext(d tunnelDefinition, auth string) string {
	return tunnelDigest([]byte(d.Entry.Policy.Provider + "\x00" + d.ControlScope + "\x00" + auth))
}

func (p *tunnelProviderRuntime) explicitClient(d tunnelDefinition, ref string) (tunnelHTTP, error) {
	if err := p.authorizeScope(d); err != nil {
		return tunnelHTTP{}, err
	}
	token, err := p.c.tunnelSecret(ref)
	if err != nil {
		return tunnelHTTP{}, err
	}
	auth := tunnelAuthorization(token)
	binding := tunnelCredentialContext(d, auth)
	return tunnelHTTP{CredentialContext: binding, RoundTrip: tunnelBoundTransport{provider: p, definition: d, ref: ref, binding: binding, auth: auth}, Auth: auth}, nil
}

type tunnelBoundTransport struct {
	binding, auth string
	provider      *tunnelProviderRuntime
	definition    tunnelDefinition
	ref           string
}

func (t tunnelBoundTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if err := t.provider.authorizeScope(t.definition); err != nil {
		return nil, err
	}
	token, err := t.provider.c.tunnelSecret(t.ref)
	if err != nil {
		return nil, err
	}
	if tunnelCredentialContext(t.definition, tunnelAuthorization(token)) != t.binding || request.Header.Get("Authorization") != t.auth {
		return nil, fmt.Errorf("explicit provider capability changed during effect; reconcile without switching context")
	}
	return t.provider.HTTP.RoundTrip(request)
}

func (p *tunnelProviderRuntime) intent(ctx context.Context, call tunnelEffectCall, kind, scope, id string, body any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	intent := tunnelProviderIntent{Kind: kind, Scope: scope, ID: id, Request: tunnelDigest(data)}
	if err = intent.validate(); err != nil {
		return err
	}
	return p.c.tunnelRuntimeStore().Update(ctx, func(s *tunnelState) error {
		e := lifecycleEntry(s, call.Entry.Key)
		if e == nil || e.Operation == nil || e.Operation.ID != call.OperationID || e.Operation.Lifecycle == nil || !lifecycleUncertain(e.Operation) {
			return errTunnelReconcile
		}
		e.Operation.Lifecycle.ProviderIntent = &intent
		return nil
	})
}

func (p *tunnelProviderRuntime) checkpoint(ctx context.Context, call tunnelEffectCall, add, remove []tunnelResourceRef, verified bool) error {
	return p.c.tunnelRuntimeStore().Update(ctx, func(s *tunnelState) error {
		e := lifecycleEntry(s, call.Entry.Key)
		if e == nil || e.Operation == nil || e.Operation.ID != call.OperationID || !lifecycleUncertain(e.Operation) {
			return errTunnelReconcile
		}
		return lifecycleApply(e, call.Owner, "receipt", tunnelEffectResult{Add: add, Remove: remove}, verified)
	})
}

func providerReceipt(call tunnelEffectCall, kind, scope, id, fingerprint, lifetime string) tunnelResourceRef {
	return tunnelResourceRef{Provider: call.Entry.Provider, Scope: scope, Kind: kind, ID: id, Ownership: "owned", Owner: call.Owner, Fingerprint: fingerprint, Lifetime: lifetime}
}

func (p *tunnelProviderRuntime) effect(ctx context.Context, call tunnelEffectCall) (result tunnelEffectResult, err error) {
	defer func() {
		if err == nil {
			return
		}
		state, readErr := p.c.readTunnelJournal()
		if readErr != nil {
			return
		}
		entry := lifecycleEntry(&state, call.Entry.Key)
		if entry != nil && entry.Operation != nil && entry.Operation.ID == call.OperationID && entry.Operation.Lifecycle != nil && entry.Operation.Lifecycle.ProviderIntent == nil {
			// Built-in errors are bounded/redacted at their transport and child
			// boundaries. Preserve the actionable refusal without provider output.
			err = fmt.Errorf("%w: %v", errTunnelNotApplied, err)
		}
	}()
	if err = p.authorize(ctx, call); err != nil {
		return result, err
	}
	switch call.Stage {
	case "register":
		return p.register(ctx, call)
	case "start":
		return p.start(ctx, call)
	case "stop":
		return p.stop(ctx, call)
	case "unregister":
		return p.unregister(ctx, call)
	}
	return tunnelEffectResult{}, fmt.Errorf("unsupported provider effect")
}

func (p *tunnelProviderRuntime) register(ctx context.Context, call tunnelEffectCall) (tunnelEffectResult, error) {
	d := call.Definition
	o := d.Entry.Policy.Options
	result := tunnelEffectResult{}
	switch call.Entry.Provider {
	case "tailscale":
		target, err := p.tailscaleTarget(d)
		if err != nil {
			return result, err
		}
		receipt, _ := target.resource(call.Owner)
		receipt.Lifetime = "registration"
		receipt.Kind = "reservation"
		if err = p.reserveTailscale(ctx, call, target); err != nil {
			return result, err
		}
		result.Add = []tunnelResourceRef{receipt}
		address, _ := parseTunnelAddress("https://" + net.JoinHostPort(target.DNSName, strconv.Itoa(target.Port)) + target.Path)
		result.Address = &address
		return result, nil
	case "ngrok":
		address, _ := parseTunnelAddress("https://" + o.Domain)
		result.Address = &address
		if !o.Reserve {
			result.Add = []tunnelResourceRef{{Provider: "ngrok", Scope: d.ProviderScope, Kind: "domain", ID: o.Domain, Ownership: "borrowed", Lifetime: "registration"}}
			return result, nil
		}
		client, err := p.client(d, false)
		if err != nil {
			return result, err
		}
		if err = p.intent(ctx, call, "reserve-domain", d.ProviderScope, o.Domain, o); err != nil {
			return result, err
		}
		r, err := reserveNgrokDomain(ctx, client, o.Domain, call.OperationID)
		if r.ID != "" {
			receipt := providerReceipt(call, "domain", d.ProviderScope, r.ID, tunnelDigest([]byte(r.ID+"/"+r.Domain+"/"+call.OperationID)), "registration")
			receipt.CredentialContext = client.CredentialContext
			if !r.IdentityVerified {
				receipt.Ownership = "unverified"
				receipt.Owner = ""
				receipt.Fingerprint = ""
			}
			result.Add = append(result.Add, receipt)
		}
		return result, err
	case "devtunnel":
		return p.registerDev(ctx, call, "registration")
	case "cloudflare":
		return p.registerCloudflare(ctx, call)
	}
	return result, fmt.Errorf("unsupported persistent provider registration")
}

func (p *tunnelProviderRuntime) registerDev(ctx context.Context, call tunnelEffectCall, lifetime string) (tunnelEffectResult, error) {
	result := tunnelEffectResult{}
	d := call.Definition
	client, err := p.client(d, false)
	if err != nil {
		return result, err
	}
	id := d.Entry.Policy.Options.Registration
	if id == "" {
		id = "taba-" + tunnelDigest([]byte(call.Owner + "/" + call.Entry.Key.String() + "/" + call.OperationID))[:24]
	}
	target, err := url.Parse(d.Target)
	if err != nil {
		return result, err
	}
	port, err := strconv.Atoi(target.Port())
	if err != nil {
		return result, fmt.Errorf("devtunnel backend requires an explicit local port")
	}
	if err = p.intent(ctx, call, "create-devtunnel", d.ProviderScope, id, map[string]any{"id": id, "port": port, "public": d.Entry.Policy.Access == "public"}); err != nil {
		return result, err
	}
	r, err := createDevTunnel(ctx, client, id, port, d.Entry.Policy.Access == "public")
	if r.TunnelID != "" && r.ClusterID != "" {
		receipt := providerReceipt(call, "tunnel", r.ClusterID, r.TunnelID, tunnelDigest([]byte(r.ClusterID+"/"+r.TunnelID+"/"+strconv.Itoa(port))), lifetime)
		receipt.CredentialContext = client.CredentialContext
		if !r.IdentityVerified {
			receipt.Ownership = "unverified"
			receipt.Owner = ""
			receipt.Fingerprint = ""
		}
		result.Add = []tunnelResourceRef{receipt}
	}
	if r.URL != "" {
		address, e := parseTunnelAddress(r.URL)
		if e == nil {
			result.Address = &address
		}
	}
	return result, err
}
