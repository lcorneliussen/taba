package wtc

import (
	"bufio"
	"bytes"
	"io"
	"sort"
	"strings"
)

// RequirementInput is an already authorized source snapshot. The filesystem
// adapter must establish ownership before opening it. Values never enter the
// returned inventory; callers must not log this input.
type RequirementInput struct {
	Source RequirementSource
	Values map[string]string
	State  string // inspected, missing, or unverified
}
type RequirementItem struct {
	Name          string                `json:"name"`
	Kind          string                `json:"kind"`
	Purpose       string                `json:"purpose,omitempty"`
	Affects       string                `json:"affects,omitempty"`
	Scope         string                `json:"scope,omitempty"`
	Source        string                `json:"source,omitempty"`
	Applicability string                `json:"applicability"`
	State         string                `json:"state"`
	Required      string                `json:"required,omitempty"`
	Hint          string                `json:"hint,omitempty"`
	Doc           string                `json:"doc,omitempty"`
	Condition     *RequirementCondition `json:"condition,omitempty"`
}

// ParseRequirementInput is shared by local diagnosis and CI lint. A malformed
// input is never partly accepted. Errors disclose only a line and fixed code.
func ParseRequirementInput(r io.Reader) (map[string]string, error) {
	data, err := io.ReadAll(io.LimitReader(r, requirementMaxBytes+1))
	if err != nil {
		return nil, &RequirementError{0, "unreadable-input"}
	}
	if len(data) > requirementMaxBytes {
		return nil, &RequirementError{0, "input-too-large"}
	}
	result := map[string]string{}
	scan := bufio.NewScanner(bytes.NewReader(data))
	scan.Buffer(make([]byte, 1024), requirementMaxLine)
	line := 0
	for scan.Scan() {
		line++
		s := strings.TrimSpace(scan.Text())
		if s == "" || strings.HasPrefix(s, "#") {
			continue
		}
		k, v, ok := requirementAssignment(s)
		if !ok {
			return nil, &RequirementError{line, "malformed-input"}
		}
		if _, exists := result[k]; exists {
			return nil, &RequirementError{line, "duplicate-input-key"}
		}
		result[k] = v
		if len(result) > requirementMaxKeys {
			return nil, &RequirementError{line, "too-many-inputs"}
		}
	}
	if scan.Err() != nil {
		return nil, &RequirementError{line + 1, "input-line-too-large"}
	}
	return result, nil
}

type requirementValue struct{ value, source, scope, state string }

// ConfigRequirementsInventory evaluates ordered authorized snapshots. Files
// follow the canonical source order (last wins, even when empty); only owned
// process names participate. env must be supplied by an explicit opt-in
// adapter. A nil map means the process source was not inspected.
// fileStates contains presence-only observations for declared file paths.
func ConfigRequirementsInventory(schema RequirementSchema, inputs []RequirementInput, env map[string]string, fileStates map[string]string) []RequirementItem {
	effective := map[string]requirementValue{}
	declared := map[string]ConfigRequirement{}
	for _, req := range schema.Requirements {
		declared[req.Name] = req
	}
	unknown := map[string]RequirementItem{}
	unresolved := false
	for _, source := range schema.Sources.Files {
		var input *RequirementInput
		for i := range inputs {
			if inputs[i].Source == source {
				if input != nil {
					input = nil
					break
				}
				input = &inputs[i]
			}
		}
		if input == nil || input.State != "missing" && input.State != "inspected" {
			unresolved = true
			for k, v := range effective {
				v.state = "unverified"
				effective[k] = v
			}
			continue
		}
		if input.State == "missing" {
			continue
		}
		for key, value := range input.Values {
			if !requirementName.MatchString(key) {
				unresolved = true
				continue
			}
			effective[key] = requirementValue{value, source.Path, source.Scope, "present"}
			if _, ok := declared[key]; !ok {
				unknown[key] = RequirementItem{Name: key, Source: source.Path, Scope: source.Scope, Applicability: "unknown", State: "undocumented"}
			}
		}
	}
	owns := func(key string) bool {
		for _, n := range schema.Sources.EnvNames {
			if key == n {
				return true
			}
		}
		for _, p := range schema.Sources.EnvPrefixes {
			if strings.HasPrefix(key, p) {
				return true
			}
		}
		return false
	}
	if len(schema.Sources.EnvNames)+len(schema.Sources.EnvPrefixes) > 0 {
		if env == nil {
			unresolved = true
			for k, v := range effective {
				if owns(k) {
					v.state = "unverified"
					effective[k] = v
				}
			}
		} else {
			for k, v := range env {
				if !owns(k) || !requirementName.MatchString(k) {
					continue
				}
				effective[k] = requirementValue{v, "process", "collection", "present"}
				if _, ok := declared[k]; !ok {
					unknown[k] = RequirementItem{Name: k, Source: "process", Scope: "collection", Applicability: "unknown", State: "undocumented"}
				}
			}
		}
	}
	result := make([]RequirementItem, 0, len(declared)+len(unknown))
	for _, r := range schema.Requirements {
		item := RequirementItem{Name: r.Name, Kind: r.Kind, Purpose: r.Purpose, Affects: r.Affects, Scope: r.Scope, Required: r.Required, Hint: r.Hint, Doc: r.Doc, Condition: r.When, Applicability: "applicable"}
		if r.When != nil {
			v, ok := effective[r.When.Key]
			if !ok || v.state == "unverified" || !requirementChoice(declared[r.When.Key], v.value) {
				item.Applicability = "unknown"
			} else if v.value != r.When.Equals {
				item.Applicability = "inapplicable"
			}
		}
		v, exists := effective[r.Name]
		if r.File != "" {
			item.State = fileStates[r.File]
			if item.State != "present" && item.State != "missing" {
				item.State = "unverified"
			}
			item.Source = r.File
		} else if exists {
			item.Source = v.source
			item.Scope = v.scope
			item.State = v.state
			if item.State == "present" {
				if v.value == "" {
					item.State = "empty"
				} else if p, ok := schema.placeholders[r.Name]; ok && p == v.value {
					item.State = "placeholder"
				} else if len(r.Choices) > 0 && !requirementChoice(r, v.value) {
					item.State = "malformed"
				}
			}
		} else if unresolved {
			item.State = "unverified"
		} else {
			item.State = "missing"
		}
		if item.Applicability == "inapplicable" {
			item.State = "not-required"
		} else if item.Applicability == "unknown" {
			item.State = "unverified"
		}
		result = append(result, item)
	}
	for _, item := range unknown {
		result = append(result, item)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}
