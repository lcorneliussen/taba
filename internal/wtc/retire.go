package wtc

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type RetireOptions struct {
	Name  string
	Force bool
	// Self is reserved for the cleanup workspace worker. Interactive callers
	// must hand off before retiring their own collection.
	Self bool
	// WorkspaceID pins the Herdr workspace selected during a self handoff.
	WorkspaceID string
	// InPlace retires an ad-hoc collection from inside itself without Herdr:
	// it may have no other collection to hand the work to.
	InPlace bool
}

type RetireResult struct {
	Collection      string   `json:"collection"`
	Removed         []string `json:"removed"`
	Leftovers       []string `json:"leftovers,omitempty"`
	Warnings        []string `json:"warnings,omitempty"`
	WorkspaceClosed bool     `json:"workspace_closed"`
	FolderRemoved   bool     `json:"folder_removed"`
}

type retireWorktree struct {
	name, path, owner string
}

// RetireCollection first inspects every worktree, then removes only the named
// collection. Remote branches and bare owners are never changed.
func (c *Context) RetireCollection(opt RetireOptions) (RetireResult, error) {
	if opt.Self {
		if opt.Name != filepath.Base(c.Collection) {
			return RetireResult{}, fmt.Errorf("self-retirement target must be the current collection")
		}
		if opt.InPlace && !c.AdHoc {
			return RetireResult{}, fmt.Errorf("in-place retirement is only for ad-hoc collections")
		}
		if opt.WorkspaceID == "" && !opt.InPlace {
			return RetireResult{}, fmt.Errorf("self-retirement requires a verified Herdr workspace ID")
		}
	}
	if !opt.Self && collectionNamePattern.MatchString(opt.Name) {
		target := filepath.Join(c.Workspace, opt.Name)
		if info, err := os.Lstat(target); err == nil && info.IsDir() && RetiredFolder(target) {
			return c.finishRetiredFolder(target)
		}
	}
	result, worktrees, err := c.retirePreflight(opt)
	if err != nil {
		return result, err
	}
	target := result.Collection
	// Close the source workspace before hooks or worktree removal. Once closed,
	// no new pane or agent turn can start against a collection being deleted.
	if opt.Self && !opt.InPlace {
		closed, warning := c.closeRetiredWorkspaceID(opt.Name, opt.WorkspaceID)
		if !closed {
			if warning == "" {
				warning = "source workspace was not found or Herdr is unavailable"
			}
			return result, fmt.Errorf("cannot close source workspace before retirement: %s", warning)
		}
		result.WorkspaceClosed = true
		// Agent work may have changed the collection after the first preflight
		// but before Herdr closed its workspace. Inspect it again once no new
		// source-pane activity can begin, and use this fresh worktree list.
		checked, current, err := c.retirePreflight(opt)
		if err != nil {
			checked.WorkspaceClosed = true
			return checked, err
		}
		result, worktrees = checked, current
		result.WorkspaceClosed = true
	}
	values := map[string]string{"target": target, "force": strconv.FormatBool(opt.Force)}
	if err := c.RunHook("retire.pre", values); err != nil {
		return result, err
	}
	// Runtime ownership belongs to the target, not to the caller's harness.
	runtimeTarget, err := OpenCollection(target)
	if err != nil {
		return result, err
	}
	unlockWatchers, err := runtimeTarget.lockStatusWatchers()
	if err != nil {
		return result, err
	}
	defer unlockWatchers()
	err = runtimeTarget.withRuntimeLock(func() error {
		if err := runtimeTarget.retireRuntime(); err != nil {
			return fmt.Errorf("runtime cleanup: %w", err)
		}
		if err := stopRetiredStatusWatchers(target); err != nil {
			return err
		}
		postRan := false
		for _, wt := range worktrees {
			// A self-retire loses its harness hook source when that final worktree
			// goes away. Run the post hook after product teardown, while it exists.
			if opt.Self && wt.name == "harness" {
				if err := c.RunHook("retire.post", values); err != nil {
					return err
				}
				postRan = true
			}
			RunRepoTeardown(wt.path)
			args := []string{"--git-dir=" + wt.owner, "worktree", "remove"}
			if opt.Force {
				args = append(args, "--force")
			}
			args = append(args, wt.path)
			if _, err := gitOutput(args...); err != nil {
				return fmt.Errorf("remove %s: %w", wt.name, err)
			}
			if _, err := gitOutput("--git-dir="+wt.owner, "worktree", "prune"); err != nil {
				return fmt.Errorf("prune %s: %w", wt.name, err)
			}
			result.Removed = append(result.Removed, wt.name)
		}
		if opt.Self && !postRan {
			if err := c.RunHook("retire.post", values); err != nil {
				return err
			}
		}
		if !result.WorkspaceClosed && !opt.InPlace {
			closed, warning := c.closeRetiredWorkspaceID(opt.Name, opt.WorkspaceID)
			result.WorkspaceClosed = closed
			if warning != "" {
				result.Warnings = append(result.Warnings, warning)
			}
		}
		unlockDev, err := lockDevRetirement(target)
		if err != nil {
			if !runtimeTarget.AdHoc {
				if guardErr := writeRetiredMarker(target, nil); guardErr != nil {
					return fmt.Errorf("dev cleanup lock: %w; preserving retired guard failed: %v", err, guardErr)
				}
			}
			return err
		}
		defer func() {
			if unlockDev != nil {
				unlockDev()
			}
		}()
		// Publish the existing guard before removing locks and generated roots.
		// A late dev publisher must reject this folder, including the short
		// guard-removal window when its collection boundary is already gone.
		if !runtimeTarget.AdHoc {
			if err := writeRetiredMarker(target, nil); err != nil {
				return err
			}
		}
		if err := removeRetiredGeneratedFiles(target, runtimeTarget.AdHoc); err != nil {
			return err
		}
		if !runtimeTarget.AdHoc {
			finished, err := removeRetiredFolder(target)
			if err != nil {
				return err
			}
			result.FolderRemoved, result.Leftovers = finished.FolderRemoved, finished.Leftovers
			result.Warnings = append(result.Warnings, finished.Warnings...)
		} else if err := os.Remove(target); err == nil {
			result.FolderRemoved = true
		} else {
			leftovers, readErr := retireLeftovers(target)
			if readErr != nil {
				return fmt.Errorf("remove collection folder: %w", err)
			}
			result.Leftovers = leftovers
			// Ad-hoc folders belong to the user and may be initialized again.
		}
		unlockDev()
		unlockDev = nil
		if !opt.Self {
			if err := c.RunHook("retire.post", values); err != nil {
				return err
			}
		}
		return nil
	})
	return result, err
}

// RetirePreflight checks the same deletion blockers as RetireCollection without
// running hooks or removing files. The cleanup worker repeats it before acting.
func (c *Context) RetirePreflight(opt RetireOptions) (RetireResult, error) {
	result, _, err := c.retirePreflight(opt)
	return result, err
}

func (c *Context) retirePreflight(opt RetireOptions) (RetireResult, []retireWorktree, error) {
	result := RetireResult{Removed: []string{}}
	if !collectionNamePattern.MatchString(opt.Name) {
		return result, nil, fmt.Errorf("invalid collection name %q", opt.Name)
	}
	if opt.Name == filepath.Base(c.Collection) && !opt.Self {
		return result, nil, fmt.Errorf("cannot retire the collection running this command")
	}
	target := filepath.Join(c.Workspace, opt.Name)
	if info, err := os.Lstat(target); err != nil {
		return result, nil, err
	} else if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return result, nil, fmt.Errorf("collection path must be a directory: %s", target)
	}
	targetContext, err := OpenCollection(target)
	if err != nil {
		return result, nil, err
	}
	result.Collection = target
	if _, err := preflightDevRetirement(target); err != nil {
		return result, nil, err
	}
	// Harness-based leftovers need a guard after teardown; ad-hoc folders do not.
	if !targetContext.AdHoc {
		path := filepath.Join(target, RetiredMarkerFile)
		if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
			return result, nil, fmt.Errorf("retirement blocked: %s is not a regular marker file", path)
		} else if err != nil && !os.IsNotExist(err) {
			return result, nil, err
		}
	}
	// Refuse before hooks run or the workspace closes.
	if err := targetContext.runtimeConflict(); err != nil {
		return result, nil, err
	}
	worktrees, problems, err := inspectRetireWorktrees(target)
	if err != nil {
		return result, nil, err
	}
	if targetContext.AdHoc {
		if err := preflightAdHocRetirement(target, worktrees); err != nil {
			return result, nil, err
		}
	}
	if len(problems) > 0 && !opt.Force {
		return result, nil, fmt.Errorf("retirement blocked: %s (use --force only after confirming the work is disposable)", strings.Join(problems, "; "))
	}
	if len(problems) > 0 {
		result.Warnings = append(result.Warnings, problems...)
	}
	if _, err := os.Stat(filepath.Join(target, "HANDOFF.md")); err == nil {
		result.Warnings = append(result.Warnings, "unconsumed HANDOFF.md is being removed")
	}
	if _, err := os.Stat(filepath.Join(target, ".harness-backups")); err == nil {
		result.Warnings = append(result.Warnings, ".harness-backups remains in the collection folder")
	}
	return result, worktrees, nil
}

func inspectRetireWorktrees(target string) ([]retireWorktree, []string, error) {
	entries, err := os.ReadDir(target)
	if err != nil {
		return nil, nil, err
	}
	worktrees := []retireWorktree{}
	problems := []string{}
	for _, entry := range entries {
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		path := filepath.Join(target, entry.Name())
		gitEntry, err := os.Lstat(filepath.Join(path, ".git"))
		if os.IsNotExist(err) {
			continue
		} else if err != nil {
			return nil, nil, err
		}
		if !gitEntry.Mode().IsRegular() {
			return nil, nil, fmt.Errorf("%s is not a linked Git worktree", path)
		}
		top, err := gitOutput("-C", path, "rev-parse", "--show-toplevel")
		resolvedTop, topErr := filepath.EvalSymlinks(top)
		resolvedPath, pathErr := filepath.EvalSymlinks(path)
		if err != nil || topErr != nil || pathErr != nil || resolvedTop != resolvedPath {
			return nil, nil, fmt.Errorf("%s is not an independent Git worktree", path)
		}
		owner, err := gitOutput("-C", path, "rev-parse", "--path-format=absolute", "--git-common-dir")
		if err != nil {
			return nil, nil, err
		}
		if info, err := os.Stat(owner); err != nil || !info.IsDir() {
			return nil, nil, fmt.Errorf("cannot find Git owner for %s", path)
		}
		worktrees = append(worktrees, retireWorktree{name: entry.Name(), path: path, owner: owner})
		status, err := gitOutput("-C", path, "status", "--porcelain")
		if err != nil {
			return nil, nil, err
		}
		if status != "" {
			problems = append(problems, entry.Name()+" has uncommitted changes")
		}
		unpushed, err := gitOutput("-C", path, "log", "--oneline", "HEAD", "--not", "--remotes")
		if err != nil {
			return nil, nil, err
		}
		if unpushed != "" {
			problems = append(problems, entry.Name()+" has commits absent from remote refs")
		}
	}
	// Teardown product repositories before their harness, which may supply
	// collection-level configuration used by product hooks.
	sort.Slice(worktrees, func(i, j int) bool {
		if worktrees[i].name == "harness" {
			return false
		}
		if worktrees[j].name == "harness" {
			return true
		}
		return worktrees[i].name < worktrees[j].name
	})
	return worktrees, problems, nil
}

func (c *Context) closeRetiredWorkspace(name string) (bool, string) {
	return c.closeRetiredWorkspaceID(name, "")
}

func (c *Context) closeRetiredWorkspaceID(name, expectedID string) (bool, string) {
	herdr, err := exec.LookPath("herdr")
	if err != nil {
		return false, ""
	}
	session := os.Getenv("HARNESS_HERDR_SESSION")
	if session == "" {
		session = c.Config.Herdr.Session
	}
	if session == "" {
		session = strings.TrimSuffix(strings.TrimSuffix(filepath.Base(c.Workspace), "-harness"), "-wtc")
	}
	output, err := exec.Command(herdr, "--session", session, "workspace", "list").Output()
	if err != nil {
		return false, "" // No running session is normal.
	}
	var listing struct {
		Result struct {
			Workspaces []struct {
				Label string `json:"label"`
				ID    string `json:"workspace_id"`
			} `json:"workspaces"`
		} `json:"result"`
	}
	if err := json.Unmarshal(output, &listing); err != nil {
		return false, fmt.Sprintf("could not read herdr workspaces: %v", err)
	}
	for _, workspace := range listing.Result.Workspaces {
		if workspace.Label != name || expectedID != "" && workspace.ID != expectedID {
			continue
		}
		if output, err := exec.Command(herdr, "--session", session, "workspace", "close", workspace.ID).CombinedOutput(); err != nil {
			return false, fmt.Sprintf("could not close herdr workspace %s: %v: %s", workspace.ID, err, strings.TrimSpace(string(output)))
		}
		return true, ""
	}
	if expectedID != "" {
		return false, "target Herdr workspace identity changed; no workspace closed"
	}
	return false, ""
}

func removeRetiredGeneratedFiles(target string, adHoc bool) error {
	files := []string{
		"HANDOFF.md", ".env.collection", ".env.collection.local", "mise.toml", ".DS_Store",
		"WTC-SCOPE.md", "SCOPE.md", ".env.toolchain",
		".wtc-runtime.lock", ".wtc-status-watchers.lock",
		".wtc-prs", ".wtc-prs.lock", ".last-wtc-status.yml", ".wtc-status.json", ".wtc-status.md",
		".wtc-catch-up.json", ".wtc-catch-up.json.md", ReviewModeFile,
		".taba-prs", ".last-taba-status.yml", ".taba-status.json", ".taba-status.md",
		".taba-catch-up.json", ".taba-catch-up.json.md",
		".taba-review-mode", ".taba-runtime.lock", ".taba-status-watchers.lock",
		".wtc-dev-mode.json", ".wtc-dev-mode.lock", ".wtc-dev-build.lock",
		".taba-dev-mode.json", ".taba-dev-mode.lock", ".taba-dev-build.lock",
	}
	if adHoc {
		// An ad-hoc registry is remembered per user. Hand-written local
		// hooks and config stay behind as leftovers for inspection.
		registries, err := adHocRetireRegistryFiles(target)
		if err != nil {
			return err
		}
		files = append(files, registries...)
	}
	for _, name := range files {
		if err := os.Remove(filepath.Join(target, name)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	if err := removeManagedAgentFiles(target); err != nil {
		return err
	}
	if err := removeManagedCollectionEntry(target); err != nil {
		return err
	}
	for _, name := range []string{".wtc", ".taba"} {
		root := filepath.Join(target, name)
		info, err := os.Lstat(root)
		if os.IsNotExist(err) {
			continue
		} else if err != nil {
			return err
		}
		if !info.IsDir() {
			continue // Linked roots and unknown files stay for inspection.
		}
		if info, err := os.Lstat(filepath.Join(root, "runtime", "manifest.json")); err == nil && info.Mode().IsRegular() {
			if err := os.RemoveAll(filepath.Join(root, "runtime")); err != nil {
				return err
			}
		}
		// Services enlisted with wtc svc belong to this collection alone.
		if info, err := os.Lstat(filepath.Join(root, "svc")); err == nil && info.IsDir() {
			if err := os.RemoveAll(filepath.Join(root, "svc")); err != nil {
				return err
			}
		}
		// review-hooks holds the pre-push hook of a review-only collection.
		for _, generated := range []string{"instructions", "skills", "dev", "dev-generations", "review-hooks"} {
			if err := os.RemoveAll(filepath.Join(root, generated)); err != nil {
				return err
			}
		}
		config := filepath.Join(root, "config")
		if info, err := os.Lstat(config); err == nil && info.IsDir() {
			_ = os.Remove(config) // Only when no hand-written configuration remains.
		}
		_ = os.Remove(root) // Other namespaces and local files stay for inspection.
	}
	return nil
}

// A hidden registry must not become active when the selected one is removed.
// Preflight every slot before teardown, and again before deleting any metadata.
// Registry leaf links can be removed locally; linked parents cannot be followed.
func adHocRetireRegistryFiles(target string) ([]string, error) {
	var files []string
	for _, root := range []string{".wtc", ".taba"} {
		rel := filepath.Join(root, "config", adHocRegistryFile)
		path := filepath.Join(target, rel)
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			continue
		} else if err != nil {
			return nil, err
		}
		if err := safeGeneratedParent(target, filepath.Dir(path)); err != nil {
			return nil, fmt.Errorf("cannot retire local registry: %w", err)
		}
		if !info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 {
			return nil, fmt.Errorf("registry slot must be a regular file or link: %s", path)
		}
		files = append(files, rel)
	}
	return files, nil
}

func preflightAdHocRetirement(target string, worktrees []retireWorktree) error {
	if _, err := adHocRetireRegistryFiles(target); err != nil {
		return err
	}
	// A retained harness marker is another discovery source. Owned worktrees
	// disappear during teardown; refuse before teardown when it would remain.
	harness := filepath.Join(target, "harness")
	if info, err := os.Stat(harness); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	} else if !info.IsDir() {
		return nil
	}
	registry := filepath.Join(harness, ".harness-repos.yml")
	if _, err := os.Lstat(registry); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	for _, wt := range worktrees {
		if wt.path == harness {
			return nil
		}
	}
	return fmt.Errorf("retirement blocked: an unowned harness registry would keep the collection active: %s", registry)
}

func removeManagedCollectionEntry(target string) error {
	entry := filepath.Join(target, "AGENTS.md")
	if link, err := os.Readlink(entry); err == nil &&
		(link == "harness/collection-AGENTS.md" || link == ".wtc/collection-AGENTS.md" ||
			link == ".taba/collection-AGENTS.md" || link == ".wtc/config/collection-AGENTS.md" || link == ".taba/config/collection-AGENTS.md") {
		if err := os.Remove(entry); err != nil {
			return err
		}
	}
	envrc := filepath.Join(target, ".envrc")
	if info, err := os.Lstat(envrc); err == nil && info.Mode().IsRegular() {
		data, err := os.ReadFile(envrc)
		if err != nil {
			return err
		}
		defaultBody, err := ReadDefault("agent-envrc")
		if err != nil {
			return err
		}
		if string(data) == string(defaultBody) || strings.HasPrefix(string(data), "# Generated by harness/tools/link-skills.sh.") || strings.HasPrefix(string(data), "# Generated by wtc skills render.") {
			if err := os.Remove(envrc); err != nil {
				return err
			}
		}
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, name := range []string{".wtc", ".taba"} {
		root := filepath.Join(target, name)
		info, err := os.Lstat(root)
		if os.IsNotExist(err) {
			continue
		} else if err != nil {
			return err
		}
		if !info.IsDir() {
			continue
		}
		fallback := filepath.Join(root, "collection-AGENTS.md")
		info, err = os.Lstat(fallback)
		if os.IsNotExist(err) {
			continue
		} else if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			continue
		}
		data, err := os.ReadFile(fallback)
		if err != nil {
			return err
		}
		defaultBody, err := ReadDefault("collection-AGENTS.md")
		if err != nil {
			return err
		}
		if string(data) == string(defaultBody) {
			if err := os.Remove(fallback); err != nil {
				return err
			}
		}
		_ = os.Remove(root)
	}
	return nil
}

// Rendering preserves local agent overrides and merges MCP settings, so
// retirement must remove only entries it can identify as generated.
func removeManagedAgentFiles(target string) error {
	harness := filepath.Base(filepath.Join(target, "harness"))
	for _, root := range []string{".claude/skills", ".agents/skills"} {
		dir := filepath.Join(target, root)
		entries, err := os.ReadDir(dir)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		for _, entry := range entries {
			path := filepath.Join(dir, entry.Name())
			link, err := os.Readlink(path)
			if err == nil && managedSkillTarget(link, harness) {
				if err := os.Remove(path); err != nil {
					return err
				}
			}
		}
		_ = os.Remove(dir)
	}
	for _, source := range []string{harness, AdHocConfigDir, ".taba/config"} {
		for path, want := range map[string]string{
			".grok/hooks/wtc-agent-env.json": "../../" + source + "/hooks/agent-env.json",
			".claude/settings.json":          "../" + source + "/hooks/agent-env.json",
			".cursor/hooks.json":             "../" + source + "/hooks/agent-env.json",
		} {
			full := filepath.Join(target, path)
			if link, err := os.Readlink(full); err == nil && link == want {
				if err := os.Remove(full); err != nil {
					return err
				}
			}
		}
	}
	for _, path := range []string{".mcp.json", ".cursor/mcp.json", ".codex/config.toml"} {
		full := filepath.Join(target, path)
		data, err := os.ReadFile(full)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		managed := false
		if strings.HasSuffix(path, ".json") {
			managed = isOnlyGeneratedMCPJSON(data)
		} else {
			managed = isOnlyGeneratedMCPTOML(data)
		}
		if managed {
			if err := os.Remove(full); err != nil {
				return err
			}
		}
	}
	for _, name := range []string{".grok/hooks", ".grok", ".claude", ".agents", ".cursor", ".codex"} {
		_ = os.Remove(filepath.Join(target, name))
	}
	return nil
}

func isOnlyGeneratedMCPJSON(data []byte) bool {
	var file struct {
		Generated string                     `json:"_generated"`
		Names     []string                   `json:"_wtcGeneratedServers"`
		Servers   map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &file); err != nil || !strings.HasPrefix(file.Generated, "wtc mcp render") {
		return false
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return false
	}
	for key := range fields {
		if key != "_generated" && key != "_wtcGeneratedServers" && key != "mcpServers" {
			return false
		}
	}
	if len(file.Names) != len(file.Servers) {
		return false
	}
	for _, name := range file.Names {
		if _, ok := file.Servers[name]; !ok {
			return false
		}
	}
	return true
}

func isOnlyGeneratedMCPTOML(data []byte) bool {
	const begin = "# BEGIN WTC MCP SERVERS"
	const end = "# END WTC MCP SERVERS"
	content := strings.TrimSpace(string(data))
	return strings.HasPrefix(content, begin) && strings.HasSuffix(content, end) &&
		strings.Count(content, begin) == 1 && strings.Count(content, end) == 1
}
