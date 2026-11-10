package wtc

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type SkillRenderOptions struct {
	DryRun    bool
	SeedScope bool
}

type SkillRenderResult struct {
	Collection string   `json:"collection"`
	Linked     int      `json:"linked"`
	Current    int      `json:"already_current"`
	Pruned     int      `json:"pruned"`
	Skipped    int      `json:"skipped"`
	Actions    []string `json:"actions"`
	DryRun     bool     `json:"dry_run"`
}

// RenderSkills exposes harness overrides and embedded skills to the agent
// clients, while preserving hand-authored files at every destination.
func (c *Context) RenderSkills(opt SkillRenderOptions) (SkillRenderResult, error) {
	r := SkillRenderResult{Collection: c.Collection, DryRun: opt.DryRun, Actions: []string{}}
	harnessName := c.harnessRel()
	type source struct {
		path, target string
		data         []byte
	}
	sources := map[string]source{}
	paths, err := DefaultPaths()
	if err != nil {
		return r, err
	}
	for _, path := range paths {
		parts := strings.Split(path, "/")
		if len(parts) != 3 || parts[0] != "skills" || parts[2] != "SKILL.md" {
			continue
		}
		data, err := ReadDefault(path)
		if err != nil {
			return r, err
		}
		name := parts[1]
		if name == "taba-svc" && c.Config.Runtime.Backend != "dekit" {
			// Services exist only where the backend is enabled; elsewhere the
			// skill would send agents to a command that refuses.
			continue
		}
		sources[name] = source{path: filepath.Join(c.Collection, ".wtc", "skills", name, "SKILL.md"), target: "../../.wtc/skills/" + name, data: data}
	}
	for _, layer := range []string{"skills", filepath.Join("overlays", "skills")} {
		root := filepath.Join(c.Harness, layer)
		entries, err := os.ReadDir(root)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return r, err
		}
		for _, entry := range entries {
			if !entry.IsDir() && entry.Type()&os.ModeSymlink == 0 {
				continue
			}
			name := entry.Name()
			path := filepath.Join(root, name, "SKILL.md")
			if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
				prefix := "../../" + harnessName + "/" + filepath.ToSlash(layer)
				sources[name] = source{path: path, target: prefix + "/" + name}
			}
		}
	}
	// A harness override under a skill's old wtc-* name keeps winning: it
	// stays exposed under that name, and taba-<name> becomes a pointer to it
	// instead of the default, so agents never see two different procedures
	// for one action. Renaming the override to taba-* is the migration.
	for name, s := range sources {
		if s.data != nil {
			continue
		}
		if current, ok := renamedSkill(name); ok {
			if d, ok := sources[current]; ok && d.data != nil {
				sources[current] = source{path: d.path, target: d.target, data: skillPointer(current, name)}
			}
		}
	}
	patchRoot := filepath.Join(c.Harness, "overlays", "skills")
	patchSkills, err := os.ReadDir(patchRoot)
	if err != nil && !os.IsNotExist(err) {
		return r, err
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
			return r, err
		}
		if len(patches) == 0 {
			continue
		}
		if _, err := os.Stat(filepath.Join(patchRoot, name, "SKILL.md")); err == nil {
			return r, fmt.Errorf("skill %s has both a full overlay and section patches", name)
		} else if !os.IsNotExist(err) {
			return r, err
		}
		overlayDir := filepath.Join(patchRoot, name)
		s, ok := sources[name]
		if current, renamed := renamedSkill(name); !ok && renamed {
			// Section patches written for the old name patch the renamed
			// default; their recorded base stays in the old-name folder.
			name = current
			s, ok = sources[name]
		}
		if !ok {
			return r, fmt.Errorf("section patches for unknown skill %s", name)
		}
		base := s.data
		if base == nil {
			base, err = os.ReadFile(s.path)
			if err != nil {
				return r, err
			}
		}
		digestPath, err := skillBaseInput(overlayDir)
		if err != nil {
			return r, err
		}
		if recorded, err := os.ReadFile(digestPath); err == nil {
			if strings.TrimSpace(string(recorded)) != skillDigest(base) {
				return r, fmt.Errorf("skill %s section patch base has drifted; review and update %s", name, filepath.Base(digestPath))
			}
		} else if !os.IsNotExist(err) {
			return r, err
		}
		seenHeadings := map[string]bool{}
		for _, patch := range patches {
			if patch.IsDir() || !strings.HasSuffix(patch.Name(), ".md") {
				continue
			}
			body, err := os.ReadFile(filepath.Join(patchDir, patch.Name()))
			if err != nil {
				return r, err
			}
			heading := strings.SplitN(string(body), "\n", 2)[0]
			if seenHeadings[heading] {
				return r, fmt.Errorf("duplicate skill %s section patch for %q", name, heading)
			}
			seenHeadings[heading] = true
			base, err = applySkillSection(base, body)
			if err != nil {
				return r, fmt.Errorf("%s: %w", filepath.Join(patchDir, patch.Name()), err)
			}
		}
		sources[name] = source{path: filepath.Join(c.Collection, ".wtc", "skills", name, "SKILL.md"), target: "../../.wtc/skills/" + name, data: base}
	}
	// Each renamed skill keeps its old name as a shim until 0.3, so agents and
	// people who type /wtc-<name> are sent to the taba-<name> procedure.
	for name := range sources {
		old, ok := legacySkillName(name)
		if !ok {
			continue
		}
		if _, taken := sources[old]; taken {
			continue
		}
		sources[old] = source{path: filepath.Join(c.Collection, ".wtc", "skills", old, "SKILL.md"), target: "../../.wtc/skills/" + old, data: skillShim(old, name)}
	}
	if err := c.renderInstructions(opt.DryRun, &r); err != nil {
		return r, err
	}
	names := make([]string, 0, len(sources))
	for name := range sources {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		s := sources[name]
		if s.data == nil {
			continue
		}
		old, err := os.ReadFile(s.path)
		if err == nil && bytes.Equal(old, s.data) {
			continue
		}
		if err != nil && !os.IsNotExist(err) {
			return r, err
		}
		if err := safeGeneratedParent(c.Collection, filepath.Dir(s.path)); err != nil {
			return r, err
		}
		if info, err := os.Lstat(s.path); err == nil && !info.Mode().IsRegular() {
			return r, fmt.Errorf("generated skill target is not a regular file: %s", s.path)
		}
		r.Actions = append(r.Actions, "render default: "+name)
		if !opt.DryRun {
			if err := os.MkdirAll(filepath.Dir(s.path), 0755); err != nil {
				return r, err
			}
			if err := os.WriteFile(s.path, s.data, 0644); err != nil {
				return r, err
			}
		}
	}
	for _, root := range []string{".claude/skills", ".agents/skills"} {
		for _, name := range names {
			dest := filepath.Join(c.Collection, root, name)
			if err := renderSkillLink(c.Collection, dest, sources[name].target, root+"/"+name, opt.DryRun, &r); err != nil {
				return r, err
			}
		}
		entries, err := os.ReadDir(filepath.Join(c.Collection, root))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return r, err
		}
		for _, entry := range entries {
			if !entry.Type().IsRegular() && entry.Type()&os.ModeSymlink == 0 {
				continue
			}
			name := entry.Name()
			if _, present := sources[name]; present {
				continue
			}
			dest := filepath.Join(c.Collection, root, name)
			target, err := os.Readlink(dest)
			if err != nil || !managedSkillTarget(target, harnessName) {
				continue
			}
			r.Actions = append(r.Actions, "prune: "+root+"/"+name)
			r.Pruned++
			if !opt.DryRun {
				if err := os.Remove(dest); err != nil {
					return r, err
				}
			}
		}
	}
	entryTarget := harnessName + "/collection-AGENTS.md"
	if _, err := os.Stat(filepath.Join(c.Harness, "collection-AGENTS.md")); os.IsNotExist(err) {
		data, err := ReadDefault("collection-AGENTS.md")
		if err != nil {
			return r, err
		}
		dest := filepath.Join(c.Collection, ".wtc", "collection-AGENTS.md")
		if info, err := os.Lstat(dest); err == nil && !info.Mode().IsRegular() {
			return r, fmt.Errorf("generated entry target is not a regular file: %s", dest)
		}
		old, err := os.ReadFile(dest)
		if err != nil && !os.IsNotExist(err) {
			return r, err
		}
		if !bytes.Equal(old, data) {
			r.Actions = append(r.Actions, "render default: collection-AGENTS.md")
			if !opt.DryRun {
				if err := safeGeneratedParent(c.Collection, filepath.Dir(dest)); err != nil {
					return r, err
				}
				if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
					return r, err
				}
				if err := os.WriteFile(dest, data, 0644); err != nil {
					return r, err
				}
			}
		}
		entryTarget = ".wtc/collection-AGENTS.md"
	} else if err != nil {
		return r, err
	}
	if err := renderSkillLink(c.Collection, filepath.Join(c.Collection, "AGENTS.md"), entryTarget, "AGENTS.md", opt.DryRun, &r); err != nil {
		return r, err
	}
	if opt.SeedScope {
		if err := c.seedScope(opt.DryRun, &r); err != nil {
			return r, err
		}
	}
	if _, err := os.Stat(filepath.Join(c.Harness, "hooks", "agent-env.json")); err == nil {
		for _, hook := range [][2]string{
			{".grok/hooks/wtc-agent-env.json", "../../" + harnessName + "/hooks/agent-env.json"},
			{".claude/settings.json", "../" + harnessName + "/hooks/agent-env.json"},
			{".cursor/hooks.json", "../" + harnessName + "/hooks/agent-env.json"},
		} {
			if err := renderSkillLink(c.Collection, filepath.Join(c.Collection, hook[0]), hook[1], hook[0], opt.DryRun, &r); err != nil {
				return r, err
			}
		}
	}
	if err := c.renderEnvrc(opt.DryRun, &r); err != nil {
		return r, err
	}
	if opt.DryRun {
		r.Actions = append(r.Actions, "would refresh: .env.toolchain")
	} else if _, err := c.AgentToolchainPath(true); err != nil {
		r.Actions = append(r.Actions, "warning: toolchain refresh failed: "+err.Error())
	} else {
		r.Actions = append(r.Actions, "refreshed: .env.toolchain")
	}
	return r, nil
}

// renamedSkill maps a skill name in the old wtc-<name> form, such as a
// harness override's folder, to the embedded taba-<name> it now corresponds to.
func renamedSkill(name string) (string, bool) {
	rest, ok := strings.CutPrefix(name, "wtc-")
	if !ok {
		return "", false
	}
	current := "taba-" + rest
	if _, err := ReadDefault("skills/" + current + "/SKILL.md"); err != nil {
		return "", false
	}
	return current, true
}

// legacySkillName is the old wtc-<name> of an embedded taba-<name> skill.
func legacySkillName(name string) (string, bool) {
	rest, ok := strings.CutPrefix(name, "taba-")
	if !ok {
		return "", false
	}
	if _, err := ReadDefault("skills/" + name + "/SKILL.md"); err != nil {
		return "", false
	}
	return "wtc-" + rest, true
}

func skillShim(old, current string) []byte {
	return []byte("---\nname: " + old + "\ndescription: Old name of the " + current + " skill, kept until taba 0.3. Use " + current + " instead.\n---\n\n" +
		"# " + old + " is now " + current + "\n\n" +
		"wtc is now taba, and this skill was renamed with it. Use the `" + current + "` skill\n" +
		"(`/" + current + "`) for this procedure. Type `taba` where an older note says\n" +
		"`wtc`; both commands work in 0.2.\n")
}

// skillPointer stands in for a default whose harness override still uses the
// old name, so the taba-<name> that status hints mention leads to it.
func skillPointer(current, old string) []byte {
	return []byte("---\nname: " + current + "\ndescription: This harness provides this procedure as the " + old + " skill. Use " + old + ".\n---\n\n" +
		"# " + current + ": use " + old + "\n\n" +
		"This harness overrides the procedure under its old name. Use the `" + old + "`\n" +
		"skill (`/" + old + "`).\n")
}

func managedSkillTarget(target, harnessName string) bool {
	target = filepath.ToSlash(filepath.Clean(target))
	for _, prefix := range []string{
		"../../" + harnessName + "/skills/", "../../" + harnessName + "/overlays/skills/",
		"../../.wtc/skills/", "../../.taba/skills/",
		"../../.wtc/config/skills/", "../../.wtc/config/overlays/skills/",
		"../../.taba/config/skills/", "../../.taba/config/overlays/skills/",
	} {
		if strings.HasPrefix(target, prefix) {
			return true
		}
	}
	return false
}

// A section patch is a complete Markdown H2 section. It replaces only the
// matching H2 in the base skill, preserving its frontmatter and other sections.
func applySkillSection(base, patch []byte) ([]byte, error) {
	if len(patch) == 0 || patch[len(patch)-1] != '\n' {
		return nil, fmt.Errorf("section patch must end with a newline")
	}
	patchLines := strings.SplitAfter(string(patch), "\n")
	if len(patchLines) < 2 || !strings.HasPrefix(patchLines[0], "## ") {
		return nil, fmt.Errorf("section patch must start with its exact H2 heading")
	}
	heading := strings.TrimSuffix(patchLines[0], "\n")
	var fence byte
	var fenceWidth int
	for _, line := range patchLines[1 : len(patchLines)-1] {
		if nextFence, width, rest := skillFence(line); width > 0 {
			if fence == 0 {
				fence, fenceWidth = nextFence, width
			} else if nextFence == fence && width >= fenceWidth && strings.TrimSpace(rest) == "" {
				fence = 0
			}
			continue
		}
		if fence == 0 && skillTopHeading(line) {
			return nil, fmt.Errorf("section patch may contain only one H2 section")
		}
	}
	if fence != 0 {
		return nil, fmt.Errorf("section patch has an unclosed code fence")
	}
	lines := strings.SplitAfter(string(base), "\n")
	start, end := -1, -1
	fence, fenceWidth = 0, 0
	for i, line := range lines {
		if nextFence, width, rest := skillFence(line); width > 0 {
			if fence == 0 {
				fence, fenceWidth = nextFence, width
			} else if nextFence == fence && width >= fenceWidth && strings.TrimSpace(rest) == "" {
				fence = 0
			}
			continue
		}
		if fence != 0 {
			continue
		}
		if strings.TrimSuffix(line, "\n") == heading {
			if start >= 0 {
				return nil, fmt.Errorf("duplicate heading %q", heading)
			}
			start = i
			continue
		}
		if start >= 0 && end < 0 && skillTopHeading(line) {
			end = i
		}
	}
	if start < 0 {
		return nil, fmt.Errorf("heading %q not found", heading)
	}
	if end < 0 {
		end = len(lines)
	}
	return []byte(strings.Join(lines[:start], "") + string(patch) + strings.Join(lines[end:], "")), nil
}

func skillTopHeading(line string) bool {
	return strings.HasPrefix(line, "# ") || strings.HasPrefix(line, "## ")
}

func skillFence(line string) (byte, int, string) {
	line = strings.TrimSuffix(line, "\n")
	spaces := len(line) - len(strings.TrimLeft(line, " "))
	if spaces > 3 {
		return 0, 0, ""
	}
	line = line[spaces:]
	if len(line) < 3 || line[0] != '`' && line[0] != '~' {
		return 0, 0, ""
	}
	i := 0
	for i < len(line) && line[i] == line[0] {
		i++
	}
	if i < 3 {
		return 0, 0, ""
	}
	return line[0], i, line[i:]
}

func renderSkillLink(collection, dest, want, label string, dryRun bool, r *SkillRenderResult) error {
	info, err := os.Lstat(dest)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			current, err := os.Readlink(dest)
			if err != nil {
				return err
			}
			if current == want {
				r.Current++
				return nil
			}
		} else {
			r.Skipped++
			r.Actions = append(r.Actions, "skip local override: "+label)
			return nil
		}
	}
	r.Linked++
	if dryRun {
		r.Actions = append(r.Actions, "would link: "+label+" -> "+want)
		return nil
	}
	if err := safeGeneratedParent(collection, filepath.Dir(dest)); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return err
	}
	if info != nil {
		if err := os.Remove(dest); err != nil {
			return err
		}
	}
	if err := os.Symlink(want, dest); err != nil {
		return err
	}
	r.Actions = append(r.Actions, "linked: "+label+" -> "+want)
	return nil
}

func safeGeneratedParent(collection, parent string) error {
	for path := parent; path != collection; path = filepath.Dir(path) {
		if path == filepath.Dir(path) {
			return fmt.Errorf("generated path escapes collection")
		}
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("generated path has unsafe parent %s", path)
		}
	}
	return nil
}

func (c *Context) seedScope(dryRun bool, r *SkillRenderResult) error {
	dest, err := legacyInput(c.Collection, "WTC-SCOPE.md", "SCOPE.md")
	if err != nil {
		return err
	}
	_, statErr := os.Lstat(dest)
	if os.IsNotExist(statErr) && TabaNative(c.Harness) {
		// Neither name exists. Only a harness that moved to the new names gets
		// SCOPE.md; others may have tooling that reads WTC-SCOPE.md.
		dest = filepath.Join(c.Collection, "SCOPE.md")
	}
	name := filepath.Base(dest)
	if _, err := os.Lstat(dest); err == nil {
		r.Actions = append(r.Actions, "scope: already present")
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	template, err := os.ReadFile(filepath.Join(c.Harness, "collection-SCOPE.md"))
	if os.IsNotExist(err) {
		template, err = ReadDefault("collection-SCOPE.md")
	}
	if err != nil {
		return err
	}
	var rows strings.Builder
	rows.WriteString("| Repo | Why it is here |\n|---|---|\n")
	entries, err := os.ReadDir(c.Collection)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if _, err := os.Lstat(filepath.Join(c.Collection, entry.Name(), ".git")); err == nil {
			fmt.Fprintf(&rows, "| `%s` |  |\n", entry.Name())
		}
	}
	var rendered strings.Builder
	for _, line := range strings.SplitAfter(string(template), "\n") {
		if strings.Contains(line, "{{REPOS}}") {
			rendered.WriteString(rows.String())
		} else {
			rendered.WriteString(strings.ReplaceAll(line, "{{COLLECTION}}", filepath.Base(c.Collection)))
		}
	}
	if dryRun {
		r.Actions = append(r.Actions, "would seed: "+name)
		return nil
	}
	if err := os.WriteFile(dest, []byte(rendered.String()), 0644); err != nil {
		return err
	}
	r.Actions = append(r.Actions, "seeded: "+name)
	return nil
}

func (c *Context) renderEnvrc(dryRun bool, r *SkillRenderResult) error {
	dest := filepath.Join(c.Collection, ".envrc")
	body, err := ReadDefault("agent-envrc")
	if err != nil {
		return err
	}
	info, err := os.Lstat(dest)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			r.Skipped++
			r.Actions = append(r.Actions, "skip symlink: .envrc")
			return nil
		}
		if !info.Mode().IsRegular() {
			r.Skipped++
			r.Actions = append(r.Actions, "skip local override: .envrc")
			return nil
		}
		old, err := os.ReadFile(dest)
		if err != nil {
			return err
		}
		if bytes.Equal(old, body) {
			r.Current++
			return nil
		}
		if !bytes.HasPrefix(old, []byte("# Generated by harness/tools/link-skills.sh")) &&
			!bytes.HasPrefix(old, []byte("# Generated by wtc skills render")) {
			r.Skipped++
			r.Actions = append(r.Actions, "skip local override: .envrc")
			return nil
		}
	}
	if dryRun {
		r.Actions = append(r.Actions, "would write: .envrc")
		return nil
	}
	if err := os.WriteFile(dest, body, 0644); err != nil {
		return err
	}
	r.Actions = append(r.Actions, "wrote: .envrc")
	return nil
}
