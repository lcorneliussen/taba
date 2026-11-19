package wtc

import "time"

// StatusPRPresentationFreshness expires an active state observation as time
// passes between collections. It does not refresh evidence, change cache data,
// or date checks, reviews or mergeability. Confirmed terminal facts are handled
// separately by the caller.
func StatusPRPresentationFreshness(freshness, observed string, now time.Time) string {
	if freshness != "live" && freshness != "cached" {
		return freshness
	}
	at, err := time.Parse(time.RFC3339Nano, observed)
	if err != nil || at.After(now) {
		return "unavailable"
	}
	if now.Sub(at) >= statusForgeCacheAge() {
		return "stale"
	}
	return freshness
}
