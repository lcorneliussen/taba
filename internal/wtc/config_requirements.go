package wtc

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"regexp"
	"strings"
	"unicode"
)

const requirementMaxBytes = 256 * 1024
const requirementMaxLine = 16 * 1024
const requirementMaxKeys = 1024

// RequirementError deliberately omits source text and decoder errors: inputs
// can contain credentials, including in malformed lines.
type RequirementError struct {
	Line int
	Code string
}

func (e *RequirementError) Error() string {
	return fmt.Sprintf("requirements line %d: %s", e.Line, e.Code)
}

type RequirementSource struct {
	Path   string `json:"path"`
	Scope  string `json:"scope"`
	Format string `json:"format"`
}
type RequirementSources struct {
	Files       []RequirementSource `json:"files"`
	EnvNames    []string            `json:"env_names,omitempty"`
	EnvPrefixes []string            `json:"env_prefixes,omitempty"`
}
type RequirementCondition struct {
	Key    string `json:"key"`
	Equals string `json:"equals"`
}
type ConfigRequirement struct {
	Name     string                `json:"name"`
	Kind     string                `json:"kind"`
	Purpose  string                `json:"purpose"`
	Affects  string                `json:"affects"`
	Required string                `json:"required"`
	Scope    string                `json:"scope"`
	Example  string                `json:"example"`
	Hint     string                `json:"hint"`
	Doc      string                `json:"doc"`
	When     *RequirementCondition `json:"when,omitempty"`
	Choices  []string              `json:"choices,omitempty"`
	// File is a relative path whose presence is reported. Required determines
	// whether absence is a failure; the dotenv reader never parses its contents.
	File string `json:"file,omitempty"`
}

// RequirementSchema contains declarations only. Example assignments never
// supply values to the effective configuration.
type RequirementSchema struct {
	Sources      RequirementSources  `json:"sources"`
	Requirements []ConfigRequirement `json:"requirements"`
	placeholders map[string]string
}

var requirementName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)
var requirementRelativePath = regexp.MustCompile(`^[A-Za-z0-9_./-]+$`)
var requirementField = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

func requirementPath(p string) bool {
	return len(p) <= 240 && requirementRelativePath.MatchString(p) && !strings.HasPrefix(p, "/") && path.Clean(p) == p && p != ".." && !strings.HasPrefix(p, "../") && p != "."
}
func requirementDotenv(p string) bool {
	b := path.Base(p)
	return b == ".env" || strings.HasPrefix(b, ".env.") || strings.HasSuffix(b, ".env")
}

// decodeRequirementJSON rejects unknown and duplicate fields at every depth.
// Returning a boolean prevents JSON's potentially value-bearing errors from
// crossing the inventory boundary.
func decodeRequirementJSON(s string, out any) bool {
	d := json.NewDecoder(strings.NewReader(s))
	if !requirementJSONValue(d, 0) {
		return false
	}
	if _, err := d.Token(); err != io.EOF {
		return false
	}
	d = json.NewDecoder(strings.NewReader(s))
	d.DisallowUnknownFields()
	if d.Decode(out) != nil {
		return false
	}
	return d.Decode(new(any)) == io.EOF
}
func requirementJSONValue(d *json.Decoder, depth int) bool {
	if depth > 8 {
		return false
	}
	t, err := d.Token()
	if err != nil {
		return false
	}
	delim, ok := t.(json.Delim)
	if !ok {
		return true
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			k, e := d.Token()
			if e != nil {
				return false
			}
			key, ok := k.(string)
			if !ok || !requirementField.MatchString(key) || seen[key] {
				return false
			}
			seen[key] = true
			if !requirementJSONValue(d, depth+1) {
				return false
			}
		}
		t, err = d.Token()
		return err == nil && t == json.Delim('}')
	case '[':
		for d.More() {
			if !requirementJSONValue(d, depth+1) {
				return false
			}
		}
		t, err = d.Token()
		return err == nil && t == json.Delim(']')
	}
	return false
}

// ParseConfigRequirements parses an explicitly selected canonical dotenv
// example. It performs no filesystem, environment or shell operations.
func ParseConfigRequirements(filename string, r io.Reader) (RequirementSchema, error) {
	var result RequirementSchema
	result.placeholders = map[string]string{}
	fail := func(line int, code string) (RequirementSchema, error) {
		return RequirementSchema{}, &RequirementError{line, code}
	}
	if !strings.HasSuffix(filename, ".example") || !requirementPath(filename) || !requirementDotenv(strings.TrimSuffix(filename, ".example")) {
		return fail(0, "unsupported-example-format")
	}
	data, err := io.ReadAll(io.LimitReader(r, requirementMaxBytes+1))
	if err != nil {
		return fail(0, "unreadable-example")
	}
	if len(data) > requirementMaxBytes {
		return fail(0, "example-too-large")
	}
	scan := bufio.NewScanner(bytes.NewReader(data))
	scan.Buffer(make([]byte, 1024), requirementMaxLine)
	header, sources := false, false
	var pending *ConfigRequirement
	names := map[string]bool{}
	line := 0
	for scan.Scan() {
		line++
		s := strings.TrimSpace(scan.Text())
		if s == "" {
			continue
		}
		if s == "# taba:requirements v1" {
			if header || sources || len(names) > 0 {
				return fail(line, "duplicate-or-misplaced-header")
			}
			header = true
			continue
		}
		if strings.HasPrefix(s, "# taba:") {
			if !header {
				return fail(line, "missing-version-header")
			}
			if strings.HasPrefix(s, "# taba:source ") {
				if sources || pending != nil || len(names) > 0 || !decodeRequirementJSON(strings.TrimPrefix(s, "# taba:source "), &result.Sources) {
					return fail(line, "invalid-source-declaration")
				}
				sources = true
				continue
			}
			var req ConfigRequirement
			if strings.HasPrefix(s, "# taba:require ") {
				if !sources || pending != nil || !decodeRequirementJSON(strings.TrimPrefix(s, "# taba:require "), &req) || req.Name != "" || req.File != "" {
					return fail(line, "invalid-key-declaration")
				}
				pending = &req
				continue
			}
			if strings.HasPrefix(s, "# taba:file ") {
				if !sources || pending != nil || !decodeRequirementJSON(strings.TrimPrefix(s, "# taba:file "), &req) || !requirementPath(req.File) || req.Name != "" {
					return fail(line, "invalid-file-declaration")
				}
				req.Name = req.File
			} else {
				return fail(line, "unknown-annotation")
			}
			if names["file:"+req.Name] || !validConfigRequirement(req) {
				return fail(line, "contradictory-declaration")
			}
			names["file:"+req.Name] = true
			result.Requirements = append(result.Requirements, req)
		} else if strings.HasPrefix(s, "#") {
			continue
		} else {
			key, example, ok := requirementAssignment(s)
			if !ok {
				return fail(line, "malformed-example-assignment")
			}
			if !header || !sources || pending == nil {
				return fail(line, "undeclared-example-key")
			}
			req := *pending
			pending = nil
			req.Name = key
			if req.Example == "placeholder" && example != "" {
				result.placeholders[key] = example
			}
			if names["key:"+key] || !validConfigRequirement(req) {
				return fail(line, "contradictory-declaration")
			}
			names["key:"+key] = true
			result.Requirements = append(result.Requirements, req)
		}
		if len(names) > requirementMaxKeys {
			return fail(line, "too-many-requirements")
		}
	}
	if scan.Err() != nil {
		return fail(line+1, "example-line-too-large")
	}
	if !header || !sources || pending != nil || len(names) == 0 {
		return fail(line, "incomplete-schema")
	}
	if !validRequirementSources(result.Sources, strings.TrimSuffix(filename, ".example")) {
		return fail(0, "invalid-source-mapping")
	}
	// Conditions may depend only on unconditional declared configuration, so
	// unresolved selectors cannot be hidden by dependency cycles or secrets.
	byName := map[string]ConfigRequirement{}
	for _, r := range result.Requirements {
		if r.File == "" {
			byName[r.Name] = r
		}
	}
	for _, req := range result.Requirements {
		if req.When != nil {
			selector, ok := byName[req.When.Key]
			if !ok || selector.Kind != "config" || selector.File != "" || selector.When != nil || selector.Example == "placeholder" || !requirementChoice(selector, req.When.Equals) {
				return fail(0, "invalid-condition-reference")
			}
		}
	}
	return result, nil
}
func validConfigRequirement(r ConfigRequirement) bool {
	if len(r.Choices) > 32 || len(r.Choices) > 0 && (r.Kind != "config" || r.File != "") {
		return false
	}
	seen := map[string]bool{}
	for _, choice := range r.Choices {
		if !requirementText(choice, 128) || seen[choice] {
			return false
		}
		seen[choice] = true
	}
	if r.Kind != "config" && r.Kind != "secret" {
		return false
	}
	if r.Required != "required" && r.Required != "optional" && r.Required != "conditional" {
		return false
	}
	if r.Scope != "project" && r.Scope != "collection" {
		return false
	}
	if r.Example != "placeholder" && r.Example != "nonsecret-default" {
		return false
	}
	if r.Kind == "secret" && r.Example != "placeholder" {
		return false
	}
	for _, s := range []string{r.Purpose, r.Affects, r.Hint, r.Doc} {
		if !requirementText(s, 512) {
			return false
		}
	}
	if (r.Required == "conditional") != (r.When != nil) {
		return false
	}
	return r.When == nil || requirementName.MatchString(r.When.Key) && requirementText(r.When.Equals, 128)
}

func requirementText(s string, limit int) bool {
	return s != "" && len(s) <= limit && strings.IndexFunc(s, unicode.IsControl) < 0
}

func requirementChoice(r ConfigRequirement, value string) bool {
	for _, choice := range r.Choices {
		if value == choice {
			return true
		}
	}
	return false
}
func validRequirementSources(s RequirementSources, canonical string) bool {
	if len(s.Files) == 0 || len(s.Files) > 16 || len(s.EnvNames) > requirementMaxKeys || len(s.EnvPrefixes) > 16 {
		return false
	}
	seen := map[string]bool{}
	canonicalFound := false
	for _, f := range s.Files {
		if !requirementPath(f.Path) || !requirementDotenv(f.Path) || strings.HasSuffix(f.Path, ".example") || f.Format != "dotenv" || f.Scope != "project" && f.Scope != "collection" {
			return false
		}
		k := f.Scope + ":" + f.Path
		if seen[k] {
			return false
		}
		seen[k] = true
		if f.Path == canonical {
			canonicalFound = true
		}
	}
	for _, n := range append(append([]string{}, s.EnvNames...), s.EnvPrefixes...) {
		if !requirementName.MatchString(n) || seen["env:"+n] {
			return false
		}
		seen["env:"+n] = true
	}
	return canonicalFound
}

// requirementAssignment accepts literal dotenv assignments only. Quoting
// removes one matching pair; shell escapes and substitutions remain data.
func requirementAssignment(s string) (string, string, bool) {
	key, value, ok := strings.Cut(s, "=")
	key = strings.TrimSpace(key)
	value = strings.TrimSpace(value)
	if !ok || !requirementName.MatchString(key) || strings.ContainsRune(value, 0) {
		return "", "", false
	}
	if strings.HasPrefix(value, "\"") || strings.HasPrefix(value, "'") {
		q := value[0]
		if len(value) < 2 || value[len(value)-1] != q {
			return "", "", false
		}
		value = value[1 : len(value)-1]
	}
	return key, value, true
}
