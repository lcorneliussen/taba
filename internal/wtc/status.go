package wtc

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"taba.sh/taba/internal/next"
)

// StatusSnapshot is the canonical status schema shared by the one-shot view
// and, once ported, the live renderer. This is the local Git portion; forge
// and process facts are added by later collection stages.
type StatusSnapshot struct {
	Schema      int    `json:"schema"`
	Collection  string `json:"collection"`
	GeneratedAt string `json:"generated_at"`
	// CLIVersion identifies the snapshot producer, not a later cache reader.
	// Absent in older schema-1 snapshots.
	CLIVersion           string         `json:"cli_version,omitempty"`
	ShowCollectionColumn bool           `json:"show_collection_column"`
	StaleCount           int            `json:"stale_count"`
	Repos                []StatusRepo   `json:"repos"`
	PRs                  []StatusPRRow  `json:"prs"`
	Orphans              []StatusOrphan `json:"orphans"`
	// Retired lists folders a retire could not delete, in a workspace sweep.
	Retired      []StatusRetired        `json:"retired,omitempty"`
	PRsEmptyHint bool                   `json:"prs_empty_hint,omitempty"`
	Runtime      []RuntimeItem          `json:"runtime,omitempty"`
	Retirement   []RetirementAssessment `json:"retirement,omitempty"`
}

type StatusRepo struct {
	Collection    string         `json:"collection"`
	Dir           string         `json:"dir"`
	Repo          string         `json:"repo"`
	RenamedTo     string         `json:"renamed_to,omitempty"` // current name when Dir is an alias
	Worktree      string         `json:"worktree"`
	Slug          string         `json:"slug"`
	Forge         string         `json:"forge,omitempty"`
	BranchKind    string         `json:"branch_kind"`
	Branch        string         `json:"branch"`
	BranchDisplay string         `json:"branch_display"`
	Ahead         int            `json:"ahead"`
	AheadRef      string         `json:"ahead_ref,omitempty"`
	AheadBasis    string         `json:"ahead_basis,omitempty"`
	UpstreamState string         `json:"upstream_state,omitempty"`
	Landing       *StatusLanding `json:"landing,omitempty"`
	Behind        int            `json:"behind"`
	Tree          string         `json:"tree"`
	Changed       int            `json:"changed"`
	Conflict      bool           `json:"conflict,omitempty"`
	Operation     string         `json:"operation,omitempty"`
	PR            *StatusPRFacts `json:"pr"`
	Tip           *StatusBuild   `json:"tip"`
	Prod          *StatusBuild   `json:"prod"`
	Runtime       string         `json:"runtime,omitempty"`
	ReviewOnly    bool           `json:"review_only,omitempty"`
	// Next is the repository's next step, read from the facts above; nil
	// when nothing waits on anyone.
	Next    *next.Step    `json:"next,omitempty"`
	Choices *next.Choices `json:"choices,omitempty"`
	// baseRef is the ref Behind counts against: the development tip, or an
	// open pull request's base.
	baseRef string
}

type StatusPRFacts struct {
	Number         string   `json:"number"`
	Head           string   `json:"head,omitempty"`
	URL            string   `json:"url,omitempty"`
	Checks         string   `json:"checks"`
	Merge          string   `json:"merge"`
	Review         string   `json:"review"`
	Draft          bool     `json:"draft"`
	MergeConflicts []string `json:"merge_conflicts,omitempty"`
	MergeSource    string   `json:"merge_source,omitempty"`
	MergeHead      string   `json:"merge_head,omitempty"`
	MergeBase      string   `json:"merge_base,omitempty"`
}

type StatusBuild struct {
	Branch string  `json:"branch"`
	Checks *string `json:"checks"`
	Build  *string `json:"build"`
	URL    *string `json:"url"`
	// SHA is the commit the build ran on, when the provider knows it.
	SHA *string `json:"sha,omitempty"`
	// HeadInBuild says whether this row's HEAD is contained in SHA; nil when
	// either commit is unknown.
	HeadInBuild *bool `json:"head_in_build,omitempty"`
	// Error replaces the facts when the provider failed for this tier.
	Error string `json:"error,omitempty"`
}

// StatusFollow is where a merged PR stands on its way through the build tiers:
// the first tier that has not yet passed with the merge in it, or "done".
type StatusFollow struct {
	Stage  string  `json:"stage"`
	Checks string  `json:"checks"`
	Build  *string `json:"build,omitempty"`
	URL    *string `json:"url,omitempty"`
}

type StatusPRRow struct {
	Repo            string         `json:"repo"`
	Number          string         `json:"number"`
	State           string         `json:"state,omitempty"`
	Checks          *string        `json:"checks"`
	Merge           *string        `json:"merge"`
	Review          *string        `json:"review"`
	Title           string         `json:"title"`
	DisplayTitle    string         `json:"display_title"`
	Slug            string         `json:"slug"`
	URL             *string        `json:"url"`
	FollowTipBuild  *string        `json:"follow_tip_build"`
	FollowProdBuild *string        `json:"follow_prod_build"`
	Follow          *StatusFollow  `json:"follow,omitempty"`
	MergeCommit     string         `json:"merge_commit,omitempty"`
	HeadCommit      string         `json:"head_commit,omitempty"`
	Landing         *StatusLanding `json:"landing,omitempty"`
	Base            string         `json:"base,omitempty"`
	Archived        bool           `json:"archived"`
	MergedOn        *string        `json:"merged_on"`
	Draft           bool           `json:"draft"`
	OnBranch        bool           `json:"on_branch"`
	MergeConflicts  []string       `json:"merge_conflicts,omitempty"`
	MergeSource     string         `json:"merge_source,omitempty"`
	MergeHead       string         `json:"merge_head,omitempty"`
	MergeBase       string         `json:"merge_base,omitempty"`
	// worktreeDir carries a verified branch association during collection;
	// cached snapshots already contain the resulting Next and Choices.
	worktreeDir string
}

type StatusRetired struct {
	Folder        string   `json:"folder"`
	RetiredAt     string   `json:"retired_at,omitempty"`
	BlockingPaths []string `json:"blocking_paths"`
}

// statusRetired reads the retire markers of a workspace sweep. A marker that
// cannot be parsed still lists its folder, with what it holds now.
func (c *Context) statusRetired(snapshot *StatusSnapshot) {
	folders, err := RetiredFolders(c.Workspace)
	if err != nil {
		return
	}
	for _, dir := range folders {
		row := StatusRetired{Folder: filepath.Base(dir)}
		if marker, err := ReadRetiredMarker(dir); err == nil {
			row.RetiredAt, row.BlockingPaths = marker.RetiredAt, marker.BlockingPaths
		} else {
			row.BlockingPaths, _ = retireLeftovers(dir)
		}
		if row.BlockingPaths == nil {
			row.BlockingPaths = []string{}
		}
		snapshot.Retired = append(snapshot.Retired, row)
	}
}

type StatusOrphan struct {
	Repo   string `json:"repo"`
	Branch string `json:"branch"`
	State  string `json:"state"`
}

func (c *Context) statusProgress(message string) {
	if c.StatusProgress != nil {
		c.StatusProgress(message)
	}
}

func statusCount(worktree, rangeSpec string) (int, error) {
	value, err := statusGit(worktree, "rev-list", "--count", rangeSpec)
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("invalid rev-list count %q: %w", value, err)
	}
	return n, nil
}

func statusLocalRepo(t catchUpTarget) (StatusRepo, error) {
	dir := filepath.Base(t.path)
	row := StatusRepo{Collection: t.collection, Dir: dir, Repo: t.repo,
		Worktree: t.path, Tree: "clean", baseRef: t.ref, RenamedTo: t.renamed}
	remote, err := statusGit(t.path, "remote", "get-url", "origin")
	if err == nil {
		row.Slug, row.Forge = catchUpForge(remote)
	}
	branch, err := statusGit(t.path, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err == nil {
		row.BranchKind = "branch"
		row.Branch = branch
		row.BranchDisplay = branch
	} else {
		row.BranchKind = "detached"
		row.Branch = catchUpBranchName(t.ref)
		row.BranchDisplay = "⌂ " + row.Branch
	}
	if reviewOnlyWorktree(t.path) {
		row.ReviewOnly = true
		row.BranchDisplay += " (review-only)"
	}
	upstream := t.ref
	row.AheadBasis, row.UpstreamState = "development-tip", "none"
	if row.BranchKind == "branch" {
		if found, err := statusGit(t.path, "rev-parse", "--abbrev-ref", "@{u}"); err == nil {
			upstream = found
			row.AheadBasis, row.UpstreamState = "upstream", "tracking"
		} else if tracking, err := statusGit(t.path, "for-each-ref", "--format=%(upstream:track)", "refs/heads/"+row.Branch); err == nil && tracking == "[gone]" {
			row.UpstreamState = "gone"
			row.BranchDisplay += " [gone]"
		}
	}
	row.AheadRef = upstream
	row.Ahead, err = statusCount(t.path, upstream+"..HEAD")
	if err != nil {
		return StatusRepo{}, fmt.Errorf("%s ahead count: %w", t.path, err)
	}
	row.Behind, err = statusCount(t.path, "HEAD.."+t.ref)
	if err != nil {
		return StatusRepo{}, fmt.Errorf("%s development-tip count: %w", t.path, err)
	}
	porcelain, err := statusGit(t.path, "status", "--porcelain")
	if err != nil {
		return StatusRepo{}, fmt.Errorf("%s working tree: %w", t.path, err)
	}
	if porcelain != "" {
		row.Changed = len(strings.Split(porcelain, "\n"))
		row.Tree = fmt.Sprintf("±%d", row.Changed)
	}
	row.Operation = catchUpOperation(t)
	if unmerged, err := statusGit(t.path, "diff", "--name-only", "--diff-filter=U"); err == nil && unmerged != "" {
		row.Conflict = true
	}
	return row, nil
}

// StatusLocalSnapshot reads worktrees and local refs without contacting a
// forge, updating refs, or writing a cache. All is an explicit workspace sweep.
func (c *Context) StatusLocalSnapshot(all bool) (StatusSnapshot, error) {
	snapshot := StatusSnapshot{Schema: 1,
		CLIVersion:           RunningCLIVersion(),
		GeneratedAt:          time.Now().UTC().Format("2006-01-02T15:04:05Z"),
		ShowCollectionColumn: all,
		Repos:                []StatusRepo{}, PRs: []StatusPRRow{}, Orphans: []StatusOrphan{}}
	if !all {
		snapshot.Collection = filepath.Base(c.Collection)
	}
	report, targets, err := c.CatchUpInventory(CatchUpOptions{All: all, DryRun: true})
	if err != nil {
		return snapshot, err
	}
	if report.ExitStatus != 0 {
		return snapshot, fmt.Errorf("cannot read %d collection(s) in status inventory", len(report.Outcomes))
	}
	for i, target := range targets {
		c.statusProgress(fmt.Sprintf("Reading worktrees %d/%d", i+1, len(targets)))
		row, err := statusLocalRepo(target)
		if err != nil {
			return snapshot, err
		}
		if row.Behind != 0 {
			snapshot.StaleCount++
		}
		snapshot.Repos = append(snapshot.Repos, row)
	}
	if all {
		c.statusRetired(&snapshot)
	}
	c.statusRuntime(&snapshot, all)
	statusNext(&snapshot)
	c.statusRetirement(&snapshot, all)
	return snapshot, nil
}

// statusNext reads each repository's next step from its row.
func statusNext(snapshot *StatusSnapshot) {
	for i := range snapshot.Repos {
		r := &snapshot.Repos[i]
		r.Next = nil
		r.Choices = nil
		if r.ReviewOnly {
			continue
		}
		f := statusFacts(*r)
		if r.PR == nil && r.BranchKind == "branch" {
			for _, pr := range snapshot.PRs {
				if !pr.OnBranch || pr.State != "MERGED" || !statusPRMatchesRepo(pr, *r) {
					continue
				}
				number, _ := strconv.Atoi(pr.Number)
				in := pr.MergeCommit != "" && catchUpGitOK(r.Worktree, "merge-base", "--is-ancestor", pr.MergeCommit, "HEAD")
				f.PR = &next.PR{Number: number, State: "merged", MergedIn: &in}
				f.NeedsBranch = !catchUpLanded(catchUpTarget{path: r.Worktree}, r.baseRef,
					catchUpPR{state: "MERGED", mergeCommit: pr.MergeCommit})
				break
			}
		}
		choices := next.ChoicesFor(f)
		r.Choices = &choices
		r.Next = choices.Primary()
	}
	// Refresh collection blockers after PR/build facts change, without source reads.
	statusRetirementFacts(snapshot)
}

// statusFacts is what a local row tells about the next step. Ahead counts
// against the branch's upstream (the development tip when it has none), so
// its development-tip fallback can include residual squash history.
// Observational landing fields do not change next-action authority.
func statusFacts(r StatusRepo) next.Facts {
	f := next.Facts{Collection: r.Collection, Repo: r.Repo, Host: next.HostLocal, Base: r.baseRef,
		Ahead: r.Ahead, Behind: r.Behind, Dirty: r.Changed, InProgress: r.Operation}
	if r.Repo == "" {
		f.Repo = r.Dir
	}
	if r.Conflict {
		f.Conflicted = 1
	}
	if r.BranchKind == "branch" {
		f.Branch = r.Branch
		f.ToPush = r.Ahead
	}
	f.Head, _ = statusGit(r.Worktree, "rev-parse", "HEAD")
	if r.baseRef != "" {
		f.BaseHead, _ = statusGit(r.Worktree, "rev-parse", r.baseRef)
	}
	if r.Behind > 0 && r.baseRef != "" && f.InProgress == "" {
		var ok bool
		f.MergeConflicts, ok = next.PredictMerge(r.Worktree, r.baseRef)
		f.MergeUnknown = !ok
	}
	if pr := r.PR; pr != nil {
		number, _ := strconv.Atoi(pr.Number)
		f.PR = &next.PR{Number: number, Head: pr.Head, Draft: pr.Draft, State: "open",
			Checks: map[string]string{"FAILURE": "failure", "SUCCESS": "success", "PENDING": "pending"}[strings.ToUpper(pr.Checks)],
			Review: map[string]string{"approved": "approved", "changes": "changes_requested"}[pr.Review]}
		// Open PRs live on the repository row; retained merged PRs are read
		// from the collection's PR list by statusNext.
		switch strings.ToUpper(pr.Merge) {
		case "CONFLICTING", "DIRTY":
			f.PR.Mergeable = "conflicting"
		case "CLEAN", "MERGEABLE":
			f.PR.Mergeable = "mergeable"
		}
	}
	return f
}
