package wtc

import (
	"context"
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
	// authContext is in-memory provenance only; the public snapshot schema is unchanged.
	authContext string `json:"-"`
	authInputs  string `json:"-"`
	// RuntimeObservedAt orders concurrent in-process service reads. It is not a cache timestamp or wire field.
	RuntimeObservedAt time.Time `json:"-"`
	Schema            int       `json:"schema"`
	Collection        string    `json:"collection"`
	GeneratedAt       string    `json:"generated_at"`
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
	DevelopmentBase *RepositoryBase `json:"development_base,omitempty"`
	ComparisonRef   string          `json:"comparison_ref,omitempty"`
	Collection      string          `json:"collection"`
	Dir             string          `json:"dir"`
	Repo            string          `json:"repo"`
	RenamedTo       string          `json:"renamed_to,omitempty"` // current name when Dir is an alias
	Worktree        string          `json:"worktree"`
	Slug            string          `json:"slug"`
	Forge           string          `json:"forge,omitempty"`
	BranchKind      string          `json:"branch_kind"`
	Branch          string          `json:"branch"`
	BranchDisplay   string          `json:"branch_display"`
	Ahead           int             `json:"ahead"`
	AheadRef        string          `json:"ahead_ref,omitempty"`
	AheadBasis      string          `json:"ahead_basis,omitempty"`
	UpstreamState   string          `json:"upstream_state,omitempty"`
	Landing         *StatusLanding  `json:"landing,omitempty"`
	Behind          int             `json:"behind"`
	Tree            string          `json:"tree"`
	Changed         int             `json:"changed"`
	Conflict        bool            `json:"conflict,omitempty"`
	Operation       string          `json:"operation,omitempty"`
	PR              *StatusPRFacts  `json:"pr"`
	// PRUnconfirmed blocks branch actions and collection retirement for a matching
	// local repository/branch when its enlisted PR state is stale, deferred or
	// unavailable, or its PR URL is rejected. It does not claim the PR is open.
	PRUnconfirmed bool         `json:"pr_unconfirmed,omitempty"`
	Tip           *StatusBuild `json:"tip"`
	Prod          *StatusBuild `json:"prod"`
	Runtime       string       `json:"runtime,omitempty"`
	ReviewOnly    bool         `json:"review_only,omitempty"`
	// Next is the repository's next step, read from the facts above; nil
	// when nothing waits on anyone.
	Next    *next.Step    `json:"next,omitempty"`
	Choices *next.Choices `json:"choices,omitempty"`
	// baseRef is the ref Behind counts against: the development tip, or an
	// open pull request's base.
	baseRef string
}

type StatusPRFacts struct {
	// StateSource, Freshness and ObservedAt describe only PR state. They do not
	// date checks, reviews or mergeability.
	StateSource    string   `json:"observation_source,omitempty"`
	Freshness      string   `json:"freshness,omitempty"`
	ObservedAt     string   `json:"observed_at,omitempty"`
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

	ObservationReason string `json:"observation_reason,omitempty"`
	RetryAt           string `json:"retry_at,omitempty"`
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
	// NotEnlisted marks transient branch discoveries, never durable follow-up ownership.
	NotEnlisted   bool   `json:"not_enlisted,omitempty"`
	EnlistCommand string `json:"enlist_command,omitempty"`
	// StateSource, Freshness and ObservedAt describe only PR state, independently
	// of snapshot age. They do not date checks, reviews or mergeability.
	StateSource     string         `json:"observation_source,omitempty"`
	Freshness       string         `json:"freshness,omitempty"`
	ObservedAt      string         `json:"observed_at,omitempty"`
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

	ObservationReason string `json:"observation_reason,omitempty"`
	RetryAt           string `json:"retry_at,omitempty"`
	// LocalMergeEvidence reports local Git hints (a gone upstream, a head
	// contained in the development tip) in JSON. It schedules a provider read;
	// it is never a forge state and grants no action.
	LocalMergeEvidence string `json:"local_merge_evidence,omitempty"`
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
	return statusCountContext(context.Background(), worktree, rangeSpec)
}

func statusCountContext(ctx context.Context, worktree, rangeSpec string) (int, error) {
	value, err := statusGitContext(ctx, worktree, "rev-list", "--count", rangeSpec)
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
	return statusLocalRepoContext(context.Background(), t)
}

func statusLocalRepoContext(ctx context.Context, t catchUpTarget) (StatusRepo, error) {
	dir := filepath.Base(t.path)
	row := StatusRepo{Collection: t.collection, Dir: dir, Repo: t.repo,
		Worktree: t.path, Tree: "clean", baseRef: t.ref, ComparisonRef: t.ref, DevelopmentBase: t.developmentBase, RenamedTo: t.renamed}
	remote, err := statusGitContext(ctx, t.path, "remote", "get-url", "origin")
	if err == nil {
		row.Slug, row.Forge = catchUpForge(remote)
	}
	local, batched := statusResourceLocalRead(ctx, t.path)
	branch := ""
	var branchErr error
	if batched {
		branch = local.branch
		if branch == "(detached)" {
			// The marker is also a legal branch name; confirm it with symbolic-ref.
			branch, branchErr = statusGitContext(ctx, t.path, "symbolic-ref", "--quiet", "--short", "HEAD")
		}
	} else {
		branch, branchErr = statusGitContext(ctx, t.path, "symbolic-ref", "--quiet", "--short", "HEAD")
	}
	err = branchErr
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
	branchBatch := batched && row.BranchKind == "branch" && local.branch == row.Branch
	trackingBatch := branchBatch && local.tracking && local.upstream == t.ref
	if trackingBatch {
		upstream = local.upstream
		row.AheadBasis, row.UpstreamState = "upstream", "tracking"
	} else if row.BranchKind == "branch" && !(branchBatch && local.noUpstream) {
		// The upstream probe and fallback share a 60-second bound.
		upstreamCtx, stop := context.WithTimeout(ctx, 60*time.Second)
		if found, state, ok := statusResourceUpstream(upstreamCtx, t.path, row.Branch); ok {
			row.UpstreamState = state
			if state == "tracking" {
				upstream = found
				row.AheadBasis = "upstream"
			}
			if state == "gone" {
				row.BranchDisplay += " [gone]"
			}
		} else if found, err := statusGitContext(upstreamCtx, t.path, "rev-parse", "--abbrev-ref", "@{u}"); err == nil {
			upstream = found
			row.AheadBasis, row.UpstreamState = "upstream", "tracking"
		} else if tracking, err := statusGitContext(upstreamCtx, t.path, "for-each-ref", "--format=%(upstream:track)", "refs/heads/"+row.Branch); err == nil && tracking == "[gone]" {
			row.UpstreamState = "gone"
			row.BranchDisplay += " [gone]"
		}
		stop()
	}
	row.AheadRef = upstream
	if trackingBatch {
		row.Ahead, row.Behind = local.ahead, local.behind
	} else if ahead, behind, ok := statusResourceCounts(ctx, t.path, upstream, t.ref); ok {
		row.Ahead, row.Behind = ahead, behind
	} else {
		row.Ahead, err = statusCountContext(ctx, t.path, upstream+"..HEAD")
		if err != nil {
			return StatusRepo{}, fmt.Errorf("%s ahead count: %w", t.path, err)
		}
		row.Behind, err = statusCountContext(ctx, t.path, "HEAD.."+t.ref)
		if err != nil {
			return StatusRepo{}, fmt.Errorf("%s development-tip count: %w", t.path, err)
		}
	}
	if batched {
		row.Changed, row.Conflict = local.changed, local.conflict
	} else {
		porcelain, err := statusGitContext(ctx, t.path, "status", "--porcelain")
		if err != nil {
			return StatusRepo{}, fmt.Errorf("%s working tree: %w", t.path, err)
		}
		if porcelain != "" {
			row.Changed = len(strings.Split(porcelain, "\n"))
		}
		if unmerged, err := statusGitContext(ctx, t.path, "diff", "--name-only", "--diff-filter=U"); err == nil && unmerged != "" {
			row.Conflict = true
		}
	}
	if row.Changed != 0 {
		row.Tree = fmt.Sprintf("±%d", row.Changed)
	}
	row.Operation = statusResourceOperationState(ctx, t)

	return row, nil
}

// StatusLocalSnapshot reads worktrees and local refs without contacting a
// forge, updating refs, or writing a cache. All is an explicit workspace sweep.
func (c *Context) StatusLocalSnapshot(all bool) (StatusSnapshot, error) {
	return c.statusLocalSnapshot(all, true)
}

func (c *Context) statusLocalSnapshot(all, includeChoices bool) (StatusSnapshot, error) {
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
		row, err := statusLocalRepoContext(c.resourceContext(), target)
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
	if err := c.resourceContext().Err(); err != nil {
		return snapshot, err
	}
	if includeChoices {
		statusNextContext(c.resourceContext(), &snapshot)
	}
	c.statusRetirement(&snapshot, all)
	return snapshot, nil
}

// statusNext reads each repository's next step from its row.
func statusNext(snapshot *StatusSnapshot) { statusNextContext(context.Background(), snapshot) }

func statusNextContext(ctx context.Context, snapshot *StatusSnapshot) {
	_ = statusNextWithHeads(ctx, snapshot, statusCurrentHeads)
}

type statusHeadReader func(context.Context, StatusRepo) (string, string, error)

func statusNextWithHeads(ctx context.Context, snapshot *StatusSnapshot, readHeads statusHeadReader) error {
	for i := range snapshot.Repos {
		r := &snapshot.Repos[i]
		r.Next = nil
		r.Choices = nil
		blocked := r.PRUnconfirmed || (r.PR != nil && statusObservationUnavailable(r.PR.Freshness))
		for _, pr := range snapshot.PRs {
			if pr.OnBranch && statusPRMatchesRepo(pr, *r) && statusObservationUnavailable(pr.Freshness) {
				blocked = true
			}
		}
		if r.ReviewOnly || blocked {
			continue
		}
		f, err := statusFactsWithHeads(ctx, *r, readHeads)
		if err != nil {
			return err
		}
		if r.PR == nil && r.BranchKind == "branch" {
			for _, pr := range snapshot.PRs {
				if !pr.OnBranch || statusObservationUnavailable(pr.Freshness) || pr.State != "MERGED" || !statusPRMatchesRepo(pr, *r) {
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
	return nil
}

// statusFacts is what a local row tells about the next step. Ahead counts
// against the branch's upstream (the development tip when it has none), so
// its development-tip fallback can include residual squash history.
// Observational landing fields do not change next-action authority.
func statusFacts(r StatusRepo) next.Facts { return statusFactsContext(context.Background(), r) }

func statusFactsContext(ctx context.Context, r StatusRepo) next.Facts {
	f, _ := statusFactsWithHeads(ctx, r, statusCurrentHeads)
	return f
}

func statusCurrentHeads(ctx context.Context, r StatusRepo) (string, string, error) {
	if head, base, ok := statusResourceHeads(ctx, r.Worktree, r.baseRef); ok {
		return head, base, nil
	}
	head, _ := statusGitContext(ctx, r.Worktree, "rev-parse", "HEAD")
	base := ""
	if r.baseRef != "" {
		base, _ = statusGitContext(ctx, r.Worktree, "rev-parse", r.baseRef)
	}
	return head, base, nil
}

func statusFactsWithHeads(ctx context.Context, r StatusRepo, readHeads statusHeadReader) (next.Facts, error) {
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
	var err error
	f.Head, f.BaseHead, err = readHeads(ctx, r)
	if err != nil {
		return f, err
	}
	if ctx.Err() == nil && r.Behind > 0 && r.baseRef != "" && f.InProgress == "" {
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
	return f, nil
}

// StatusPreferNewerObservations protects presentation from an older collector
// completion. It never grants local action authority from retained UI facts.
func StatusPreferNewerObservations(incoming, current StatusSnapshot) StatusSnapshot {
	if incoming.Collection != current.Collection || incoming.authContext != current.authContext || strings.HasPrefix(incoming.authContext, "unknown-") {
		return incoming
	}
	knownMerge := func(row StatusPRRow) bool {
		return row.State == "MERGED" && (row.Merge != nil && *row.Merge == "MERGED" || !statusObservationUnavailable(row.Freshness))
	}
	for i, row := range incoming.PRs {
		for _, old := range current.PRs {
			if row.Repo != old.Repo || row.Number != old.Number || row.URL == nil || old.URL == nil || *row.URL != *old.URL {
				continue
			}
			if knownMerge(row) && !knownMerge(old) {
				continue
			}
			at, err := time.Parse(time.RFC3339Nano, row.ObservedAt)
			before, oldErr := time.Parse(time.RFC3339Nano, old.ObservedAt)
			terminal := knownMerge(old) && !knownMerge(row)
			if !terminal && (oldErr != nil || (err == nil && !before.After(at))) {
				continue
			}
			retained := old
			if statusObservationUnavailable(row.Freshness) {
				// Recheck failure cannot revoke a confirmed terminal observation.
				// Keep its reason/retry separate from the retained fact's age.
				retained.ObservationReason, retained.RetryAt = row.ObservationReason, row.RetryAt
				if !knownMerge(old) {
					retained.Freshness = row.Freshness
				}
			}
			var historicalBranch *StatusRepo
			for j := range incoming.Repos {
				repo := &incoming.Repos[j]
				if repo.Repo != row.Repo {
					continue
				}
				if retained.OnBranch && knownMerge(retained) && statusRetainedMergedBranch(*repo, current.Repos, retained) {
					historicalBranch = repo
				}
				if repo.PR != nil && !statusRetainedPRIdentity(repo.PR, row) {
					continue
				}
				var facts *StatusPRFacts
				for _, prior := range current.Repos {
					if statusRetainedPRAssociation(*repo, prior, retained) {
						if !knownMerge(retained) {
							facts = statusRetainedPRFacts(retained)
						}
						break
					}
				}
				// Display evidence never supplies checkout/action authority.
				// Without the exact association, show no active PR facts.
				repo.PR = facts
				repo.PRUnconfirmed = true
				repo.Next, repo.Choices = nil, nil
			}
			if retained.OnBranch {
				if historicalBranch == nil {
					retained.OnBranch, retained.Landing = false, nil
					retained.DisplayTitle = retained.Title
				} else {
					// The branch is still associated, but snapshots do not prove
					// that its checkout head or landing classification is current.
					retained.Landing = &StatusLanding{State: "unknown", Reason: "retained merge; current branch landing not established"}
					historicalBranch.Landing = retained.Landing
					retained.DisplayTitle = "MERGED — " + retained.LandingLabel() + "; still on " + historicalBranch.Branch + "  " + retained.Title
				}
			}
			incoming.PRs[i] = retained
		}
	}
	return incoming
}

func statusRetainedPRIdentity(facts *StatusPRFacts, row StatusPRRow) bool {
	return facts != nil && row.URL != nil && *row.URL != "" && facts.URL == *row.URL && facts.Number == row.Number
}

// Snapshot association must agree on local identity and the reported PR head.
// A matching repository name alone cannot transfer checks across checkouts.
func statusRetainedPRAssociation(repo, prior StatusRepo, row StatusPRRow) bool {
	return statusRetainedLocalAssociation(repo, prior, row) &&
		statusRetainedPRIdentity(repo.PR, row) && statusRetainedPRIdentity(prior.PR, row) &&
		row.HeadCommit != "" && repo.PR.Head == row.HeadCommit && prior.PR.Head == row.HeadCommit
}

func statusRetainedLocalAssociation(repo, prior StatusRepo, row StatusPRRow) bool {
	return repo.Repo == row.Repo && prior.Repo == row.Repo &&
		(row.worktreeDir == "" || row.worktreeDir == repo.Dir) &&
		repo.Collection != "" && repo.Collection == prior.Collection &&
		repo.Dir != "" && repo.Dir == prior.Dir && repo.Worktree != "" && repo.Worktree == prior.Worktree &&
		repo.BranchKind == "branch" && prior.BranchKind == "branch" && repo.Branch != "" && repo.Branch == prior.Branch &&
		repo.Slug != "" && repo.Slug == prior.Slug && repo.Slug == row.Slug && repo.Forge != "" && repo.Forge == prior.Forge
}

// A historical merged-branch warning is not active PR/check evidence. Its
// stable local binding survives follow-up commits even though merged repos
// have no active PR facts. Cached rows without a directory need a unique prior
// branch candidate; an ambiguous alias must never inherit the warning.
func statusRetainedMergedBranch(repo StatusRepo, priors []StatusRepo, row StatusPRRow) bool {
	candidate := -1
	for i, prior := range priors {
		if prior.Repo != row.Repo || prior.BranchKind != "branch" || row.worktreeDir != "" && row.worktreeDir != prior.Dir {
			continue
		}
		if candidate >= 0 {
			return false
		}
		candidate = i
	}
	return candidate >= 0 && statusRetainedLocalAssociation(repo, priors[candidate], row)
}

func statusRetainedPRFacts(row StatusPRRow) *StatusPRFacts {
	value := func(s *string) string {
		if s != nil {
			return *s
		}
		return ""
	}
	return &StatusPRFacts{Number: row.Number, URL: value(row.URL), Head: row.HeadCommit,
		Checks: value(row.Checks), Merge: value(row.Merge), Review: value(row.Review), Draft: row.Draft,
		Freshness: row.Freshness, ObservedAt: row.ObservedAt, StateSource: row.StateSource,
		ObservationReason: row.ObservationReason, RetryAt: row.RetryAt,
		MergeConflicts: append([]string(nil), row.MergeConflicts...), MergeSource: row.MergeSource, MergeHead: row.MergeHead, MergeBase: row.MergeBase}
}

// StatusSnapshotAuthCurrent revalidates local selection at delivery boundaries,
// never during painting. A local-only snapshot has no provider provenance.
func StatusSnapshotAuthCurrent(snapshot StatusSnapshot) bool {
	if snapshot.authContext == "" {
		return true
	}
	inputs := snapshot.authInputs
	if inputs == "" {
		inputs = snapshot.authContext
	}
	return inputs == statusAuthSnapshotScope(context.Background())
}

// StatusWithoutChangedAuth removes provider observations and derived actions
// while retaining local Git/runtime facts until the replacement read completes.
func StatusWithoutChangedAuth(snapshot StatusSnapshot) (StatusSnapshot, bool) {
	if StatusSnapshotAuthCurrent(snapshot) {
		return snapshot, false
	}
	records := []StatusPRRow{}
	for _, row := range snapshot.PRs {
		if row.StateSource == "registry" && row.State == "MERGED" {
			records = append(records, row)
		}
	}
	snapshot.PRs = records
	snapshot.Orphans = nil
	snapshot.Retirement = nil
	snapshot.Repos = append([]StatusRepo(nil), snapshot.Repos...)
	for i := range snapshot.Repos {
		row := &snapshot.Repos[i]
		if row.PR == nil || row.PR.StateSource != "registry" {
			row.PR = nil
			row.PRUnconfirmed = true
		}
		row.Tip = nil
		row.Prod = nil
		row.Landing = nil
		row.Next = nil
		row.Choices = nil
	}
	snapshot.authContext = ""
	snapshot.authInputs = ""
	return snapshot, true
}
