package wtc

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"taba.sh/taba/internal/envcompat"
)

func statusBBMergeEventTime(raw []byte) string {
	var log struct {
		Activities []struct {
			Update struct {
				State string `json:"state"`
				Date  string `json:"date"`
			} `json:"update"`
		} `json:"activities"`
	}
	if json.Unmarshal(raw, &log) != nil {
		return ""
	}
	for _, activity := range log.Activities {
		if !strings.EqualFold(activity.Update.State, "MERGED") {
			continue
		}
		if when, err := time.Parse(time.RFC3339Nano, activity.Update.Date); err == nil {
			return when.UTC().Format(time.RFC3339)
		}
	}
	return ""
}

func statusBBCompleteMergeTime(detail statusPRDetail, slug string) statusPRDetail {
	if detail.State != "MERGED" || detail.MergedOn != "" {
		return detail
	}
	parts := strings.SplitN(slug, "/", 2)
	if len(parts) != 2 {
		return detail
	}
	raw, err := statusSingleFlight("activity", "bitbucket.org", slug, detail.Number, func() ([]byte, error) {
		return statusJSON("bb", "pr", "activity", detail.Number, "--workspace", parts[0], "--repo", parts[1], "--json")
	})
	if err == nil {
		detail.MergedOn = statusBBMergeEventTime(raw)
	}
	return detail
}

func statusString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func prURLForStatus(forge, slug, number string) string {
	if forge == "" || slug == "" {
		return ""
	}
	return prURL("https://"+forge+"/"+slug, number)
}

func (c *Context) statusRecordForge(record PRRecord, _ []StatusRepo) (slug, forge string) {
	return c.prRepositories(record.Repo).forge(record.Repo)
}

func statusUnknownDetail(record PRRecord) statusPRDetail {
	return statusPRDetail{Number: record.Number, State: "UNKNOWN", Checks: "NONE", Merge: "UNKNOWN", Review: "none", Title: record.Title}
}

func statusDeferredDetail(record PRRecord) statusPRDetail {
	return statusPRDetail{Number: record.Number, Checks: "PENDING", Title: record.Title}
}

func statusParseForgeDetail(forge string, raw []byte, record PRRecord) (statusPRDetail, error) {
	var detail statusPRDetail
	var err error
	switch forge {
	case "github.com":
		detail, err = statusGHDetail(raw, record)
	case "bitbucket.org":
		detail, err = statusBBDetail(raw, record)
	default:
		return statusPRDetail{}, fmt.Errorf("unsupported forge")
	}
	if err == nil && detail.Number != record.Number {
		err = fmt.Errorf("forge returned a different PR number")
	}
	return detail, err
}

func statusCachedSettledMerge(record PRRecord, slug, forge string) (statusPRDetail, bool) {
	// Reuse merged forge detail until its final facts are written to the
	// collection registry; the registry then avoids later forge calls.
	cached, ok := statusReadMergedForgeCache(forge, slug, record.Number)
	if !ok {
		return statusPRDetail{}, false
	}
	detail, err := statusParseForgeDetail(forge, cached, record)
	if err != nil || detail.State != "MERGED" || detail.MergedOn == "" || detail.ChecksUnsettled || detail.Checks == "PENDING" {
		return statusPRDetail{}, false
	}
	return detail, true
}

func statusReadCachedDetail(record PRRecord, slug, forge string) (statusPRDetail, bool) {
	cached, ok := statusReadForgeCache(forge, slug, record.Number)
	if !ok {
		return statusPRDetail{}, false
	}
	detail, err := statusParseForgeDetail(forge, cached, record)
	if err != nil {
		return statusPRDetail{}, false
	}
	if forge == "bitbucket.org" {
		detail = statusBBCompleteMergeTime(detail, slug)
	}
	return detail, true
}

func statusEnrichRecord(record PRRecord, slug, forge string) statusPRDetail {
	detail := statusEnrichRecordMetadata(record, slug, forge)
	if forge == "bitbucket.org" && (detail.State == "OPEN" || detail.State == "DRAFT") {
		evidence := statusBBHeadChecks(slug, detail.HeadCommit, false)
		detail.Checks, detail.ChecksUnsettled = evidence.Result, !evidence.Complete || evidence.Result == "PENDING"
		if detail.State == "DRAFT" && detail.Checks == "NONE" {
			detail.Checks = "draft"
		}
	}
	return detail
}

func statusEnrichRecordMetadata(record PRRecord, slug, forge string) statusPRDetail {
	if slug == "" || forge == "" {
		return statusUnknownDetail(record)
	}
	if detail, ok := statusCachedSettledMerge(record, slug, forge); ok {
		return detail
	}
	if listed, truncated, err := statusOpenPRs(forge, slug); err == nil {
		if item, ok := listed[record.Number]; ok {
			return statusEnrichListed(record, slug, forge, item)
		}
		if truncated {
			if detail, ok := statusReadCachedDetail(record, slug, forge); ok {
				return statusConfirmFullPR(record, slug, forge, detail, "capped")
			}
			return statusViewFreshPR(record, slug, forge)
		}
		if detail, ok := statusReadCachedDetail(record, slug, forge); ok && detail.State != "OPEN" && detail.State != "DRAFT" {
			return statusConfirmFullPR(record, slug, forge, detail, "list-omitted")
		}
		viewed := statusViewPR(record, slug, forge, true)
		if statusFullRefreshDue(forge, slug, record.Number) && (viewed.State == "OPEN" || viewed.State == "DRAFT") {
			statusLogIncrementalMiss(forge, slug, "#"+record.Number, "list-omitted", "state:"+viewed.State)
			statusMarkFullRefresh(forge, slug, record.Number)
		}
		return viewed
	}
	if detail, ok := statusReadCachedDetail(record, slug, forge); ok {
		return detail
	}
	return statusViewPR(record, slug, forge, false)
}

func statusEnrichListed(record PRRecord, slug, forge string, item statusOpenPR) statusPRDetail {
	unlock, lockErr := statusLockCache(statusForgeEntryPath("pr", forge, slug, record.Number))
	detail, used, matched := statusDetailFromOpenItem(record, slug, forge, item)
	if !used {
		if lockErr == nil {
			unlock()
		}
		return statusViewPR(record, slug, forge, false)
	}
	source := "list"
	if matched {
		source = "stamp"
	}
	detail = statusConfirmFullPRLocked(record, slug, forge, detail, source)
	if lockErr == nil {
		unlock()
	}
	return detail
}

func statusViewPR(record PRRecord, slug, forge string, bypassCache bool) statusPRDetail {
	return statusViewPRWithFreshness(record, slug, forge, bypassCache, false)
}

func statusViewFreshPR(record PRRecord, slug, forge string) statusPRDetail {
	return statusViewPRWithFreshness(record, slug, forge, false, true)
}

func statusViewPRWithFreshness(record PRRecord, slug, forge string, bypassCache, requireFresh bool) statusPRDetail {
	fetch := func() ([]byte, error) {
		switch forge {
		case "github.com":
			return statusJSON("gh", "pr", "view", record.Number, "--repo", slug, "--json", "number,state,title,isDraft,statusCheckRollup,reviewDecision,mergeStateStatus,mergeable,reviewRequests,latestReviews,mergedAt,baseRefName,mergeCommit,headRefOid")
		case "bitbucket.org":
			parts := strings.SplitN(slug, "/", 2)
			if len(parts) != 2 {
				return nil, fmt.Errorf("invalid Bitbucket repository slug")
			}
			return statusJSON("bb", "pr", "view", record.Number, "--workspace", parts[0], "--repo", parts[1], "--json")
		default:
			return nil, fmt.Errorf("unsupported forge")
		}
	}
	var raw []byte
	var err error
	var live bool
	if bypassCache {
		unlock, lockErr := statusLockCache(statusForgeEntryPath("pr", forge, slug, record.Number))
		if lockErr == nil {
			defer unlock()
		}
		if _, ok := statusReadForgeEntry("omit", forge, slug, record.Number); ok {
			if cached, ok := statusReadCachedDetail(record, slug, forge); ok {
				return cached
			}
		}
		stale, _ := statusReadForgeEntryAny("pr", forge, slug, record.Number)
		started := time.Now()
		raw, live, err = statusWithForgeBudget(forge, stale, fetch)
		if live && len(raw) > 0 {
			statusWriteForgeEntrySince("pr", forge, slug, record.Number, raw, started)
			statusWriteForgeEntry("omit", forge, slug, record.Number, []byte("ok"))
		}
	} else {
		raw, live, err = statusFetchShared("pr", forge, slug, record.Number, fetch)
	}
	if requireFresh && err == nil && !live {
		// Shared reads can fall back to expired data during cooling or a
		// spent allowance. An incomplete list cannot make that data current.
		var ok bool
		raw, ok = statusReadForgeCache(forge, slug, record.Number)
		if !ok {
			return statusDeferredDetail(record)
		}
	}
	if err == nil {
		if detail, parseErr := statusParseForgeDetail(forge, raw, record); parseErr == nil {
			if forge == "bitbucket.org" {
				detail = statusBBCompleteMergeTime(detail, slug)
			}
			return detail
		}
	}
	if errors.Is(err, errForgeBudget) || errors.Is(err, errForgeCooling) {
		return statusDeferredDetail(record)
	}
	return statusUnknownDetail(record)
}

// Forge CLIs spend most of their time waiting on network responses. Bound the
// fan-out so a large enlistment refreshes promptly without flooding a forge.
func (c *Context) statusEnrichRecords(records []PRRecord, rows []StatusRepo) ([]statusPRDetail, error) {
	repos := c.prRepositories()
	identities := make([][2]string, len(records))
	details := make([]statusPRDetail, len(records))
	pending := make([]int, 0, len(records))
	recorded := 0
	for _, record := range records {
		if err := ValidatePRIdentity(record.Repo, record.Number); err != nil {
			return nil, fmt.Errorf("invalid enlisted PR: %w", err)
		}
	}
	for i, record := range records {
		slug, forge, refErr := c.statusReadOnlyPRForge(repos, record)
		if refErr != nil {
			c.statusProgress(fmt.Sprintf("Cannot look up enlisted PR %s#%s: %v", record.Repo, record.Number, refErr))
			if record.MergedOn == "" {
				details[i] = statusUnknownDetail(record)
				continue
			}
		}
		identities[i] = [2]string{slug, forge}
		if record.MergedOn == "" {
			pending = append(pending, i)
			continue
		}
		checks := record.FinalChecks
		if checks == "" {
			checks = "NONE"
		}
		details[i] = statusPRDetail{Number: record.Number, State: "MERGED", Checks: checks,
			Merge: "MERGED", Review: "merged", Title: record.Title, MergedOn: record.MergedOn, MergeCommit: record.MergeCommit, HeadCommit: record.MergeHead, Base: record.MergeBase}
		recorded++
	}
	if recorded != 0 {
		c.statusProgress(fmt.Sprintf("Using %d recorded merges", recorded))
	}
	if len(pending) != 0 {
		c.statusProgress(fmt.Sprintf("Checking %d live pull requests", len(pending)))
	}
	previousNote := statusAuditNote
	statusAuditNote = func(message string) { c.statusProgress(message) }
	defer func() { statusAuditNote = previousNote }()
	const parallel = 4
	limit := make(chan struct{}, parallel)
	done := make(chan struct{}, len(pending))
	var workers sync.WaitGroup
	for _, i := range pending {
		record := records[i]
		workers.Add(1)
		go func() {
			defer workers.Done()
			limit <- struct{}{}
			defer func() { <-limit }()
			slug, forge := identities[i][0], identities[i][1]
			details[i] = statusEnrichRecord(record, slug, forge)
			done <- struct{}{}
		}()
	}
	for i := range pending {
		<-done
		c.statusProgress(fmt.Sprintf("Checked live pull requests %d/%d", i+1, len(pending)))
	}
	workers.Wait()
	return details, nil
}

// StatusForgePreview adds enlisted PRs and discovers open PRs on active
// branches. Build facts, snapshot persistence, and the live renderer are
// supplied by later stages. A forge failure never becomes a merged claim.
// It records final merges and returns clean merged worktrees to the tip.
func (c *Context) StatusForgePreview() (StatusSnapshot, error) {
	return c.statusForgePreview(true, true)
}

func (c *Context) statusForgePreview(includeBuild, recordMerges bool) (StatusSnapshot, error) {
	snapshot, err := c.StatusLocalSnapshot(false)
	if err != nil {
		return snapshot, err
	}
	records, err := c.ListPRs()
	if err != nil {
		return snapshot, err
	}
	if len(records) == 0 {
		_, err := os.Stat(c.PRFile())
		snapshot.PRsEmptyHint = os.IsNotExist(err)
		if err != nil && !os.IsNotExist(err) {
			return snapshot, err
		}
	}
	enlistedBranches := map[string]bool{}
	details, err := c.statusEnrichRecords(records, snapshot.Repos)
	if err != nil {
		return snapshot, err
	}
	if recordMerges {
		if count, err := c.recordMergedPRs(records, details); err != nil {
			c.statusProgress(fmt.Sprintf("Could not record merged pull requests: %v", err))
		} else if count != 0 {
			c.statusProgress(fmt.Sprintf("Recorded %d final merges", count))
		}
		c.statusReturnMerged(&snapshot, records, details)
	}
	repos := c.prRepositories()
	for i, record := range records {
		localSlug, localForge := repos.forge(record.Repo)
		slug, forge, refErr := c.statusReadOnlyPRForge(repos, record)
		detail := statusPredictBBMerge(details[i], localForge, localSlug, snapshot.Repos)
		mergeHead, mergeBase := statusMergeProvenance(detail)
		worktreeDir := ""
		if repo, err := repos.resolve(record.Repo); err == nil && refErr == nil {
			worktreeDir = repo.dir()
		}
		if record.Branch != "" && worktreeDir != "" {
			enlistedBranches[worktreeDir+"\x00"+record.Branch] = true
		}
		branchRow := -1
		for i, row := range snapshot.Repos {
			if row.Dir == worktreeDir && row.BranchKind == "branch" && row.Branch == record.Branch {
				branchRow = i
				break
			}
		}
		if detail.State == "OPEN" || detail.State == "DRAFT" {
			statusPRBase(&snapshot, branchRow, detail.Base)
			if branchRow >= 0 && snapshot.Repos[branchRow].PR == nil {
				prURL := prURLForStatus(forge, slug, record.Number)
				snapshot.Repos[branchRow].PR = &StatusPRFacts{Number: detail.Number, Head: detail.HeadCommit, Checks: detail.Checks,
					URL: prURL, Merge: detail.Merge, Review: detail.Review, Draft: detail.State == "DRAFT",
					MergeConflicts: detail.MergeConflicts, MergeSource: detail.MergeSource, MergeHead: mergeHead, MergeBase: mergeBase}
			}
		}
		if detail.State == "CLOSED" || detail.State == "DECLINED" || detail.State == "SUPERSEDED" {
			if branchRow >= 0 {
				snapshot.Orphans = append(snapshot.Orphans, StatusOrphan{Repo: worktreeDir, Branch: record.Branch, State: detail.State})
			}
			continue
		}
		url := ""
		if refErr == nil {
			url = prURLForStatus(forge, slug, record.Number)
		}
		row := StatusPRRow{Repo: record.Repo, Number: detail.Number, State: detail.State, Checks: statusString(detail.Checks),
			Merge: statusString(detail.Merge), Review: statusString(detail.Review), Title: detail.Title,
			DisplayTitle: detail.Title, Slug: slug, URL: statusString(url), MergedOn: statusString(detail.MergedOn),
			Draft: detail.State == "DRAFT", OnBranch: detail.State == "MERGED" && branchRow >= 0, MergeCommit: detail.MergeCommit, HeadCommit: detail.HeadCommit, Base: detail.Base,
			Archived:       detail.State == "MERGED" && branchRow < 0 && statusArchived(detail.MergedOn, time.Now()),
			MergeConflicts: detail.MergeConflicts, MergeSource: detail.MergeSource, MergeHead: mergeHead, MergeBase: mergeBase,
			worktreeDir: worktreeDir,
		}
		if row.OnBranch {
			repoRow := &snapshot.Repos[branchRow]
			row.Landing = statusMergedLanding(*repoRow, detail)
			repoRow.Landing = row.Landing
			row.DisplayTitle = "MERGED — " + row.LandingLabel() + "; still on " + record.Branch + "  " + detail.Title
		}
		snapshot.PRs = append(snapshot.PRs, row)
	}
	for i, row := range snapshot.Repos {
		if row.BranchKind != "branch" || row.PR != nil || enlistedBranches[row.Dir+"\x00"+row.Branch] {
			continue
		}
		slug, forge := repos.forge(row.Repo)
		c.statusProgress(fmt.Sprintf("Finding branch pull requests %d/%d", i+1, len(snapshot.Repos)))
		number, err := statusDiscoverBranch(forge, slug, row.Branch)
		if err != nil || number == "" {
			continue
		}
		detail := statusPredictBBMerge(statusEnrichRecord(PRRecord{Repo: row.Repo, Number: number}, slug, forge), forge, slug, snapshot.Repos)
		mergeHead, mergeBase := statusMergeProvenance(detail)
		if detail.State == "OPEN" || detail.State == "DRAFT" {
			statusPRBase(&snapshot, i, detail.Base)
			snapshot.Repos[i].PR = &StatusPRFacts{Number: detail.Number, Head: detail.HeadCommit, Checks: detail.Checks,
				URL: prURLForStatus(forge, slug, number), Merge: detail.Merge, Review: detail.Review, Draft: detail.State == "DRAFT",
				MergeConflicts: detail.MergeConflicts, MergeSource: detail.MergeSource, MergeHead: mergeHead, MergeBase: mergeBase}
		}
	}
	if includeBuild {
		c.statusProgress("Checking build providers")
		if err := c.statusBuildFacts(&snapshot); err != nil {
			return snapshot, err
		}
	}
	// The pull requests change what comes next.
	statusNext(&snapshot)
	return snapshot, nil
}

// statusReturnMergedEnabled reads the opt-out: WTC_STATUS_RETURN_MERGED set
// to off, 0, false or no in the environment of the status process, or
// return_merged = false under [status] in the harness's wtc.toml.
func (c *Context) statusReturnMergedEnabled() bool {
	switch strings.ToLower(envcompat.Getenv("WTC_STATUS_RETURN_MERGED")) {
	case "off", "0", "false", "no":
		return false
	}
	return c.Config.Status.ReturnMerged == nil || *c.Config.Status.ReturnMerged
}

// statusReturnMerged returns eligible clean merged worktrees to the development
// tip once landing is established. Refused PR URLs and ambiguous local identities
// do not authorize a return. Remaining branches keep their merged PR row,
// labelled by the observed landing state.
func (c *Context) statusReturnMerged(snapshot *StatusSnapshot, records []PRRecord, details []statusPRDetail) {
	if !c.statusReturnMergedEnabled() || len(records) != len(details) {
		return
	}
	// Resolve the harness from this collection's own owner or origin, as
	// catch-up does; an inherited WTC_HARNESS_REPO must not redirect it.
	harnessName, err := c.catchUpHarnessRepo()
	if err != nil {
		return
	}
	// If an inherited WTC_HARNESS_REPO names a different repository from this
	// collection's own harness, do not return its worktree.
	if inherited, _ := c.HarnessRepoName(); inherited != harnessName {
		return
	}
	repos := c.prRepositories()
	worktreeDir := func(name string) string {
		if repo, err := repos.resolve(name); err == nil {
			return repo.dir()
		}
		return ""
	}
	// A branch name may be reused: any other record on it that is not merged
	// leaves the branch to its owner. This is stricter than catch-up, which
	// settles on one state per branch. Both harness aliases name one worktree.
	live := map[string]bool{}
	for i, record := range records {
		if details[i].State != "MERGED" {
			live[worktreeDir(record.Repo)+"\x00"+record.Branch] = true
		}
	}
	for i, record := range records {
		if _, _, err := c.statusReadOnlyPRForge(repos, record); err != nil {
			continue
		}
		dir := worktreeDir(record.Repo)
		if details[i].State != "MERGED" || record.Branch == "" || live[dir+"\x00"+record.Branch] {
			continue
		}
		for j, row := range snapshot.Repos {
			if row.Dir != dir || row.BranchKind != "branch" || row.Branch != record.Branch || row.Tree != "clean" || row.Operation != "" {
				continue
			}
			_, targets, err := c.CatchUpInventory(CatchUpOptions{Repos: []string{dir}, DryRun: true})
			if err != nil || len(targets) != 1 {
				break
			}
			t := targets[0]
			tip, err := catchUpGit(t.path, "rev-parse", "--verify", t.ref+"^{commit}")
			if err != nil {
				break
			}
			// The snapshot predates the forge lookups; reconcile only the
			// branch it saw.
			pr := catchUpPR{state: "MERGED", mergeCommit: details[i].MergeCommit, branch: record.Branch}
			if !catchUpLanded(t, tip, pr) {
				// Reconciliation deliberately omits the recorded head observation.
				// Refresh head and merge together from the forge, as catch-up does.
				full := c.catchUpPRState(t, record.Branch)
				if full.state != "MERGED" {
					break
				}
				// Take head and merge commit from the same PR, never mixed
				// with this record's facts.
				pr.head, pr.mergeCommit = full.head, full.mergeCommit
			}
			outcome, reason := c.catchUpReconcile(t, t.ref, tip, pr, CatchUpOptions{CleanOnly: true})
			if outcome != "updated" {
				break
			}
			c.statusProgress(fmt.Sprintf("Returned %s to %s after its PR merged: %s", dir, t.ref, reason))
			if updated, err := statusLocalRepo(t); err == nil {
				updated.Runtime = row.Runtime // Services are untouched by the move.
				if row.Behind != 0 {
					snapshot.StaleCount--
				}
				if updated.Behind != 0 {
					snapshot.StaleCount++
				}
				snapshot.Repos[j] = updated
			}
			break
		}
	}
}

// statusPRBase uses the same base for enlisted and discovered pull requests.
func statusPRBase(snapshot *StatusSnapshot, index int, base string) {
	if index < 0 || base == "" {
		return
	}
	r := &snapshot.Repos[index]
	if !catchUpGitOK(r.Worktree, "check-ref-format", "--branch", base) {
		return
	}
	behind, err := statusCount(r.Worktree, "HEAD..origin/"+base)
	if err != nil {
		return
	}
	if r.Behind > 0 {
		snapshot.StaleCount--
	}
	r.Behind, r.baseRef = behind, "origin/"+base
	if behind > 0 {
		snapshot.StaleCount++
	}
}
