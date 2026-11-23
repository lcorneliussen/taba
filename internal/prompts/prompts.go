// Package prompts resolves Taba-authored instructions sent to agents. Each
// prompt is one Markdown file with frontmatter, identified by a stable ID
// derived from its path ("launch.handoff" is launch/handoff.md). A project
// override at <harness>/prompts/<path> wins over the embedded default.
package prompts

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"taba.sh/taba/internal/branding"
)

type Source string

const (
	SourceProject  Source = "project"
	SourceEmbedded Source = "embedded"
)

type Audience string

const (
	Private Audience = "private"
	Public  Audience = "public"
)

// Context selects where prompts resolve from. An empty Harness means no
// project layer. A Public audience never reads or applies project overrides
// (#221); it only notes that one exists.
type Context struct {
	Harness  string
	Defaults fs.FS
	Audience Audience
}

type Definition struct {
	ID       string   `json:"id"`
	Path     string   `json:"path"`
	Version  int      `json:"version"`
	Inputs   []string `json:"inputs"`
	Audience Audience `json:"audience"`
	Body     string   `json:"-"`
}

type Skip struct {
	File   string `json:"file"`
	Reason string `json:"reason"`
}

// Provenance explains which text was used. Layers are tried in order and the
// passed-over ones are listed in Skipped, so a later reject or strict layer
// reports through the same shape.
type Provenance struct {
	ID              string   `json:"id"`
	Path            string   `json:"path"`
	Source          Source   `json:"source"`
	File            string   `json:"file"`
	Audience        Audience `json:"audience"`
	Inputs          []string `json:"inputs"`
	EmbeddedVersion int      `json:"embedded_version"`
	BaseVersion     int      `json:"base_version,omitempty"`
	BaseFile        string   `json:"base_file,omitempty"`
	BaseCurrent     bool     `json:"base_current,omitempty"`
	Skipped         []Skip   `json:"skipped,omitempty"`
}

// Text is rendered prompt content. Agent dispatch points accept Text rather
// than string, so assembled strings cannot reach them; outside this package
// only Render produces a non-empty Text.
type Text struct {
	s    string
	prov Provenance
}

func (t Text) String() string { return t.s }

func (t Text) Provenance() Provenance { return t.prov }

// Resolution is the effective template for an ID before rendering.
type Resolution struct {
	Provenance Provenance `json:"provenance"`
	Body       string     `json:"body"`
}

const BaseDir = ".base"

func PathFor(id string) string {
	return strings.ReplaceAll(id, ".", "/") + ".md"
}

func IDFor(p string) string {
	return strings.ReplaceAll(strings.TrimSuffix(p, ".md"), "/", ".")
}

var validID = regexp.MustCompile(`^[a-z0-9-]+(\.[a-z0-9-]+)+$`)

func Lookup(ctx Context, id string) (Definition, error) {
	if !validID.MatchString(id) {
		return Definition{}, fmt.Errorf("invalid prompt id %q", id)
	}
	if ctx.Defaults == nil {
		return Definition{}, fmt.Errorf("prompt %s: no embedded defaults", id)
	}
	raw, err := fs.ReadFile(ctx.Defaults, PathFor(id))
	if err != nil {
		return Definition{}, fmt.Errorf("unknown prompt %q", id)
	}
	def, err := Parse(PathFor(id), raw)
	if err != nil {
		return Definition{}, err
	}
	if def.ID != id {
		return Definition{}, fmt.Errorf("embedded prompt %s declares id %q", def.Path, def.ID)
	}
	if def.Version < 1 {
		return Definition{}, fmt.Errorf("embedded prompt %s has no version", id)
	}
	if def.Audience != Private && def.Audience != Public {
		return Definition{}, fmt.Errorf("embedded prompt %s has audience %q", id, def.Audience)
	}
	return def, nil
}

func Resolve(ctx Context, id string) (Resolution, error) {
	def, err := Lookup(ctx, id)
	if err != nil {
		return Resolution{}, err
	}
	prov := Provenance{ID: id, Path: def.Path, Audience: def.Audience, Inputs: def.Inputs, EmbeddedVersion: def.Version}
	if ctx.Audience == Public && def.Audience != Public {
		return Resolution{}, fmt.Errorf("prompt %s is not approved for a public audience", id)
	}
	body := def.Body
	prov.Source, prov.File = SourceEmbedded, "embedded:prompts/"+def.Path
	if ctx.Harness != "" {
		override := filepath.Join(ctx.Harness, "prompts", filepath.FromSlash(def.Path))
		if ctx.Audience == Public {
			if _, err := os.Lstat(override); err == nil {
				prov.Skipped = append(prov.Skipped, Skip{File: override, Reason: "public audience uses embedded text"})
			}
			return Resolution{Provenance: prov, Body: body}, nil
		}
		raw, err := os.ReadFile(override)
		switch {
		case errors.Is(err, fs.ErrNotExist):
		case err != nil:
			return Resolution{}, err
		default:
			project, err := Parse(def.Path, raw)
			if err != nil {
				return Resolution{}, fmt.Errorf("%s: %w", override, err)
			}
			if project.ID != "" && project.ID != id {
				return Resolution{}, fmt.Errorf("%s declares id %q, expected %q", override, project.ID, id)
			}
			if err := checkPlaceholders(project.Body, def.Inputs); err != nil {
				return Resolution{}, fmt.Errorf("%s: %w", override, err)
			}
			body = project.Body
			prov.Source, prov.File = SourceProject, override
		}
		base := filepath.Join(ctx.Harness, "prompts", BaseDir, filepath.FromSlash(def.Path))
		if raw, err := os.ReadFile(base); err == nil {
			prov.BaseFile = base
			if recorded, err := Parse(def.Path, raw); err == nil && recorded.Version > 0 {
				prov.BaseVersion = recorded.Version
			}
			embedded, _ := fs.ReadFile(ctx.Defaults, def.Path)
			prov.BaseCurrent = bytes.Equal(raw, embedded)
		}
	}
	return Resolution{Provenance: prov, Body: body}, nil
}

// Render substitutes {{key}} placeholders in one pass; values are inserted
// verbatim. Every declared input must be supplied, nothing undeclared may be,
// and {{command}} is always the invoked command name.
func Render(ctx Context, id string, data map[string]string) (Text, error) {
	res, err := Resolve(ctx, id)
	if err != nil {
		return Text{}, err
	}
	declared := map[string]bool{}
	for _, input := range res.Provenance.Inputs {
		declared[input] = true
		if _, ok := data[input]; !ok && input != "command" {
			return Text{}, fmt.Errorf("prompt %s: missing input %q", id, input)
		}
	}
	for key := range data {
		if !declared[key] {
			return Text{}, fmt.Errorf("prompt %s: undeclared input %q", id, key)
		}
	}
	var failure error
	out := placeholder.ReplaceAllStringFunc(res.Body, func(m string) string {
		key := placeholder.FindStringSubmatch(m)[1]
		if key == "command" {
			return branding.Name()
		}
		value, ok := data[key]
		if !ok && failure == nil {
			failure = fmt.Errorf("prompt %s: unknown placeholder {{%s}}", id, key)
		}
		return value
	})
	if failure != nil {
		return Text{}, failure
	}
	return Text{s: strings.TrimRight(out, "\n"), prov: res.Provenance}, nil
}

func List(ctx Context) ([]Provenance, error) {
	if ctx.Defaults == nil {
		return nil, fmt.Errorf("no embedded prompt defaults")
	}
	var ids []string
	err := fs.WalkDir(ctx.Defaults, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && path.Ext(p) == ".md" {
			ids = append(ids, IDFor(p))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(ids)
	list := make([]Provenance, 0, len(ids))
	for _, id := range ids {
		res, err := Resolve(Context{Harness: ctx.Harness, Defaults: ctx.Defaults}, id)
		if err != nil {
			return nil, err
		}
		list = append(list, res.Provenance)
	}
	return list, nil
}

var placeholder = regexp.MustCompile(`\{\{\s*([^{}\s]+)\s*\}\}`)

func checkPlaceholders(body string, inputs []string) error {
	declared := map[string]bool{"command": true}
	for _, input := range inputs {
		declared[input] = true
	}
	for _, m := range placeholder.FindAllStringSubmatch(body, -1) {
		if !declared[m[1]] {
			return fmt.Errorf("unknown placeholder {{%s}}", m[1])
		}
	}
	return nil
}

// Parse reads optional frontmatter: flat "key: value" lines, inputs as a
// [a, b] list. The body starts after the closing "---" line.
func Parse(p string, raw []byte) (Definition, error) {
	def := Definition{Path: p}
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")
	if !strings.HasPrefix(text, "---\n") {
		def.Body = text
		return def, nil
	}
	end := strings.Index(text[4:], "\n---\n")
	if end < 0 {
		return def, fmt.Errorf("%s: unterminated frontmatter", p)
	}
	head, body := text[4:4+end], text[4+end+5:]
	for _, line := range strings.Split(head, "\n") {
		if i := strings.Index(line, " #"); i >= 0 {
			line = line[:i]
		}
		if strings.TrimSpace(line) == "" {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			return def, fmt.Errorf("%s: malformed frontmatter line %q", p, line)
		}
		value = strings.TrimSpace(value)
		switch strings.TrimSpace(key) {
		case "id":
			def.ID = value
		case "version":
			n, err := strconv.Atoi(value)
			if err != nil {
				return def, fmt.Errorf("%s: version %q is not a number", p, value)
			}
			def.Version = n
		case "inputs":
			if !strings.HasPrefix(value, "[") || !strings.HasSuffix(value, "]") {
				return def, fmt.Errorf("%s: inputs must be a [a, b] list", p)
			}
			def.Inputs = []string{}
			for _, input := range strings.Split(strings.Trim(value, "[]"), ",") {
				if input = strings.TrimSpace(input); input != "" {
					def.Inputs = append(def.Inputs, input)
				}
			}
		case "audience":
			def.Audience = Audience(value)
		default:
			return def, fmt.Errorf("%s: unknown frontmatter key %q", p, key)
		}
	}
	def.Body = body
	if err := checkPlaceholders(body, def.Inputs); err != nil && def.Inputs != nil {
		return def, fmt.Errorf("%s: %w", p, err)
	}
	return def, nil
}
