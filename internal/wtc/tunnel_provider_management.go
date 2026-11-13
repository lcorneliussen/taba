package wtc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func tunnelJSONFingerprint(data []byte) (string, error) {
	var value any
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	if d.Decode(&value) != nil {
		return "", fmt.Errorf("invalid provider observation")
	}
	canonical, _ := json.Marshal(value)
	return tunnelDigest(canonical), nil
}
func cloudflareTunnelFingerprint(account string, r cloudflareTunnelRegistration) string {
	data, _ := json.Marshal([]string{account, r.ID, r.Name, r.ConfigSource})
	return tunnelDigest(data)
}

func (p *tunnelProviderRuntime) cloudflareObservation(ctx context.Context, client tunnelHTTP, receipt tunnelResourceRef) (tunnelResourceObservation, error) {
	observation := tunnelResourceObservation{Resource: receipt, CredentialContext: client.CredentialContext}
	path := "accounts/" + receipt.Scope + "/cfd_tunnel/" + receipt.ID
	if receipt.Kind == "configuration" {
		path += "/configurations"
	}
	if receipt.Kind == "dns" {
		path = "zones/" + receipt.Scope + "/dns_records/" + receipt.ID
	}
	var wire json.RawMessage
	code, err := cloudflareRequest(ctx, client, http.MethodGet, path, nil, &wire)
	if code == 404 {
		observation.Absent = true
		return observation, nil
	}
	if err != nil {
		return observation, err
	}
	switch receipt.Kind {
	case "tunnel":
		var r cloudflareTunnelRegistration
		if json.Unmarshal(wire, &r) != nil || r.ID != receipt.ID || r.AccountTag != "" && r.AccountTag != receipt.Scope {
			return observation, fmt.Errorf("Cloudflare exact identity unverified")
		}
		observation.Resource.Fingerprint = cloudflareTunnelFingerprint(receipt.Scope, r)
	case "dns":
		observation.Resource.Fingerprint, err = tunnelJSONFingerprint(wire)
	case "configuration":
		var config struct {
			Config json.RawMessage `json:"config"`
		}
		if json.Unmarshal(wire, &config) != nil || len(config.Config) == 0 {
			return observation, fmt.Errorf("Cloudflare configuration unavailable")
		}
		observation.Resource.Fingerprint, err = tunnelJSONFingerprint(config.Config)
	default:
		return observation, fmt.Errorf("unsupported exact Cloudflare observation")
	}
	return observation, err
}

func (p *tunnelProviderRuntime) registerCloudflare(ctx context.Context, call tunnelEffectCall) (tunnelEffectResult, error) {
	result := tunnelEffectResult{}
	d := call.Definition
	o := d.Entry.Policy.Options
	client, err := p.client(d, false)
	if err != nil {
		return result, err
	}
	if !cloudflareID.MatchString(o.AccountID) || !cloudflareID.MatchString(o.ZoneID) {
		return result, fmt.Errorf("named Cloudflare requires exact account_id and zone_id")
	}
	name := o.Registration
	if name == "" {
		name = "taba-" + tunnelDigest([]byte(call.Owner + "/" + call.Entry.Key.String()))[:24]
	}
	if err = p.intent(ctx, call, "create-tunnel", o.AccountID, name, o); err != nil {
		return result, err
	}
	r, err := createCloudflareTunnel(ctx, client, o.AccountID, name)
	if r.ID == "" {
		return result, err
	}
	tunnel := providerReceipt(call, "tunnel", o.AccountID, r.ID, cloudflareTunnelFingerprint(o.AccountID, r), "registration")
	tunnel.CredentialContext = client.CredentialContext
	if !r.IdentityVerified {
		tunnel.Ownership = "unverified"
		tunnel.Owner = ""
		tunnel.Fingerprint = ""
		result.Add = []tunnelResourceRef{tunnel}
		return result, err
	}
	if err = p.checkpoint(ctx, call, []tunnelResourceRef{tunnel}, nil, true); err != nil {
		return result, err
	}
	if err != nil {
		return result, err
	}
	config := providerReceipt(call, "configuration", o.AccountID, r.ID, tunnelDigest([]byte("initial")), "registration")
	config.CredentialContext = client.CredentialContext
	observed, err := p.cloudflareObservation(ctx, client, config)
	if err != nil || observed.Absent {
		return result, fmt.Errorf("created Cloudflare configuration unconfirmed; retain tunnel")
	}
	config = observed.Resource
	if err = p.checkpoint(ctx, call, []tunnelResourceRef{config}, nil, true); err != nil {
		return result, err
	}
	if err = p.intent(ctx, call, "configure-tunnel", o.AccountID, r.ID, map[string]string{"hostname": o.Hostname, "target": d.Target}); err != nil {
		return result, err
	}
	if err = putCloudflareConfiguration(ctx, client, call.Owner, config, observed, o.Hostname, d.Target); err != nil {
		return result, err
	}
	updated, err := p.cloudflareObservation(ctx, client, config)
	if err != nil || updated.Absent {
		return result, fmt.Errorf("Cloudflare configuration update unconfirmed")
	}
	if err = p.checkpoint(ctx, call, []tunnelResourceRef{updated.Resource}, []tunnelResourceRef{config}, true); err != nil {
		return result, err
	}
	if err = p.intent(ctx, call, "create-dns", o.ZoneID, o.Hostname, map[string]string{"hostname": o.Hostname, "tunnel": r.ID}); err != nil {
		return result, err
	}
	dns, err := createCloudflareDNS(ctx, client, o.ZoneID, o.Hostname, r.ID, call.Owner)
	if dns.Receipt != nil {
		receipt := *dns.Receipt
		receipt.CredentialContext = client.CredentialContext
		receipt.Lifetime = "registration"
		result.Add = append(result.Add, receipt)
	} else if dns.ID != "" {
		result.Add = append(result.Add, tunnelResourceRef{Provider: "cloudflare", Scope: o.ZoneID, Kind: "dns", ID: dns.ID, Ownership: "unverified", Lifetime: "registration"})
	}
	if err == nil {
		address, _ := parseTunnelAddress("https://" + o.Hostname)
		result.Address = &address
	}
	return result, err
}

func (p *tunnelProviderRuntime) reserveTailscale(ctx context.Context, call tunnelEffectCall, target tailscaleServeTarget) error {
	return p.c.withTunnelRuntimeLock(ctx, func() error {
		receiver, err := p.c.receivingContext()
		if err != nil {
			return err
		}
		collections := []string{p.c.Collection}
		if receiver.Project != nil {
			policy := filepath.Join(receiver.Project.Root, ".config", "tunnels.toml")
			if err = runtimeSafeParents(receiver.Project.Root, policy); err != nil {
				return err
			}
			if err = os.MkdirAll(filepath.Dir(policy), 0700); err != nil {
				return err
			}
			bound, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			unlock, err := lockDevFile(bound, filepath.Join(filepath.Dir(policy), ".tunnels-policy.lock"))
			if err != nil {
				return err
			}
			defer unlock()
			collections, err = WorkspaceCollections(receiver.Project.Root)
			if err != nil {
				return err
			}
		}
		candidate, _ := target.resource(call.Owner)
		candidate.Kind = "reservation"
		candidate.Lifetime = "registration"
		for _, collection := range collections {
			other, err := OpenCollection(collection)
			if err != nil {
				return fmt.Errorf("cannot verify local tunnel reservation owner")
			}
			if receiver.Project != nil && (other.Project == nil || !sameCanonicalPath(other.Project.Root, receiver.Project.Root)) {
				continue
			}
			state, err := other.readTunnelJournal()
			if err != nil {
				return fmt.Errorf("local tunnel reservation journal needs reconciliation")
			}
			for _, entry := range state.Entries {
				if sameCanonicalPath(collection, p.c.Collection) && entry.Key == call.Entry.Key {
					continue
				}
				for _, receipt := range entry.Resources {
					if receipt.Provider != "tailscale" || receipt.Kind != "reservation" || receipt.Scope != candidate.Scope {
						continue
					}
					if tailscaleReservationOverlap(receipt.ID, candidate.ID) {
						return fmt.Errorf("Tailscale listener/path is reserved by another named entry")
					}
				}
			}
		}
		state, err := p.c.readTunnelJournal()
		if err != nil {
			return err
		}
		entry := lifecycleEntry(&state, call.Entry.Key)
		if entry == nil || entry.Operation == nil || entry.Operation.ID != call.OperationID || !lifecycleUncertain(entry.Operation) {
			return errTunnelReconcile
		}
		if err = lifecycleApply(entry, call.Owner, "receipt", tunnelEffectResult{Add: []tunnelResourceRef{candidate}}, true); err != nil {
			return err
		}
		data, err := encodeTunnelState(state, call.Owner)
		if err != nil {
			return err
		}
		return runtimeWrite(p.c.tunnelJournalPath(), data)
	})
}

func tailscaleReservationOverlap(a, b string) bool {
	ah, ap, ok := strings.Cut(a, "/")
	if !ok {
		return true
	}
	bh, bp, ok := strings.Cut(b, "/")
	if !ok {
		return true
	}
	if ah != bh {
		return false
	}
	ap = "/" + ap
	bp = "/" + bp
	return ap == bp || strings.HasPrefix(ap, strings.TrimSuffix(bp, "/")+"/") || strings.HasPrefix(bp, strings.TrimSuffix(ap, "/")+"/")
}

func (p *tunnelProviderRuntime) unregister(ctx context.Context, call tunnelEffectCall) (tunnelEffectResult, error) {
	result := tunnelEffectResult{}
	if call.Entry.Active != nil || lifecycleSessionResources(call.Entry) {
		return result, fmt.Errorf("owned sessions must be stopped before registration cleanup")
	}
	receipts := append([]tunnelResourceRef(nil), call.Entry.Resources...)
	priority := func(r tunnelResourceRef) int {
		if r.Kind == "dns" {
			return 0
		}
		if r.Kind == "tunnel" {
			return 2
		}
		return 1
	}
	sort.SliceStable(receipts, func(i, j int) bool { return priority(receipts[i]) < priority(receipts[j]) })
	observations := map[string]tunnelResourceObservation{}
	var client tunnelHTTP
	var clientErr error
	clientLoaded := false
	getClient := func() (tunnelHTTP, error) {
		if !clientLoaded {
			client, clientErr = p.client(call.Definition, true)
			clientLoaded = true
		}
		return client, clientErr
	}
	if call.Entry.Provider == "cloudflare" {
		client, err := getClient()
		if err != nil {
			return result, err
		}
		owned := []tunnelResourceRef{}
		observed := []tunnelResourceObservation{}
		for _, receipt := range receipts {
			if receipt.Provider != "cloudflare" {
				continue
			}
			if receipt.Ownership != "owned" {
				return result, errTunnelReconcile
			}
			o, err := p.cloudflareObservation(ctx, client, receipt)
			if err != nil {
				return result, err
			}
			owned = append(owned, receipt)
			observed = append(observed, o)
			observations[receipt.key()] = o
		}
		if _, err = tunnelCleanupTargets(call.Owner, owned, observed); err != nil {
			return result, err
		}
		for i, receipt := range receipts {
			if receipt.Provider != "cloudflare" {
				continue
			}
			bound, err := p.bindProviderObservation(ctx, call, client, receipt, observations[receipt.key()])
			if err != nil {
				return result, err
			}
			receipts[i] = bound
			o := observations[receipt.key()]
			o.Resource = bound
			observations[receipt.key()] = o
		}
	}
	for _, receipt := range receipts {
		if receipt.Provider == "callback" {
			continue
		}
		if receipt.Ownership == "unverified" {
			return result, errTunnelReconcile
		}
		if receipt.Ownership == "borrowed" || receipt.Provider == "tailscale" {
			result.Remove = append(result.Remove, receipt)
			continue
		}
		if receipt.Provider == "cloudflare" && receipt.Kind == "configuration" {
			continue
		}
		client, err := getClient()
		if err != nil {
			return result, err
		}
		remove := []tunnelResourceRef{receipt}
		switch receipt.Provider {
		case "cloudflare":
			if err = p.intent(ctx, call, "delete-"+receipt.Kind, receipt.Scope, receipt.ID, receipt); err != nil {
				return result, err
			}
			if err = deleteCloudflareResource(ctx, client, call.Owner, receipt, observations[receipt.key()], true); err != nil {
				return result, err
			}
			if receipt.Kind == "tunnel" {
				for _, child := range receipts {
					if child.Provider == "cloudflare" && child.Kind == "configuration" && child.Scope == receipt.Scope && child.ID == receipt.ID {
						remove = append(remove, child)
					}
				}
			}
		case "ngrok":
			receipt, err = p.checkNgrokOwned(ctx, client, call, receipt)
			if err != nil {
				return result, err
			}
			remove = []tunnelResourceRef{receipt}
			if err = p.intent(ctx, call, "delete-domain", receipt.Scope, receipt.ID, receipt); err != nil {
				return result, err
			}
			if err = deleteNgrokDomain(ctx, client, receipt.ID); err != nil {
				return result, err
			}
		case "devtunnel":
			receipt, err = p.checkDevOwned(ctx, client, call, receipt)
			if err != nil {
				return result, err
			}
			remove = []tunnelResourceRef{receipt}
			if err = p.intent(ctx, call, "delete-tunnel", receipt.Scope, receipt.ID, receipt); err != nil {
				return result, err
			}
			if err = deleteDevTunnel(ctx, client, receipt.Scope, receipt.ID); err != nil {
				return result, err
			}
		default:
			return result, fmt.Errorf("unknown owned provider cleanup")
		}
		if err = p.checkpoint(ctx, call, nil, remove, true); err != nil {
			return result, err
		}
	}
	return result, nil
}

// Absence needs the recorded capability context. A different capability can
// rebind only after a positive exact-ID/fingerprint observation, persisted in
// this same journal before deletion. Provider JSON supplies no such authority.
func (p *tunnelProviderRuntime) bindProviderObservation(ctx context.Context, call tunnelEffectCall, client tunnelHTTP, receipt tunnelResourceRef, observation tunnelResourceObservation) (tunnelResourceRef, error) {
	if client.CredentialContext == "" || observation.CredentialContext != client.CredentialContext {
		return receipt, fmt.Errorf("provider observation is not from the pinned capability context")
	}
	if observation.Absent {
		if receipt.CredentialContext == "" || receipt.CredentialContext != client.CredentialContext || observation.CredentialContext != client.CredentialContext {
			return receipt, fmt.Errorf("provider absence is in an unproven credential context; retain receipt")
		}
		return receipt, nil
	}
	if observation.Resource != receipt {
		return receipt, fmt.Errorf("exact provider identity/fingerprint changed")
	}
	if receipt.CredentialContext == client.CredentialContext {
		return receipt, nil
	}
	rebound := receipt
	rebound.CredentialContext = client.CredentialContext
	if err := p.checkpoint(ctx, call, []tunnelResourceRef{rebound}, []tunnelResourceRef{receipt}, true); err != nil {
		return receipt, err
	}
	return rebound, nil
}

func (p *tunnelProviderRuntime) checkNgrokOwned(ctx context.Context, client tunnelHTTP, call tunnelEffectCall, receipt tunnelResourceRef) (tunnelResourceRef, error) {
	var r struct{ ID, Domain, Metadata string }
	code, err := client.request(ctx, http.MethodGet, "https://api.ngrok.com/reserved_domains/"+receipt.ID, map[string]string{"ngrok-version": "2"}, nil, &r)
	observation := tunnelResourceObservation{Resource: receipt, CredentialContext: client.CredentialContext}
	if code == 404 {
		observation.Absent = true
		return p.bindProviderObservation(ctx, call, client, receipt, observation)
	}
	if err != nil {
		return receipt, err
	}
	if r.ID != receipt.ID || r.Domain != call.Definition.Entry.Policy.Options.Domain || tunnelDigest([]byte(r.ID+"/"+r.Domain+"/"+r.Metadata)) != receipt.Fingerprint {
		return receipt, fmt.Errorf("ngrok domain ownership changed; cleanup refused")
	}
	return p.bindProviderObservation(ctx, call, client, receipt, observation)
}

func (p *tunnelProviderRuntime) checkDevOwned(ctx context.Context, client tunnelHTTP, call tunnelEffectCall, receipt tunnelResourceRef) (tunnelResourceRef, error) {
	var r struct {
		TunnelID  string `json:"tunnelId"`
		ClusterID string `json:"clusterId"`
		Ports     []struct {
			Port int `json:"portNumber"`
		} `json:"ports"`
	}
	code, err := client.request(ctx, http.MethodGet, "https://"+receipt.Scope+".rel.tunnels.api.visualstudio.com/tunnels/"+receipt.ID+"?api-version=2023-09-27-preview&includePorts=true", nil, nil, &r)
	observation := tunnelResourceObservation{Resource: receipt, CredentialContext: client.CredentialContext}
	if code == 404 {
		observation.Absent = true
		return p.bindProviderObservation(ctx, call, client, receipt, observation)
	}
	if err != nil {
		return receipt, err
	}
	port := 0
	for _, p := range r.Ports {
		port = p.Port
		break
	}
	if len(r.Ports) != 1 || r.TunnelID != receipt.ID || r.ClusterID != receipt.Scope || tunnelDigest([]byte(r.ClusterID+"/"+r.TunnelID+"/"+fmt.Sprint(port))) != receipt.Fingerprint {
		return receipt, fmt.Errorf("devtunnel identity/port changed; cleanup refused")
	}
	return p.bindProviderObservation(ctx, call, client, receipt, observation)
}
