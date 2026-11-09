package openclaw

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"taba.sh/taba/internal/next"
)

// CollectionSchema names the JSON that describes a collection's checkouts. A
// gateway plugin renders it; the fields are what a person looks for before
// publishing or catching up.
const CollectionSchema = "wtc.collection/v1"

// Collection is the status of a set of checkouts that belong to one task.
type Collection struct {
	Schema string           `json:"schema"`
	Repos  []CheckoutStatus `json:"repos"`
}

// CheckoutStatus is one checkout, seen through plain git.
type CheckoutStatus struct {
	Path string `json:"path"`
	Repo string `json:"repo,omitempty"`
	// Owner is the repository's owner on its forge: a GitHub user or
	// organization, a Bitbucket workspace.
	Owner  string `json:"owner,omitempty"`
	Forge  Forge  `json:"forge,omitempty"`
	Branch string `json:"branch,omitempty"`
	// Base is the ref Ahead and Behind are counted against.
	Base       string `json:"base,omitempty"`
	Ahead      int    `json:"ahead"`
	Behind     int    `json:"behind"`
	Dirty      int    `json:"dirty"`
	Untracked  int    `json:"untracked"`
	Conflicted int    `json:"conflicted"`
	// Published says that the gateway has published this branch before.
	Published bool `json:"published"`
	// Unpublished counts the commits since then; with Dirty it is what the
	// next publication would send.
	Unpublished int          `json:"unpublished"`
	PullRequest *PullRequest `json:"pullRequest"`
	// PullRequests keeps completed rounds in the collection's PR list.
	PullRequests []PullRequest `json:"pullRequests,omitempty"`
	// Proposal is the title and description proposed for the branch's pull
	// request, which the person's push opens it with.
	Proposal *Proposal `json:"proposal,omitempty"`
	// Next is the repository's next step, read from all of the above; nil
	// when nothing waits on anyone.
	Next    *next.Step    `json:"next,omitempty"`
	Choices *next.Choices `json:"choices,omitempty"`
	Error   string        `json:"error,omitempty"`
}

// PullRequest is the pull request of a checkout's branch.
type PullRequest struct {
	Number int    `json:"number"`
	URL    string `json:"url"`
	State  string `json:"state"`
	Draft  bool   `json:"draft"`
	Title  string `json:"title,omitempty"`
	// Checks is the CI of its head: success, failure, pending or none.
	Checks  string `json:"checks"`
	Passed  int    `json:"passed"`
	Failed  int    `json:"failed"`
	Running int    `json:"running"`
	// Mergeable is whether it merges into its base: mergeable, conflicting,
	// or unknown while GitHub is still working it out.
	Mergeable string `json:"mergeable,omitempty"`
	// Review is the review decision: approved, changes_requested,
	// review_required, or empty when the repository requires none.
	Review string `json:"review,omitempty"`
	// MergedIn says, of a merged pull request whose merge commit is known,
	// whether the checkout has that commit. Absent otherwise.
	MergedIn *bool `json:"mergedIn,omitempty"`
	// Head is the commit the pull request ended at.
	Head        string `json:"head,omitempty"`
	Branch      string `json:"branch,omitempty"`
	MergeCommit string `json:"mergeCommit,omitempty"`
}

// ManagedCollections finds the gateway's collections: the managed worktrees
// under stateDir, grouped by worktree name, which is the collection's name.
// Each repository's worktree is in a directory of its own project.
func ManagedCollections(stateDir string) (map[string][]string, error) {
	dirs, err := filepath.Glob(filepath.Join(stateDir, "worktrees", "*", "*"))
	if err != nil {
		return nil, err
	}
	collections := map[string][]string{}
	for _, dir := range dirs {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
			continue
		}
		name := filepath.Base(dir)
		if ValidWorktreeName(name) {
			collections[name] = append(collections[name], dir)
		}
	}
	return collections, nil
}

// GatewayProject returns the clone's stable native project ID, or "" when
// no clone matches the requested current origin or operator-verified alias.
// The ID is its directory basename, which need not be the forge's repo name.
// Only clones under stateDir/projects/<hash> or stateDir/repos are searched.
func GatewayProject(stateDir string, origin Origin) string {
	if dir := GatewayProjectDir(stateDir, origin); dir != "" {
		return filepath.Base(dir)
	}
	return ""
}

// GatewayProjectDirByName is the directory of the gateway's clone of the
// native project ID first, then by an unambiguous current or verified alias
// repository short name, or "" when neither resolves.
func GatewayProjectDirByName(stateDir, name string) string {
	if !validProjectName(name) {
		return ""
	}
	dirs, _ := filepath.Glob(filepath.Join(stateDir, "projects", "*", name))
	dirs = append(dirs, filepath.Join(stateDir, "repos", name))
	for _, dir := range dirs {
		if gitOK(dir, "rev-parse", "--git-dir") {
			return dir
		}
	}
	return projectNamed(stateDir, name)
}

// GatewayProjectDir is the directory of the gateway's clone that
// GatewayProject names, matching the current origin or an operator-verified
// alias independently of the directory name. Ambiguous fallback matches return "".
func GatewayProjectDir(stateDir string, origin Origin) string {
	if !validProjectOrigin(origin) {
		return ""
	}
	dirs := gatewayProjectDirs(stateDir)
	for _, aliases := range []bool{false, true} {
		var found string
		for _, dir := range dirs {
			matches := sameOrigin(GatewayProjectOrigin(dir), origin)
			if aliases {
				matches = projectMatchesOrigin(dir, origin)
			}
			if matches {
				if found != "" && found != dir {
					return ""
				}
				found = dir
			}
		}
		if found != "" {
			return found
		}
	}
	return ""
}

// CheckoutsStatus describes the checkouts at paths. With pullRequests it also asks
// GitHub, through gh and whatever login gh has, for each GitHub checkout's
// pull request; a checkout it cannot ask about just has none.
func CheckoutsStatus(paths []string, pullRequests bool) Collection {
	c := Collection{Schema: CollectionSchema, Repos: []CheckoutStatus{}}
	for _, p := range paths {
		c.Repos = append(c.Repos, checkoutStatus(p, pullRequests))
	}
	return c
}

func checkoutStatus(path string, pullRequests bool) CheckoutStatus {
	s := CheckoutStatus{Path: path}
	top, err := git(path, "rev-parse", "--show-toplevel")
	if err != nil {
		s.Error = "not a git checkout"
		if untrusted := (*UntrustedConfigError)(nil); errors.As(err, &untrusted) {
			s.Error = err.Error()
		}
		return s
	}
	s.Branch, _ = git(top, "branch", "--show-current")
	if rounds, err := readCompletedRounds(top); err != nil {
		s.Error = err.Error()
		return s
	} else {
		s.PullRequests = rounds.PRs
	}
	origin := Origin{Forge: ForgeUnknown}
	if url, err := git(top, "remote", "get-url", "origin"); err == nil {
		origin = ParseOrigin(url)
	}
	s.Forge, s.Owner, s.Repo = origin.Forge, origin.Owner, origin.Repo

	changes, err := (&Checkout{Dir: top}).pending()
	if err != nil {
		s.Error = err.Error()
		return s
	}
	for _, ch := range changes {
		s.Dirty++
		switch {
		case ch.status == "??":
			s.Untracked++
		case strings.Contains(ch.status, "U") || ch.status == "AA" || ch.status == "DD":
			s.Conflicted++
		}
	}

	head, _ := git(top, "symbolic-ref", "--short", "refs/remotes/origin/HEAD")
	if b := checkedOutBase(top); b != "" {
		head = "origin/" + b
	}
	for _, candidate := range []string{head, "origin/main", "origin/master", "origin/develop"} {
		if candidate != "" && gitOK(top, "rev-parse", "--verify", "--quiet", candidate+"^{commit}") {
			s.Base = candidate
			break
		}
	}
	if s.Base != "" {
		if counts, err := git(top, "rev-list", "--left-right", "--count", s.Base+"...HEAD"); err == nil {
			if f := strings.Fields(counts); len(f) == 2 {
				s.Behind, _ = strconv.Atoi(f[0])
				s.Ahead, _ = strconv.Atoi(f[1])
			}
		}
	}
	s.Unpublished = s.Ahead
	if name, managed := strings.CutPrefix(s.Branch, BranchPrefix); managed {
		if published := (&Checkout{Dir: top, Worktree: name}).publishedHead(); published != "" {
			s.Published = true
			// Commits that came from the base, such as the pull request's
			// merge after a fast-forward catch-up, are not new work.
			args := []string{"rev-list", "--count", "HEAD", "^" + published}
			if s.Base != "" {
				args = append(args, "^"+s.Base)
			}
			if n, err := git(top, args...); err == nil {
				s.Unpublished, _ = strconv.Atoi(n)
			}
		}
	}
	s.Proposal = ReadProposal(top, s.Branch)
	if pullRequests && origin.Forge == ForgeGitHub && s.Branch != "" {
		s.PullRequest = gitHubPullRequest(top, origin, s.Branch)
	}
	choices := next.ChoicesFor(s.facts(top))
	s.Choices = &choices
	s.Next = choices.Primary()
	return s
}

// facts is what the next step is read from: the status, the commits it is
// about, and whether merging the base would conflict.
func (s CheckoutStatus) facts(top string) next.Facts {
	f := next.Facts{Repo: s.Repo, Host: next.HostOpenClaw, Branch: s.Branch, Base: s.Base,
		Ahead: s.Ahead, Behind: s.Behind, Dirty: s.Dirty, Conflicted: s.Conflicted,
		InProgress: (&Checkout{Dir: top}).inProgress()}
	if f.Repo == "" {
		f.Repo = filepath.Base(top)
	}
	f.Head, _ = git(top, "rev-parse", "HEAD")
	if s.Base != "" {
		f.BaseHead, _ = git(top, "rev-parse", s.Base)
	}
	// What ↑ would send: the commits since the gateway's last publication,
	// or before its first one everything ahead of the base. A detached
	// checkout has no branch to push.
	if s.Branch != "" {
		f.ToPush = s.Unpublished
	}
	if s.Behind > 0 && s.Base != "" && f.InProgress == "" {
		var ok bool
		f.MergeConflicts, ok = next.PredictMerge(top, s.Base)
		f.MergeUnknown = !ok
	}
	// Work after a merged pull request: commits neither in it nor in the
	// base, leaving out merges (a catch-up's merge of the base is no work).
	// Unknown without the pull request's head, so then none is claimed.
	if pr := s.PullRequest; pr != nil && pr.State == "merged" && pr.Head != "" && s.Base != "" {
		if n, err := git(top, "rev-list", "--count", "--no-merges", "HEAD", "^"+pr.Head, "^"+s.Base); err == nil {
			f.AfterMerge, _ = strconv.Atoi(n)
		}
		if f.AfterMerge == 0 {
			f.NeedsBranch, _ = (&Checkout{Dir: top}).hasFollowUpWork(pr, s.Base)
		}
	}
	if pr := s.PullRequest; pr != nil {
		f.PR = &next.PR{Number: pr.Number, Head: pr.Head, State: pr.State, Draft: pr.Draft, Checks: pr.Checks, Mergeable: pr.Mergeable, Review: pr.Review, MergedIn: pr.MergedIn}
	}
	return f
}

// gitHubPullRequest is the newest pull request from branch, or nil: without
// gh, without a login, or when there is none.
func gitHubPullRequest(dir string, origin Origin, branch string) *PullRequest {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gh", "pr", "list", "--repo", origin.Owner+"/"+origin.Repo, "--head", branch,
		"--state", "all", "--limit", "1", "--json", "number,url,state,isDraft,title,statusCheckRollup,mergeable,reviewDecision,mergeCommit,headRefOid")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	var found []struct {
		Number  int    `json:"number"`
		URL     string `json:"url"`
		State   string `json:"state"`
		IsDraft bool   `json:"isDraft"`
		Title   string `json:"title"`
		Merge   string `json:"mergeable"`
		Review  string `json:"reviewDecision"`
		Head    string `json:"headRefOid"`
		Commit  *struct {
			OID string `json:"oid"`
		} `json:"mergeCommit"`
		Rollup []struct {
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
			State      string `json:"state"`
		} `json:"statusCheckRollup"`
	}
	if json.Unmarshal(out, &found) != nil || len(found) == 0 {
		return nil
	}
	f := found[0]
	pr := &PullRequest{Number: f.Number, URL: f.URL, State: strings.ToLower(f.State), Draft: f.IsDraft, Title: f.Title, Checks: "none", Branch: branch,
		Mergeable: strings.ToLower(f.Merge), Review: strings.ToLower(f.Review), Head: f.Head}
	if pr.State == "merged" && f.Commit != nil && f.Commit.OID != "" {
		pr.MergeCommit = f.Commit.OID
		in := gitOK(dir, "merge-base", "--is-ancestor", f.Commit.OID, "HEAD")
		pr.MergedIn = &in
	}
	// Check runs report status and conclusion, commit statuses a state.
	for _, c := range f.Rollup {
		switch result := strings.ToUpper(c.Conclusion + c.State); {
		case c.Status != "" && c.Status != "COMPLETED", result == "PENDING", result == "EXPECTED":
			pr.Running++
		case result == "SUCCESS", result == "NEUTRAL", result == "SKIPPED":
			pr.Passed++
		default:
			pr.Failed++
		}
	}
	switch {
	case pr.Failed > 0:
		pr.Checks = "failure"
	case pr.Running > 0:
		pr.Checks = "pending"
	case pr.Passed > 0:
		pr.Checks = "success"
	}
	return pr
}
