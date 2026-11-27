package wtc

import (
	"strings"
	"time"
)

// StatusPRPresentationFreshness expires an active state observation as time
// passes between collections, after the open-list age of the forge that url
// (the PR's web address) belongs to. It does not refresh evidence, change
// cache data, or date checks, reviews or mergeability. Confirmed terminal
// facts are handled separately by the caller.
func StatusPRPresentationFreshness(freshness, observed, url string, now time.Time) string {
	if freshness != "live" && freshness != "cached" {
		return freshness
	}
	at, err := time.Parse(time.RFC3339Nano, observed)
	if err != nil || at.After(now) {
		return "unavailable"
	}
	if now.Sub(at) >= statusOpenListAge(statusPRURLForge(url)) {
		return "stale"
	}
	return freshness
}

func statusPRURLForge(url string) string {
	for _, forge := range []string{"github.com", "bitbucket.org"} {
		if strings.HasPrefix(url, "https://"+forge+"/") {
			return forge
		}
	}
	return ""
}
