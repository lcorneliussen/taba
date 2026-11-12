package wtc

import "time"

func statusObservationUnavailable(freshness string) bool {
	return freshness == "stale" || freshness == "deferred" || freshness == "unavailable"
}

// Freshness describes PR state, not separate checks or mergeability evidence.
// Never borrow a newer file's timestamp for different returned facts.
func statusObserveDetail(detail statusPRDetail, slug, forge string) statusPRDetail {
	if detail.ObservedAt != "" || detail.State == "UNKNOWN" || detail.State == "" {
		return detail
	}
	raw, observed, ok := statusReadForgeEntryObserved("pr", forge, slug, detail.Number, 365*24*time.Hour)
	if !ok {
		return detail
	}
	saved, err := statusParseForgeDetail(forge, raw, PRRecord{Number: detail.Number})
	if err != nil || saved.State != detail.State || saved.HeadCommit != detail.HeadCommit {
		return detail
	}
	return statusObserveAt(detail, observed, "detail-cache")
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
