package wtc

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"taba.sh/taba/internal/branding"
)

type ReviewBundleOptions struct {
	Repo      string
	PR        string
	Base      string
	Head      string
	Dir       string
	Round     int
	Public    bool
	Private   bool
	NoCatchUp bool
}

type ReviewManifest struct {
	Repo       string `json:"repo"`
	PR         string `json:"pr"`
	Forge      string `json:"forge"`
	Slug       string `json:"slug"`
	URL        string `json:"url"`
	BaseRef    string `json:"base_ref"`
	BaseSHA    string `json:"base_sha"`
	HeadSHA    string `json:"head_sha"`
	HeadBranch string `json:"head_branch"`
	Round      int    `json:"round"`
	RepoDir    string `json:"repo_dir"`
	Collection string `json:"collection"`
	Public     bool   `json:"public"`
	Audience   string `json:"audience"`
	ModeReason string `json:"mode_reason"`
	Downstream string `json:"downstream,omitempty"`
	Upstream   string `json:"upstream,omitempty"`
}

type ReviewBundle struct {
	Dir      string         `json:"dir"`
	Manifest ReviewManifest `json:"manifest"`
	Files    int            `json:"files"`
	Concerns int            `json:"concerns"`
	Related  int            `json:"related"`
}

func reviewGit(worktree string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", append([]string{"-C", worktree}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return out, nil
}

func reviewRef(worktree, ref string) (string, error) {
	out, err := reviewGit(worktree, "rev-parse", "--verify", ref+"^{commit}")
	return strings.TrimSpace(string(out)), err
}

func checkoutReviewBranch(worktree, branch string) error {
	if _, err := reviewGit(worktree, "check-ref-format", "--branch", branch); err != nil {
		return fmt.Errorf("invalid branch name %q", branch)
	}
	if _, err := reviewGit(worktree, "fetch", "origin", branch); err != nil {
		fmt.Fprintf(os.Stderr, branding.Text("{{command}}: warning: could not fetch PR branch %s before checkout\n"), branch)
	}
	if _, err := reviewGit(worktree, "show-ref", "--verify", "refs/heads/"+branch); err == nil {
		_, err = reviewGit(worktree, "switch", "--merge", branch)
		return err
	}
	if _, err := reviewGit(worktree, "show-ref", "--verify", "refs/remotes/origin/"+branch); err != nil {
		return fmt.Errorf("PR branch is not available locally")
	}
	_, err := reviewGit(worktree, "switch", "--merge", "-c", branch, "--track", "origin/"+branch)
	return err
}

// BuildPublicReviewBundle retains the original public, no-catch-up contract
// for callers that explicitly prepare a review for an external audience.
func (c *Context) BuildPublicReviewBundle(opt ReviewBundleOptions) (ReviewBundle, error) {
	opt.Public = true
	opt.NoCatchUp = true
	return c.BuildReviewBundle(opt)
}

func (c *Context) BuildReviewBundle(opt ReviewBundleOptions) (ReviewBundle, error) {
	if opt.Public && opt.Private {
		return ReviewBundle{}, fmt.Errorf("--public and --private cannot be combined")
	}
	worktree, slug, forge, err := c.ReviewRepo(opt.Repo)
	if err != nil {
		return ReviewBundle{}, err
	}
	reviewOnly := reviewOnlyWorktree(worktree)
	if reviewOnly {
		// The merge with the tip would be a write to the author's branch.
		opt.NoCatchUp = true
		mode, _ := ReadReviewMode(c.Collection)
		if opt.PR == "" && mode.Repo == opt.Repo {
			opt.PR = mode.PR
		} else if opt.PR != "" && mode.PR != "" && opt.PR != mode.PR {
			return ReviewBundle{}, fmt.Errorf("this collection reviews %s#%s, not PR #%s", mode.Repo, mode.PR, opt.PR)
		}
	}
	if opt.PR != "" && !prNumber.MatchString(opt.PR) {
		return ReviewBundle{}, fmt.Errorf("invalid PR number %q", opt.PR)
	}
	if opt.Head == "" {
		opt.Head = "HEAD"
	}
	branchBytes, err := reviewGit(worktree, "branch", "--show-current")
	if err != nil {
		return ReviewBundle{}, err
	}
	branch := strings.TrimSpace(string(branchBytes))
	if opt.PR == "" && branch != "" {
		if enlisted, e := c.EnlistedReviewPR(opt.Repo); e == nil {
			opt.PR = enlisted
		}
	}
	prTitle, prBody, prBase, prHead, prBranch, prURL := "", "", "", "", "", ""
	if opt.PR != "" {
		var raw []byte
		if forge == "github" {
			raw, err = exec.Command("gh", "pr", "view", opt.PR, "--repo", slug, "--json", "title,body,baseRefName,headRefOid,headRefName,url").Output()
		} else {
			raw, err = reviewBitbucketCommand(ReviewManifest{RepoDir: worktree, Slug: slug}, []string{"pr", "view", opt.PR, "--json"}, nil)
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, branding.Text("{{command}}: warning: PR #%s metadata unavailable; bundle will use local refs\n"), opt.PR)
			raw = nil
		}
		if len(raw) > 0 {
			var p struct {
				Title       string `json:"title"`
				Body        string `json:"body"`
				Description string `json:"description"`
				BaseRefName string `json:"baseRefName"`
				HeadRefOid  string `json:"headRefOid"`
				HeadRefName string `json:"headRefName"`
				URL         string `json:"url"`
				Destination struct {
					Branch struct {
						Name string `json:"name"`
					} `json:"branch"`
				} `json:"destination"`
				Source struct {
					Branch struct {
						Name string `json:"name"`
					} `json:"branch"`
					Commit struct {
						Hash string `json:"hash"`
					} `json:"commit"`
				} `json:"source"`
				Links struct {
					HTML struct {
						Href string `json:"href"`
					} `json:"html"`
				} `json:"links"`
			}
			if err := json.Unmarshal(raw, &p); err != nil {
				fmt.Fprintf(os.Stderr, branding.Text("{{command}}: warning: PR #%s metadata is invalid; bundle will use local refs\n"), opt.PR)
			} else {
				prTitle, prBody, prBase, prHead, prBranch, prURL = p.Title, p.Body, p.BaseRefName, p.HeadRefOid, p.HeadRefName, p.URL
				if prBody == "" {
					prBody = p.Description
				}
				if prBase == "" {
					prBase = p.Destination.Branch.Name
				}
				if prHead == "" {
					prHead = p.Source.Commit.Hash
				}
				if prBranch == "" {
					prBranch = p.Source.Branch.Name
				}
				if prURL == "" {
					prURL = p.Links.HTML.Href
				}
			}
		}
		if prBranch == "" {
			branchFromEnlistment, err := c.reviewEnlistedBranch(opt.Repo, opt.PR)
			if err != nil {
				return ReviewBundle{}, fmt.Errorf("cannot verify PR #%s branch without forge metadata: %w", opt.PR, err)
			}
			prBranch = branchFromEnlistment
		}
	}
	if reviewOnly && opt.PR != "" && opt.Head == "HEAD" {
		if err := followReviewHead(c.Collection, worktree, forge, opt.PR, prBranch, prBase, prHead); err != nil {
			return ReviewBundle{}, fmt.Errorf("move review worktree to PR #%s head: %w", opt.PR, err)
		}
	} else if opt.PR != "" && opt.Head == "HEAD" {
		if prBranch != "" && branch != prBranch {
			if err := checkoutReviewBranch(worktree, prBranch); err != nil {
				return ReviewBundle{}, fmt.Errorf("check out PR #%s branch %q: %w", opt.PR, prBranch, err)
			}
			branch = prBranch
		}
		if !opt.NoCatchUp {
			if _, err := c.CatchUp(CatchUpOptions{}); err != nil {
				return ReviewBundle{}, fmt.Errorf("catch-up before review: %w", err)
			}
			refreshed, err := OpenCollection(c.Collection)
			if err != nil {
				return ReviewBundle{}, fmt.Errorf("read collection after catch-up: %w", err)
			}
			c = refreshed
			if current, err := reviewGit(worktree, "branch", "--show-current"); err != nil || strings.TrimSpace(string(current)) != prBranch && prBranch != "" {
				return ReviewBundle{}, fmt.Errorf("PR branch changed during catch-up")
			}
		}
	}
	if prURL == "" && opt.PR != "" {
		suffix := "pull/"
		host := "github.com"
		if forge == "bitbucket" {
			suffix = "pull-requests/"
			host = "bitbucket.org"
		}
		prURL = "https://" + host + "/" + slug + "/" + suffix + opt.PR
	}
	audience := reviewRepositoryAudience(worktree, forge, slug)
	public, reason := c.reviewBundleMode(opt, forge, slug, audience)
	opt.Public = public
	headSHA, err := reviewRef(worktree, opt.Head)
	if err != nil {
		return ReviewBundle{}, err
	}
	if opt.Head != "HEAD" {
		branch = opt.Head
	}
	if prHead != "" && !SameReviewSHA(headSHA, prHead) {
		fmt.Fprintf(os.Stderr, branding.Text("{{command}}: warning: local head %s differs from PR head %s\n"), headSHA[:7], prHead[:min(7, len(prHead))])
	}
	baseRef := opt.Base
	if baseRef == "" && prBase != "" {
		baseRef = "origin/" + prBase
	}
	if baseRef == "" {
		baseRef = "origin/main"
		for _, r := range c.Registry.Repos {
			if r.Answers(opt.Repo) || opt.Repo == "harness" && r.Name == c.Config.Harness.Name {
				if r.DefaultRef != "" {
					baseRef = r.DefaultRef
				}
				break
			}
		}
	}
	if _, err := reviewRef(worktree, baseRef); err != nil {
		return ReviewBundle{}, fmt.Errorf("base ref %q not found: %w", baseRef, err)
	}
	baseBytes, err := reviewGit(worktree, "merge-base", baseRef, headSHA)
	if err != nil {
		return ReviewBundle{}, err
	}
	baseSHA := strings.TrimSpace(string(baseBytes))
	diff, err := reviewGit(worktree, "diff", "--no-color", "--no-ext-diff", "--no-textconv", "--src-prefix=a/", "--dst-prefix=b/", baseSHA, headSHA)
	if err != nil {
		return ReviewBundle{}, err
	}
	changedZ, err := reviewGit(worktree, "diff", "--name-only", "-z", baseSHA, headSHA)
	if err != nil {
		return ReviewBundle{}, err
	}
	var changedPaths []string
	for _, path := range strings.Split(string(changedZ), "\x00") {
		if path != "" {
			changedPaths = append(changedPaths, path)
		}
	}
	changed := []byte(strings.Join(changedPaths, "\n"))
	if len(changedPaths) > 0 {
		changed = append(changed, '\n')
	}
	log, err := reviewGit(worktree, "log", "--oneline", baseSHA+".."+headSHA)
	if err != nil {
		return ReviewBundle{}, err
	}
	roots := ReviewBundleRoots(c.Collection)
	root := roots[0]
	customParent := ""
	if opt.Dir != "" {
		absoluteDir, err := filepath.Abs(opt.Dir)
		if err != nil {
			return ReviewBundle{}, err
		}
		customParent = filepath.Dir(absoluteDir)
	}
	round := opt.Round
	if round == 0 {
		round = nextReviewRound(root, opt.Repo, opt.PR, branch, customParent, roots[1])
	}
	if round < 1 {
		return ReviewBundle{}, fmt.Errorf("round must be positive")
	}
	dir := opt.Dir
	if dir == "" {
		name := opt.Repo + "-br-" + headSHA[:7]
		if opt.PR != "" {
			name = opt.Repo + "-pr" + opt.PR + "-" + headSHA[:7]
		}
		dir = filepath.Join(root, fmt.Sprintf("%s-r%d", name, round))
	}
	dir, err = filepath.Abs(dir)
	if err != nil {
		return ReviewBundle{}, err
	}
	if entries, e := os.ReadDir(dir); e == nil && len(entries) > 0 {
		return ReviewBundle{}, fmt.Errorf("bundle directory is not empty: %s", dir)
	} else if e != nil && !os.IsNotExist(e) {
		return ReviewBundle{}, e
	}
	if err := os.MkdirAll(filepath.Join(dir, "concerns"), 0755); err != nil {
		return ReviewBundle{}, err
	}
	manifest := ReviewManifest{Repo: opt.Repo, PR: opt.PR, Forge: forge, Slug: slug, URL: prURL, BaseRef: baseRef, BaseSHA: baseSHA, HeadSHA: headSHA, HeadBranch: branch, Round: round, RepoDir: worktree, Collection: filepath.Base(c.Collection), Public: opt.Public, Audience: audience, ModeReason: reason}
	prText := fmt.Sprintf("# %s\n\n%s\n", prTitle, prBody)
	if opt.PR == "" {
		prText = fmt.Sprintf("# %s\n\n(branch-only review)\n", branch)
	} else if prTitle == "" {
		prText = fmt.Sprintf("# %s\n\n(no PR text: PR #%s not readable)\n", branch, opt.PR)
	}
	for rel, body := range map[string][]byte{"pr.md": []byte(prText), "diff.patch": diff, "changed-files.txt": changed, "log.txt": log} {
		if err := os.WriteFile(filepath.Join(dir, rel), body, 0644); err != nil {
			return ReviewBundle{}, err
		}
	}
	if !opt.Public {
		downstream, upstream, err := c.snapshotReviewRepositories(dir, opt.Repo)
		if err != nil {
			return ReviewBundle{}, err
		}
		manifest.Downstream = strings.Join(downstream, " ")
		manifest.Upstream = strings.Join(upstream, " ")
	}
	if err := writeReviewManifest(dir, manifest); err != nil {
		return ReviewBundle{}, err
	}
	if _, err := writeReviewChecks(dir, manifest); err != nil {
		return ReviewBundle{}, err
	}
	var concerns int
	if opt.Public {
		concerns, err = c.copyPublicReviewConcerns(dir, worktree, baseSHA, string(changed))
	} else {
		concerns, err = c.copyPrivateReviewConcerns(dir, worktree, baseSHA, string(changed))
	}
	if err != nil {
		return ReviewBundle{}, err
	}
	if err := copyReviewPrior(dir, root, manifest, roots[1]); err != nil {
		return ReviewBundle{}, err
	}
	copyPublicReviewComments(dir, manifest)
	related := 0
	if !opt.Public {
		related, err = c.copyRelatedReviewPatches(dir, opt.Repo, opt.PR)
		if err != nil {
			return ReviewBundle{}, err
		}
	}
	files := len(changedPaths)
	return ReviewBundle{Dir: dir, Manifest: manifest, Files: files, Concerns: concerns, Related: related}, nil
}

// ReviewBundleRoots lists both bundle directories, the one new bundles use
// first. Rounds and earlier reviews continue across them.
func ReviewBundleRoots(collection string) []string {
	return []string{filepath.Join(collection, ".wtc-reviews"), filepath.Join(collection, ".taba-reviews")}
}

func nextReviewRound(root, repo, pr, branch string, extra ...string) int {
	maxRound := 0
	seen := map[string]bool{}
	for _, scan := range append([]string{root}, extra...) {
		if scan == "" || seen[scan] {
			continue
		}
		seen[scan] = true
		entries, _ := os.ReadDir(scan)
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			m, ok, _ := readPriorReviewManifest(filepath.Join(scan, e.Name()))
			if !ok {
				continue
			}
			if m.Repo == repo && m.PR == pr && (pr != "" || m.HeadBranch == branch) && m.Round > maxRound {
				maxRound = m.Round
			}
		}
	}
	return maxRound + 1
}

func writeReviewManifest(dir string, m ReviewManifest) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), append(data, '\n'), 0644); err != nil {
		return err
	}
	fields := [][2]string{{"REPO", m.Repo}, {"PR", m.PR}, {"FORGE", m.Forge}, {"SLUG", m.Slug}, {"URL", m.URL}, {"BASE_REF", m.BaseRef}, {"BASE_SHA", m.BaseSHA}, {"HEAD_SHA", m.HeadSHA}, {"HEAD_BRANCH", m.HeadBranch}, {"ROUND", strconv.Itoa(m.Round)}, {"REPO_DIR", m.RepoDir}, {"COLLECTION", m.Collection}, {"PUBLIC", strconv.FormatBool(m.Public)}, {"REVIEW_MODE", m.Mode()}, {"AUDIENCE", m.Audience}, {"MODE_REASON", m.ModeReason}, {"DOWNSTREAM", m.Downstream}, {"UPSTREAM", m.Upstream}}
	var b strings.Builder
	b.WriteString("# review bundle manifest (sourceable)\n")
	for _, field := range fields {
		fmt.Fprintf(&b, "%s='%s'\n", field[0], strings.ReplaceAll(field[1], "'", "'\\''"))
	}
	return os.WriteFile(filepath.Join(dir, "manifest.env"), []byte(b.String()), 0644)
}

var reviewConcernID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

func (c *Context) copyPublicReviewConcerns(dir, worktree, baseSHA, changed string) (int, error) {
	// Public rules belong to the target at the review base. Never follow
	// working-tree overlays or Git symlinks into another repository.
	sourceSHA := baseSHA
	paths, err := reviewGit(worktree, "ls-tree", "-r", "-z", sourceSHA, "review/concerns", ".review/concerns")
	if err != nil {
		return 0, err
	}
	var names []string
	for _, entry := range strings.Split(string(paths), "\x00") {
		path := entry
		if sourceSHA != "" {
			metadata, name, ok := strings.Cut(entry, "\t")
			// Read blobs only: a symlink is repository data, not a rule file.
			if !ok || !strings.HasPrefix(metadata, "100644 blob ") && !strings.HasPrefix(metadata, "100755 blob ") {
				continue
			}
			path = name
		}
		if (strings.HasPrefix(path, "review/concerns/") || strings.HasPrefix(path, ".review/concerns/")) && strings.HasSuffix(path, ".md") {
			names = append(names, path)
		}
	}
	if len(names) == 0 {
		sourceSHA = ""
		defaults, err := DefaultPaths()
		if err != nil {
			return 0, err
		}
		var embedded []string
		for _, path := range defaults {
			if strings.HasPrefix(path, "review/concerns/") && strings.HasSuffix(path, ".md") {
				embedded = append(embedded, path)
			}
		}
		names = embedded
		fmt.Fprintln(os.Stderr, branding.Text("{{command}}: review base has no generic concerns; using versioned CLI defaults"))
	}
	sort.Strings(names)
	count := 0
	seen := map[string]bool{}
	for _, path := range names {
		var body []byte
		if sourceSHA == "" {
			body, err = ReadDefault(path)
		} else {
			body, err = reviewGit(worktree, "show", sourceSHA+":"+path)
		}
		if err != nil {
			return count, err
		}
		id := strings.TrimSuffix(filepath.Base(path), ".md")
		for _, line := range strings.Split(string(body), "\n") {
			if strings.HasPrefix(line, "id:") {
				id = strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, "id:")), `"'`)
				break
			}
		}
		if !reviewConcernID.MatchString(id) {
			return count, fmt.Errorf("invalid concern id in %s", path)
		}
		if seen[id] {
			return count, fmt.Errorf("duplicate concern id %q", id)
		}
		seen[id] = true
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

func reviewConcernApplies(body, changed string) bool {
	applies := "always"
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "applies:") {
			applies = strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, "applies:")), `"'`)
			break
		}
	}
	if applies == "" || applies == "always" {
		return true
	}
	if applies == "never" {
		return false
	}
	for _, pattern := range strings.Fields(applies) {
		pattern = strings.TrimPrefix(pattern, "/")
		for _, file := range strings.Split(strings.TrimSuffix(changed, "\n"), "\n") {
			if matchReviewGlob(pattern, file) || !strings.Contains(pattern, "/") && matchReviewGlob(pattern, filepath.Base(file)) {
				return true
			}
		}
	}
	return false
}

func matchReviewGlob(pattern, file string) bool {
	var b strings.Builder
	b.WriteByte('^')
	for i := 0; i < len(pattern); i++ {
		switch pattern[i] {
		case '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				b.WriteString(".*")
				i++
			} else {
				b.WriteString("[^/]*")
			}
		case '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(string(pattern[i])))
		}
	}
	b.WriteByte('$')
	return regexp.MustCompile(b.String()).MatchString(file)
}
