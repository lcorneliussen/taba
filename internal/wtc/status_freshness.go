package wtc

import (
	"os"
	"time"
)

func statusObservationUnavailable(freshness string) bool {
	return freshness == "stale" || freshness == "deferred"
}

// Observation age belongs to the forge fact, not to the enclosing snapshot.
func statusObserveDetail(detail statusPRDetail, slug, forge string) statusPRDetail {
	if detail.ObservedAt != "" {
		return detail
	}
	if info, err := os.Lstat(statusForgeCachePath(forge, slug, detail.Number)); err == nil && info.Mode().IsRegular() {
		detail.ObservedAt = info.ModTime().UTC().Format(time.RFC3339Nano)
		if detail.Freshness == "" {
			detail.Freshness = "cached"
			if time.Since(info.ModTime()) >= statusForgeCacheAge() {
				detail.Freshness = "stale"
			}
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
