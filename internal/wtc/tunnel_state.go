package wtc

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// The eventual locked writer persists this one state before each external
// effect. These pure codecs do not discover paths, run hooks or call providers.
// A pending effect after interruption is uncertain, not permission to replay.
type tunnelState struct {
	Schema  int                `json:"schema"`
	Owner   string             `json:"owner"`
	Entries []tunnelEntryState `json:"entries"`
}

type tunnelEntryState struct {
	Key        tunnelKey           `json:"key"`
	Provider   string              `json:"provider"`
	Mode       string              `json:"mode"`
	Definition string              `json:"definition"`
	Consent    *tunnelConsent      `json:"consent,omitempty"`
	Registered bool                `json:"registered"`
	Known      *tunnelAddress      `json:"known,omitempty"`
	Active     *tunnelAddress      `json:"active,omitempty"`
	Resources  []tunnelResourceRef `json:"resources"`
	Operation  *tunnelOperation    `json:"operation,omitempty"`
}

type tunnelOperation struct {
	ID         string                    `json:"id"`
	Action     string                    `json:"action"`
	Definition string                    `json:"definition"`
	Steps      []tunnelEffectStep        `json:"steps"`
	Lifecycle  *tunnelLifecycleOperation `json:"lifecycle,omitempty"`
}

type tunnelEffectStep struct {
	Name   string `json:"name"`
	Status string `json:"status"` // pending, needs-reconcile, complete
}

// nextStep refuses an ambiguous retry. The caller must inspect/reconcile using
// this same operation ID before recording completion or a safe next step.
func (op tunnelOperation) nextStep(name string) (bool, error) {
	if !tunnelComponent.MatchString(name) {
		return false, fmt.Errorf("invalid tunnel effect step")
	}
	for _, step := range op.Steps {
		if step.Status != "complete" {
			return false, fmt.Errorf("tunnel operation needs reconciliation")
		}
		if step.Name == name {
			return false, nil
		}
	}
	return true, nil
}

type tunnelResourceRef struct {
	Provider    string `json:"provider"`
	Scope       string `json:"scope"` // non-secret provider account/zone/cluster
	Kind        string `json:"kind"`
	ID          string `json:"id"`
	Ownership   string `json:"ownership"` // owned, borrowed, or unverified (never deletion authority)
	Owner       string `json:"owner,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
	Lifetime    string `json:"lifetime,omitempty"` // registration or session; absent legacy receipts are retained conservatively
}

func (r tunnelResourceRef) key() string {
	b, _ := json.Marshal([4]string{r.Provider, r.Scope, r.Kind, r.ID})
	return string(b)
}

func (r tunnelResourceRef) validate(owner string) error {
	if r.Lifetime != "" && r.Lifetime != "registration" && r.Lifetime != "session" {
		return fmt.Errorf("unknown tunnel resource lifetime")
	}
	for _, s := range []string{r.Provider, r.Scope, r.Kind, r.ID} {
		if s == "" || len(s) > 512 || strings.ContainsAny(s, "\r\n\x00") {
			return fmt.Errorf("invalid tunnel resource identity")
		}
	}
	if r.Ownership != "owned" && r.Ownership != "borrowed" && r.Ownership != "unverified" {
		return fmt.Errorf("unknown tunnel resource ownership")
	}
	if r.Ownership == "unverified" && (r.Owner != "" || r.Fingerprint != "") {
		return fmt.Errorf("unverified tunnel identity cannot carry ownership evidence")
	}
	if r.Ownership == "owned" && (r.Owner != owner || !validTunnelDigest(r.Fingerprint)) {
		return fmt.Errorf("tunnel resource lacks exact ownership receipt")
	}
	return nil
}

func (s tunnelState) validate(owner string) error {
	if s.Schema != 1 || !tunnelComponent.MatchString(owner) || s.Owner != owner {
		return fmt.Errorf("unsupported tunnel state or receiving owner mismatch")
	}
	keys, resources := map[tunnelKey]bool{}, map[string]bool{}
	for _, entry := range s.Entries {
		if err := entry.Key.validate(); err != nil {
			return err
		}
		if keys[entry.Key] || !validTunnelDigest(entry.Definition) {
			return fmt.Errorf("duplicate or invalid tunnel state entry")
		}
		keys[entry.Key] = true
		contract, err := tunnelContract(entry.Provider, entry.Mode)
		if err != nil {
			return err
		}
		if entry.Registered && contract.Registration == "none" {
			return fmt.Errorf("ephemeral tunnel has no persistent registration")
		}
		if entry.Consent != nil && !validTunnelDigest(entry.Consent.Definition) {
			return fmt.Errorf("invalid tunnel consent receipt")
		}
		for _, address := range []*tunnelAddress{entry.Active, entry.Known} {
			if address != nil {
				a, err := parseTunnelAddress(address.URL)
				if err != nil || a != *address {
					return fmt.Errorf("invalid tunnel state address")
				}
			}
		}
		for _, resource := range entry.Resources {
			if err := resource.validate(owner); err != nil {
				return err
			}
			if resources[resource.key()] {
				return fmt.Errorf("duplicate tunnel resource receipt")
			}
			resources[resource.key()] = true
		}
		if op := entry.Operation; op != nil {
			if op.Lifecycle != nil {
				if err := op.Lifecycle.validate(s.Owner, entry, *op); err != nil {
					return err
				}
			}
			if !tunnelComponent.MatchString(op.ID) || !validTunnelDigest(op.Definition) {
				return fmt.Errorf("invalid tunnel operation identity")
			}
			switch op.Action {
			case "register", "start", "stop", "unregister":
			default:
				return fmt.Errorf("unknown tunnel operation action")
			}
			steps, incomplete := map[string]bool{}, false
			for _, step := range op.Steps {
				if !tunnelComponent.MatchString(step.Name) || steps[step.Name] || incomplete {
					return fmt.Errorf("invalid tunnel operation step sequence")
				}
				steps[step.Name] = true
				switch step.Status {
				case "complete":
				case "pending", "needs-reconcile":
					incomplete = true
				default:
					return fmt.Errorf("unknown tunnel effect status")
				}
			}
		}
	}
	return nil
}

func (e tunnelEntryState) needsCleanup() bool {
	if e.Registered || e.Active != nil {
		return true
	}
	for _, r := range e.Resources {
		if r.Ownership == "owned" || r.Ownership == "unverified" {
			return true
		}
	}
	if e.Operation != nil {
		if e.Operation.Lifecycle != nil {
			return !lifecycleComplete(e.Operation)
		}
		if len(e.Operation.Steps) == 0 {
			return true
		}
		for _, step := range e.Operation.Steps {
			if step.Status != "complete" {
				return true
			}
		}
	}
	return false
}

const tunnelStateLimit = 4 << 20

func decodeTunnelState(data []byte, owner string) (tunnelState, error) {
	var state tunnelState
	if len(data) > tunnelStateLimit || !json.Valid(data) || !tunnelUniqueJSONKeys(json.NewDecoder(bytes.NewReader(data))) {
		return state, fmt.Errorf("invalid or oversized tunnel state")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&state); err != nil {
		return tunnelState{}, fmt.Errorf("invalid tunnel state JSON")
	}
	if err := state.validate(owner); err != nil {
		return tunnelState{}, err
	}
	return state, nil
}

// Reject duplicate JSON members before decoding: otherwise an overwritten
// resources/owner field could hide cleanup obligations in a damaged state.
func tunnelUniqueJSONKeys(d *json.Decoder) bool {
	var value func() bool
	value = func() bool {
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
					// State tags are lowercase ASCII. encoding/json's Unicode
					// folding would otherwise also accept aliases such as ſ for s.
					if err != nil || !ok || strings.ToLower(name) != name || seen[name] {
						return false
					}
					for _, c := range name {
						if c > 127 {
							return false
						}
					}
					seen[name] = true
				}
				if !value() {
					return false
				}
			}
			_, err = d.Token()
			return err == nil
		}
		return true
	}
	if !value() {
		return false
	}
	_, err := d.Token()
	return err == io.EOF
}

func encodeTunnelState(s tunnelState, owner string) ([]byte, error) {
	if err := s.validate(owner); err != nil {
		return nil, err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return nil, err
	}
	if len(data)+1 > tunnelStateLimit {
		return nil, fmt.Errorf("tunnel state is too large")
	}
	return append(data, '\n'), nil
}

type tunnelResourceObservation struct {
	Resource tunnelResourceRef
	Absent   bool // exact-ID not-found, never a failed query
}

// Only an exact receipt and a current exact-ID observation permit deletion.
// A borrowed or externally modified resource is never a deletion target.
func tunnelCleanupTargets(owner string, receipts []tunnelResourceRef, observations []tunnelResourceObservation) ([]tunnelResourceRef, error) {
	observed := map[string]tunnelResourceObservation{}
	for _, observation := range observations {
		key := observation.Resource.key()
		if _, exists := observed[key]; exists {
			return nil, fmt.Errorf("duplicate tunnel resource observation")
		}
		observed[key] = observation
	}
	var targets []tunnelResourceRef
	seen := map[string]bool{}
	for _, receipt := range receipts {
		if err := receipt.validate(owner); err != nil {
			return nil, err
		}
		if seen[receipt.key()] {
			return nil, fmt.Errorf("duplicate tunnel resource receipt")
		}
		seen[receipt.key()] = true
		if receipt.Ownership == "unverified" {
			return nil, fmt.Errorf("unverified tunnel identity needs reconciliation")
		}
		if receipt.Ownership != "owned" {
			continue
		}
		observation, ok := observed[receipt.key()]
		if !ok {
			return nil, fmt.Errorf("owned tunnel resource needs reconciliation")
		}
		if observation.Absent {
			continue
		}
		if observation.Resource != receipt {
			return nil, fmt.Errorf("owned tunnel resource changed; cleanup refused")
		}
		targets = append(targets, receipt)
	}
	return targets, nil
}
