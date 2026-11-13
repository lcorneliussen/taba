package wtc

import "time"

func statusObservationUnavailable(freshness string) bool {
	return freshness == "stale" || freshness == "deferred" || freshness == "unavailable"
}

// Freshness describes PR state, not separate checks or mergeability evidence.
// Only matching state and head may supply the state-observation timestamp.
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

// Observe cached payloads without fetching ancillary evidence or changing their
// timestamps. Age bounds ordinary detail; retained MERGED state is bounded by
// statusMergedCacheAge.
func statusReadObservedDetail(record PRRecord, slug, forge string, age time.Duration) (statusPRDetail, bool) {
	// A fresh active detail from a released writer cannot supersede retained
	// merge state. Its original age still controls freshness and action gates.
	if detail, ok := statusRetainedMerge(record, slug, forge); ok {
		return detail, true
	}
	return statusReadObservedDetailKind("pr", record, slug, forge, age)
}

func statusReadObservedDetailKind(kind string, record PRRecord, slug, forge string, age time.Duration) (statusPRDetail, bool) {
	raw, observed, ok := statusReadForgeEntryObserved(kind, forge, slug, record.Number, age)
	if !ok {
		return statusPRDetail{}, false
	}
	detail, err := statusParseForgeDetail(forge, raw, record)
	if err != nil {
		return statusPRDetail{}, false
	}
	return statusObserveAt(detail, observed, "detail-cache"), true
}

func statusRetainedMerge(record PRRecord, slug, forge string) (statusPRDetail, bool) {
	detail, known := statusReadObservedDetailKind("merge", record, slug, forge, statusMergedCacheAge())
	known = known && detail.State == "MERGED"
	raw, observed, ok := statusReadForgeEntryObserved("pr", forge, slug, record.Number, statusMergedCacheAge())
	if ok {
		current, err := statusParseForgeDetail(forge, raw, record)
		retainedAt, _ := time.Parse(time.RFC3339Nano, detail.ObservedAt)
		if err == nil && current.State == "MERGED" && (!known || observed.After(retainedAt)) {
			// Accept legacy authority only after preserving the same bytes and age
			// independently of released writers' ordinary cache publications.
			if statusRetainMergeObservation(forge, slug, record.Number, raw, observed) {
				winner, found := statusReadObservedDetailKind("merge", record, slug, forge, statusMergedCacheAge())
				if found && winner.State == "MERGED" {
					return winner, true
				}
			}
		}
	}
	return detail, known
}
