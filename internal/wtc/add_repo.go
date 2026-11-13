package wtc

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type AddRepoOptions struct {
	Collection string
	Repos      []string
	Branch     string
}

type AddRepoResult struct {
	Collection string   `json:"collection"`
	Added      []string `json:"added"`
	Skipped    []string `json:"skipped,omitempty"`
}

// AddRepositories extends an existing collection using its own registry and
// lifecycle configuration. Repository init runs after the collection env,
// secrets, skills, and MCP configuration are available.
func (c *Context) AddRepositories(opt AddRepoOptions) (AddRepoResult, error) {
	result := AddRepoResult{Added: []string{}}
	if len(opt.Repos) == 0 {
		return result, fmt.Errorf("at least one repository is required")
	}
	if opt.Branch != "" {
		if _, err := gitOutput("check-ref-format", "--branch", opt.Branch); err != nil {
			return result, fmt.Errorf("invalid explicit branch: %w", err)
		}
	}
	path := c.Collection
	if opt.Collection != "" {
		if err := c.RequireWorkspace("add-repo --collection"); err != nil {
			return result, err
		}
		if !collectionNamePattern.MatchString(opt.Collection) {
			return result, fmt.Errorf("invalid collection name %q", opt.Collection)
		}
		path = filepath.Join(c.Workspace, opt.Collection)
	}
	target, err := openCollection(path, c.creatingReceiver && sameCanonicalPath(path, c.Collection))
	if err != nil {
		return result, err
	}
	result.Collection = target.Collection
	if _, err := os.Stat(filepath.Join(target.Collection, ".env.collection")); os.IsNotExist(err) {
		if _, err := target.ValidateControlRoot(); err != nil {
			return result, err
		}
	} else if err != nil {
		return result, err
	}
	if len(opt.Repos) > 1 && opt.Collection == "" {
		if _, err := target.Repository(opt.Repos[0]); err != nil {
			if info, statErr := os.Stat(filepath.Join(c.Workspace, opt.Repos[0], "harness")); statErr == nil && info.IsDir() {
				return result, fmt.Errorf("%q is a collection, not a repository; use --collection %s", opt.Repos[0], opt.Repos[0])
			}
		}
	}
	harnessRepo, err := target.HarnessRepoName()
	if err != nil {
		return result, err
	}
	seen := map[string]bool{}
	for _, name := range opt.Repos {
		if name == "harness" || name == harnessRepo {
			result.Skipped = append(result.Skipped, name)
			continue
		}
		repo, err := target.Repository(name)
		if err != nil {
			return result, err
		}
		if seen[repo.Name] {
			return result, fmt.Errorf("repository %q was requested more than once", repo.Name)
		}
		seen[repo.Name] = true
		// A sibling checked out under an earlier name is the same repository.
		for _, candidate := range repo.Names() {
			if _, err := os.Lstat(filepath.Join(target.Collection, candidate)); err == nil {
				return result, fmt.Errorf("worktree path already exists: %s", filepath.Join(target.Collection, candidate))
			} else if !os.IsNotExist(err) {
				return result, err
			}
		}
	}
	// New checkouts use the current name, even when requested by an alias.
	opt.Repos = append([]string(nil), opt.Repos...)
	for i, name := range opt.Repos {
		if repo, err := target.Repository(name); err == nil && name != "harness" && name != harnessRepo {
			opt.Repos[i] = repo.Name
		}
	}
	toAdd := make([]string, 0, len(seen))
	for _, name := range opt.Repos {
		if name != "harness" && name != harnessRepo {
			toAdd = append(toAdd, name)
		}
	}
	values := map[string]string{"repositories": strings.Join(toAdd, ","), "branch": opt.Branch}
	if err := target.RunHook("add-repo.pre", values); err != nil {
		return result, err
	}
	for _, name := range opt.Repos {
		if name == "harness" || name == harnessRepo {
			continue
		}
		if err := target.AddWorktree(name, name, target.Collection, opt.Branch); err != nil {
			if rollbackErr := target.removeAddedWorktrees(result.Added); rollbackErr != nil {
				return result, fmt.Errorf("add %s: %w; rollback failed: %v", name, err, rollbackErr)
			}
			result.Added = []string{}
			return result, fmt.Errorf("add %s: %w (earlier worktrees rolled back)", name, err)
		}
		result.Added = append(result.Added, name)
	}
	// A repository may have been added to the registry after this collection
	// was created. Refresh the environment before its init hook needs its port.
	if err := target.RunHook("env.pre", nil); err != nil {
		return result, err
	}
	env, err := target.RenderEnv()
	if err != nil {
		return result, err
	}
	oldEnv, err := os.ReadFile(filepath.Join(target.Collection, ".env.collection"))
	if err != nil && !os.IsNotExist(err) {
		return result, err
	}
	if !bytes.Equal(oldEnv, env) {
		if err := target.WriteEnv(env); err != nil {
			return result, err
		}
	}
	if err := target.EnsureEnvSupport(); err != nil {
		return result, err
	}
	if err := target.TrustMise(); err != nil {
		return result, err
	}
	if err := target.RunHook("env.post", nil); err != nil {
		return result, err
	}
	if err := target.RunHook("secrets.link.pre", nil); err != nil {
		return result, err
	}
	for _, name := range result.Added {
		if _, err := target.LinkSecrets(SecretLinkOptions{Repo: name}); err != nil {
			return result, err
		}
	}
	if err := target.RunHook("secrets.link.post", nil); err != nil {
		return result, err
	}
	if _, err := target.RenderSkills(SkillRenderOptions{}); err != nil {
		return result, err
	}
	if _, err := os.Stat(filepath.Join(target.Harness, ".mcp-servers.yml")); err == nil {
		outputs, err := target.RenderMCP()
		if err != nil {
			return result, err
		}
		if _, err := target.WriteMCP(outputs, false); err != nil {
			return result, err
		}
	} else if !os.IsNotExist(err) {
		return result, err
	}
	for _, name := range result.Added {
		target.runRepoLifecycle(filepath.Join(target.Collection, name), "init")
	}
	_, _ = target.AgentToolchainPath(true)
	if err := target.RunHook("add-repo.post", values); err != nil {
		return result, err
	}
	return result, nil
}

// removeAddedWorktrees only rolls back checkouts made by the current call,
// before any repository init hook has run. Git refuses removal if a worktree
// has acquired uncommitted files, so a failed rollback never silently drops
// data and its path is reported to the caller.
func (c *Context) removeAddedWorktrees(names []string) error {
	for i := len(names) - 1; i >= 0; i-- {
		bare, err := c.bareFor(names[i])
		if err != nil {
			return err
		}
		path := filepath.Join(c.Collection, names[i])
		if _, err := gitOutput("--git-dir="+bare, "worktree", "remove", path); err != nil {
			return fmt.Errorf("remove %s: %w", path, err)
		}
	}
	return nil
}
