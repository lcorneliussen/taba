package wtc

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

type tunnelKey struct {
	Repo    string `json:"repo"`
	Service string `json:"service"`
	Name    string `json:"name"`
}

var tunnelComponent = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

func (k tunnelKey) validate() error {
	for _, s := range []string{k.Repo, k.Service, k.Name} {
		if !tunnelComponent.MatchString(s) {
			return fmt.Errorf("invalid tunnel identity component")
		}
	}
	return nil
}

func (k tunnelKey) String() string { return k.Repo + "/" + k.Service + "/" + k.Name }

type tunnelProviderOptions struct {
	Hostname     string `toml:"hostname" json:"hostname,omitempty"`
	Registration string `toml:"registration" json:"registration,omitempty"`
	ZoneID       string `toml:"zone_id" json:"zone_id,omitempty"`
	Domain       string `toml:"domain" json:"domain,omitempty"`
	ListenerPort int    `toml:"listener_port" json:"listener_port,omitempty"`
	Path         string `toml:"path" json:"path,omitempty"`
}

type tunnelPolicyEntry struct {
	Enabled          *bool                 `toml:"enabled" json:"enabled,omitempty"`
	Provider         string                `toml:"provider" json:"provider"`
	Mode             string                `toml:"mode" json:"mode"`
	Access           string                `toml:"access" json:"access"`
	StartWithService bool                  `toml:"start_with_service" json:"start_with_service"`
	Options          tunnelProviderOptions `toml:"options" json:"options"`
}

func (e tunnelPolicyEntry) disabled() bool { return e.Enabled != nil && !*e.Enabled }

func (e tunnelPolicyEntry) validate() error {
	if e.disabled() {
		if e.Provider != "" || e.Mode != "" || e.Access != "" || e.StartWithService || e.Options != (tunnelProviderOptions{}) {
			return fmt.Errorf("a disabled tunnel must contain only enabled = false")
		}
		return nil
	}
	return validateTunnelProviderPolicy(e)
}

// Tunnel policy is receiving-local desired state, never generated runtime data
// or configuration selected through a credential root. Callers supply already
// resolved, authorized source paths and bytes; these helpers discover nothing.
type tunnelPolicy struct {
	Source  string
	Entries map[tunnelKey]tunnelPolicyEntry
}

func parseTunnelPolicy(source string, data []byte) (tunnelPolicy, error) {
	p := tunnelPolicy{Source: source, Entries: map[tunnelKey]tunnelPolicyEntry{}}
	if !filepath.IsAbs(source) {
		return p, fmt.Errorf("tunnel policy requires an explicit resolved source path")
	}
	var doc struct {
		Schema  int                                                `toml:"schema"`
		Tunnels map[string]map[string]map[string]tunnelPolicyEntry `toml:"tunnels"`
	}
	meta, err := toml.Decode(string(data), &doc)
	if err != nil {
		// Parser errors can quote input values. Do not echo credential literals
		// accidentally placed in a policy file into status or public diagnostics.
		return p, fmt.Errorf("invalid tunnel policy TOML")
	}
	if doc.Schema != 1 || len(meta.Undecoded()) != 0 {
		return p, fmt.Errorf("unsupported tunnel policy schema or field")
	}
	for repo, services := range doc.Tunnels {
		for service, entries := range services {
			for name, entry := range entries {
				key := tunnelKey{repo, service, name}
				if err := key.validate(); err != nil {
					return p, err
				}
				if err := entry.validate(); err != nil {
					return p, fmt.Errorf("invalid tunnel entry: %w", err)
				}
				if entry.disabled() {
					for _, field := range []string{"provider", "mode", "access", "start_with_service", "options"} {
						if meta.IsDefined("tunnels", repo, service, name, field) {
							return p, fmt.Errorf("a disabled tunnel must contain only enabled = false")
						}
					}
				}
				p.Entries[key] = entry
			}
		}
	}
	return p, nil
}

type tunnelEffectiveEntry struct {
	Key      tunnelKey         `json:"key"`
	Policy   tunnelPolicyEntry `json:"policy"`
	Source   string            `json:"source"`
	Scope    string            `json:"scope"`
	Matched  bool              `json:"matched"`
	Disabled bool              `json:"disabled"`
}

// A collection entry replaces the complete project entry. A tombstone remains
// visible for diagnostics; removing it restores inheritance. Matching a service
// never grants mutation authority and never provisions a provider resource.
func effectiveTunnelPolicy(project, collection tunnelPolicy, services map[string]bool) ([]tunnelEffectiveEntry, error) {
	merged := map[tunnelKey]tunnelEffectiveEntry{}
	for _, input := range []struct {
		policy tunnelPolicy
		scope  string
	}{{project, "project"}, {collection, "collection"}} {
		if len(input.policy.Entries) > 0 && !filepath.IsAbs(input.policy.Source) {
			return nil, fmt.Errorf("tunnel policy requires an explicit resolved source path")
		}
		for key, entry := range input.policy.Entries {
			if err := key.validate(); err != nil {
				return nil, err
			}
			if err := entry.validate(); err != nil {
				return nil, err
			}
			// Drop the mutable pointer; absence and explicit true are equivalent.
			disabled := entry.disabled()
			entry.Enabled = nil
			merged[key] = tunnelEffectiveEntry{key, entry, input.policy.Source, input.scope, services[key.Repo+"/"+key.Service], disabled}
		}
	}
	entries := make([]tunnelEffectiveEntry, 0, len(merged))
	for _, entry := range merged {
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool { return strings.Compare(entries[i].Key.String(), entries[j].Key.String()) < 0 })
	return entries, nil
}
