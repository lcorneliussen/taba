package wtc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"taba.sh/taba/internal/branding"
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
	return statusBBCompleteMergeTimeContext(context.Background(), detail, slug)
}

func statusBBCompleteMergeTimeContext(ctx context.Context, detail statusPRDetail, slug string) statusPRDetail {
	if detail.State != "MERGED" || detail.MergedOn != "" {
		return detail
	}
	parts := strings.SplitN(slug, "/", 2)
	if len(parts) != 2 {
		return detail
	}
	raw, err := statusSingleFlightContext(ctx, "activity", "bitbucket.org", slug, detail.Number, func() ([]byte, error) {
		return statusJSONContext(ctx, "bb", "pr", "activity", detail.Number, "--workspace", parts[0], "--repo", parts[1], "--json")
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

// Inventory equality validates forge paths independently of local aliases.
// An unresolved identity cannot suppress any other inventory row.
func statusPRInventoryKey(forge, slug, number string) (string, bool) {
	if forge != "github.com" && forge != "bitbucket.org" {
		return "", false
	}
	parts := strings.Split(slug, "/")
	if len(parts) != 2 || !statusPRForgePart(parts[0]) || !statusPRForgePart(parts[1]) || !prNumber.MatchString(number) || strings.HasPrefix(number, "0") {
		return "", false
	}
	if forge == "github.com" {
		slug = strings.ToLower(slug)
	}
	return forge + "/" + slug + "#" + number, true
}

func (c *Context) statusRecordForge(record PRRecord, _ []StatusRepo) (slug, forge string) {
	return c.prRepositories(record.Repo).forge(record.Repo)
}

func statusUnknownDetail(record PRRecord) statusPRDetail {
	return statusPRDetail{Number: record.Number, State: "UNKNOWN", Freshness: "unavailable", Checks: "NONE", Merge: "UNKNOWN", Review: "none", Title: record.Title}
}

func statusDeferredDetail(record PRRecord) statusPRDetail {
	return statusPRDetail{Number: record.Number, Freshness: "deferred", Checks: "PENDING", Title: record.Title}
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
	// Settled merge detail retains authority within statusMergedCacheAge (24h
	// by default).
	detail, ok := statusRetainedMerge(record, slug, forge)
	if !ok || detail.MergedOn == "" || detail.ChecksUnsettled || detail.Checks == "PENDING" {
		return statusPRDetail{}, false
	}
	detail.Freshness, detail.StateSource = "settled", "settled-cache"
	return detail, true
}

func statusReadCachedDetail(record PRRecord, slug, forge string) (statusPRDetail, bool) {
	return statusReadCachedDetailContext(context.Background(), record, slug, forge)
}

func statusReadCachedDetailContext(ctx context.Context, record PRRecord, slug, forge string) (statusPRDetail, bool) {
	detail, ok := statusResourceObservedDetail(ctx, record, slug, forge, statusForgeCacheAge())
	if ok && detail.Freshness == "stale" {
		return statusPRDetail{}, false
	}
	if ok && forge == "bitbucket.org" {
		detail = statusBBCompleteMergeTimeContext(ctx, detail, slug)
	}
	return detail, ok
}

func statusEnrichRecord(record PRRecord, slug, forge string) statusPRDetail {
	return statusEnrichRecordContext(context.Background(), record, slug, forge)
}

func statusEnrichRecordContext(ctx context.Context, record PRRecord, slug, forge string) statusPRDetail {
	ctx = statusAuthOperation(ctx)
	detail := statusEnrichRecordMetadataContext(ctx, record, slug, forge)
	if forge == "bitbucket.org" && (detail.State == "OPEN" || detail.State == "DRAFT") {
		evidence, _ := statusBBHeadChecksContext(ctx, StatusRefreshBackground, slug, detail.HeadCommit, false)
		detail.Checks, detail.ChecksUnsettled = evidence.Result, !evidence.Complete || evidence.Result == "PENDING"
		if detail.State == "DRAFT" && detail.Checks == "NONE" {
			detail.Checks = "draft"
		}
	}
	return detail
}

func statusEnrichRecordMetadata(record PRRecord, slug, forge string) (result statusPRDetail) {
	return statusEnrichRecordMetadataContext(context.Background(), record, slug, forge)
}

func statusEnrichRecordMetadataContext(ctx context.Context, record PRRecord, slug, forge string) (result statusPRDetail) {
	ctx = statusAuthOperation(ctx)
	if ctx.Err() != nil {
		refused := statusRefusalFor(ctx.Err())
		if known, ok := statusResourceRetainedMerge(ctx, record, slug, forge); ok {
			known = refused.apply(known)
			known.Freshness = "unavailable"
			return known
		}
		return refused.apply(statusDeferredDetail(record))
	}
	defer func() { result = statusObserveDetailContext(ctx, result, slug, forge) }()
	if slug == "" || forge == "" {
		return statusUnknownDetail(record)
	}
	if detail, ok := statusResourceSettledMerge(ctx, record, slug, forge); ok {
		statusForgetConfirmation(forge, slug+"#"+record.Number)
		return detail
	}
	if _, known := statusResourceRetainedMerge(ctx, record, slug, forge); known {
		// State confirmation is complete even if ancillary merge time/checks
		// still need their own admitted refreshes.
		statusForgetConfirmation(forge, slug+"#"+record.Number)
	}
	if listed, truncated, current, err := statusOpenPRsCurrentContext(ctx, forge, slug); err == nil {
		if _, ok := listed[record.Number]; ok && !current && statusLocalEvidenceHinted(ctx, forge, slug, record.Number) {
			// A checked-out branch whose upstream is gone is likely merged or
			// declined. An open list served stale after a refused read cannot
			// confirm it is still open; confirm the PR itself as for an
			// omitted PR (Bitbucket keeps its confirmation queue place), at
			// most once per cache interval. A current list is trusted.
			return statusViewPRWithLiveContext(ctx, record, slug, forge, true, false, nil)
		}
		if item, ok := listed[record.Number]; ok {
			statusForgetConfirmation(forge, slug+"#"+record.Number)
			return statusEnrichListedContext(ctx, record, slug, forge, item)
		}
		if truncated {
			if detail, ok := statusReadCachedDetailContext(ctx, record, slug, forge); ok {
				return statusConfirmFullPRContext(ctx, record, slug, forge, detail, "capped")
			}
			return statusViewPRWithLiveContext(ctx, record, slug, forge, false, true, nil)
		}
		if detail, ok := statusReadCachedDetailContext(ctx, record, slug, forge); ok && detail.State != "OPEN" && detail.State != "DRAFT" {
			return statusConfirmFullPRContext(ctx, record, slug, forge, detail, "list-omitted")
		}
		return statusViewPRWithLiveContext(ctx, record, slug, forge, true, false, func(viewed statusPRDetail) {
			if statusFullRefreshDueContext(ctx, forge, slug, record.Number) && (viewed.State == "OPEN" || viewed.State == "DRAFT") {
				statusLogIncrementalMiss(forge, slug, "#"+record.Number, "list-omitted", "state:"+viewed.State)
				statusMarkFullRefreshContext(ctx, forge, slug, record.Number)
			}
		})
	}
	if detail, ok := statusReadCachedDetailContext(ctx, record, slug, forge); ok {
		return detail
	}
	return statusViewPRWithLiveContext(ctx, record, slug, forge, false, false, nil)
}

type statusLocalEvidenceKey struct{}

func statusLocalEvidenceHinted(ctx context.Context, forge, slug, number string) bool {
	hinted, _ := ctx.Value(statusLocalEvidenceKey{}).(map[string]bool)
	key, valid := statusPRInventoryKey(forge, slug, number)
	return valid && hinted[key]
}

// statusLocalMergeEvidence reads local hints for enlisted PRs whose branch is
// checked out: a gone upstream, and whether the checkout head is contained in
// its development tip. The hints only schedule a provider read and are shown
// as evidence. Neither is a forge-confirmed merge: they never set a MERGED
// state, record a merge, or authorize a checkout move.
func (c *Context) statusLocalMergeEvidence(rows []StatusRepo, records []PRRecord) []string {
	repos := c.prRepositories()
	out := make([]string, len(records))
	for i, record := range records {
		if record.Branch == "" || record.MergedOn != "" {
			continue
		}
		repo, err := repos.resolve(record.Repo)
		if err != nil || repo == nil {
			continue
		}
		for _, row := range rows {
			if row.Dir != repo.dir() || row.BranchKind != "branch" || row.Branch != record.Branch || row.UpstreamState != "gone" {
				continue
			}
			out[i] = "upstream-gone"
			if row.ComparisonRef != "" && catchUpGitOK(row.Worktree, "merge-base", "--is-ancestor", "HEAD", row.ComparisonRef) {
				out[i] += "; head in " + row.ComparisonRef
			}
			break
		}
	}
	return out
}

func statusEnrichListed(record PRRecord, slug, forge string, item statusOpenPR) statusPRDetail {
	return statusEnrichListedContext(context.Background(), record, slug, forge, item)
}

func statusEnrichListedContext(ctx context.Context, record PRRecord, slug, forge string, item statusOpenPR) statusPRDetail {
	ctx = statusAuthOperation(ctx)
	unlock, lockErr := statusLockCacheContext(ctx, statusForgeEntryPathContext(ctx, "pr", forge, slug, record.Number))
	if lockErr != nil {
		return statusLockUnavailableDetail(ctx, record, slug, forge, lockErr)
	}
	detail, used, matched := statusDetailFromOpenItemContext(ctx, record, slug, forge, item)
	if !used {
		unlock()
		return statusViewPRWithLiveContext(ctx, record, slug, forge, false, false, nil)
	}
	source := "list"
	if matched {
		source = "stamp"
	}
	if detail.State == "MERGED" {
		source = "merged-cache"
	}
	detail = statusConfirmFullPRLockedContext(ctx, record, slug, forge, detail, source)
	detail = statusObserveDetailContext(ctx, detail, slug, forge)
	// The list in this pass observed the matching OPEN/DRAFT state, and its
	// item carries the check rollup and review summary the stamp fingerprints,
	// so its time dates the row and its action-gating freshness (unchanged
	// behavior). Conflicting audit detail keeps its own age. The persisted
	// confirmation only dates the state of later fallbacks, which have no such
	// list item, and never improves their freshness.
	observed, _ := time.Parse(time.RFC3339Nano, detail.ObservedAt)
	listed, listErr := statusParseForgeDetail(forge, item.Raw, record)
	if listErr == nil && listed.State == detail.State && (detail.State == "OPEN" || detail.State == "DRAFT") {
		statusRecordListedContext(ctx, forge, slug, listed, item.Started)
		// A confirmation persisted by an earlier pass of this same list may
		// already have dated the state to item.Started; this pass's list
		// still observed it, so equal time also dates the row.
		if !item.Started.Before(observed) {
			detail.Freshness = ""
			detail = statusObserveAt(detail, item.Started, "open-list")
		}
	}
	unlock()
	return detail
}

func statusViewPR(record PRRecord, slug, forge string, bypassCache bool) statusPRDetail {
	return statusViewPRWithFreshness(record, slug, forge, bypassCache, false)
}

func statusViewFreshPR(record PRRecord, slug, forge string) statusPRDetail {
	return statusViewPRWithFreshness(record, slug, forge, false, true)
}

func statusViewPRWithFreshness(record PRRecord, slug, forge string, bypassCache, requireFresh bool) statusPRDetail {
	return statusViewPRWithLive(record, slug, forge, bypassCache, requireFresh, nil)
}

func statusViewPRWithLive(record PRRecord, slug, forge string, bypassCache, requireFresh bool, onLive func(statusPRDetail)) statusPRDetail {
	return statusViewPRWithLiveContext(context.Background(), record, slug, forge, bypassCache, requireFresh, onLive)
}

func statusViewPRWithLiveContext(ctx context.Context, record PRRecord, slug, forge string, bypassCache, requireFresh bool, onLive func(statusPRDetail)) statusPRDetail {
	ctx = statusAuthOperation(ctx)
	fetchRaw := func() ([]byte, error) {
		switch forge {
		case "github.com":
			return statusJSONContext(ctx, "gh", "pr", "view", record.Number, "--repo", slug, "--json", "number,state,title,isDraft,statusCheckRollup,reviewDecision,mergeStateStatus,mergeable,reviewRequests,latestReviews,mergedAt,closedAt,baseRefName,mergeCommit,headRefOid")
		case "bitbucket.org":
			parts := strings.SplitN(slug, "/", 2)
			if len(parts) != 2 {
				return nil, fmt.Errorf("invalid Bitbucket repository slug")
			}
			return statusJSONContext(ctx, "bb", "pr", "view", record.Number, "--workspace", parts[0], "--repo", parts[1], "--json")
		default:
			return nil, fmt.Errorf("unsupported forge")
		}
	}
	fetch := func() ([]byte, error) {
		raw, err := fetchRaw()
		if err == nil {
			var candidate statusPRDetail
			candidate, err = statusParseForgeDetail(forge, raw, record)
			if err == nil && candidate.State != "MERGED" {
				if _, known := statusResourceRetainedMerge(ctx, record, slug, forge); known {
					err = fmt.Errorf("forge response conflicts with a retained merge observation")
				}
			}
		}
		return raw, err
	}
	var raw []byte
	var err error
	var live bool
	var refused statusRefusal
	if bypassCache {
		unlock, lockErr := statusLockCacheContext(ctx, statusForgeEntryPathContext(ctx, "pr", forge, slug, record.Number))
		if lockErr != nil {
			return statusLockUnavailableDetail(ctx, record, slug, forge, lockErr)
		}
		defer unlock()
		if _, ok := statusReadForgeEntryContext(ctx, "omit", forge, slug, record.Number); ok {
			if cached, ok := statusReadCachedDetailContext(ctx, record, slug, forge); ok {
				return cached
			}
		}
		stale, _ := statusReadForgeEntryAnyContext(ctx, "pr", forge, slug, record.Number)
		started := time.Now()
		raw, live, refused, err = statusWithForgeAdmissionContext(ctx, forge, slug+"#"+record.Number, stale, fetch)
		if live && len(raw) > 0 {
			if !statusResourceWriteSince(ctx, "pr", forge, slug, record.Number, raw, started) {
				if winner, ok := statusResourceObservedDetail(ctx, record, slug, forge, 365*24*time.Hour); ok {
					return winner
				}
				return statusDeferredDetail(record)
			}
			statusResourceWriteEntry(ctx, "omit", forge, slug, record.Number, []byte("ok"))
		}
	} else {
		raw, live, refused, err = statusFetchSharedAdmissionContext(ctx, "pr", forge, slug, record.Number, fetch)
	}
	if requireFresh && err == nil && !live {
		// Shared reads can fall back to expired data during cooling or a
		// spent allowance. An incomplete list cannot make that data current.
		var ok bool
		raw, ok = statusReadForgeCacheContext(ctx, forge, slug, record.Number)
		if !ok {
			if known, found := statusResourceRetainedMerge(ctx, record, slug, forge); found {
				known.Freshness = "deferred"
				return refused.apply(known)
			}
			return refused.apply(statusDeferredDetail(record))
		}
		refused = statusRefusal{}
	}
	if err == nil {
		if detail, parseErr := statusParseForgeDetail(forge, raw, record); parseErr == nil {
			if detail.State != "MERGED" {
				if known, ok := statusResourceRetainedMerge(ctx, record, slug, forge); ok {
					return known
				}
			}
			if live && onLive != nil {
				onLive(detail)
			}
			if forge == "bitbucket.org" {
				detail = statusBBCompleteMergeTimeContext(ctx, detail, slug)
			}
			if live {
				detail.Freshness = "live"
			} else if bypassCache {
				detail.Freshness = "deferred"
			}
			// Older bytes served after a refused read are deferred, with the
			// refusal as their reason; a current cache hit is not.
			detail = refused.apply(detail)
			return statusObserveDetailContext(ctx, detail, slug, forge)
		}
	}
	if err != nil {
		if known, ok := statusResourceRetainedMerge(ctx, record, slug, forge); ok {
			known = refused.apply(known)
			known.Freshness = "unavailable"
			return known
		}
	}
	if bypassCache && err != nil {
		if saved, ok := statusReadForgeEntryAnyContext(ctx, "pr", forge, slug, record.Number); ok {
			if detail, parseErr := statusParseForgeDetail(forge, saved, record); parseErr == nil {
				detail = refused.apply(detail)
				detail.Freshness = "unavailable"
				return statusObserveDetailContext(ctx, detail, slug, forge)
			}
		}
	}
	if errors.Is(err, errForgeBudget) || errors.Is(err, errForgeCooling) {
		return refused.apply(statusDeferredDetail(record))
	}
	return statusUnknownDetail(record)
}

// statusLockUnavailableDetail keeps a retained merge or defers, naming the
// cache lock (or the cancellation that interrupted it) as the reason.
func statusLockUnavailableDetail(ctx context.Context, record PRRecord, slug, forge string, lockErr error) statusPRDetail {
	detail := statusResourceUnavailableDetail(ctx, record, slug, forge)
	refused := statusRefusalFor(lockErr)
	if refused.reason == "" {
		refused.reason = "cache-lock"
	}
	freshness := detail.Freshness
	detail = refused.apply(detail)
	detail.Freshness = freshness
	return detail
}

// Forge CLIs spend most of their time waiting on network responses. Bound the
// fan-out so a large enlistment refreshes promptly without flooding a forge.
func (c *Context) statusEnrichRecords(records []PRRecord, rows []StatusRepo) ([]statusPRDetail, error) {
	details, _, err := c.statusEnrichRecordsWithIntent(context.Background(), StatusRefreshBackground, records, rows)
	return details, err
}

func (c *Context) statusEnrichRecordsWithIntent(ctx context.Context, intent StatusRefreshIntent, records []PRRecord, rows []StatusRepo) ([]statusPRDetail, []StatusObservation, error) {
	ctx = statusAuthOperation(ctx)
	repos := c.prRepositories()
	identities := make([][2]string, len(records))
	details := make([]statusPRDetail, len(records))
	observations := make([]StatusObservation, len(records))
	pending := make([]int, 0, len(records))
	recorded := 0
	for _, record := range records {
		if err := ValidatePRIdentity(record.Repo, record.Number); err != nil {
			return nil, nil, fmt.Errorf("invalid enlisted PR: %w", err)
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
			Merge: "MERGED", Review: "merged", Freshness: "recorded", StateSource: "registry", Title: record.Title, MergedOn: record.MergedOn, MergeCommit: record.MergeCommit, HeadCommit: record.MergeHead, Base: record.MergeBase}
		recorded++
	}
	if recorded != 0 {
		c.statusProgress(fmt.Sprintf("Using %d recorded merges", recorded))
	}
	if len(pending) != 0 {
		c.statusProgress(fmt.Sprintf("Collecting %d pull request facts", len(pending)))
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
			select {
			case limit <- struct{}{}:
			case <-ctx.Done():
				if intent == StatusRefreshForeground {
					slug, forge := identities[i][0], identities[i][1]
					details[i], observations[i] = statusRefreshRecord(ctx, intent, record, slug, forge)
				} else {
					details[i], observations[i] = statusFailedPRObservation(statusUnknownDetail(record), ctx.Err())
				}
				done <- struct{}{}
				return
			}
			defer func() { <-limit }()
			slug, forge := identities[i][0], identities[i][1]
			if intent == StatusRefreshForeground {
				details[i], observations[i] = statusRefreshRecord(ctx, intent, record, slug, forge)
			} else {
				details[i] = statusEnrichRecordContext(ctx, record, slug, forge)
			}
			done <- struct{}{}
		}()
	}
	for i := range pending {
		<-done
		c.statusProgress(fmt.Sprintf("Collected pull request facts %d/%d", i+1, len(pending)))
	}
	workers.Wait()
	// Every path that observes a Bitbucket merge (list, retained, background or
	// explicit detail) completes its merge time from the activity log, so it
	// can be recorded. An unavailable activity read leaves the time unknown.
	for _, i := range pending {
		if identities[i][1] == "bitbucket.org" && details[i].State == "MERGED" && details[i].MergedOn == "" && !statusObservationUnavailable(details[i].Freshness) {
			details[i] = statusBBCompleteMergeTimeContext(ctx, details[i], identities[i][0])
		}
	}
	for i := range observations {
		if observations[i].Freshness == "" {
			observations[i] = statusDetailObservation(details[i])
		}
	}
	return details, observations, nil
}

// StatusForgePreview adds enlisted PRs and discovers open PRs on active
// branches. A forge failure never becomes a merged claim. A preview does not
// record merges in the PR registry, repair PR references, return merged
// worktrees or write a snapshot; only full collectors do. Like any status
// read it may publish provider observations to the account-scoped forge cache.
func (c *Context) StatusForgePreview() (StatusSnapshot, error) {
	return c.statusForgePreview(true, false)
}

func (c *Context) statusForgePreview(includeBuild, recordMerges bool) (StatusSnapshot, error) {
	return c.statusForgePreviewWithIntent(c.resourceContext(), StatusRefreshBackground, includeBuild, recordMerges, 0)
}

func (c *Context) statusForgePreviewWithIntent(ctx context.Context, intent StatusRefreshIntent, includeBuild, recordMerges bool, metadataBudget time.Duration) (StatusSnapshot, error) {
	return c.statusForgePreviewFinalized(ctx, intent, includeBuild, recordMerges, metadataBudget, func(ctx context.Context, snapshot *StatusSnapshot) error {
		statusNextContext(ctx, snapshot)
		return ctx.Err()
	})
}

func (c *Context) statusForgePreviewFinalized(ctx context.Context, intent StatusRefreshIntent, includeBuild, recordMerges bool, metadataBudget time.Duration, finalize func(context.Context, *StatusSnapshot) error) (StatusSnapshot, error) {
	ctx = statusAuthOperation(ctx)
	c = c.WithStatusContext(ctx)
	if err := ctx.Err(); err != nil {
		return StatusSnapshot{}, err
	}
	// Final choices need the PR/build observations and any branch return below.
	// Collect local rows without issuing an unused preliminary head read.
	snapshot, err := c.statusLocalSnapshot(false, false)
	snapshot.authContext = statusAuthSnapshotScope(ctx)
	snapshot.authInputs = snapshot.authContext
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
	// Retain the original references for branch-return authority. A recovered
	// URL repairs status metadata; it must not move a checkout in this refresh.
	originalRecords := append([]PRRecord(nil), records...)
	if recordMerges && !c.statusSweep && intent != StatusRefreshForeground {
		c.repairPRReferences(records)
	}
	if err := ctx.Err(); err != nil {
		return snapshot, err
	}
	localEvidence := c.statusLocalMergeEvidence(snapshot.Repos, records)
	hinted := map[string]bool{}
	for i, record := range records {
		if localEvidence[i] == "" {
			continue
		}
		if slug, forge, err := c.statusReadOnlyPRForge(c.prRepositories(), record); err == nil {
			if key, valid := statusPRInventoryKey(forge, slug, record.Number); valid {
				hinted[key] = true
			}
		}
	}
	observationCtx, stopObserving := context.WithValue(ctx, statusLocalEvidenceKey{}, hinted), func() {}
	if intent == StatusRefreshForeground {
		observationCtx, stopObserving = context.WithTimeout(observationCtx, metadataBudget)
	}
	details, observations, err := c.statusEnrichRecordsWithIntent(observationCtx, intent, records, snapshot.Repos)
	stopObserving()
	if err != nil {
		return snapshot, err
	}
	if err := ctx.Err(); err != nil {
		return snapshot, err
	}
	if !statusAuthSelectionCurrent(ctx) {
		return snapshot, fmt.Errorf("forge credential context changed")
	}
	if recordMerges {
		settled := append([]statusPRDetail(nil), details...)
		for i := range settled {
			if statusObservationUnavailable(settled[i].Freshness) {
				settled[i].State = ""
			}
		}
		if count, err := c.recordMergedPRs(records, settled); err != nil {
			c.statusProgress(fmt.Sprintf("Could not record merged pull requests: %v", err))
		} else if count != 0 {
			c.statusProgress(fmt.Sprintf("Recorded %d final merges", count))
		}
		c.statusReturnMerged(&snapshot, originalRecords, details)
	}
	repos := c.prRepositories()
	inventoried := map[string]bool{}
	for i, record := range records {
		localSlug, localForge := repos.forge(record.Repo)
		slug, forge, refErr := c.statusReadOnlyPRForge(repos, record)
		detail := statusPredictBBMerge(details[i], localForge, localSlug, snapshot.Repos)
		mergeHead, mergeBase := statusMergeProvenance(detail)
		worktreeDir, localDir := "", ""
		if repo, err := repos.resolve(record.Repo); err == nil {
			localDir = repo.dir()
			if refErr == nil {
				worktreeDir = localDir
			}
		}
		if record.Branch != "" && localDir != "" {
			enlistedBranches[localDir+"\x00"+record.Branch] = true
		}
		branchRow := -1
		for i, row := range snapshot.Repos {
			if row.Dir == localDir && localDir != "" && row.BranchKind == "branch" && row.Branch == record.Branch {
				// Local uncertainty blocks actions without authorizing facts
				// or checkout association from a rejected PR URL.
				if refErr != nil || statusObservationUnavailable(detail.Freshness) {
					snapshot.Repos[i].PRUnconfirmed = true
				}
				if refErr == nil {
					branchRow = i
				}
				break
			}
		}
		if detail.State == "OPEN" || detail.State == "DRAFT" {
			statusPRBase(&snapshot, branchRow, detail.Base)
			if branchRow >= 0 && snapshot.Repos[branchRow].PR == nil {
				prURL := prURLForStatus(forge, slug, record.Number)
				snapshot.Repos[branchRow].PR = &StatusPRFacts{Number: detail.Number, Head: detail.HeadCommit, Checks: detail.Checks,
					URL: prURL, Merge: detail.Merge, Review: detail.Review, Draft: detail.State == "DRAFT",
					Freshness: detail.Freshness, ObservedAt: detail.ObservedAt, StateSource: detail.StateSource,
					ObservationReason: observations[i].Reason, RetryAt: observations[i].RetryAt,
					MergeConflicts: detail.MergeConflicts, MergeSource: detail.MergeSource, MergeHead: mergeHead, MergeBase: mergeBase}
			}
		}
		if (detail.State == "CLOSED" || detail.State == "DECLINED" || detail.State == "SUPERSEDED") && !statusObservationUnavailable(detail.Freshness) {
			if branchRow >= 0 {
				snapshot.Orphans = append(snapshot.Orphans, StatusOrphan{Repo: worktreeDir, Branch: record.Branch, State: detail.State})
			}
			// CLOSED/DECLINED retain their row and any checked-out orphan warning;
			// SUPERSEDED keeps the warning but omits its row. Closure never grants
			// merge authority or permission to return/delete the branch.
			if detail.State == "SUPERSEDED" {
				continue
			}
		}
		url := ""
		if refErr == nil {
			url = prURLForStatus(forge, slug, record.Number)
		}
		row := StatusPRRow{Repo: record.Repo, Number: detail.Number, State: detail.State, Checks: statusString(detail.Checks),
			Merge: statusString(detail.Merge), Review: statusString(detail.Review), Title: detail.Title,
			DisplayTitle: detail.Title, Slug: slug, URL: statusString(url), MergedOn: statusString(detail.MergedOn),
			Draft: detail.State == "DRAFT", OnBranch: detail.State == "MERGED" && branchRow >= 0, MergeCommit: detail.MergeCommit, HeadCommit: detail.HeadCommit, Base: detail.Base,
			// A Bitbucket decline is final, so any observation of it (current,
			// stale, deferred or unavailable) keeps archive treatment by its
			// closure time. A GitHub close can be reopened and needs a current
			// observation; neither grants actions.
			Archived: branchRow < 0 &&
				(detail.State == "MERGED" && statusArchived(detail.MergedOn, time.Now()) ||
					(detail.State == "DECLINED" || detail.State == "CLOSED" && !statusObservationUnavailable(detail.Freshness)) && statusArchived(detail.ClosedOn, time.Now())),
			Freshness: detail.Freshness, ObservedAt: detail.ObservedAt, StateSource: detail.StateSource,
			ObservationReason: observations[i].Reason, RetryAt: observations[i].RetryAt,
			MergeConflicts: detail.MergeConflicts, MergeSource: detail.MergeSource, MergeHead: mergeHead, MergeBase: mergeBase,
			LocalMergeEvidence: localEvidence[i],
			worktreeDir:        worktreeDir,
		}
		if row.OnBranch {
			repoRow := &snapshot.Repos[branchRow]
			row.Landing = statusMergedLanding(*repoRow, detail)
			repoRow.Landing = row.Landing
			row.DisplayTitle = "MERGED — " + row.LandingLabel() + "; still on " + record.Branch + "  " + detail.Title
		}
		snapshot.PRs = append(snapshot.PRs, row)
		if identity, valid := statusPRInventoryKey(forge, slug, record.Number); refErr == nil && valid {
			inventoried[identity] = true
		}
	}
	for i, row := range snapshot.Repos {
		if row.BranchKind != "branch" || row.PR != nil || enlistedBranches[row.Dir+"\x00"+row.Branch] {
			continue
		}
		slug, forge := repos.forge(row.Repo)
		c.statusProgress(fmt.Sprintf("Finding branch pull requests %d/%d", i+1, len(snapshot.Repos)))
		number, err := statusDiscoverBranchContext(ctx, forge, slug, row.Branch)
		if err != nil || number == "" {
			continue
		}
		detail := statusPredictBBMerge(statusEnrichRecordContext(ctx, PRRecord{Repo: row.Repo, Number: number}, slug, forge), forge, slug, snapshot.Repos)
		mergeHead, mergeBase := statusMergeProvenance(detail)
		if detail.State == "OPEN" || detail.State == "DRAFT" {
			statusPRBase(&snapshot, i, detail.Base)
			snapshot.Repos[i].PR = &StatusPRFacts{Number: detail.Number, Head: detail.HeadCommit, Checks: detail.Checks,
				URL: prURLForStatus(forge, slug, number), Merge: detail.Merge, Review: detail.Review, Draft: detail.State == "DRAFT",
				Freshness: detail.Freshness, ObservedAt: detail.ObservedAt, StateSource: detail.StateSource,
				MergeConflicts: detail.MergeConflicts, MergeSource: detail.MergeSource, MergeHead: mergeHead, MergeBase: mergeBase}
			identity, valid := statusPRInventoryKey(forge, slug, number)
			if !valid || !inventoried[identity] {
				command := branding.Text("{{command}} pr enlist ") + shellQuote(row.Repo) + " " + number + " --branch " + shellQuote(row.Branch)
				snapshot.PRs = append(snapshot.PRs, StatusPRRow{
					Repo: row.Repo, Number: detail.Number, State: detail.State,
					NotEnlisted: true, EnlistCommand: command,
					Title: detail.Title, DisplayTitle: "not enlisted — " + command + "  " + detail.Title,
					Slug: slug, URL: statusString(prURLForStatus(forge, slug, number)),
					Checks: statusString(detail.Checks), Merge: statusString(detail.Merge), Review: statusString(detail.Review),
					Draft: detail.State == "DRAFT", HeadCommit: detail.HeadCommit, Base: detail.Base,
					Freshness: detail.Freshness, ObservedAt: detail.ObservedAt, StateSource: detail.StateSource,
					MergeConflicts: detail.MergeConflicts, MergeSource: detail.MergeSource, MergeHead: mergeHead, MergeBase: mergeBase,
					worktreeDir: row.Dir,
				})
				if valid {
					inventoried[identity] = true
				}
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return snapshot, err
	}
	if includeBuild {
		c.statusProgress("Checking build providers")
		if err := c.statusBuildFacts(&snapshot); err != nil {
			return snapshot, err
		}
	}
	if err := ctx.Err(); err != nil {
		return snapshot, err
	}
	if !statusAuthSelectionCurrent(ctx) {
		return snapshot, fmt.Errorf("forge credential context changed")
	}
	snapshot.authContext = statusSnapshotAuthStamp(ctx, snapshot)
	// The pull requests change what comes next.
	if err := finalize(ctx, &snapshot); err != nil {
		return snapshot, err
	}
	if err := ctx.Err(); err != nil {
		return snapshot, err
	}
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
	if inherited, _ := c.harnessRepoNameContext(c.resourceContext()); inherited != harnessName {
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
		if details[i].State != "MERGED" || statusObservationUnavailable(details[i].Freshness) {
			live[worktreeDir(record.Repo)+"\x00"+record.Branch] = true
		}
	}
	for i, record := range records {
		if _, _, err := c.statusReadOnlyPRForge(repos, record); err != nil {
			continue
		}
		dir := worktreeDir(record.Repo)
		if details[i].State != "MERGED" || statusObservationUnavailable(details[i].Freshness) || record.Branch == "" || live[dir+"\x00"+record.Branch] {
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
			if updated, err := statusLocalRepoContext(c.resourceContext(), t); err == nil {
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
	r.ComparisonRef = r.baseRef
	if behind > 0 {
		snapshot.StaleCount++
	}
}
