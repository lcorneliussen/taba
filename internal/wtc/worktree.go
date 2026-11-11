package wtc

import (
	"bufio"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"taba.sh/taba/internal/branding"
	"taba.sh/taba/internal/envcompat"
)

func gitOutput(args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

func (c *Context) Repository(name string) (Repo, error) {
	for _, repo := range c.Registry.Repos {
		if repo.Answers(name) {
			return repo, nil
		}
	}
	return Repo{}, fmt.Errorf("repository %q is not in the registry", name)
}

// sameRepository reports whether two names are one registry repository, by
// name or alias, or are equal.
func (c *Context) sameRepository(a, b string) bool {
	if a == b {
		return true
	}
	repo, err := c.Repository(a)
	return err == nil && repo.Answers(b)
}

// WorktreeDir is the sibling folder for a repository name or alias: the
// folder of that exact name when present, else the first present folder under
// the repository's name or aliases, else the folder for the name as given.
// Collections created before a rename keep their folder under the old name.
func (c *Context) WorktreeDir(name string) string {
	exact := filepath.Join(c.Collection, name)
	if _, err := os.Lstat(exact); err == nil {
		return exact
	}
	if repo, err := c.Repository(name); err == nil {
		for _, candidate := range repo.Names() {
			path := filepath.Join(c.Collection, candidate)
			if _, err := os.Lstat(path); err == nil {
				return path
			}
		}
	}
	return exact
}

// HarnessRepoName resolves the registry identity of harness/, which need not
// be named harness in a downstream registry. An ad-hoc collection has none.
func (c *Context) HarnessRepoName() (string, error) {
	if c.AdHoc {
		return "", nil
	}
	if name := envcompat.Getenv("WTC_HARNESS_REPO"); name != "" {
		if _, err := c.Repository(name); err != nil {
			return "", err
		}
		return name, nil
	}
	owner, err := gitOutput("-C", c.Harness, "rev-parse", "--git-common-dir")
	if err == nil {
		name := strings.TrimSuffix(filepath.Base(owner), ".git")
		if _, err := c.Repository(name); err == nil {
			return name, nil
		}
	}
	remote, err := gitOutput("-C", c.Harness, "remote", "get-url", "origin")
	if err == nil {
		for _, repo := range c.Registry.Repos {
			if repo.Remote == remote {
				return repo.Name, nil
			}
		}
	}
	return "", fmt.Errorf("cannot infer harness repository; set WTC_HARNESS_REPO")
}

func (c *Context) bareFor(name string) (string, error) {
	repo, err := c.Repository(name)
	if err != nil {
		return "", err
	}
	if c.AdHoc {
		if repo.Owner != "" {
			return repo.Owner, nil
		}
		return userOwnerPath(repo.Remote)
	}
	data, err := os.ReadFile(filepath.Join(c.Harness, ".harness-repos"))
	if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	for _, candidate := range repo.Names() {
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, candidate+"=") {
				path := strings.TrimPrefix(line, candidate+"=")
				if path != "" {
					return path, nil
				}
			}
		}
	}
	// A renamed repository keeps using the owner cloned under an earlier name:
	// worktrees in every collection link into it, so it is never moved.
	for _, candidate := range repo.Names() {
		path := filepath.Join(c.Workspace, ".bare", candidate+".git")
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
	}
	return filepath.Join(c.Workspace, ".bare", repo.Name+".git"), nil
}

// remoteRepoParts splits a URL-form remote (scheme://, including file://, or
// scp-style user@host:path) into its host and owner path and the repository
// name. Plain filesystem paths and unrecognised forms are not split.
func remoteRepoParts(remote string) (place, name string, ok bool) {
	remote = strings.TrimSpace(remote)
	if !strings.Contains(remote, "://") && !scpRemote.MatchString(remote) {
		return "", "", false
	}
	normalized := normalizeCatchUpRemote(remote)
	i := strings.LastIndex(normalized, "/")
	if i <= 0 || i == len(normalized)-1 {
		return "", "", false
	}
	return strings.ToLower(normalized[:i]), normalized[i+1:], true
}

// syncOwnerOrigin points a shared owner's origin at the registry remote after
// a forge rename: both are URLs on the same host and owner, origin still names
// the repository by its registry name or an alias, and the registry remote
// uses a new name. Everything else is left alone: spellings of one repository
// (SSH or HTTPS, a .git suffix), forks and transfers, other repositories of
// the same owner such as a renamed mirror, and plain filesystem paths (a
// file:// URL is a URL and follows the rule). A collection on an
// older registry revision does not set it back, since the new name is not one
// of its names. It reports whether origin changed.
func syncOwnerOrigin(owner string, repo Repo) (bool, error) {
	if repo.Remote == "" || repo.Owner != "" {
		return false, nil
	}
	current, err := gitOutput("--git-dir="+owner, "remote", "get-url", "origin")
	if err != nil {
		return false, nil
	}
	fromPlace, fromName, fromOK := remoteRepoParts(current)
	toPlace, toName, toOK := remoteRepoParts(repo.Remote)
	if !fromOK || !toOK || fromPlace != toPlace || strings.EqualFold(fromName, toName) {
		return false, nil
	}
	known := false
	for _, name := range repo.Names() {
		known = known || strings.EqualFold(fromName, name)
	}
	if !known {
		return false, nil
	}
	if _, err := gitOutput("--git-dir="+owner, "remote", "set-url", "origin", repo.Remote); err != nil {
		return false, err
	}
	return true, nil
}

func (c *Context) ensureBare(repo Repo) (string, error) {
	bare, err := c.bareFor(repo.Name)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(bare); os.IsNotExist(err) {
		if repo.Owner != "" {
			// An existing checkout is owned by the user; never re-create it.
			return "", fmt.Errorf("owner of %q is missing: %s", repo.Name, bare)
		}
		if repo.Remote == "" {
			return "", fmt.Errorf("repository %q has no remote", repo.Name)
		}
		if _, err := gitOutput("clone", "--bare", repo.Remote, bare); err != nil {
			return "", err
		}
		if _, err := gitOutput("--git-dir="+bare, "config", "remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/*"); err != nil {
			return "", err
		}
	} else if err != nil {
		return "", err
	} else if !c.AdHoc {
		changed, err := syncOwnerOrigin(bare, repo)
		if err != nil {
			return "", err
		}
		if changed {
			fmt.Fprintf(os.Stderr, "%s: origin now follows the registry remote %s\n", repo.Name, repo.Remote)
		}
	}
	if _, err := gitOutput("--git-dir="+bare, "fetch", "--prune", "origin"); err != nil {
		return "", err
	}
	return bare, nil
}

// AddWorktree checks out the current development tip without claiming a branch.
// An explicit branch is used for a PR head or the uncommon -b override.
func (c *Context) AddWorktree(name, directory, destination, branch string) error {
	repo, err := c.Repository(name)
	if err != nil {
		return err
	}
	if !validRepoName.MatchString(directory) {
		return fmt.Errorf("invalid worktree directory %q", directory)
	}
	bare, err := c.ensureBare(repo)
	if err != nil {
		return err
	}
	ref := repo.DefaultRef
	if ref == "" {
		ref = "origin/main"
	}
	if _, err := gitOutput("--git-dir="+bare, "rev-parse", "--verify", ref); err != nil {
		return fmt.Errorf("%s development tip %s is unavailable: %w", name, ref, err)
	}
	path := filepath.Join(destination, directory)
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("worktree path already exists: %s", path)
	} else if !os.IsNotExist(err) {
		return err
	}
	args := []string{"--git-dir=" + bare, "worktree", "add"}
	if branch == "" {
		args = append(args, "--detach", path, ref)
	} else {
		if _, err := gitOutput("check-ref-format", "--branch", branch); err != nil {
			return fmt.Errorf("invalid branch %q: %w", branch, err)
		}
		if _, err := gitOutput("--git-dir="+bare, "rev-parse", "--verify", "refs/heads/"+branch); err == nil {
			args = append(args, path, branch)
		} else if _, err := gitOutput("--git-dir="+bare, "rev-parse", "--verify", "refs/remotes/origin/"+branch); err == nil {
			args = append(args, "-b", branch, path, "origin/"+branch)
		} else {
			args = append(args, "-b", branch, path, ref)
		}
	}
	if _, err := gitOutput(args...); err != nil {
		return err
	}
	trustWorktreeMise(path)
	return nil
}

type PRWorktreeCheckout struct {
	LocalBranch string
	PushRemote  string
}

// AddPRWorktree fetches GitHub's PR head and verifies its source branch.
// A missing head must never turn into a branch at the development tip.
func (c *Context) AddPRWorktree(name, directory, destination, branch, number, expectedSHA, headRemote string) (PRWorktreeCheckout, error) {
	result := PRWorktreeCheckout{LocalBranch: branch, PushRemote: "origin"}
	if !prNumber.MatchString(number) || !validRepoName.MatchString(directory) || branch == "" {
		return result, fmt.Errorf("invalid PR worktree identity")
	}
	decoded, err := hex.DecodeString(expectedSHA)
	if err != nil || (len(decoded) != 20 && len(decoded) != 32) {
		return result, fmt.Errorf("invalid PR head commit")
	}
	if _, err := gitOutput("check-ref-format", "--branch", branch); err != nil {
		return result, fmt.Errorf("invalid PR head branch %q: %w", branch, err)
	}
	repo, err := c.Repository(name)
	if err != nil {
		return result, err
	}
	bare, err := c.ensureBare(repo)
	if err != nil {
		return result, err
	}
	ref := "refs/remotes/wtc-pr/" + number
	if _, err := gitOutput("--git-dir="+bare, "fetch", "origin", "+refs/pull/"+number+"/head:"+ref); err != nil {
		return result, fmt.Errorf("fetch PR head #%s: %w", number, err)
	}
	head, err := gitOutput("--git-dir="+bare, "rev-parse", "--verify", ref)
	if err != nil || !strings.EqualFold(head, expectedSHA) {
		return result, fmt.Errorf("PR head changed while creating collection; expected %s, fetched %s", expectedSHA, head)
	}
	remote := "origin"
	if headRemote != "" {
		remote = "wtc-pr-" + number
		result.PushRemote = remote
		if existing, err := gitOutput("--git-dir="+bare, "remote", "get-url", remote); err == nil {
			if existing != headRemote {
				return result, fmt.Errorf("PR head remote %s already points elsewhere", remote)
			}
		} else if _, err := gitOutput("--git-dir="+bare, "remote", "add", remote, headRemote); err != nil {
			return result, err
		}
	}
	trackingRef := "refs/remotes/" + remote + "/" + branch
	if _, err := gitOutput("--git-dir="+bare, "fetch", remote, "+refs/heads/"+branch+":"+trackingRef); err != nil {
		return result, fmt.Errorf("fetch pushable PR branch from %s: %w", remote, err)
	}
	trackingHead, err := gitOutput("--git-dir="+bare, "rev-parse", "--verify", trackingRef)
	if err != nil || !strings.EqualFold(trackingHead, expectedSHA) {
		return result, fmt.Errorf("PR source branch changed while creating collection; expected %s, fetched %s", expectedSHA, trackingHead)
	}
	path := filepath.Join(destination, directory)
	if _, err := os.Lstat(path); err == nil {
		return result, fmt.Errorf("worktree path already exists: %s", path)
	} else if !os.IsNotExist(err) {
		return result, err
	}
	occupied, err := gitOutput("--git-dir="+bare, "worktree", "list", "--porcelain")
	if err != nil {
		return result, err
	}
	branchOccupied := strings.Contains("\n"+occupied+"\n", "\nbranch refs/heads/"+branch+"\n")
	localHead, localErr := gitOutput("--git-dir="+bare, "rev-parse", "--verify", "refs/heads/"+branch)
	useAlternate := branchOccupied || localErr == nil && !strings.EqualFold(localHead, expectedSHA)
	if useAlternate {
		// A branch may be checked out in only one worktree. Keep the review
		// collection usable with a distinct local name and an explicit push
		// target in its launch note.
		base := "wtc-pr-" + number + "-review"
		for suffix := 0; ; suffix++ {
			candidate := base
			if suffix > 0 {
				candidate = fmt.Sprintf("%s-%d", base, suffix+1)
			}
			if _, err := gitOutput("--git-dir="+bare, "rev-parse", "--verify", "refs/heads/"+candidate); err != nil {
				result.LocalBranch = candidate
				break
			}
		}
	}
	args := []string{"--git-dir=" + bare, "worktree", "add"}
	if localErr == nil && !useAlternate {
		args = append(args, path, branch)
	} else {
		args = append(args, "-b", result.LocalBranch, path, remote+"/"+branch)
	}
	if _, err := gitOutput(args...); err != nil {
		return result, err
	}
	if _, err := gitOutput("-C", path, "branch", "--set-upstream-to", remote+"/"+branch, result.LocalBranch); err != nil {
		return result, err
	}
	trustWorktreeMise(path)
	return result, nil
}

func trustWorktreeMise(path string) {
	if _, err := os.Stat(filepath.Join(path, "mise.toml")); err == nil {
		if mise, err := exec.LookPath("mise"); err == nil {
			cmd := exec.Command(mise, "trust", filepath.Join(path, "mise.toml"))
			cmd.Dir = path
			_ = cmd.Run() // Matches the shell bootstrap: a missing trust is reported by later use.
		}
	}
}

// RunRepoInit preserves the repository lifecycle hook contract. Failure is
// reported but does not discard an otherwise valid new worktree.
func RunRepoInit(worktree string) {
	runRepoLifecycle(worktree, "init")
}

// RunRepoTeardown reports hook failures but allows retirement to continue.
func RunRepoTeardown(worktree string) {
	runRepoLifecycle(worktree, "teardown")
}

func runRepoLifecycle(worktree, hook string) {
	if mise, err := exec.LookPath("mise"); err == nil {
		if _, err := os.Stat(filepath.Join(worktree, "mise.toml")); err == nil {
			cmd := exec.Command(mise, "tasks", "ls")
			cmd.Dir = worktree
			if output, err := cmd.Output(); err == nil {
				scanner := bufio.NewScanner(strings.NewReader(string(output)))
				for scanner.Scan() {
					if fields := strings.Fields(scanner.Text()); len(fields) > 0 && fields[0] == "harness:"+hook {
						runRepoCommand(worktree, hook, mise, "run", "harness:"+hook)
						return
					}
				}
			}
		}
	}
	path := filepath.Join(worktree, ".harness", hook+".sh")
	if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() && info.Mode()&0111 != 0 {
		runRepoCommand(worktree, hook, path)
	}
}

func runRepoCommand(worktree, hook, program string, args ...string) {
	collection := filepath.Dir(worktree)
	// mise loads the parent config itself, but direct script hooks need the same
	// generated and local collection env when mise is absent. Source the files
	// as data in a child shell, then replace it with the actual hook process.
	argv := append([]string{"-c", `set -a; [ ! -f "$1" ] || . "$1"; [ ! -f "$2" ] || . "$2"; shift 2; exec "$@"`, "wtc-" + hook, filepath.Join(collection, ".env.collection"), filepath.Join(collection, ".env.collection.local"), program}, args...)
	cmd := exec.Command("/bin/sh", argv...)
	cmd.Dir = worktree
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, branding.Text("{{command}}: warning: %s hook for %s failed: %v\n"), hook, filepath.Base(worktree), err)
	}
}
