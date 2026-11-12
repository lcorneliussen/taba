package wtc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const activityRESTHead = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

// The transport refuses unexpected commands; these tests never contact a forge.
func activityRESTFixture(t *testing.T, rest, rollup string) (*Context, string) {
	t.Helper()
	dir := t.TempDir()
	worktree, bin := filepath.Join(dir, "app"), filepath.Join(dir, "bin")
	for _, path := range []string{worktree, bin} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	fixtureFile(t, filepath.Join(worktree, ".git"), "synthetic existence sentinel\n", 0600)
	fixtureFile(t, filepath.Join(dir, "meta.json"), `{"headRefOid":"`+activityRESTHead+`","reviewRequests":[],"statusCheckRollup":`+rollup+`}`, 0600)
	fixtureFile(t, filepath.Join(dir, "rest.json"), rest, 0600)
	fixtureFile(t, filepath.Join(dir, "reviews.json"), `[[{"user":{"login":"copilot-pull-request-reviewer"},"state":"COMMENTED","commit_id":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","body":"previous head report"}]]`, 0600)
	fixtureFile(t, filepath.Join(dir, "threads.json"), string(activityRaw([]any{activityThreadPage(activityRESTHead, false)})), 0600)
	fixtureFile(t, filepath.Join(bin, "gh"), `#!/bin/sh
printf '%s\n' "$*" >> "$ACTIVITY_FIXTURE/calls"
case "$*" in
 'pr view 7 --repo example/app --json headRefOid,reviewRequests,statusCheckRollup') cat "$ACTIVITY_FIXTURE/meta.json" ;;
 'api repos/example/app/commits/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/check-runs?per_page=100 --paginate --slurp') cat "$ACTIVITY_FIXTURE/rest.json"; exit "${ACTIVITY_REST_EXIT:-0}" ;;
 'api repos/example/app/pulls/7/reviews?per_page=100 --paginate --slurp') cat "$ACTIVITY_FIXTURE/reviews.json" ;;
 'api graphql '*) cat "$ACTIVITY_FIXTURE/threads.json" ;;
 *) echo 'unexpected fake-gh invocation' >&2; exit 96 ;;
esac
`, 0700)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("ACTIVITY_FIXTURE", dir)
	t.Setenv("ACTIVITY_REST_EXIT", "0")
	return &Context{Collection: dir, Registry: Registry{Repos: []Repo{{Name: "app", Remote: "https://github.com/example/app.git"}}}}, dir
}

func TestReviewActivityRESTOmittedFromRollup(t *testing.T) {
	for _, rollup := range []string{`[]`, `[{"name":"copilot-pull-request-reviewer","status":"IN_PROGRESS"}]`} {
		t.Run(rollup, func(t *testing.T) {
			c, dir := activityRESTFixture(t, `[{"total_count":1,"check_runs":[{"name":"copilot-pull-request-reviewer","head_sha":"`+activityRESTHead+`","status":"in_progress","conclusion":null,"app":{"slug":"github-actions"}}]}]`, rollup)
			got, err := c.GetReviewActivity("app", "7")
			if err != nil {
				t.Fatal(err)
			}
			if got.Head != activityRESTHead || len(got.Waiting) != 1 || got.Waiting[0] != "Copilot" || got.Reports[0].State != "waiting" {
				t.Fatalf("expected active exact-head REST check to mean waiting=[Copilot]; got %s", activityRaw(got))
			}
			calls, err := os.ReadFile(filepath.Join(dir, "calls"))
			if err != nil {
				t.Fatal(err)
			}
			if rollup == `[]` && !strings.Contains(string(calls), "/commits/"+activityRESTHead+"/check-runs?per_page=100 --paginate --slurp") {
				t.Fatalf("did not fetch paginated exact-head REST evidence: %s", calls)
			}
		})
	}
}
