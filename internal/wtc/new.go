package wtc

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"taba.sh/taba/internal/branding"
)

type NewOptions struct {
	Slug    string
	Repos   []string
	Issue   string
	Tracker string
	PR      string
	Branch  string
	// Review makes a PR collection review-only: pushing from the PR worktree
	// is disabled and the author's review steps refuse to run.
	Review bool
}

type NewResult struct {
	Collection     string   `json:"collection"`
	IntendedBranch string   `json:"intended_branch"`
	LocalBranch    string   `json:"local_branch,omitempty"`
	PushRemote     string   `json:"push_remote,omitempty"`
	Repositories   []string `json:"repositories"`
	Source         string   `json:"source"`
	Review         bool     `json:"review,omitempty"`
}

var collectionNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)
var githubSlugPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]*/[A-Za-z0-9][A-Za-z0-9._-]*$`)

func (c *Context) NewCollection(opt NewOptions) (NewResult, error) {
	result := NewResult{}
	if c.AdHoc {
		if opt.Review {
			return result, fmt.Errorf("review mode needs a harness collection")
		}
		return c.newAdHoc(opt)
	}
	if (opt.Issue != "" && opt.Tracker != "") || (opt.PR != "" && (opt.Issue != "" || opt.Tracker != "")) {
		return result, fmt.Errorf("choose one source: issue, tracker, or PR")
	}
	if opt.Review && opt.PR == "" {
		return result, fmt.Errorf("review mode needs a PR: --pr <repo>#<number>")
	}
	if opt.PR != "" && opt.Branch != "" {
		return result, fmt.Errorf("--branch cannot override a PR head branch")
	}
	harnessRepo, err := c.HarnessRepoName()
	if err != nil {
		return result, err
	}
	primary, primaryBranch, prID, prHead, prRemote, prTitle, prWebURL := "", opt.Branch, "", "", "", "", ""
	if opt.PR != "" {
		parts := strings.Split(opt.PR, "#")
		if len(parts) != 2 || !prNumber.MatchString(parts[1]) {
			return result, fmt.Errorf("--pr expects <repo>#<number>")
		}
		repo, err := c.Repository(parts[0])
		if err != nil {
			return result, err
		}
		url := prURL(repo.Remote, parts[1])
		if !strings.HasPrefix(url, "https://github.com/") {
			return result, fmt.Errorf("PR collection requires a GitHub repository remote")
		}
		slug := strings.TrimSuffix(strings.TrimPrefix(url, "https://github.com/"), "/pull/"+parts[1])
		output, err := exec.Command("gh", "pr", "view", parts[1], "--repo", slug, "--json", "headRefName,headRefOid,headRepository,isCrossRepository,title").Output()
		if err != nil {
			return result, fmt.Errorf("resolve PR head: %w", err)
		}
		var details struct {
			HeadRefName    string `json:"headRefName"`
			HeadRefOID     string `json:"headRefOid"`
			HeadRepository struct {
				NameWithOwner string `json:"nameWithOwner"`
			} `json:"headRepository"`
			IsCrossRepository bool   `json:"isCrossRepository"`
			Title             string `json:"title"`
		}
		if err := json.Unmarshal(output, &details); err != nil || details.HeadRefName == "" || details.HeadRefOID == "" {
			return result, fmt.Errorf("could not read PR head for %s", opt.PR)
		}
		result.Collection = parts[0] + "-pr" + parts[1]
		result.IntendedBranch = details.HeadRefName
		result.Source = fmt.Sprintf("Review wtc for %s#%s.", slug, parts[1])
		if opt.Review {
			result.Source = fmt.Sprintf("Review-only wtc for %s#%s: review someone else's PR without writing to it.", slug, parts[1])
		}
		primary, primaryBranch, prID, prHead = repo.Name, details.HeadRefName, parts[1], details.HeadRefOID
		prTitle, prWebURL = details.Title, url
		if details.IsCrossRepository {
			if !githubSlugPattern.MatchString(details.HeadRepository.NameWithOwner) {
				return result, fmt.Errorf("PR head repository is unavailable")
			}
			prRemote = "https://github.com/" + details.HeadRepository.NameWithOwner + ".git"
		}
	} else if opt.Issue != "" {
		if !collectionNamePattern.MatchString(opt.Issue) || !collectionNamePattern.MatchString(opt.Slug) {
			return result, fmt.Errorf("invalid issue ID or collection slug")
		}
		prefix := strings.SplitN(opt.Issue, "-", 2)[0] + "-"
		for _, repo := range c.Registry.Repos {
			if repo.IssuesPrefix == prefix {
				primary = repo.Name
				break
			}
		}
		if primary == "" {
			return result, fmt.Errorf("no repository owns issue prefix %q", prefix)
		}
		result.Collection = opt.Issue + "-" + opt.Slug
		result.IntendedBranch = result.Collection
		result.Source = fmt.Sprintf("Issue wtc for `%s` (repo: %s).", opt.Issue, primary)
	} else if opt.Tracker != "" {
		if !collectionNamePattern.MatchString(opt.Tracker) || !collectionNamePattern.MatchString(opt.Slug) {
			return result, fmt.Errorf("invalid tracker key or collection slug")
		}
		result.Collection = strings.ToLower(opt.Tracker) + "-" + opt.Slug
		result.IntendedBranch = result.Collection
		result.Source = fmt.Sprintf("Tracker wtc for %s — create the linking issue (frontmatter `tracker: %s`) when consuming this note.", opt.Tracker, opt.Tracker)
	} else {
		if !collectionNamePattern.MatchString(opt.Slug) {
			return result, fmt.Errorf("invalid collection slug %q", opt.Slug)
		}
		result.Collection = opt.Slug
		result.IntendedBranch = opt.Slug
	}
	if opt.Branch != "" {
		if _, err := gitOutput("check-ref-format", "--branch", opt.Branch); err != nil {
			return result, fmt.Errorf("invalid explicit branch: %w", err)
		}
		result.IntendedBranch = opt.Branch
	}
	if !collectionNamePattern.MatchString(result.Collection) {
		return result, fmt.Errorf("invalid collection name %q", result.Collection)
	}
	result.Repositories = []string{harnessRepo}
	if primary != "" && primary != harnessRepo {
		result.Repositories = append(result.Repositories, primary)
	}
	for _, name := range opt.Repos {
		if name == "harness" || name == harnessRepo {
			continue
		}
		repo, err := c.Repository(name)
		if err != nil {
			return result, err
		}
		name = repo.Name // new collections use the current name
		found := false
		for _, existing := range result.Repositories {
			found = found || existing == name
		}
		if !found {
			result.Repositories = append(result.Repositories, name)
		}
	}
	result.Collection = filepath.Join(c.Workspace, result.Collection)
	if _, err := os.Lstat(result.Collection); err == nil {
		return result, fmt.Errorf("collection already exists: %s", result.Collection)
	} else if !os.IsNotExist(err) {
		return result, err
	}
	if opt.Review {
		// Refused here, before any folder or checkout exists.
		repo, err := c.Repository(primary)
		if err != nil {
			return result, err
		}
		bare, err := c.ensureBare(repo)
		if err != nil {
			return result, err
		}
		if err := refuseReviewPushURL(bare); err != nil {
			return result, err
		}
	}
	values := map[string]string{"collection": result.Collection, "source": result.Source, "branch": result.IntendedBranch}
	if err := c.RunHook("new.pre", values); err != nil {
		return result, err
	}
	if err := os.Mkdir(result.Collection, 0755); err != nil {
		return result, err
	}
	// Preserve the shell command's ordering: harness first, then the primary
	// issue/PR repository, then explicit siblings.
	for _, name := range result.Repositories {
		directory, branch := name, opt.Branch
		if name == harnessRepo {
			directory = "harness"
		}
		if name == primary {
			branch = primaryBranch
		}
		if name == primary && prID != "" {
			var checkout PRWorktreeCheckout
			checkout, err = c.AddPRWorktree(name, directory, result.Collection, branch, prID, prHead, prRemote)
			if err == nil {
				result.LocalBranch, result.PushRemote = checkout.LocalBranch, checkout.PushRemote
			}
			if err == nil && opt.Review {
				// The marker comes first: should the lock fail, the partial
				// collection is still review-only, and catch-up leaves it alone.
				if err = writeReviewMode(result.Collection, ReviewMode{Repo: primary, Dir: directory, Branch: checkout.LocalBranch, PR: prID, URL: prWebURL, Heads: []string{prHead}}); err == nil {
					err = lockReviewPush(result.Collection, filepath.Join(result.Collection, directory))
				}
				result.Review = err == nil
			}
		} else {
			err = c.AddWorktree(name, directory, result.Collection, branch)
		}
		if err != nil {
			return result, fmt.Errorf("add %s: %w (partial collection at %s)", name, err, result.Collection)
		}
	}
	target, err := OpenCollection(result.Collection)
	if err != nil {
		return result, err
	}
	// A freshly created collection has no env file yet. Inherit the chosen
	// control root from the creating collection rather than the machine default.
	target.ConfigRoot = c.ConfigRoot
	if err := target.ValidateEnvSupport(); err != nil {
		return result, err
	}
	if err := target.RunHook("env.pre", nil); err != nil {
		return result, err
	}
	env, err := target.RenderEnv()
	if err != nil {
		return result, err
	}
	if err := target.WriteEnv(env); err != nil {
		return result, err
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
	if _, err := target.LinkSecrets(SecretLinkOptions{}); err != nil {
		return result, err
	}
	if err := target.RunHook("secrets.link.post", nil); err != nil {
		return result, err
	}
	if _, err := target.RenderSkills(SkillRenderOptions{SeedScope: true}); err != nil {
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
	paths := make([]string, 0, len(result.Repositories))
	for _, name := range result.Repositories {
		if name == harnessRepo {
			paths = append(paths, filepath.Join(result.Collection, "harness"))
		} else {
			paths = append(paths, filepath.Join(result.Collection, name))
		}
	}
	sort.Strings(paths)
	for _, path := range paths {
		RunRepoInit(path)
	}
	_, _ = target.AgentToolchainPath(true) // A bare agent shell can fill this later.
	var branchNote string
	switch {
	case opt.Review:
		branchNote = fmt.Sprintf(branding.Text("**Review-only collection.** You review someone else's PR; the result is a verdict and nothing else.\nProduce and post the review only: `{{command}} review bundle`, `{{command}} review run`, `{{command}} review post`.\nNo commits, no pushes, no thread resolution, no `{{command}} review ready`; those belong to the author.\nPushing from `%s` is disabled. When the author pushes again, `{{command}} review bundle` moves to the new head for the next round.\n\nOther siblings start detached at the tip."), primaryDir(primary, harnessRepo))
	case opt.PR != "":
		branchNote = fmt.Sprintf("The primary sibling is on a local branch at the exact PR head. Push review work with:\n\n    git -C %s push %s %s\n\nOther siblings start detached at the tip.", shellQuote(primaryDir(primary, harnessRepo)), shellQuote(result.PushRemote), shellQuote("HEAD:refs/heads/"+result.IntendedBranch))
	case opt.Branch != "":
		branchNote = "All siblings are already on the explicitly requested branch. Commit on that branch; do not create it again."
	default:
		branchNote = fmt.Sprintf("Worktrees start **detached at the development tip** — no branch exists yet.\nCreate one at your first commit:\n\n    git switch -c %s\n\n(that is the expected name; adjust it if the work turns out to be something else).", result.IntendedBranch)
	}
	handoff := fmt.Sprintf(branding.Text("# wtc: %s — launch note (EPHEMERAL)\n\n**Goal:** %s\n\nFirst agent on this wtc: read this, turn anything durable into issues /\ncommits / PRs, then **delete this file as your very first action**\n(harness/AGENTS.md → \"State lives in git\").\n\n%s Collection env: `.env.collection` (inherited via `mise.toml`).\nRetire with `{{command}} retire .` from this collection's Herdr workspace, or `{{command}} retire %s` from another collection.\n"), filepath.Base(result.Collection), defaultSource(result.Source), branchNote, filepath.Base(result.Collection))
	if err := os.WriteFile(filepath.Join(result.Collection, "HANDOFF.md"), []byte(handoff), 0644); err != nil {
		return result, err
	}
	if prID != "" {
		if _, err := target.EnlistPR(PRRecord{Repo: primary, Number: prID, Branch: result.LocalBranch, URL: prWebURL, Title: prTitle}); err != nil {
			return result, err
		}
	}
	if err := c.RunHook("new.post", values); err != nil {
		return result, err
	}
	return result, nil
}

func primaryDir(primary, harnessRepo string) string {
	if primary == harnessRepo {
		return "harness"
	}
	return primary
}

func defaultSource(source string) string {
	if source == "" {
		return "(fill in — what this wtc exists for)"
	}
	return source
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
