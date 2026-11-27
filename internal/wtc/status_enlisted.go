package wtc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"taba.sh/taba/internal/branding"
	"taba.sh/taba/internal/envcompat"
	"taba.sh/taba/internal/gitref"
)

// statusBBActivityLog is the activity log bitbucketReadPRActivity returns.
// TWG documents update.state and update.date on each entry of its
// chronological activity list ("twg help describe 'bitbucket pull-requests
// activity'"); bb output is assumed to carry the same Bitbucket Cloud values.
type statusBBActivityLog struct {
	Activities []struct {
		Update struct {
			State string `json:"state"`
			Date  string `json:"date"`
		} `json:"update"`
	} `json:"activities"`
}

func statusBBMergeEventTime(raw []byte) string {
	var log statusBBActivityLog
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

// statusBBDeclineEventTime is when the PR entered its current DECLINED state:
// the earliest update of the trailing run of DECLINED updates, ordered by
// event date, so a reopen and second decline dates by the second decline.
// The time is unknown when the latest update is in another state or any
// state update lacks a valid date; updated_on and observation time never
// stand in.
func statusBBDeclineEventTime(raw []byte) string {
	var log statusBBActivityLog
	if json.Unmarshal(raw, &log) != nil {
		return ""
	}
	type update struct {
		at       time.Time
		declined bool
	}
	var updates []update
	for _, activity := range log.Activities {
		if activity.Update.State == "" {
			continue
		}
		at, err := time.Parse(time.RFC3339Nano, activity.Update.Date)
		if err != nil {
			return ""
		}
		updates = append(updates, update{at, strings.EqualFold(activity.Update.State, "DECLINED")})
	}
	sort.SliceStable(updates, func(i, j int) bool { return updates[i].at.Before(updates[j].at) })
	declined := ""
	for i := len(updates) - 1; i >= 0 && updates[i].declined; i-- {
		declined = updates[i].at.UTC().Format(time.RFC3339)
	}
	return declined
}

// statusBBCompleteMergeTimeContext completes a Bitbucket terminal event time
// from the activity log: MergedOn of a MERGED PR, ClosedAt of a DECLINED one.
// It shares the single-flight, cached and rate-limited activity read; other
// states and known times cost no read, and an unavailable read leaves the
// time unknown.
//
// A decline, unlike a merge, can be reopened (a recorded decline holds only
// until the open list shows the PR again), so it is dated only where one read
// can serve every later run:
//   - a known credential context (a TWG environment token or a host read
//     credential), whose cache is fingerprinted and dies with the credential;
//     a TWG profile or bb is not one (docs/status-auth-cache.md), and
//   - a known updated_on (ClosedOn), which keys the log: a reopen or
//     re-decline changes it, so a log cached for an earlier decline is never
//     reused, while an unchanged decline reuses its log at any age.
//
// Anywhere else the decline stays unknown and costs no activity read. bb is
// excluded also because bb pr activity stops at its default page limit
// without saying so.
//
// An incomplete, malformed or refused read is not cached, and one status
// operation can complete the same decline on several paths (a detail read,
// a full confirmation, the collector), even concurrently. A decline's read is
// therefore shared through the operation (statusOperationShared): every path
// gets the first read's outcome, error included, and a new operation reads
// again.
func statusBBCompleteMergeTimeContext(ctx context.Context, detail statusPRDetail, slug string) statusPRDetail {
	merged := detail.State == "MERGED" && detail.MergedOn == ""
	declined := detail.State == "DECLINED" && detail.ClosedAt == ""
	if !merged && !declined {
		return detail
	}
	key := detail.Number
	if declined {
		key += "@" + detail.ClosedOn
		if detail.ClosedOn == "" || bitbucketReadFor(ctx).selection.Client == bitbucketClientBB || !statusEntryKnown(ctx, "activity", "bitbucket.org", slug, key) {
			return detail
		}
		if raw, ok := statusReadForgeEntryAnyContext(ctx, "activity", "bitbucket.org", slug, key); ok {
			detail.ClosedAt = statusBBDeclineEventTime(raw)
			return detail
		}
	}
	read := func() ([]byte, error) {
		return statusSingleFlightContext(ctx, "activity", "bitbucket.org", slug, key, func() ([]byte, error) {
			return bitbucketReadPRActivity(ctx, slug, detail.Number)
		})
	}
	var raw []byte
	var err error
	if declined {
		raw, err = statusOperationShared(ctx, "bitbucket-decline-activity\x00"+slug+"\x00"+key, read)
	} else {
		raw, err = read()
	}
	if err != nil {
		return detail
	}
	if merged {
		detail.MergedOn = statusBBMergeEventTime(raw)
	} else {
		detail.ClosedAt = statusBBDeclineEventTime(raw)
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

// statusRecordedDeclineDetail is a Bitbucket decline from the PR registry.
func statusRecordedDeclineDetail(record PRRecord) statusPRDetail {
	return statusPRDetail{Number: record.Number, State: "DECLINED", Checks: "NONE", Merge: "UNKNOWN", Review: "none",
		Title: record.Title, ClosedOn: record.DeclinedOn, ClosedAt: record.DeclinedAt, Freshness: "recorded", StateSource: "registry"}
}

// No provider read happened, so checks are unknown.
func statusDeferredDetail(record PRRecord) statusPRDetail {
	return statusPRDetail{Number: record.Number, Freshness: "deferred", Checks: "UNKNOWN", Title: record.Title}
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

func statusEnrichRecordContext(ctx context.Context, intent StatusRefreshIntent, record PRRecord, slug, forge string) statusPRDetail {
	ctx = statusAuthOperation(ctx)
	detail := statusEnrichRecordMetadataContext(ctx, intent, record, slug, forge)
	if ctx.Err() != nil {
		// The phase ended before the record finished.
		return statusLastObservationContext(ctx, record, slug, forge)
	}
	if forge == "bitbucket.org" && (detail.State == "OPEN" || detail.State == "DRAFT") {
		evidence, _ := statusBBHeadChecksContext(ctx, intent, slug, detail.HeadCommit, false)
		detail = statusWithBBChecks(detail, evidence)
	}
	return detail
}

func statusWithBBChecks(detail statusPRDetail, evidence headCheckEvidence) statusPRDetail {
	detail.Checks, detail.ChecksUnsettled = evidence.Result, !evidence.Complete || evidence.Result == "PENDING"
	if detail.State == "DRAFT" && detail.Checks == "NONE" {
		detail.Checks = "draft"
	}
	return detail
}

// statusEnrichRecordMetadataContext observes one PR through its repository's
// open list, the only routine read where a list exists. A listed PR takes its
// state from the list; a PR missing from it is resolved by
// statusUnlistedContext. A Bitbucket host without a REST credential has no
// list, and reads the PR by number (errBitbucketNoREST): once per cache age in
// a known credential context, every pass in an unknown one.
func statusEnrichRecordMetadataContext(ctx context.Context, intent StatusRefreshIntent, record PRRecord, slug, forge string) (result statusPRDetail) {
	ctx = statusAuthOperation(ctx)
	if ctx.Err() != nil {
		return statusLastObservationContext(ctx, record, slug, forge)
	}
	defer func() { result = statusObserveDetailContext(ctx, result, slug, forge) }()
	if _, valid := statusPRInventoryKey(forge, slug, record.Number); !valid {
		return statusUnknownDetail(record)
	}
	if detail, ok := statusResourceSettledMerge(ctx, record, slug, forge); ok {
		return detail
	}
	declined := record.DeclinedOn != "" && forge == "bitbucket.org"
	list, err := statusPRListContext(ctx, intent, record, slug, forge)
	if errors.Is(err, errBitbucketNoREST) && !declined {
		// Without a REST credential there is no open list to read. The PR is
		// read by number instead: at most once per cache age where the cache is
		// shared (a known credential context), every pass otherwise; `r` reads
		// it now.
		since := time.Now()
		if intent != StatusRefreshForeground {
			since = since.Add(-statusForgeCacheAge())
		}
		return statusReadUnlistedContext(ctx, intent, record, slug, forge, since)
	}
	current := err == nil && list.refused.reason == ""
	// A list served stale after a refused read may predate a recorded
	// decline, so only a current one reopens it, even capped: presence is
	// evidence, only absence from a capped list is not.
	if item, ok := list.prs[record.Number]; ok && err == nil && (current || !declined) {
		detail := statusEnrichListedContext(ctx, record, slug, forge, item, list.live)
		if detail.State == "OPEN" || detail.State == "DRAFT" {
			// Older list bytes served after a refused read name the refusal.
			detail = list.refused.apply(detail)
		}
		return detail
	}
	if declined {
		// A recorded decline costs no PR read. It may have been
		// reopened and merged while no current list saw it open, so an
		// explicit refresh reads it once; a refused or failed read keeps it.
		if intent == StatusRefreshForeground && (current || errors.Is(err, errBitbucketNoREST)) {
			since := list.started
			if err != nil {
				since = time.Now()
			}
			if detail := statusReadUnlistedContext(ctx, intent, record, slug, forge, since); !statusObservationUnavailable(detail.Freshness) && detail.State != "" && detail.State != "UNKNOWN" {
				return detail
			}
		}
		return statusRecordedDeclineDetail(record)
	}
	return statusUnlistedContext(ctx, intent, record, slug, forge, list, err)
}

// statusLastObservationContext is a PR's row once its operation has ended,
// with no read: a recorded decline as recorded, a retained merge marked
// unavailable, otherwise the last observation deferred with the reason.
func statusLastObservationContext(ctx context.Context, record PRRecord, slug, forge string) statusPRDetail {
	refused := statusRefusalFor(ctx.Err())
	if known, ok := statusResourceRetainedMerge(ctx, record, slug, forge); ok {
		known = refused.apply(known)
		known.Freshness = "unavailable"
		return known
	}
	if record.DeclinedOn != "" && forge == "bitbucket.org" {
		// Recorded like a merge: no read is needed to show it.
		return statusRecordedDeclineDetail(record)
	}
	if slug != "" && forge != "" {
		if known, ok := statusReadObservedDetailKindContext(ctx, "pr", record, slug, forge, 365*24*time.Hour); ok {
			if forge == "bitbucket.org" && (known.State == "OPEN" || known.State == "DRAFT") {
				// Bitbucket checks are kept per head, apart from the PR.
				evidence, cached := statusBBCachedHeadChecks(ctx, slug, known.HeadCommit)
				if !cached {
					evidence = unknownHeadChecks(known.HeadCommit)
				}
				known = statusWithBBChecks(known, evidence)
			}
			return refused.apply(known)
		}
	}
	return refused.apply(statusDeferredDetail(record))
}

// statusPRListContext is the open list that decides a PR. An explicit
// refresh reads it again. So does a complete list cached before this
// operation that lacks a PR never observed before, since the PR may be newer
// than the list; the PR is then shown from the list instead of a read of its
// own. The newest list known to lack such a PR is recorded ("unlisted"), so
// no list is read again for it and its own read gets the next token. A
// capped list is not read again: the PR may lie beyond the cap.
func statusPRListContext(ctx context.Context, intent StatusRefreshIntent, record PRRecord, slug, forge string) (statusOpenList, error) {
	if intent == StatusRefreshForeground {
		list, err := statusOpenListReadContext(ctx, intent, forge, slug, statusOperationBegan(ctx))
		if err == nil {
			return list, nil
		}
		// A list still within its age is shown with the refusal; nothing is
		// read twice.
		if raw, ok := statusReadForgeEntryContext(ctx, "openprs", forge, slug, "open"); ok {
			list, decodeErr := statusDecodeOpenList(raw)
			list.refused = statusRefusalFor(err)
			return list, decodeErr
		}
		return statusOpenList{}, err
	}
	began := statusOperationBegan(ctx)
	list, err := statusOpenListContext(ctx, forge, slug)
	if err != nil || list.refused.reason != "" || list.truncated {
		return list, err
	}
	if _, listed := list.prs[record.Number]; listed {
		return list, nil
	}
	if _, seen := statusResourceObservedDetail(ctx, record, slug, forge, 365*24*time.Hour); seen {
		return list, nil
	}
	stamp := func(at time.Time) []byte { return []byte(strconv.FormatInt(at.UnixNano(), 10)) }
	lacked, _ := statusReadForgeEntryAnyContext(ctx, "unlisted", forge, slug, record.Number)
	if list.started.Before(began) && string(lacked) != string(stamp(list.started)) {
		if fresh, err := statusOpenListReadContext(ctx, StatusRefreshBackground, forge, slug, list.started); err == nil {
			list = fresh
		}
		if _, listed := list.prs[record.Number]; listed {
			return list, nil
		}
	}
	statusResourceWriteEntry(ctx, "unlisted", forge, slug, record.Number, stamp(list.started))
	return list, nil
}

// statusOperationBegan is when this status operation first asked. A list
// that started later was read during the operation, by this pane or another.
func statusOperationBegan(ctx context.Context) time.Time {
	raw, _ := statusOperationShared(ctx, "operation-began", func() ([]byte, error) {
		return []byte(strconv.FormatInt(time.Now().UnixNano(), 10)), nil
	})
	began, _ := strconv.ParseInt(string(raw), 10, 64)
	return time.Unix(0, began)
}

// statusFinal reports whether an observation ends a PR's reads: closed,
// declined or merged with settled checks. A reopen shows in the open list.
func statusFinal(detail statusPRDetail) bool {
	switch detail.State {
	case "CLOSED", "DECLINED", "SUPERSEDED":
		return true
	case "MERGED":
		return !detail.ChecksUnsettled && detail.Checks != "PENDING"
	}
	return false
}

// statusUnlistedContext observes a PR missing from its open list. Only a
// current, complete list shows that the PR is no longer open. A final
// observation then stands, dated by the list. Any other PR is read on its
// own, once per list, to learn how it ended. A truncated or refused list
// confirms nothing. Background passes read no PR in its place; an explicit
// refresh reads a PR beyond a truncated list on its own.
func statusUnlistedContext(ctx context.Context, intent StatusRefreshIntent, record PRRecord, slug, forge string, list statusOpenList, listErr error) statusPRDetail {
	known, seen := statusResourceObservedDetail(ctx, record, slug, forge, 365*24*time.Hour)
	observed, _ := time.Parse(time.RFC3339Nano, known.ObservedAt)
	refused := list.refused
	if listErr != nil {
		refused = statusRefusalFor(listErr)
	} else if refused.reason == "" && list.truncated {
		refused.reason = "list-truncated"
	}
	current := listErr == nil && refused.reason == ""
	// notCurrent marks an observation this list cannot confirm: deferred
	// with the refusal, or unavailable when the list read failed.
	notCurrent := func(detail statusPRDetail) statusPRDetail {
		if refused.reason != "" {
			return refused.apply(detail)
		}
		detail.Freshness, detail.Reason = "unavailable", statusObservationFailure(listErr).Reason
		return detail
	}
	// An explicit refresh reads a close once more: it can have been undone
	// and merged between two lists. A merge is final once its checks have
	// settled (statusFinal), except a GitHub merge without its time, which
	// cannot be recorded until a read supplies it.
	untimed := forge == "github.com" && known.State == "MERGED" && known.MergedOn == ""
	final := seen && statusFinal(known) && !untimed && (intent != StatusRefreshForeground || known.State == "MERGED")
	switch {
	case final:
		if current && observed.Before(list.started) {
			known.Freshness = ""
			known = statusObserveAt(known, list.started, "open-list", forge)
		} else if !current && known.State != "MERGED" {
			// A close can be undone; without a current list it is not current.
			known = notCurrent(known)
		}
		return known
	case intent == StatusRefreshForeground && listErr == nil && list.refused.reason == "" && list.truncated:
		// A PR beyond a capped list is read on its own only under `r`:
		// nothing else would ever observe how it ended.
		return statusReadUnlistedContext(ctx, intent, record, slug, forge, list.started)
	case !current && seen:
		return notCurrent(known)
	case !current && refused.reason != "":
		return refused.apply(statusDeferredDetail(record))
	case !current:
		return notCurrent(statusUnknownDetail(record))
	}
	return statusReadUnlistedContext(ctx, intent, record, slug, forge, list.started)
}

// statusLocalMergeEvidence reads local hints for enlisted PRs whose branch is
// checked out: a gone upstream, and whether the checkout head is contained in
// its development tip. They are reported as evidence only. Neither is a
// forge-confirmed merge: they never set a MERGED state, record a merge,
// schedule a read, or authorize a checkout move.
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
			if !row.baseRef.IsZero() && catchUpGitOK(row.Worktree, "merge-base", "--is-ancestor", "HEAD", row.baseRef.String()) {
				out[i] += "; head in " + row.ComparisonRef
			}
			break
		}
	}
	return out
}

// statusEnrichListedContext takes a listed PR's state from its list item. A
// list read by this operation makes the row live.
func statusEnrichListedContext(ctx context.Context, record PRRecord, slug, forge string, item statusOpenPR, live bool) statusPRDetail {
	ctx = statusAuthOperation(ctx)
	listed, listErr := statusParseForgeDetail(forge, item.Raw, record)
	if listErr != nil {
		// The provider listed an item without identity or state: the list
		// says nothing about it, so it is read on its own, once per list.
		return statusReadUnlistedContext(ctx, StatusRefreshBackground, record, slug, forge, item.Started)
	}
	unlock, lockErr := statusLockCacheContext(ctx, statusForgeEntryPathContext(ctx, "pr", forge, slug, record.Number))
	if lockErr != nil {
		return statusLockUnavailableDetail(ctx, record, slug, forge, lockErr)
	}
	defer unlock()
	detail, _, _ := statusDetailFromOpenItemContext(ctx, record, slug, forge, item)
	detail = statusObserveDetailContext(ctx, detail, slug, forge)
	// The list in this pass observed the matching OPEN/DRAFT state, and an
	// unchanged stamp means its fingerprinted summary is unchanged too (GitHub:
	// update time, check rollup, reviews and mergeability; Bitbucket: update
	// time and participants, with checks read per head elsewhere). Its time
	// therefore dates the row and its action-gating freshness. The persisted
	// confirmation only dates
	// the state of later observations without a list item, and never
	// improves their freshness.
	observed, _ := time.Parse(time.RFC3339Nano, detail.ObservedAt)
	if listed.State == detail.State && (detail.State == "OPEN" || detail.State == "DRAFT") {
		statusRecordListedContext(ctx, forge, slug, listed, item.Started)
		// A confirmation persisted by an earlier pass of this same list may
		// already have dated the state to item.Started; this pass's list
		// still observed it, so equal time also dates the row.
		if !item.Started.Before(observed) {
			detail.Freshness = ""
			if live {
				detail.Freshness = "live"
			}
			detail = statusObserveAt(detail, item.Started, "open-list", forge)
		}
	}
	return detail
}

// statusFetchPRContext reads one PR from the provider. The caller admits it.
func statusFetchPRContext(ctx context.Context, record PRRecord, slug, forge string) ([]byte, error) {
	switch forge {
	case "github.com":
		return statusJSONContext(ctx, "gh", "pr", "view", record.Number, "--repo", slug, "--json", "number,state,title,isDraft,statusCheckRollup,reviewDecision,mergeStateStatus,mergeable,reviewRequests,latestReviews,mergedAt,closedAt,baseRefName,mergeCommit,headRefOid")
	case "bitbucket.org":
		return bitbucketReadPR(ctx, slug, record.Number)
	}
	return nil, fmt.Errorf("unsupported forge")
}

// statusReadUnlistedContext reads a PR that left its open list, on its own.
// Under the entry lock, an observation made since the list started (by
// another pane) stands instead of a second read.
func statusReadUnlistedContext(ctx context.Context, intent StatusRefreshIntent, record PRRecord, slug, forge string, since time.Time) statusPRDetail {
	ctx = statusAuthOperation(ctx)
	unlock, lockErr := statusLockCacheContext(ctx, statusForgeEntryPathContext(ctx, "pr", forge, slug, record.Number))
	if lockErr != nil {
		return statusLockUnavailableDetail(ctx, record, slug, forge, lockErr)
	}
	defer unlock()
	if known, ok := statusResourceObservedDetail(ctx, record, slug, forge, 365*24*time.Hour); ok {
		if observed, err := time.Parse(time.RFC3339Nano, known.ObservedAt); err == nil && !observed.Before(since) {
			return known
		}
	}
	stale, _ := statusReadForgeEntryAnyContext(ctx, "pr", forge, slug, record.Number)
	started := time.Now()
	raw, live, refused, err := statusWithForgeAdmissionContext(ctx, intent, forge, stale, func() ([]byte, error) {
		raw, err := statusFetchPRContext(ctx, record, slug, forge)
		if err == nil {
			var candidate statusPRDetail
			candidate, err = statusParseForgeDetail(forge, raw, record)
			if err == nil && candidate.State == "UNKNOWN" {
				err = errors.New("provider returned no PR state")
			}
			if err == nil && candidate.State != "MERGED" {
				if _, known := statusResourceRetainedMerge(ctx, record, slug, forge); known {
					err = fmt.Errorf("forge response conflicts with a retained merge observation")
				}
			}
		}
		return raw, err
	})
	if live && !statusResourceWriteSince(ctx, "pr", forge, slug, record.Number, raw, started) {
		if winner, ok := statusResourceObservedDetail(ctx, record, slug, forge, 365*24*time.Hour); ok {
			return winner
		}
		return statusRefusalFor(ctx.Err()).apply(statusDeferredDetail(record))
	}
	if err == nil {
		if detail, parseErr := statusParseForgeDetail(forge, raw, record); parseErr == nil {
			if detail.State != "MERGED" {
				if known, ok := statusResourceRetainedMerge(ctx, record, slug, forge); ok {
					return known
				}
			}
			if forge == "bitbucket.org" {
				detail = statusBBCompleteMergeTimeContext(ctx, detail, slug)
			}
			detail.Freshness = "deferred"
			if live {
				detail.Freshness = "live"
			}
			// Older bytes served after a refused read are deferred, with the
			// refusal as their reason.
			return statusObserveDetailContext(ctx, refused.apply(detail), slug, forge)
		}
	}
	if known, ok := statusResourceRetainedMerge(ctx, record, slug, forge); ok {
		known = refused.apply(known)
		known.Freshness = "unavailable"
		return known
	}
	if saved, ok := statusReadForgeEntryAnyContext(ctx, "pr", forge, slug, record.Number); ok {
		if detail, parseErr := statusParseForgeDetail(forge, saved, record); parseErr == nil {
			failure := statusObservationFailure(err)
			detail.Freshness, detail.Reason, detail.RetryAt = failure.Freshness, failure.Reason, failure.RetryAt
			return statusObserveDetailContext(ctx, detail, slug, forge)
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
func (c *Context) statusEnrichRecordsWithIntent(ctx context.Context, intent StatusRefreshIntent, records []PRRecord, rows []StatusRepo) ([]statusPRDetail, error) {
	ctx = statusAuthOperation(ctx)
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
			Merge: "MERGED", Review: "merged", Freshness: "recorded", StateSource: "registry", Title: record.Title, MergedOn: record.MergedOn, MergeCommit: record.MergeCommit, HeadCommit: record.MergeHead, Base: record.MergeBase}
		recorded++
	}
	if recorded != 0 {
		c.statusProgress(fmt.Sprintf("Using %d recorded merges", recorded))
	}
	if len(pending) != 0 {
		c.statusProgress(fmt.Sprintf("Collecting %d pull request facts", len(pending)))
	}
	const parallel = 4
	limit := make(chan struct{}, parallel)
	done := make(chan struct{}, len(pending))
	var workers sync.WaitGroup
	for _, i := range pending {
		workers.Add(1)
		go func() {
			defer workers.Done()
			select {
			case limit <- struct{}{}:
				defer func() { <-limit }()
			case <-ctx.Done():
				// An ended phase keeps the last observation, deferred, and
				// reads nothing.
			}
			details[i] = statusEnrichRecordContext(ctx, intent, records[i], identities[i][0], identities[i][1])
			done <- struct{}{}
		}()
	}
	for n := range pending {
		<-done
		c.statusProgress(fmt.Sprintf("Collected pull request facts %d/%d", n+1, len(pending)))
	}
	workers.Wait()
	// Every path that observes a Bitbucket merge or decline (list, retained,
	// background or explicit detail) completes its event time from the
	// activity log, so it can be recorded. An unavailable activity read leaves
	// the time unknown.
	for _, i := range pending {
		if identities[i][1] == "bitbucket.org" && !statusObservationUnavailable(details[i].Freshness) {
			details[i] = statusBBCompleteMergeTimeContext(ctx, details[i], identities[i][0])
		}
	}
	return details, nil
}

// StatusForgePreview adds enlisted PRs and discovers open PRs on active
// branches. A forge failure never becomes a merged claim. It serves the
// --forge preview and the next-step recheck, and neither records merges in
// the PR registry, repairs PR references, returns merged worktrees nor writes
// a snapshot. Scoped one-shot and periodic collections do all of these; an
// explicit refresh records merges and returns worktrees but skips reference
// repair; a workspace sweep does none. Like any status read it may publish
// provider observations to the account-scoped forge cache.
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
	observationCtx, stopObserving := ctx, func() {}
	if intent == StatusRefreshForeground {
		observationCtx, stopObserving = context.WithTimeout(ctx, metadataBudget)
	}
	details, err := c.statusEnrichRecordsWithIntent(observationCtx, intent, records, snapshot.Repos)
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
		bitbucket := make([]bool, len(records))
		for i, record := range records {
			_, forge, err := c.statusReadOnlyPRForge(c.prRepositories(), record)
			bitbucket[i] = err == nil && forge == "bitbucket.org"
		}
		if recorded, cleared, err := c.recordDeclinedPRs(records, settled, bitbucket); err != nil {
			c.statusProgress(fmt.Sprintf("Could not record declined pull requests: %v", err))
		} else if recorded != 0 || cleared != 0 {
			c.statusProgress(fmt.Sprintf("Recorded %d declines, cleared %d reopened", recorded, cleared))
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
		observation := statusDetailObservation(details[i])
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
					ObservationReason: observation.Reason, RetryAt: observation.RetryAt,
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
			DisplayTitle: detail.Title, Slug: slug, URL: statusString(url), MergedOn: statusString(detail.MergedOn), ClosedAt: statusString(detail.ClosedAt),
			Draft: detail.State == "DRAFT", OnBranch: detail.State == "MERGED" && branchRow >= 0, MergeCommit: detail.MergeCommit, HeadCommit: detail.HeadCommit, Base: detail.Base,
			// A Bitbucket decline holds until the PR is seen open again (a
			// reopened PR returns to the open list, which ends the decline), so
			// a DECLINED row keeps archive treatment once its ClosedOn date is
			// old enough, whatever its freshness: recorded, live, or the last
			// cached observation of a deferred or unavailable pass. A GitHub
			// close is not followed that way and needs a current observation;
			// neither grants actions.
			Archived: branchRow < 0 &&
				(detail.State == "MERGED" && statusArchived(detail.MergedOn, time.Now()) ||
					(detail.State == "DECLINED" || detail.State == "CLOSED" && !statusObservationUnavailable(detail.Freshness)) && statusArchived(detail.ClosedOn, time.Now())),
			Freshness: detail.Freshness, ObservedAt: detail.ObservedAt, StateSource: detail.StateSource,
			ObservationReason: observation.Reason, RetryAt: observation.RetryAt,
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
		detail := statusPredictBBMerge(statusEnrichRecordContext(ctx, intent, PRRecord{Repo: row.Repo, Number: number}, slug, forge), forge, slug, snapshot.Repos)
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
			tip, err := catchUpGit(t.path, "rev-parse", "--verify", t.ref.String()+"^{commit}")
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
			c.statusProgress(fmt.Sprintf("Returned %s to %s after its PR merged: %s", dir, t.ref.Short(), reason))
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
	ref := gitref.RemoteBranch("origin", base)
	behind, err := statusCount(r.Worktree, "HEAD.."+ref.String())
	if err != nil {
		return
	}
	if r.Behind > 0 {
		snapshot.StaleCount--
	}
	r.Behind, r.baseRef = behind, ref
	r.ComparisonRef = ref.Short()
	if behind > 0 {
		snapshot.StaleCount++
	}
}
