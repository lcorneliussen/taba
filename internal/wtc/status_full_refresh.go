package wtc

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"taba.sh/taba/internal/envcompat"
)

var statusAuditNote func(string)

func statusFullRefreshEvery() time.Duration {
	raw := strings.TrimSpace(envcompat.Getenv("WTC_FORGE_FULL_REFRESH"))
	if raw == "" {
		return 10 * time.Minute
	}
	seconds, err := strconv.Atoi(raw)
	if err != nil || seconds <= 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}

func statusFullRefreshDue(forge, slug, key string) bool {
	return statusFullRefreshDueContext(context.Background(), forge, slug, key)
}

func statusFullRefreshDueContext(ctx context.Context, forge, slug, key string) bool {
	ctx = statusAuthOperation(ctx)
	// Incremental audits protect reuse of persistent observations. Unknown
	// selection has only live operation-private observations, so there is no
	// persistent fingerprint to audit. Explicit/by-number reads still run.
	if !statusEntryKnown(ctx, "full", forge, slug, key) {
		return false
	}
	every := statusFullRefreshEvery()
	if every <= 0 {
		return false
	}
	_, _, fresh := statusReadForgeEntryObservedContext(ctx, "full", forge, slug, key, every)
	return !fresh
}

func statusMarkFullRefresh(forge, slug, key string) {
	statusMarkFullRefreshContext(context.Background(), forge, slug, key)
}

func statusMarkFullRefreshContext(ctx context.Context, forge, slug, key string) {
	statusResourceWriteEntry(ctx, "full", forge, slug, key, []byte("ok"))
}

func statusAuditValue(value string) string {
	value = strings.ReplaceAll(value, "\n", " ")
	if len(value) > 80 {
		value = value[:80]
	}
	return value
}

func statusAuditDiff(name, from, to string, parts []string) []string {
	if from == to {
		return parts
	}
	return append(parts, name+":"+statusAuditValue(from)+"->"+statusAuditValue(to))
}

func statusPRDetailDiff(before, after statusPRDetail) string {
	var parts []string
	parts = statusAuditDiff("state", before.State, after.State, parts)
	parts = statusAuditDiff("checks", before.Checks, after.Checks, parts)
	parts = statusAuditDiff("checks-unsettled", strconv.FormatBool(before.ChecksUnsettled), strconv.FormatBool(after.ChecksUnsettled), parts)
	parts = statusAuditDiff("merge", before.Merge, after.Merge, parts)
	parts = statusAuditDiff("review", before.Review, after.Review, parts)
	parts = statusAuditDiff("title", before.Title, after.Title, parts)
	parts = statusAuditDiff("base", before.Base, after.Base, parts)
	parts = statusAuditDiff("merged", before.MergedOn, after.MergedOn, parts)
	parts = statusAuditDiff("commit", before.MergeCommit, after.MergeCommit, parts)
	return strings.Join(parts, " ")
}

func statusBuildValue(build *StatusBuild) (number, checks, sha string) {
	if build == nil {
		return "", "", ""
	}
	if build.Build != nil {
		number = *build.Build
	}
	if build.Checks != nil {
		checks = *build.Checks
	}
	if build.SHA != nil {
		sha = *build.SHA
	}
	return number, checks, sha
}

func statusBuildDiff(before, after *StatusBuild) string {
	fromNumber, fromChecks, fromSHA := statusBuildValue(before)
	toNumber, toChecks, toSHA := statusBuildValue(after)
	var parts []string
	parts = statusAuditDiff("build", fromNumber, toNumber, parts)
	parts = statusAuditDiff("checks", fromChecks, toChecks, parts)
	parts = statusAuditDiff("sha", fromSHA, toSHA, parts)
	return strings.Join(parts, " ")
}

func statusLogIncrementalMiss(forge, slug, key, source, diff string) {
	if strings.TrimSpace(diff) == "" {
		return
	}
	line := fmt.Sprintf("incremental miss %s %s %s %s %s", forge, slug, key, source, diff)
	if statusAuditNote != nil {
		statusAuditNote(line)
	}
	dir := statusForgeCacheDir()
	if !statusSecureCacheDir(dir) {
		return
	}
	file, err := os.OpenFile(filepath.Join(dir, "incremental-misses.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return
	}
	defer file.Close()
	fmt.Fprintf(file, "%s %s\n", time.Now().UTC().Format(time.RFC3339), line)
}

func statusIncrementalMissLog() string {
	return filepath.Join(statusForgeCacheDir(), "incremental-misses.log")
}

// statusRefreshRecord is the explicit local metadata primitive. It does not
// enumerate repositories or read an open list, so stale membership cannot
// suppress the selected record's foreground observation. Collector integration
// supplies only active records; recorded and retained terminal state is also
// excluded here as a second safety boundary.
func statusRefreshRecord(ctx context.Context, intent StatusRefreshIntent, record PRRecord, slug, forge string) (result statusPRDetail, observation StatusObservation) {
	ctx = statusAuthOperation(ctx)
	if intent != StatusRefreshForeground {
		detail := statusEnrichRecordMetadataContext(ctx, record, slug, forge)
		return detail, statusDetailObservation(detail)
	}
	ctx, cancel := statusRefreshContext(ctx)
	defer cancel()
	defer func() {
		if forge != "bitbucket.org" || (result.State != "OPEN" && result.State != "DRAFT") {
			return
		}
		checks, err := statusBBHeadChecksContext(ctx, intent, slug, result.HeadCommit, true)
		result.Checks, result.ChecksUnsettled = checks.Result, !checks.Complete || checks.Result == "PENDING"
		if result.State == "DRAFT" && result.Checks == "NONE" {
			result.Checks = "draft"
		}
		if err != nil && observation.Freshness == "live" {
			failure := statusObservationFailure(err)
			failure.ObservedAt, failure.Source = observation.ObservedAt, observation.Source
			observation = failure
			result.Freshness = failure.Freshness
		}
	}()
	fallback := statusUnknownDetail(record)
	if record.MergedOn != "" {
		checks := record.FinalChecks
		if checks == "" {
			checks = "NONE"
		}
		detail := statusPRDetail{Number: record.Number, State: "MERGED", Checks: checks, Merge: "MERGED", Review: "merged", Title: record.Title,
			MergedOn: record.MergedOn, MergeCommit: record.MergeCommit, HeadCommit: record.MergeHead, Base: record.MergeBase,
			Freshness: "recorded", StateSource: "registry"}
		return detail, statusDetailObservation(detail)
	}
	if _, valid := statusPRInventoryKey(forge, slug, record.Number); !valid {
		return fallback, statusObservationFailure(fmt.Errorf("invalid PR identity"))
	}
	// Pure cache reads retain known facts even if cancellation prevents lock
	// admission. The locked path below still revalidates retained scoped evidence.
	if known, ok := statusReadObservedDetailKindContext(ctx, "merge", record, slug, forge, statusMergedCacheAge()); ok && known.State == "MERGED" {
		fallback = known
	} else if known, ok := statusReadObservedDetailKindContext(ctx, "pr", record, slug, forge, 365*24*time.Hour); ok {
		fallback = known
	}
	unlock, err := statusLockCacheContext(ctx, statusForgeEntryPathContext(ctx, "pr", forge, slug, record.Number))
	if err != nil {
		return statusFailedPRObservation(fallback, err)
	}
	defer unlock()
	known, ok, readErr := statusReadObservedDetailContext(ctx, record, slug, forge, 365*24*time.Hour)
	if ok {
		fallback = known
	}
	if readErr != nil {
		return statusFailedPRObservation(fallback, readErr)
	}
	if ok && known.State == "MERGED" {
		return known, statusDetailObservation(known)
	}
	if err := statusTakeForegroundToken(ctx, forge); err != nil {
		outcome := statusObservationFailure(err)
		fallback.Freshness = outcome.Freshness
		outcome.ObservedAt, outcome.Source = fallback.ObservedAt, fallback.StateSource
		return fallback, outcome
	}
	// Cancellation after admission can waste a token, never start queued work.
	if err := ctx.Err(); err != nil {
		outcome := statusObservationFailure(err)
		fallback.Freshness = outcome.Freshness
		outcome.ObservedAt, outcome.Source = fallback.ObservedAt, fallback.StateSource
		return fallback, outcome
	}
	started := time.Now()
	raw, err := statusFetchPRRawContext(ctx, record, slug, forge)
	observed := time.Now()
	if err != nil {
		if statusRateLimited(err) {
			until, coolErr := statusForgeCoolContext(ctx, forge, statusRetryAfter(err))
			if coolErr != nil {
				outcome := StatusObservation{Freshness: "deferred", Reason: "provider-cooldown-unrecorded", RetryAt: until.UTC().Format(time.RFC3339Nano), ObservedAt: fallback.ObservedAt, Source: fallback.StateSource}
				fallback.Freshness = outcome.Freshness
				return fallback, outcome
			}
			err = &statusAdmissionError{cause: errForgeCooling, retry: until}
		}
		outcome := statusObservationFailure(err)
		fallback.Freshness = outcome.Freshness
		outcome.ObservedAt, outcome.Source = fallback.ObservedAt, fallback.StateSource
		return fallback, outcome
	}
	detail, err := statusParseForgeDetail(forge, raw, record)
	if err == nil && (detail.State == "" || detail.State == "UNKNOWN") {
		err = errors.New("provider returned no PR state")
	}
	if err != nil {
		fallback.Freshness = "unavailable"
		outcome := statusObservationFailure(err)
		outcome.ObservedAt, outcome.Source = fallback.ObservedAt, fallback.StateSource
		return fallback, outcome
	}
	publication, publishErr := statusWriteForgeEntrySinceObservedContext(ctx, "pr", forge, slug, record.Number, raw, started, observed)
	if !publication.Written && !publication.Retained {
		if publishErr != nil {
			return statusFailedPRObservation(fallback, publishErr)
		}
		winner, ok, readErr := statusReadObservedDetailContext(ctx, record, slug, forge, 365*24*time.Hour)
		if readErr != nil {
			return statusFailedPRObservation(fallback, readErr)
		}
		if ok {
			outcome := statusDetailObservation(winner)
			outcome.Reason = "superseded"
			if winner.State != detail.State {
				winner.Freshness, outcome.Freshness, outcome.Reason = "unavailable", "unavailable", "conflicting-observation"
			}
			return winner, outcome
		}
		fallback.Freshness = "unavailable"
		return fallback, StatusObservation{Freshness: "unavailable", Reason: "cache-publication-failed", ObservedAt: fallback.ObservedAt, Source: fallback.StateSource}
	}
	detail.ObservedAt, detail.StateSource, detail.Freshness = observed.UTC().Format(time.RFC3339Nano), "foreground", "live"
	if publication.Retained {
		// Retention can commit before cancellation prevents ordinary detail
		// publication. Read that independent authority without another lock or
		// copy; never revoke a persisted merge because the request ended.
		if retained, ok := statusReadObservedDetailKindContext(ctx, "merge", record, slug, forge, statusMergedCacheAge()); ok && retained.State == "MERGED" {
			if retained.ObservedAt != detail.ObservedAt || retained.State != detail.State || retained.HeadCommit != detail.HeadCommit || retained.Title != detail.Title {
				outcome := statusDetailObservation(retained)
				outcome.Reason = "superseded"
				return retained, outcome
			}
		}
	}
	outcome := StatusObservation{Freshness: "live", ObservedAt: detail.ObservedAt, Source: "foreground", ProviderConfirmed: true}
	switch {
	case errors.Is(publishErr, context.Canceled) || errors.Is(ctx.Err(), context.Canceled):
		outcome.Reason = "cancelled-after-observation"
	case errors.Is(publishErr, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded):
		outcome.Reason = "deadline-after-observation"
	case publishErr != nil:
		outcome.Reason = "detail-publication-failed"
	default:
		outcome.Reason = "provider-confirmed"
		statusMarkFullRefreshContext(ctx, forge, slug, record.Number)
	}
	return detail, outcome
}

func statusFailedPRObservation(detail statusPRDetail, err error) (statusPRDetail, StatusObservation) {
	outcome := statusObservationFailure(err)
	detail.Freshness = outcome.Freshness
	outcome.ObservedAt, outcome.Source = detail.ObservedAt, detail.StateSource
	return detail, outcome
}

// The admission caller already debited the shared bucket. No second token and
// no independent TTL path is allowed here. Commands observe cancellation while
// running; stdout remains separate from provider diagnostics on stderr.
func statusFetchPRRawContext(ctx context.Context, record PRRecord, slug, forge string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var name string
	var args []string
	switch forge {
	case "github.com":
		name, args = "gh", []string{"pr", "view", record.Number, "--repo", slug, "--json", "number,state,title,isDraft,statusCheckRollup,reviewDecision,mergeStateStatus,mergeable,reviewRequests,latestReviews,mergedAt,closedAt,baseRefName,mergeCommit,headRefOid"}
	case "bitbucket.org":
		if until := statusForgeCoolingUntil(forge); time.Now().Before(until) {
			return nil, &statusAdmissionError{cause: errForgeCooling, retry: until}
		}
		raw, err := bitbucketReadPR(ctx, slug, record.Number)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if err != nil {
			return nil, fmt.Errorf("Bitbucket PR observation: %w", err)
		}
		return raw, nil
	default:
		return nil, fmt.Errorf("unsupported forge")
	}
	if until := statusForgeCoolingUntil(forge); time.Now().Before(until) {
		return nil, &statusAdmissionError{cause: errForgeCooling, retry: until}
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = 200 * time.Millisecond
	raw, err := forgeOutput(cmd)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
			return nil, fmt.Errorf("%s PR observation: %w: %s", name, err, strings.TrimSpace(string(exitErr.Stderr)))
		}
		return nil, fmt.Errorf("%s PR observation: %w", name, err)
	}
	return raw, nil
}

func statusFetchPRRawWithRequestContext(ctx context.Context, record PRRecord, slug, forge string, onRequest func()) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if statusForgeCooling(forge) {
		return nil, errForgeCooling
	}
	if !statusTakeForgeTokenForContext(statusOrdinaryRead(ctx, "pr", forge, slug, record.Number), forge, "") {
		return nil, errForgeBudget
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var raw []byte
	var err error
	switch forge {
	case "github.com":
		if onRequest != nil {
			onRequest()
		}
		raw, err = statusJSONContext(ctx, "gh", "pr", "view", record.Number, "--repo", slug, "--json", "number,state,title,isDraft,statusCheckRollup,reviewDecision,mergeStateStatus,mergeable,reviewRequests,latestReviews,mergedAt,closedAt,baseRefName,mergeCommit,headRefOid")
	case "bitbucket.org":
		if onRequest != nil {
			onRequest()
		}
		raw, err = bitbucketReadPR(ctx, slug, record.Number)
	default:
		return nil, fmt.Errorf("unsupported forge")
	}
	if err != nil && statusRateLimited(err) {
		_, _ = statusForgeCoolContext(ctx, forge, statusRetryAfter(err))
	}
	return raw, err
}

func statusConfirmFullPR(record PRRecord, slug, forge string, incremental statusPRDetail, source string) statusPRDetail {
	return statusConfirmFullPRContext(context.Background(), record, slug, forge, incremental, source)
}

func statusConfirmFullPRContext(ctx context.Context, record PRRecord, slug, forge string, incremental statusPRDetail, source string) statusPRDetail {
	ctx = statusAuthOperation(ctx)
	unlock, lockErr := statusLockCacheContext(ctx, statusForgeEntryPathContext(ctx, "pr", forge, slug, record.Number))
	if lockErr != nil {
		return incremental
	}
	defer unlock()
	return statusConfirmFullPRLockedContext(ctx, record, slug, forge, incremental, source)
}

func statusConfirmFullPRLockedContext(ctx context.Context, record PRRecord, slug, forge string, incremental statusPRDetail, source string) statusPRDetail {
	ctx = statusAuthOperation(ctx)
	if !statusFullRefreshDueContext(ctx, forge, slug, record.Number) {
		if cached, ok := statusReadCachedDetailContext(ctx, record, slug, forge); ok {
			return cached
		}
		return incremental
	}
	started := time.Now()
	raw, err := statusFetchPRRawWithRequestContext(ctx, record, slug, forge, nil)
	if err != nil || len(raw) == 0 {
		return incremental
	}
	full, parseErr := statusParseForgeDetail(forge, raw, record)
	if parseErr != nil {
		return incremental
	}
	if forge == "bitbucket.org" {
		full = statusBBCompleteMergeTimeContext(ctx, full, slug)
	}
	if !statusResourceWriteSince(ctx, "pr", forge, slug, record.Number, raw, started) {
		if winner, ok := statusResourceObservedDetail(ctx, record, slug, forge, 365*24*time.Hour); ok {
			if winner.State != full.State {
				winner.Freshness = "unavailable"
			}
			return winner
		}
		return incremental
	}
	statusMarkFullRefreshContext(ctx, forge, slug, record.Number)
	statusLogIncrementalMiss(forge, slug, "#"+record.Number, source, statusPRDetailDiff(incremental, full))
	return full
}

func statusConfirmFullBuildContext(ctx context.Context, slug, branch string, incremental *StatusBuild, source, user, pass string) *StatusBuild {
	ctx = statusAuthOperation(ctx)
	unlock, lockErr := statusLockCacheContext(ctx, statusForgeEntryPathContext(ctx, "build", "bitbucket.org", slug, branch))
	if lockErr != nil {
		return incremental
	}
	defer unlock()
	corrected, found := statusAuditedCorrectionContext(ctx, slug, branch, incremental)
	if !statusFullRefreshDueContext(ctx, "bitbucket.org", slug, "build:"+branch) {
		if found {
			return corrected
		}
		return incremental
	}
	fallback := incremental
	if found {
		fallback = corrected
	}
	parts := strings.SplitN(slug, "/", 2)
	if len(parts) != 2 {
		return fallback
	}
	if statusForgeCooling("bitbucket.org") || !statusTakeForgeTokenForContext(ctx, "bitbucket.org", "") {
		return fallback
	}
	started := time.Now()
	raw, err := statusBBPipelinesContext(ctx, parts[0], parts[1], branch, user, pass)
	if err != nil {
		if statusRateLimited(err) {
			_, _ = statusForgeCoolContext(ctx, "bitbucket.org", statusRetryAfter(err))
		}
		return fallback
	}
	full, parseErr := statusParseBBBuild(raw, slug, branch)
	if parseErr != nil {
		return fallback
	}
	statusResourceWriteSince(ctx, "build", "bitbucket.org", slug, branch, raw, started)
	if ctx.Err() != nil {
		return full
	}
	statusResourceWriteEntry(ctx, "full", "bitbucket.org", slug, "build:"+branch, []byte(statusBuildFingerprint(incremental)))
	statusLogIncrementalMiss("bitbucket.org", slug, branch, source, statusBuildDiff(incremental, full))
	return full
}

func statusBuildFingerprint(build *StatusBuild) string {
	number, checks, sha := statusBuildValue(build)
	return number + "|" + checks + "|" + sha
}

func statusPreferAuditedBuildContext(ctx context.Context, slug, branch string, pageBuild *StatusBuild) (*StatusBuild, bool) {
	ctx = statusAuthOperation(ctx)
	if pageBuild == nil || statusFullRefreshDueContext(ctx, "bitbucket.org", slug, "build:"+branch) {
		return nil, false
	}
	return statusAuditedCorrectionContext(ctx, slug, branch, pageBuild)
}

func statusAuditedCorrectionContext(ctx context.Context, slug, branch string, pageBuild *StatusBuild) (*StatusBuild, bool) {
	if pageBuild == nil {
		return nil, false
	}
	raw, ok := statusReadForgeEntryAnyContext(ctx, "full", "bitbucket.org", slug, "build:"+branch)
	if !ok || string(raw) != statusBuildFingerprint(pageBuild) {
		return nil, false
	}
	saved, ok := statusReadForgeEntryAnyContext(ctx, "build", "bitbucket.org", slug, branch)
	if !ok {
		return nil, false
	}
	savedBuild, err := statusParseBBBuild(saved, slug, branch)
	if err != nil {
		return nil, false
	}
	return savedBuild, true
}
