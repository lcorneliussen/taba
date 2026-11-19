package wtc

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Older records have no admission reason. Observation age alone can establish
// expiration, never a budget or provider rate-limit explanation.
func statusDetailObservation(detail statusPRDetail) StatusObservation {
	out := StatusObservation{Freshness: detail.Freshness, ObservedAt: detail.ObservedAt, Source: detail.StateSource}
	switch detail.Freshness {
	case "stale":
		out.Reason = "observation-expired"
	case "deferred", "unavailable":
		out.Reason = "unknown"
	}
	if detail.Reason != "" && statusObservationUnavailable(detail.Freshness) {
		out.Reason, out.RetryAt = detail.Reason, detail.RetryAt
	}
	if detail.ObservedAt == "" && (detail.State == "UNKNOWN" || detail.State == "") {
		out.Reason = "never-observed"
	}
	return out
}

func statusObservationUnavailable(freshness string) bool {
	return freshness == "stale" || freshness == "deferred" || freshness == "unavailable"
}

// Freshness describes PR state, not separate checks or mergeability evidence.
// Only matching state and head may supply the state-observation timestamp.
func statusObserveDetail(detail statusPRDetail, slug, forge string) statusPRDetail {
	return statusObserveDetailContext(context.Background(), detail, slug, forge)
}

func statusObserveDetailContext(ctx context.Context, detail statusPRDetail, slug, forge string) statusPRDetail {
	ctx = statusAuthOperation(ctx)
	if detail.ObservedAt != "" || detail.State == "UNKNOWN" || detail.State == "" {
		return detail
	}
	raw, observed, ok := statusReadForgeEntryObservedContext(ctx, "pr", forge, slug, detail.Number, 365*24*time.Hour)
	if !ok {
		return detail
	}
	saved, err := statusParseForgeDetail(forge, raw, PRRecord{Number: detail.Number})
	if err != nil || saved.State != detail.State || saved.HeadCommit != detail.HeadCommit {
		return detail
	}
	return statusNewerListedContext(ctx, statusObserveAt(detail, observed, "detail-cache"), slug, forge)
}

// A list that confirms an unchanged open state does not rewrite the detail
// entry (its checks and reviews keep their own age). The confirmation itself
// is kept separately, so a later fallback reports the newest observation of
// that state rather than the older detail read.
func statusListedValue(detail statusPRDetail) []byte {
	head := detail.HeadCommit
	if !statusListedHead(head) {
		head = ""
	}
	return []byte(detail.State + "\x00" + head)
}

// The stored value is bounded: a state name and an optional commit id.
const statusListedMax = len("DRAFT") + 1 + 64

func statusListedHead(head string) bool {
	if len(head) > 64 {
		return false
	}
	for _, ch := range head {
		if !(ch >= '0' && ch <= '9' || ch >= 'a' && ch <= 'f') {
			return false
		}
	}
	return true
}

// statusRecordListedContext keeps the newest list confirmation of an open
// state. Callers hold the PR entry lock, which orders competing writers.
func statusRecordListedContext(ctx context.Context, forge, slug string, detail statusPRDetail, observed time.Time) {
	if (detail.State != "OPEN" && detail.State != "DRAFT") || observed.IsZero() || observed.After(time.Now()) || ctx.Err() != nil {
		return
	}
	if _, at, ok := statusReadForgeEntryObservedContext(ctx, "listed", forge, slug, detail.Number, 365*24*time.Hour); ok && !observed.After(at) {
		return
	}
	statusWriteForgeEntryObservedContext(ctx, "listed", forge, slug, detail.Number, statusListedValue(detail), observed)
}

// statusNewerListedContext dates an open state by a newer list confirmation
// of the same state (and head, when both are known). Only the state
// observation moves: freshness, and with it check/review age and action
// eligibility, stays as the detail read left it.
func statusNewerListedContext(ctx context.Context, detail statusPRDetail, slug, forge string) statusPRDetail {
	if (detail.State != "OPEN" && detail.State != "DRAFT") || detail.ObservedAt == "" {
		return detail
	}
	current, err := time.Parse(time.RFC3339Nano, detail.ObservedAt)
	if err != nil {
		return detail
	}
	raw, at, ok := statusReadForgeEntryObservedContext(ctx, "listed", forge, slug, detail.Number, 365*24*time.Hour)
	if !ok || len(raw) > statusListedMax || !at.After(current) || at.After(time.Now()) {
		return detail
	}
	state, head, _ := strings.Cut(string(raw), "\x00")
	if state != detail.State || !statusListedHead(head) || head != "" && detail.HeadCommit != "" && head != detail.HeadCommit {
		return detail
	}
	freshness := detail.Freshness
	detail = statusObserveAt(detail, at, "open-list")
	detail.Freshness = freshness
	return detail
}

func statusObserveAt(detail statusPRDetail, observed time.Time, source string) statusPRDetail {
	detail.ObservedAt = observed.UTC().Format(time.RFC3339Nano)
	detail.StateSource = source
	if detail.Freshness == "" {
		detail.Freshness = "cached"
		if time.Since(observed) >= statusForgeCacheAge() {
			detail.Freshness = "stale"
		}
	}
	return detail
}

func statusFreshnessLabel(freshness, observed string) string {
	if !statusObservationUnavailable(freshness) {
		return ""
	}
	label := freshness + " forge observation"
	if observed != "" {
		label += "; observed " + observed
	}
	return "(" + label + ")"
}

// Observe cached payloads without fetching ancillary evidence or rewriting
// them. A newer list confirmation of the same open state can supply the state
// observation time (never freshness). Age bounds ordinary detail; retained
// MERGED state is bounded by statusMergedCacheAge.
func statusReadObservedDetail(record PRRecord, slug, forge string, age time.Duration) (statusPRDetail, bool) {
	// Ordinary detail cannot supersede retained merge state in the same
	// credential context. Original age still controls freshness and action gates.
	if detail, ok := statusRetainedMerge(record, slug, forge); ok {
		return detail, true
	}
	return statusReadObservedDetailKind("pr", record, slug, forge, age)
}

func statusReadObservedDetailContext(ctx context.Context, record PRRecord, slug, forge string, age time.Duration) (statusPRDetail, bool, error) {
	ctx = statusAuthOperation(ctx)
	// Ordinary detail cannot supersede retained merge state in the same
	// credential context. Original age still controls freshness and action gates.
	if detail, ok, err := statusRetainedMergeContext(ctx, record, slug, forge); ok || err != nil {
		return detail, ok, err
	}
	detail, ok := statusReadObservedDetailKindContext(ctx, "pr", record, slug, forge, age)
	return detail, ok, ctx.Err()
}

func statusReadObservedDetailKind(kind string, record PRRecord, slug, forge string, age time.Duration) (statusPRDetail, bool) {
	return statusReadObservedDetailKindContext(context.Background(), kind, record, slug, forge, age)
}

func statusReadObservedDetailKindContext(ctx context.Context, kind string, record PRRecord, slug, forge string, age time.Duration) (statusPRDetail, bool) {
	if !statusAuthSharedKind(kind) {
		ctx = statusAuthOperation(ctx)
	}
	raw, observed, ok := statusReadForgeEntryObservedContext(ctx, kind, forge, slug, record.Number, age)
	if !ok {
		return statusPRDetail{}, false
	}
	detail, err := statusParseForgeDetail(forge, raw, record)
	if err != nil {
		return statusPRDetail{}, false
	}
	detail = statusObserveAt(detail, observed, "detail-cache")
	if kind == "pr" {
		detail = statusNewerListedContext(ctx, detail, slug, forge)
	}
	return detail, true
}

func statusRetainedMerge(record PRRecord, slug, forge string) (statusPRDetail, bool) {
	detail, known, _ := statusRetainedMergeContext(context.Background(), record, slug, forge)
	return detail, known
}

func statusRetainedMergeContext(ctx context.Context, record PRRecord, slug, forge string) (statusPRDetail, bool, error) {
	ctx = statusAuthOperation(ctx)
	detail, known := statusReadObservedDetailKindContext(ctx, "merge", record, slug, forge, statusMergedCacheAge())
	known = known && detail.State == "MERGED"
	if err := ctx.Err(); err != nil {
		return detail, known, err
	}
	raw, observed, ok := statusReadForgeEntryObservedContext(ctx, "pr", forge, slug, record.Number, statusMergedCacheAge())
	if ok {
		current, err := statusParseForgeDetail(forge, raw, record)
		retainedAt, _ := time.Parse(time.RFC3339Nano, detail.ObservedAt)
		if err == nil && current.State == "MERGED" && (!known || observed.After(retainedAt)) {
			// Copy only scoped evidence from the pinned credential context,
			// preserving bytes and observation time independently of ordinary detail.
			retained, retainErr := statusRetainMergeObservationContext(ctx, forge, slug, record.Number, raw, observed)
			if retained {
				winner, found := statusReadObservedDetailKindContext(ctx, "merge", record, slug, forge, statusMergedCacheAge())
				if found && winner.State == "MERGED" {
					return winner, true, retainErr
				}
			}
			if retainErr != nil {
				return detail, known, retainErr
			}
		}
	}
	return detail, known, ctx.Err()
}

// StatusPRFreshnessMark keeps observation age out of the title column. A
// missing/invalid time never becomes a current observation or a rate-limit claim.
func StatusPRFreshnessMark(freshness, observed string, now time.Time) string {
	if !statusObservationUnavailable(freshness) && freshness != "refreshing" {
		return ""
	}
	if observed == "" {
		if freshness == "refreshing" {
			return "reading"
		}
		return "never"
	}
	age := "?"
	if at, err := time.Parse(time.RFC3339Nano, observed); err == nil && !at.After(now) {
		seconds := int(now.Sub(at).Seconds())
		switch {
		case seconds < 60:
			age = fmt.Sprintf("%ds", seconds)
		case seconds < 3600:
			age = fmt.Sprintf("%dm", seconds/60)
		case seconds < 86400:
			age = fmt.Sprintf("%dh", seconds/3600)
		default:
			age = fmt.Sprintf("%dd", seconds/86400)
		}
	}
	prefix := map[string]string{"stale": "s", "deferred": "d", "unavailable": "u", "refreshing": "r"}[freshness]
	return prefix + ":" + age
}

// StatusPRObservationDetail is for the existing help/log surface, not a PR row.
func StatusPRObservationDetail(row StatusPRRow) string {
	observation := "not yet observed"
	if row.State != "" && row.State != "UNKNOWN" {
		observation = "last " + strings.ToLower(row.State)
	}
	if row.ObservedAt != "" {
		observation += "; observed " + row.ObservedAt
	}
	reason := row.ObservationReason
	if reason == "" {
		reason = statusDetailObservation(statusPRDetail{State: row.State, Freshness: row.Freshness, ObservedAt: row.ObservedAt}).Reason
	}
	if reason == "" {
		reason = "unknown"
	}
	reasonLabel := "; reason "
	if row.State == "MERGED" && (row.Merge != nil && *row.Merge == "MERGED" || !statusObservationUnavailable(row.Freshness)) && row.ObservationReason != "" && row.ObservationReason != "provider-confirmed" {
		reasonLabel = "; recheck reason "
	}
	text := row.Repo + "#" + row.Number + ": " + row.Freshness + "; " + observation + reasonLabel + reason
	if row.RetryAt != "" {
		text += "; retry after " + row.RetryAt
	}
	return text
}
