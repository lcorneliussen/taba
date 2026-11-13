package wtc

import (
	"bufio"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Inventories expose names and link metadata only. In particular, they never
// return environment values or read the contents of control-root files.
type SecretInventory struct {
	Files []SecretInventoryFile `json:"files"`
}

type SecretInventoryFile struct {
	Path       string `json:"path"`
	Scope      string `json:"scope"`
	Target     string `json:"target,omitempty"`
	State      string `json:"state"`
	Production bool   `json:"production,omitempty"`
	Ignored    *bool  `json:"gitignored,omitempty"`
}

type EnvInventory struct {
	Files     []EnvInventoryFile `json:"files"`
	Variables []EnvInventoryKey  `json:"variables"`
}

type EnvInventoryFile struct {
	Path   string `json:"path"`
	Scope  string `json:"scope"`
	Exists bool   `json:"exists"`
}

type EnvInventoryKey struct {
	Name      string `json:"name"`
	Source    string `json:"source"`
	Scope     string `json:"scope"`
	Overrides bool   `json:"overrides,omitempty"`
}

var envKeyName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func (c *Context) ListEnv() (EnvInventory, error) {
	if _, err := c.ValidateControlRoot(); err != nil {
		return EnvInventory{}, err
	}
	result := EnvInventory{Files: []EnvInventoryFile{}, Variables: []EnvInventoryKey{}}
	seen := map[string]bool{}
	machine, err := MachineEnvFile(c.ConfigRoot)
	if err != nil {
		return result, err
	}
	for _, source := range []struct {
		path, label, scope string
	}{
		{machine, filepath.Base(machine), "all collections using this control root (workspace defaults)"},
		{filepath.Join(c.Collection, ".env.collection"), ".env.collection", "this collection (generated)"},
		{filepath.Join(c.Collection, ".env.collection.local"), ".env.collection.local", "this collection (local override)"},
	} {
		keys, exists, err := readEnvKeyNames(source.path)
		if err != nil {
			return result, fmt.Errorf("list keys in %s: %w", source.label, err)
		}
		result.Files = append(result.Files, EnvInventoryFile{Path: source.label, Scope: source.scope, Exists: exists})
		for _, key := range keys {
			result.Variables = append(result.Variables, EnvInventoryKey{
				Name: key, Source: source.label, Scope: source.scope, Overrides: seen[key],
			})
			seen[key] = true
		}
	}
	return result, nil
}

func readEnvKeyNames(path string) ([]string, bool, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if !info.Mode().IsRegular() {
		return nil, true, fmt.Errorf("not a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, true, err
	}
	defer file.Close()
	keys := []string{}
	seen := map[string]bool{}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 10*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		line = strings.TrimPrefix(line, "export ")
		key, _, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if ok && envKeyName.MatchString(key) && !seen[key] {
			keys = append(keys, key)
			seen[key] = true
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, true, err
	}
	sort.Strings(keys)
	return keys, true, nil
}

func (c *Context) ListSecrets(repoFilter string) (SecretInventory, error) {
	scope, err := c.ValidateControlRoot()
	if err != nil {
		return SecretInventory{}, err
	}
	if err := scope.validateSecretSources(); err != nil {
		return SecretInventory{}, err
	}
	result := SecretInventory{Files: []SecretInventoryFile{}}
	harnessRepo, _ := c.HarnessRepoName()
	if repoFilter == "harness" && harnessRepo != "" {
		repoFilter = harnessRepo
	}
	if repoFilter != "" && !validRepoName.MatchString(repoFilter) {
		return result, fmt.Errorf("invalid repository name %q", repoFilter)
	}
	prod, err := c.secretProdPaths()
	if err != nil {
		return result, err
	}
	root, err := filepath.Abs(c.ConfigRoot)
	if err != nil {
		return result, err
	}
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		entries = nil
	} else if err != nil {
		return result, err
	}
	for _, entry := range entries {
		name := entry.Name()
		if strings.ContainsAny(name, "\r\n\x00") {
			return result, fmt.Errorf("control-root path contains a newline or NUL")
		}
		if repoFilter != "" && name != repoFilter {
			continue
		}
		if !entry.IsDir() {
			if repoFilter == "" && (entry.Type().IsRegular() || entry.Type()&os.ModeSymlink != 0) {
				result.Files = append(result.Files, SecretInventoryFile{Path: name, Scope: "all collections using this control root (workspace config)", State: "control-only"})
			}
			continue
		}
		sourceRoot := filepath.Join(root, name)
		worktreeName := name
		if name == harnessRepo {
			worktreeName = "harness"
		}
		worktree := filepath.Join(c.Collection, worktreeName)
		if worktreeName != "harness" {
			worktree = c.WorktreeDir(worktreeName)
		}
		machineConfig := name == "gh" || name == "jira" || name == "certificates"
		checkedOut := !machineConfig && validRepoName.MatchString(name) && validInventoryWorktree(worktree)
		err := filepath.WalkDir(sourceRoot, func(source string, item fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if item.IsDir() || !item.Type().IsRegular() && item.Type()&os.ModeSymlink == 0 {
				return nil
			}
			rel, err := filepath.Rel(sourceRoot, source)
			if err != nil {
				return err
			}
			if strings.ContainsAny(rel, "\r\n\x00") {
				return fmt.Errorf("control-root path contains a newline or NUL")
			}
			key := name + "/" + filepath.ToSlash(rel)
			row := SecretInventoryFile{Path: key, Scope: "all collections with this repository", State: "not-checked-out", Production: prod[key]}
			if checkedOut {
				row.Target = filepath.ToSlash(filepath.Join(worktreeName, rel))
				row.State, row.Ignored, err = secretTargetState(worktree, rel, source)
				if err != nil {
					return err
				}
				if row.Production && row.State == "available" {
					row.State = "prod-excluded"
				}
			} else if machineConfig || !validRepoName.MatchString(name) {
				row.Scope = "all collections using this control root (workspace config)"
				row.State = "control-only"
			}
			result.Files = append(result.Files, row)
			return nil
		})
		if err != nil {
			return result, err
		}
	}
	if repoFilter == "" {
		local := filepath.Join(c.Collection, ".env.collection.local")
		if info, err := os.Lstat(local); err == nil {
			state := "present"
			if !info.Mode().IsRegular() {
				state = "non-regular"
			}
			result.Files = append(result.Files, SecretInventoryFile{
				Path: ".env.collection.local", Scope: "this collection (local secret variables)",
				Target: "all worktrees in this collection", State: state,
			})
		} else if !os.IsNotExist(err) {
			return result, err
		}
	}
	sort.Slice(result.Files, func(i, j int) bool { return result.Files[i].Path < result.Files[j].Path })
	return result, nil
}

func validInventoryWorktree(worktree string) bool {
	info, err := os.Stat(worktree)
	if err != nil || !info.IsDir() {
		return false
	}
	top, err := exec.Command("git", "-C", worktree, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return false
	}
	resolvedWorktree, err := filepath.EvalSymlinks(worktree)
	if err != nil {
		return false
	}
	resolvedTop, err := filepath.EvalSymlinks(strings.TrimSpace(string(top)))
	return err == nil && resolvedTop == resolvedWorktree
}

func secretTargetState(worktree, rel, source string) (string, *bool, error) {
	ignored, err := gitIgnored(worktree, rel)
	if err != nil {
		return "", nil, err
	}
	if err := safeSecretParent(worktree, filepath.Dir(filepath.Join(worktree, rel))); err != nil {
		return "unsafe-parent", &ignored, nil
	}
	dest := filepath.Join(worktree, rel)
	info, err := os.Lstat(dest)
	if os.IsNotExist(err) {
		if !ignored {
			return "not-ignored", &ignored, nil
		}
		return "available", &ignored, nil
	}
	if err != nil {
		return "", nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(dest)
		if err != nil {
			return "", nil, err
		}
		if target == source {
			if !ignored {
				return "linked-not-ignored", &ignored, nil
			}
			return "linked", &ignored, nil
		}
		return "different-link", &ignored, nil
	}
	if info.Mode().IsRegular() {
		return "local-override", &ignored, nil
	}
	return "other-target", &ignored, nil
}
