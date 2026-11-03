package wtc

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type SecretLinkOptions struct {
	Repo        string
	IncludeProd bool
	DryRun      bool
}

type SecretLinkResult struct {
	Linked      int      `json:"linked"`
	Current     int      `json:"already_current"`
	Refused     int      `json:"refused"`
	ProdSkipped int      `json:"prod_skipped"`
	BackedUp    int      `json:"backed_up"`
	Actions     []string `json:"actions"`
	DryRun      bool     `json:"dry_run"`
}

func (c *Context) LinkSecrets(opt SecretLinkOptions) (SecretLinkResult, error) {
	result := SecretLinkResult{DryRun: opt.DryRun}
	harnessRepo, _ := c.HarnessRepoName()
	if opt.Repo == "harness" && harnessRepo != "" {
		opt.Repo = harnessRepo
	}
	if opt.Repo != "" && !validRepoName.MatchString(opt.Repo) {
		return result, fmt.Errorf("invalid repository name %q", opt.Repo)
	}
	root, err := filepath.Abs(c.ConfigRoot)
	if err != nil {
		return result, err
	}
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	prod, err := c.secretProdPaths()
	if err != nil {
		return result, err
	}
	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() || !validRepoName.MatchString(name) || opt.Repo != "" && !c.sameRepository(opt.Repo, name) {
			continue
		}
		worktreeName := name
		if name == harnessRepo {
			worktreeName = "harness"
		}
		worktree := filepath.Join(c.Collection, worktreeName)
		if worktreeName != "harness" {
			worktree = c.WorktreeDir(worktreeName)
		}
		info, err := os.Stat(worktree)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return result, err
		}
		if !info.IsDir() {
			continue
		}
		top, err := exec.Command("git", "-C", worktree, "rev-parse", "--show-toplevel").Output()
		resolvedWorktree, resolveErr := filepath.EvalSymlinks(worktree)
		resolvedTop, topErr := filepath.EvalSymlinks(strings.TrimSpace(string(top)))
		if err != nil || resolveErr != nil || topErr != nil || resolvedTop != resolvedWorktree {
			result.Actions = append(result.Actions, name+": not a git worktree; skipped")
			continue
		}
		sourceRoot := filepath.Join(root, name)
		err = filepath.WalkDir(sourceRoot, func(source string, item fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if item.IsDir() {
				return nil
			}
			if !item.Type().IsRegular() && item.Type()&os.ModeSymlink == 0 {
				return nil
			}
			rel, err := filepath.Rel(sourceRoot, source)
			if err != nil {
				return err
			}
			if strings.ContainsAny(rel, "\r\n\x00") {
				return fmt.Errorf("secret path contains a newline or NUL")
			}
			key := name + "/" + filepath.ToSlash(rel)
			if prod[key] && !opt.IncludeProd {
				result.ProdSkipped++
				result.Actions = append(result.Actions, "skip prod-capable: "+key)
				return nil
			}
			dest := filepath.Join(worktree, rel)
			if err := safeSecretParent(worktree, filepath.Dir(dest)); err != nil {
				return err
			}
			ignored, err := gitIgnored(worktree, rel)
			if err != nil {
				return err
			}
			if !ignored {
				result.Refused++
				result.Actions = append(result.Actions, "REFUSED (not gitignored): "+key)
				return nil
			}
			old, err := os.Lstat(dest)
			if err != nil && !os.IsNotExist(err) {
				return err
			}
			if err == nil && old.Mode()&os.ModeSymlink != 0 {
				target, err := os.Readlink(dest)
				if err != nil {
					return err
				}
				if target == source {
					result.Current++
					return nil
				}
			} else if err == nil && !old.Mode().IsRegular() {
				return fmt.Errorf("secret target is neither a file nor a symlink: %s", key)
			}
			if opt.DryRun {
				result.Linked++
				result.Actions = append(result.Actions, "would link: "+key)
				return nil
			}
			if err == nil && old.Mode().IsRegular() {
				backup := filepath.Join(c.Collection, ".harness-backups", name, rel+"."+time.Now().UTC().Format("20060102T150405.000000000Z"))
				if err := os.MkdirAll(filepath.Dir(backup), 0700); err != nil {
					return err
				}
				if err := os.Rename(dest, backup); err != nil {
					return err
				}
				result.BackedUp++
				result.Actions = append(result.Actions, "backed up: "+key)
			} else if err == nil {
				if err := os.Remove(dest); err != nil {
					return err
				}
			}
			if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
				return err
			}
			if err := os.Symlink(source, dest); err != nil {
				return err
			}
			result.Linked++
			result.Actions = append(result.Actions, "linked: "+key)
			return nil
		})
		if err != nil {
			return result, err
		}
	}
	if result.Refused > 0 {
		return result, fmt.Errorf("%d secret targets are not gitignored", result.Refused)
	}
	return result, nil
}

func (c *Context) secretProdPaths() (map[string]bool, error) {
	prod := map[string]bool{}
	for _, path := range c.Config.Secrets.ProdPaths {
		clean, err := validSecretPath(path)
		if err != nil || !strings.Contains(clean, "/") {
			return nil, fmt.Errorf("invalid secrets.prod_paths entry %q", path)
		}
		prod[clean] = true
	}
	for _, repo := range c.Registry.Repos {
		for _, path := range repo.ProdPaths {
			clean, err := validSecretPath(path)
			if err != nil {
				return nil, fmt.Errorf("invalid prod_paths entry for %s: %q", repo.Name, path)
			}
			prod[repo.Name+"/"+clean] = true
		}
	}
	return prod, nil
}

func validSecretPath(path string) (string, error) {
	if path == "" || filepath.IsAbs(path) || strings.ContainsAny(path, "\r\n\x00") {
		return "", fmt.Errorf("invalid secret path")
	}
	clean := filepath.ToSlash(filepath.Clean(path))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("secret path escapes repository")
	}
	return clean, nil
}

func gitIgnored(worktree, rel string) (bool, error) {
	err := exec.Command("git", "-C", worktree, "check-ignore", "-q", "--", rel).Run()
	if err == nil {
		return true, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("check git ignore rule for %s: %w", rel, err)
}

func safeSecretParent(worktree, parent string) error {
	for path := parent; path != worktree; path = filepath.Dir(path) {
		if path == filepath.Dir(path) {
			return fmt.Errorf("secret target escapes worktree")
		}
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("secret target has unsafe parent %s", path)
		}
	}
	return nil
}
