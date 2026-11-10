package wtc

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// SkillDiff describes a checked-in skill override relative to the version
// embedded in this CLI. A recorded base digest lets upgrades distinguish an
// intentional override from one that has not been revisited since the default
// changed.
type SkillDiff struct {
	Name         string   `json:"name"`
	Source       string   `json:"source"`
	Status       string   `json:"status"`
	DefaultHash  string   `json:"default_sha256,omitempty"`
	RecordedBase string   `json:"recorded_base_sha256,omitempty"`
	Changes      []string `json:"changes,omitempty"`
	Error        string   `json:"error,omitempty"`
}

func (c *Context) DiffSkills() ([]SkillDiff, error) {
	paths, err := DefaultPaths()
	if err != nil {
		return nil, err
	}
	defaults := map[string][]byte{}
	for _, path := range paths {
		parts := strings.Split(path, "/")
		if len(parts) != 3 || parts[0] != "skills" || parts[2] != "SKILL.md" {
			continue
		}
		defaults[parts[1]], err = ReadDefault(path)
		if err != nil {
			return nil, err
		}
	}
	// The overlay has the same precedence as the renderer. A local skill in
	// harness/skills is reported only when no overlay replaces it.
	overrides := map[string]string{}
	for _, layer := range []string{"skills", filepath.Join("overlays", "skills")} {
		root := filepath.Join(c.Harness, layer)
		entries, err := os.ReadDir(root)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if !entry.IsDir() && entry.Type()&os.ModeSymlink == 0 {
				continue
			}
			path := filepath.Join(root, entry.Name(), "SKILL.md")
			if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
				overrides[entry.Name()] = path
			} else if err != nil && !os.IsNotExist(err) {
				return nil, err
			}
		}
	}
	names := make([]string, 0, len(overrides))
	for name := range overrides {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]SkillDiff, 0, len(names))
	for _, name := range names {
		path := overrides[name]
		current, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		item := SkillDiff{Name: name, Source: filepath.ToSlash(strings.TrimPrefix(path, c.Harness+string(filepath.Separator)))}
		base, exists := defaults[name]
		if current, renamed := renamedSkill(name); !exists && renamed {
			// An override under the old name is compared with the renamed default.
			base, exists = defaults[current]
		}
		if !exists {
			item.Status = "custom"
			result = append(result, item)
			continue
		}
		item.DefaultHash = skillDigest(base)
		item.Changes = skillLineChanges(base, current)
		if len(item.Changes) == 0 {
			item.Status = "current"
			result = append(result, item)
			continue
		}
		digestPath, err := skillBaseInput(filepath.Dir(path))
		if err != nil {
			return nil, err
		}
		recorded, err := os.ReadFile(digestPath)
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		if err == nil {
			item.RecordedBase = strings.TrimSpace(string(recorded))
			if len(item.RecordedBase) != 64 || !isHexDigest(item.RecordedBase) {
				return nil, fmt.Errorf("invalid base digest in %s", digestPath)
			}
		}
		switch {
		case item.RecordedBase == "":
			item.Status = "untracked"
		case item.RecordedBase == item.DefaultHash:
			item.Status = "reviewed"
		default:
			item.Status = "drifted"
		}
		result = append(result, item)
	}
	patchRoot := filepath.Join(c.Harness, "overlays", "skills")
	patchSkills, err := os.ReadDir(patchRoot)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	for _, entry := range patchSkills {
		if !entry.IsDir() && entry.Type()&os.ModeSymlink == 0 {
			continue
		}
		name := entry.Name()
		patchDir := filepath.Join(patchRoot, name, "sections")
		patches, err := os.ReadDir(patchDir)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if len(patches) == 0 {
			continue
		}
		if _, full := overrides[name]; full && strings.HasPrefix(overrides[name], filepath.Join(patchRoot, name)+string(filepath.Separator)) {
			return nil, fmt.Errorf("skill %s has both a full overlay and section patches", name)
		}
		base, ok := defaults[name]
		if current, renamed := renamedSkill(name); !ok && renamed {
			base, ok = defaults[current]
		}
		path := filepath.Join(c.Harness, "skills", name, "SKILL.md")
		if local, err := os.ReadFile(path); err == nil {
			base, ok = local, true
		} else if !os.IsNotExist(err) {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("section patches for unknown skill %s", name)
		}
		item := SkillDiff{Name: name, Source: filepath.ToSlash(strings.TrimPrefix(patchDir, c.Harness+string(filepath.Separator))), DefaultHash: skillDigest(base)}
		digestPath, err := skillBaseInput(filepath.Join(patchRoot, name))
		if err != nil {
			return nil, err
		}
		recorded, err := os.ReadFile(digestPath)
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		if err == nil {
			item.RecordedBase = strings.TrimSpace(string(recorded))
			if len(item.RecordedBase) != 64 || !isHexDigest(item.RecordedBase) {
				return nil, fmt.Errorf("invalid base digest for skill %s", name)
			}
		}
		switch {
		case item.RecordedBase == "":
			item.Status = "untracked"
		case item.RecordedBase == item.DefaultHash:
			item.Status = "reviewed"
		default:
			item.Status = "drifted"
		}
		current := base
		seenHeadings := map[string]bool{}
		for _, patch := range patches {
			if patch.IsDir() || !strings.HasSuffix(patch.Name(), ".md") {
				continue
			}
			body, err := os.ReadFile(filepath.Join(patchDir, patch.Name()))
			if err != nil {
				return nil, err
			}
			heading := strings.SplitN(string(body), "\n", 2)[0]
			if seenHeadings[heading] {
				item.Error = fmt.Sprintf("duplicate section patch for %q", heading)
				break
			}
			seenHeadings[heading] = true
			current, err = applySkillSection(current, body)
			if err != nil {
				item.Error = patch.Name() + ": " + err.Error()
				break
			}
		}
		if item.Error != "" && item.Status != "drifted" {
			item.Status = "invalid"
		}
		if item.Error == "" {
			item.Changes = skillLineChanges(base, current)
		}
		result = append(result, item)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Name != result[j].Name {
			return result[i].Name < result[j].Name
		}
		return result[i].Source < result[j].Source
	})
	return result, nil
}

func skillDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func isHexDigest(s string) bool {
	_, err := hex.DecodeString(s)
	return err == nil
}

// skillLineChanges uses a longest common subsequence to give a compact,
// deterministic review of the override without invoking a platform diff tool.
func skillLineChanges(base, current []byte) []string {
	if string(base) == string(current) {
		return nil
	}
	a := strings.Split(strings.TrimSuffix(string(base), "\n"), "\n")
	b := strings.Split(strings.TrimSuffix(string(current), "\n"), "\n")
	rows := make([][]int, len(a)+1)
	for i := range rows {
		rows[i] = make([]int, len(b)+1)
	}
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				rows[i][j] = 1 + rows[i+1][j+1]
			} else if rows[i+1][j] >= rows[i][j+1] {
				rows[i][j] = rows[i+1][j]
			} else {
				rows[i][j] = rows[i][j+1]
			}
		}
	}
	changes := []string{}
	for i, j := 0, 0; i < len(a) || j < len(b); {
		switch {
		case i < len(a) && j < len(b) && a[i] == b[j]:
			i++
			j++
		case j == len(b) || i < len(a) && rows[i+1][j] >= rows[i][j+1]:
			changes = append(changes, "-"+a[i])
			i++
		default:
			changes = append(changes, "+"+b[j])
			j++
		}
	}
	if len(changes) == 0 {
		changes = append(changes, "~final newline differs")
	}
	return changes
}
