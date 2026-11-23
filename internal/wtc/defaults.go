package wtc

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"taba.sh/taba/internal/prompts"
)

//go:embed defaults/*.md defaults/agent-envrc defaults/browse/*.lua defaults/skills/*/SKILL.md defaults/skills/*/SKILL.*.md defaults/instructions/*.md defaults/review/concerns/*.md defaults/review/prompts/*.md defaults/prompts/*/*.md
var defaults embed.FS

func ReadDefault(path string) ([]byte, error) {
	return defaults.ReadFile("defaults/" + path)
}

func PromptDefaults() fs.FS {
	sub, err := fs.Sub(defaults, "defaults/prompts")
	if err != nil {
		panic(err)
	}
	return sub
}

// Prompts resolves agent prompts against this harness. Callers rendering for
// a public review set Audience on the returned value.
func (c *Context) Prompts() prompts.Context {
	return prompts.Context{Harness: c.Harness, Defaults: PromptDefaults(), Audience: prompts.Private}
}

func writeLaunchNote(collection string, note prompts.Text) error {
	return os.WriteFile(filepath.Join(collection, "HANDOFF.md"), []byte(note.String()+"\n"), 0644)
}

func DefaultPaths() ([]string, error) {
	var paths []string
	err := fs.WalkDir(defaults, "defaults", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			paths = append(paths, strings.TrimPrefix(path, "defaults/"))
		}
		return nil
	})
	return paths, err
}
func Eject(harness string, patterns []string) ([]string, error) {
	paths, err := DefaultPaths()
	if err != nil {
		return nil, err
	}
	var selected []string
	for _, pattern := range patterns {
		matched := false
		for _, path := range paths {
			ok, err := filepath.Match(pattern, path)
			if err != nil {
				return nil, err
			}
			if !ok {
				ok, err = filepath.Match(pattern, filepath.Dir(path))
				if err != nil {
					return nil, err
				}
			}
			if !ok {
				ok, err = filepath.Match(pattern, filepath.Base(path))
				if err != nil {
					return nil, err
				}
			}
			if ok {
				selected = append(selected, path)
				matched = true
			}
		}
		if !matched {
			return nil, fmt.Errorf("no embedded default matches %q", pattern)
		}
	}
	seen := map[string]bool{}
	var written []string
	for _, path := range selected {
		if seen[path] {
			continue
		}
		seen[path] = true
		dst := filepath.Join(harness, path)
		if _, err := os.Lstat(dst); err == nil {
			return nil, fmt.Errorf("refusing to overwrite %s", dst)
		} else if !os.IsNotExist(err) {
			return nil, err
		}
	}
	seen = map[string]bool{}
	for _, path := range selected {
		if seen[path] {
			continue
		}
		seen[path] = true
		dst := filepath.Join(harness, path)
		data, err := defaults.ReadFile("defaults/" + path)
		if err != nil {
			return written, err
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
			return written, err
		}
		if err := os.WriteFile(dst, data, 0644); err != nil {
			return written, err
		}
		written = append(written, dst)
		if rest, ok := strings.CutPrefix(path, "prompts/"); ok {
			base := filepath.Join(harness, "prompts", prompts.BaseDir, filepath.FromSlash(rest))
			if err := os.MkdirAll(filepath.Dir(base), 0755); err != nil {
				return written, err
			}
			if err := os.WriteFile(base, data, 0644); err != nil {
				return written, err
			}
			written = append(written, base)
		}
	}
	return written, nil
}
