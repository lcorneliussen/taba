package wtc

import (
	"archive/tar"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"taba.sh/taba/internal/branding"
)

func reviewDownstreamNames(repo Repo) []string {
	value, ok := repo.Extra["downstream"]
	if !ok {
		return nil
	}
	var names []string
	switch v := value.(type) {
	case string:
		names = strings.Fields(v)
	case []any:
		for _, item := range v {
			if name, ok := item.(string); ok {
				names = append(names, name)
			}
		}
	}
	var valid []string
	for _, name := range names {
		if validRepoName.MatchString(name) {
			valid = append(valid, name)
		}
	}
	return valid
}

func (c *Context) reviewEnlistedBranch(repo, number string) (string, error) {
	records, err := c.ListPRs()
	if err != nil {
		return "", err
	}
	branch := ""
	for _, record := range records {
		if record.Number != number || record.Repo != repo && !(repo == "harness" && record.Repo == c.Config.Harness.Name) {
			continue
		}
		if record.Branch == "" || branch != "" && branch != record.Branch {
			return "", fmt.Errorf("PR enlistment has no unique branch")
		}
		branch = record.Branch
	}
	if branch == "" {
		return "", fmt.Errorf("PR is not enlisted in this collection")
	}
	return branch, nil
}

func (c *Context) reviewBranchIsEnlisted(repo, branch string) bool {
	if branch == "" {
		return false
	}
	records, err := c.ListPRs()
	if err != nil {
		return false
	}
	for _, record := range records {
		if record.Branch == branch && (record.Repo == repo || repo == c.Config.Harness.Name && record.Repo == "harness") {
			return true
		}
	}
	return false
}

func (c *Context) reviewRegistryRepo(name string) (Repo, bool) {
	for _, repo := range c.Registry.Repos {
		if repo.Answers(name) || name == "harness" && repo.Name == c.Config.Harness.Name {
			return repo, true
		}
	}
	return Repo{}, false
}

func (c *Context) reviewRepositoryGroups(name string) (downstream, upstream []string) {
	reviewed, ok := c.reviewRegistryRepo(name)
	if !ok {
		return nil, nil
	}
	for _, consumer := range reviewDownstreamNames(reviewed) {
		if consumer != reviewed.Name {
			downstream = append(downstream, consumer)
		}
	}
	for _, repo := range c.Registry.Repos {
		for _, consumer := range reviewDownstreamNames(repo) {
			if consumer == reviewed.Name && repo.Name != reviewed.Name {
				upstream = append(upstream, repo.Name)
				break
			}
		}
	}
	return downstream, upstream
}

func (c *Context) snapshotReviewRepositories(dir, reviewed string) (downstream, upstream []string, err error) {
	ds, us := c.reviewRepositoryGroups(reviewed)
	for _, group := range []struct {
		kind  string
		names []string
		done  *[]string
	}{{"downstream", ds, &downstream}, {"upstream", us, &upstream}} {
		for _, name := range group.names {
			made, err := c.snapshotReviewRepository(dir, group.kind, name)
			if err != nil {
				return nil, nil, err
			}
			if made {
				*group.done = append(*group.done, name)
			}
		}
	}
	return downstream, upstream, nil
}

func (c *Context) snapshotReviewRepository(dir, kind, name string) (bool, error) {
	repo, ok := c.reviewRegistryRepo(name)
	if !ok {
		return false, nil
	}
	worktree := c.WorktreeDir(name)
	if name == c.Config.Harness.Name {
		worktree = c.Harness
	}
	owner := ""
	if _, err := os.Stat(filepath.Join(worktree, ".git")); err == nil {
		out, err := reviewGit(worktree, "rev-parse", "--path-format=absolute", "--git-common-dir")
		if err == nil {
			owner = strings.TrimSpace(string(out))
		}
	}
	if owner == "" {
		var err error
		owner, err = c.bareFor(repo.Name)
		if err != nil {
			return false, err
		}
	}
	if info, err := os.Stat(owner); err != nil || !info.IsDir() {
		fmt.Fprintf(os.Stderr, branding.Text("{{command}}: warning: %s %s has no Git owner; snapshot skipped\n"), kind, name)
		return false, nil
	}
	oldRef := repo.ProductionRef
	if oldRef == "" {
		oldRef = repo.DefaultRef
	}
	if oldRef == "" {
		oldRef = "origin/main"
	}
	oldSHA, err := reviewBareRef(owner, oldRef)
	if err != nil {
		fmt.Fprintf(os.Stderr, branding.Text("{{command}}: warning: %s %s ref %s unavailable; snapshot skipped\n"), kind, name, oldRef)
		return false, nil
	}
	baseDir := filepath.Join(dir, kind, name)
	if err := archiveReviewCommit(owner, oldSHA, filepath.Join(baseDir, "old")); err != nil {
		return false, err
	}
	refs := fmt.Sprintf("old=%s %s\n", oldRef, oldSHA)
	if _, err := os.Stat(filepath.Join(worktree, ".git")); err == nil {
		head, headErr := reviewRef(worktree, "HEAD")
		defaultRef := repo.DefaultRef
		if defaultRef == "" {
			defaultRef = "origin/main"
		}
		branch, branchErr := reviewGit(worktree, "branch", "--show-current")
		label := strings.TrimSpace(string(branch))
		if headErr == nil && branchErr == nil && c.reviewBranchIsEnlisted(name, label) {
			if _, ancestorErr := reviewGit(worktree, "merge-base", "--is-ancestor", head, defaultRef); ancestorErr != nil {
				if err := archiveReviewCommit(owner, head, filepath.Join(baseDir, "new")); err != nil {
					return false, err
				}
				refs += fmt.Sprintf("new=%s %s\n", label, head)
			}
		}
	}
	if err := os.WriteFile(filepath.Join(baseDir, "REFS"), []byte(refs), 0644); err != nil {
		return false, err
	}
	return true, nil
}

func reviewBareRef(owner, ref string) (string, error) {
	out, err := exec.Command("git", "--git-dir="+owner, "rev-parse", "--verify", ref+"^{commit}").Output()
	return strings.TrimSpace(string(out)), err
}

func archiveReviewCommit(owner, sha, dest string) error {
	if err := os.MkdirAll(dest, 0755); err != nil {
		return err
	}
	cmd := exec.Command("git", "--git-dir="+owner, "archive", sha)
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	abort := func(err error) error {
		pipe.Close()
		cmd.Wait()
		return err
	}
	reader := tar.NewReader(pipe)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return abort(err)
		}
		rel := filepath.Clean(filepath.FromSlash(header.Name))
		if rel == "." || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return abort(fmt.Errorf("unsafe archive path %q", header.Name))
		}
		path := filepath.Join(dest, rel)
		if err := safeReviewArchiveParent(dest, filepath.Dir(path)); err != nil {
			return abort(err)
		}
		switch header.Typeflag {
		case tar.TypeXGlobalHeader, tar.TypeXHeader:
			continue
		case tar.TypeDir:
			if err := os.MkdirAll(path, 0755); err != nil {
				return abort(err)
			}
		case tar.TypeReg, tar.TypeRegA:
			file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, os.FileMode(header.Mode)&0777)
			if err != nil {
				return abort(err)
			}
			_, copyErr := io.Copy(file, reader)
			closeErr := file.Close()
			if copyErr != nil || closeErr != nil {
				return abort(fmt.Errorf("extract %s: %v %v", rel, copyErr, closeErr))
			}
		case tar.TypeSymlink:
			if header.Linkname == "" || filepath.IsAbs(filepath.FromSlash(header.Linkname)) {
				return abort(fmt.Errorf("unsafe archive symlink %q", header.Name))
			}
			target := filepath.Join(filepath.Dir(path), filepath.FromSlash(header.Linkname))
			if relTarget, err := filepath.Rel(dest, target); err != nil || relTarget == ".." || strings.HasPrefix(relTarget, ".."+string(filepath.Separator)) {
				return abort(fmt.Errorf("unsafe archive symlink %q", header.Name))
			}
			if err := os.Symlink(header.Linkname, path); err != nil {
				return abort(err)
			}
		default:
			return abort(fmt.Errorf("unsupported archive entry %q", header.Name))
		}
	}
	return cmd.Wait()
}

func safeReviewArchiveParent(root, parent string) error {
	for path := parent; path != root; path = filepath.Dir(path) {
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return fmt.Errorf("archive parent is not a directory: %s", path)
		}
	}
	return os.MkdirAll(parent, 0755)
}

func (c *Context) copyRelatedReviewPatches(dir, reviewed, number string) (int, error) {
	records, err := c.ListPRs()
	if err != nil {
		return 0, err
	}
	count := 0
	for _, record := range records {
		if record.Repo == reviewed && record.Number == number || record.Branch == "" || !prRepoName.MatchString(record.Repo) || !prNumber.MatchString(record.Number) {
			continue
		}
		worktree := c.WorktreeDir(record.Repo)
		if record.Repo == "harness" || record.Repo == c.Config.Harness.Name {
			worktree = c.Harness
		}
		if _, err := os.Stat(filepath.Join(worktree, ".git")); err != nil {
			continue
		}
		branch, err := reviewGit(worktree, "branch", "--show-current")
		if err != nil || strings.TrimSpace(string(branch)) != record.Branch {
			continue
		}
		repo, ok := c.reviewRegistryRepo(record.Repo)
		if !ok {
			continue
		}
		defaultRef := repo.DefaultRef
		if defaultRef == "" {
			defaultRef = "origin/main"
		}
		mergeBase, err := reviewGit(worktree, "merge-base", defaultRef, "HEAD")
		if err != nil {
			continue
		}
		diff, err := reviewGit(worktree, "diff", "--no-color", strings.TrimSpace(string(mergeBase)), "HEAD")
		if err != nil || len(diff) == 0 {
			continue
		}
		if len(diff) > 400000 {
			diff = diff[:400000]
		}
		path := filepath.Join(dir, "related", record.Repo+"-pr"+record.Number+".patch")
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return count, err
		}
		if err := os.WriteFile(path, diff, 0644); err != nil {
			return count, err
		}
		count++
		if count == 20 {
			break
		}
	}
	return count, nil
}

func (c *Context) copyPrivateReviewConcerns(dir, worktree, baseSHA, changed string) (int, error) {
	layers := map[string][]byte{}
	add := func(path string, body []byte) error {
		id := strings.TrimSuffix(filepath.Base(path), ".md")
		for _, line := range strings.Split(string(body), "\n") {
			if strings.HasPrefix(line, "id:") {
				id = strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, "id:")), `"'`)
				break
			}
		}
		if !reviewConcernID.MatchString(id) {
			return fmt.Errorf("invalid concern id in %s", path)
		}
		layers[id] = body
		return nil
	}
	addDir := func(path string) error {
		entries, err := os.ReadDir(path)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if !entry.Type().IsRegular() || !strings.HasSuffix(entry.Name(), ".md") {
				continue
			}
			body, err := os.ReadFile(filepath.Join(path, entry.Name()))
			if err != nil {
				return err
			}
			if err := add(entry.Name(), body); err != nil {
				return err
			}
		}
		return nil
	}
	addGitDir := func(repoDir, sha, prefix string) error {
		paths, err := reviewGit(repoDir, "ls-tree", "-r", "--name-only", sha, prefix)
		if err != nil {
			return err
		}
		for _, path := range strings.Split(strings.TrimSpace(string(paths)), "\n") {
			if !strings.HasPrefix(path, prefix+"/") || !strings.HasSuffix(path, ".md") {
				continue
			}
			body, err := reviewGit(repoDir, "show", sha+":"+path)
			if err != nil {
				return err
			}
			if err := add(path, body); err != nil {
				return err
			}
		}
		return nil
	}
	if worktree == c.Harness {
		if err := addGitDir(worktree, baseSHA, "review/concerns"); err != nil {
			return 0, err
		}
	} else if err := addDir(filepath.Join(c.Harness, "review", "concerns")); err != nil {
		return 0, err
	}
	if len(layers) == 0 {
		paths, err := DefaultPaths()
		if err != nil {
			return 0, err
		}
		for _, path := range paths {
			if strings.HasPrefix(path, "review/concerns/") && strings.HasSuffix(path, ".md") {
				body, err := ReadDefault(path)
				if err != nil {
					return 0, err
				}
				if err := add(path, body); err != nil {
					return 0, err
				}
			}
		}
	}
	if err := addDir(filepath.Join(c.Harness, "review", "concerns.d")); err != nil {
		return 0, err
	}
	if err := addGitDir(worktree, baseSHA, ".review/concerns"); err != nil {
		return 0, err
	}
	var ids []string
	for id := range layers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	count := 0
	for _, id := range ids {
		body := layers[id]
		if !reviewConcernApplies(string(body), changed) {
			continue
		}
		if err := os.WriteFile(filepath.Join(dir, "concerns", id+".md"), body, 0644); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}
