package wtc

import (
	"encoding/json"
	"strings"
	"testing"
)

func activityThreadPage(head string, next bool, threads ...map[string]any) map[string]any {
	return map[string]any{"data": map[string]any{"repository": map[string]any{"pullRequest": map[string]any{"headRefOid": head, "reviewThreads": map[string]any{"nodes": threads, "pageInfo": map[string]any{"hasNextPage": next}}}}}}
}

func activityRaw(value any) []byte { raw, _ := json.Marshal(value); return raw }

func TestHostedReviewActivityDistinguishesWaitingReportsAndUnavailable(t *testing.T) {
	head := strings.Repeat("a", 40)
	for _, tc := range []struct {
		name, meta, reviews, want string
		wait                      bool
	}{
		{"absent", `"reviewRequests":[],"statusCheckRollup":[]`, `[[]]`, "absent", false},
		{"requested", `"reviewRequests":[{"login":"copilot-pull-request-reviewer"}],"statusCheckRollup":[]`, `[[]]`, "waiting", true},
		{"running", `"reviewRequests":[],"statusCheckRollup":[{"name":"Copilot code review","status":"IN_PROGRESS"}]`, `[[]]`, "waiting", true},
		{"reported", `"reviewRequests":[],"statusCheckRollup":[]`, `[[{"user":{"login":"copilot-pull-request-reviewer"},"state":"COMMENTED","commit_id":"` + head + `","body":"One finding"}]]`, "reported", false},
		{"stale", `"reviewRequests":[],"statusCheckRollup":[]`, `[[{"user":{"login":"copilot-pull-request-reviewer"},"state":"COMMENTED","commit_id":"` + strings.Repeat("b", 40) + `"}]]`, "stale", false},
		{"quota", `"reviewRequests":[{"login":"copilot-pull-request-reviewer"}],"statusCheckRollup":[]`, `[[{"user":{"login":"copilot-pull-request-reviewer"},"state":"COMMENTED","commit_id":"` + head + `","body":"Review unable to review: budget exceeded"}]]`, "unavailable", false},
		{"new-run-after-error", `"reviewRequests":[],"statusCheckRollup":[{"name":"Copilot code review","status":"IN_PROGRESS"}]`, `[[{"user":{"login":"copilot-pull-request-reviewer"},"state":"COMMENTED","commit_id":"` + head + `","body":"unable to review"}]]`, "waiting", true},
		{"failed-check-requested", `"reviewRequests":[{"login":"copilot-pull-request-reviewer"}],"statusCheckRollup":[{"name":"Copilot code review","status":"COMPLETED","conclusion":"FAILURE"}]`, `[[]]`, "unavailable", false},
		{"failed-check-with-retry", `"reviewRequests":[{"login":"copilot-pull-request-reviewer"}],"statusCheckRollup":[{"name":"Copilot code review","status":"IN_PROGRESS"},{"name":"Copilot code review","status":"COMPLETED","conclusion":"FAILURE"}]`, `[[]]`, "waiting", true},
		{"failed-context", `"reviewRequests":[{"login":"copilot-pull-request-reviewer"}],"statusCheckRollup":[{"name":"Copilot code review","state":"ERROR"}]`, `[[]]`, "unavailable", false},
		{"expected-context", `"reviewRequests":[],"statusCheckRollup":[{"name":"Copilot code review","state":"EXPECTED"}]`, `[[]]`, "waiting", true},
		{"startup-failure", `"reviewRequests":[{"login":"copilot-pull-request-reviewer"}],"statusCheckRollup":[{"name":"Copilot code review","status":"COMPLETED","conclusion":"STARTUP_FAILURE"}]`, `[[]]`, "unavailable", false},
		{"stale-check", `"reviewRequests":[{"login":"copilot-pull-request-reviewer"}],"statusCheckRollup":[{"name":"Copilot code review","status":"COMPLETED","conclusion":"STALE"}]`, `[[]]`, "unavailable", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseReviewActivity([]byte(`{"headRefOid":"`+head+`",`+tc.meta+`}`), []byte(tc.reviews), activityRaw([]any{activityThreadPage(head, false)}), []byte(`[{"total_count":0,"check_runs":[]}]`))
			if err != nil || got.Reports[0].State != tc.want || (len(got.Waiting) > 0) != tc.wait {
				t.Fatalf("%+v %v", got, err)
			}
		})
	}
}

func TestHostedReviewActivityKeepsAllOpenThreadsAcrossPages(t *testing.T) {
	head := strings.Repeat("a", 40)
	thread := func(provider string, resolved, outdated bool) map[string]any {
		return map[string]any{"id": "thread", "isResolved": resolved, "isOutdated": outdated, "path": "feature.go", "line": nil, "comments": map[string]any{"nodes": []any{map[string]any{"author": map[string]any{"login": provider}, "body": "Check old workaround", "url": "https://github.com/example/app/pull/7#discussion_r1", "originalCommit": map[string]any{"oid": strings.Repeat("b", 40)}}}}}
	}
	meta := activityRaw(map[string]any{"headRefOid": head})
	reviews := []byte(`[[{"user":{"login":"cursor[bot]"},"state":"COMMENTED","commit_id":"` + head + `","body":"reported"}]]`)
	pages := []any{activityThreadPage(head, true, thread("cursor[bot]", false, true)), activityThreadPage(head, false, thread("copilot-pull-request-reviewer", false, false), thread("cursor[bot]", true, false), thread("human", false, false))}
	got, err := parseReviewActivity(meta, reviews, activityRaw(pages), []byte(`[{"total_count":0,"check_runs":[]}]`))
	if err != nil || len(got.Findings) != 2 || !got.Findings[0].Outdated || got.Findings[0].Head == head || got.Reports[1].State != "reported" {
		t.Fatalf("%+v %v", got, err)
	}
	for _, bad := range []any{
		[]any{activityThreadPage(head, true)},
		[]any{activityThreadPage(strings.Repeat("b", 40), false)},
		[]any{map[string]any{"errors": []any{map[string]any{"message": "denied"}}}},
		[]any{},
	} {
		if _, err := parseReviewActivity(meta, reviews, activityRaw(bad), []byte(`[{"total_count":0,"check_runs":[]}]`)); err == nil {
			t.Fatalf("incomplete or stale activity accepted: %+v", bad)
		}
	}
}
