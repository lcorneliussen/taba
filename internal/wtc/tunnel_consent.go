package wtc

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

type tunnelDefinition struct {
	Owner         string               `json:"owner"`
	Entry         tunnelEffectiveEntry `json:"entry"`
	Target        string               `json:"target"`
	ProviderScope string               `json:"provider_scope"` // non-secret account/zone/cluster binding
	HooksDigest   string               `json:"hooks_digest"`
	ReloadDigest  string               `json:"reload_digest"`
}

func tunnelDigest(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func validTunnelDigest(s string) bool {
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == sha256.Size && hex.EncodeToString(b) == s
}

func (d tunnelDefinition) digest() (string, error) {
	if !tunnelComponent.MatchString(d.Owner) || d.ProviderScope == "" || len(d.ProviderScope) > 512 || strings.ContainsAny(d.ProviderScope, "\r\n\x00") || !d.Entry.Matched || d.Entry.Disabled || d.Entry.Policy.disabled() {
		return "", fmt.Errorf("tunnel definition needs owner, provider scope and a matched enabled entry")
	}
	if !filepath.IsAbs(d.Entry.Source) || (d.Entry.Scope != "project" && d.Entry.Scope != "collection") {
		return "", fmt.Errorf("tunnel definition needs explicit receiving policy provenance")
	}
	if err := d.Entry.Key.validate(); err != nil {
		return "", err
	}
	if err := d.Entry.Policy.validate(); err != nil {
		return "", err
	}
	if _, err := parseTunnelAddress(d.Target); err != nil {
		return "", fmt.Errorf("invalid tunnel target URL")
	}
	for _, s := range []string{d.HooksDigest, d.ReloadDigest} {
		if s != "" && !validTunnelDigest(s) {
			return "", fmt.Errorf("invalid tunnel hook or reload digest")
		}
	}
	// Canonicalize enabled=true and absence to the same effective definition.
	d.Entry.Policy.Enabled = nil
	data, err := json.Marshal(d)
	if err != nil {
		return "", err
	}
	return tunnelDigest(data), nil
}

type tunnelConsent struct {
	Definition string `json:"definition"`
	// Public records approval of potentially public exposure, not an access
	// observation. Actual exposure and its evidence remain separate.
	Public bool `json:"public"`
}

// Access observations must be current and bound to this effective definition.
// Neither a policy declaration nor the provider's name is verification.
type tunnelAccessEvidence struct {
	Definition string
	Exposure   string
	Verified   bool
}

func (e tunnelAccessEvidence) private(digest string, desired string) bool {
	return e.Verified && e.Definition == digest && e.Exposure == desired && (e.Exposure == "tailnet" || e.Exposure == "account")
}

func newTunnelConsent(d tunnelDefinition, explicitPublic bool, evidence tunnelAccessEvidence) (tunnelConsent, error) {
	digest, err := d.digest()
	if err != nil {
		return tunnelConsent{}, err
	}
	// Without verified private access for this definition, every mode needs
	// approval for potentially public exposure, even a declared private mode.
	if !explicitPublic && !evidence.private(digest, d.Entry.Policy.Access) {
		return tunnelConsent{}, fmt.Errorf("explicit approval for potentially public exposure is required without verified private access")
	}
	return tunnelConsent{digest, explicitPublic}, nil
}

func (c tunnelConsent) permits(d tunnelDefinition, evidence tunnelAccessEvidence) bool {
	digest, err := d.digest()
	return err == nil && validTunnelDigest(c.Definition) && c.Definition == digest &&
		(c.Public || evidence.private(digest, d.Entry.Policy.Access))
}
