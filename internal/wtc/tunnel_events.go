package wtc

import (
	"fmt"
	"reflect"
	"sort"
)

type tunnelEventEndpoint struct {
	Key     tunnelKey     `json:"key"`
	Address tunnelAddress `json:"address"`
}

type tunnelEventSnapshot struct {
	Revision  string                `json:"revision"`
	Hosts     []string              `json:"hosts"`
	Endpoints []tunnelEventEndpoint `json:"endpoints"`
}

// Event IDs are stable for a persisted operation, not a promise that arbitrary
// callbacks run exactly once. A lost callback response requires reconciliation.
// Payloads contain addresses and identity, never provider credentials or argv.
type tunnelEvent struct {
	Schema      int                 `json:"schema"`
	ID          string              `json:"id"`
	OperationID string              `json:"operation_id"`
	Type        string              `json:"type"`
	Key         tunnelKey           `json:"key"`
	Definition  string              `json:"definition"`
	Previous    *tunnelAddress      `json:"previous"`
	Current     *tunnelAddress      `json:"current"`
	Before      tunnelEventSnapshot `json:"before"`
	After       tunnelEventSnapshot `json:"after"`
}

func tunnelSnapshotForEvent(active []tunnelActiveAddress) (tunnelEventSnapshot, error) {
	snapshot, err := buildTunnelHostSnapshot(active)
	if err != nil {
		return tunnelEventSnapshot{}, err
	}
	r := tunnelEventSnapshot{Revision: snapshot.Revision, Hosts: []string{}, Endpoints: []tunnelEventEndpoint{}}
	hosts := map[string]bool{}
	for _, a := range active {
		address, _ := parseTunnelAddress(a.URL)
		r.Endpoints = append(r.Endpoints, tunnelEventEndpoint{a.Key, address})
		hosts[address.Hostname] = true
	}
	for host := range hosts {
		r.Hosts = append(r.Hosts, host)
	}
	sort.Strings(r.Hosts)
	sort.Slice(r.Endpoints, func(i, j int) bool { return r.Endpoints[i].Key.String() < r.Endpoints[j].Key.String() })
	return r, nil
}

func (s tunnelEventSnapshot) validate() error {
	active := make([]tunnelActiveAddress, 0, len(s.Endpoints))
	for _, e := range s.Endpoints {
		active = append(active, tunnelActiveAddress{e.Key, e.Address.URL})
	}
	expected, err := tunnelSnapshotForEvent(active)
	if err != nil || !reflect.DeepEqual(expected, s) {
		return fmt.Errorf("invalid tunnel event snapshot")
	}
	return nil
}

func (e tunnelEvent) validate() error {
	if e.Schema != 1 || !tunnelComponent.MatchString(e.OperationID) || !validTunnelDigest(e.Definition) || e.Key.validate() != nil {
		return fmt.Errorf("invalid tunnel event identity")
	}
	switch e.Type {
	case "registered", "started", "stopped", "unregistered":
	default:
		return fmt.Errorf("invalid tunnel event type")
	}
	if e.ID != tunnelDigest([]byte(e.OperationID+"/"+e.Key.String()+"/"+e.Type)) {
		return fmt.Errorf("invalid tunnel event id")
	}
	for _, a := range []*tunnelAddress{e.Previous, e.Current} {
		if a != nil {
			parsed, err := parseTunnelAddress(a.URL)
			if err != nil || parsed != *a {
				return fmt.Errorf("invalid tunnel event address")
			}
		}
	}
	if err := e.Before.validate(); err != nil {
		return err
	}
	return e.After.validate()
}

func tunnelStateActive(s tunnelState) []tunnelActiveAddress {
	active := []tunnelActiveAddress{}
	for _, e := range s.Entries {
		if e.Active != nil {
			active = append(active, tunnelActiveAddress{e.Key, e.Active.URL})
		}
	}
	return active
}
