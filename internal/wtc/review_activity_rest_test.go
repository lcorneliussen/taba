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

func activityRESTRun(name, status, conclusion, head string) map[string]any {
	return map[string]any{"name": name, "status": status, "conclusion": conclusion, "head_sha": head}
}

func activityRESTPages(runs ...map[string]any) string {
	return string(activityRaw([]any{map[string]any{"total_count": len(runs), "check_runs": runs}}))
}

func TestReviewActivityRESTStatesAndUnion(t *testing.T) {
	for _, tc := range []struct {
		name, provider, status, conclusion, rollup, want string
		index                                            int
	}{
		{"copilot-login", "copilot-pull-request-reviewer", "in_progress", "", `[]`, "waiting", 0},
		{"copilot-display-case", "CoPiLoT code review", "QUEUED", "", `[]`, "waiting", 0},
		{"copilot-login-case", "COPILOT-PULL-REQUEST-REVIEWER", "requested", "", `[]`, "waiting", 0},
		{"cursor-display-case", "CuRsOr Bugbot", "pending", "", `[]`, "waiting", 1},
		{"bugbot-login", "bugbot", "waiting", "", `[]`, "waiting", 1},
		{"completed-no-report", "cursor", "completed", "success", `[]`, "absent", 1},
		{"completed-old-report", "copilot-pull-request-reviewer", "completed", "success", `[]`, "stale", 0},
		{"failed", "copilot-pull-request-reviewer", "completed", "failure", `[]`, "unavailable", 0},
		{"timed-out", "copilot-pull-request-reviewer", "completed", "timed_out", `[]`, "unavailable", 0},
		{"cancelled", "copilot-pull-request-reviewer", "completed", "cancelled", `[]`, "unavailable", 0},
		{"overlap", "copilot-pull-request-reviewer", "in_progress", "", `[{"name":"Copilot code review","status":"IN_PROGRESS"}]`, "waiting", 0},
		{"rollup-active-rest-complete", "copilot-pull-request-reviewer", "completed", "success", `[{"name":"Copilot code review","status":"IN_PROGRESS"}]`, "waiting", 0},
		{"retry-after-rollup-failure", "copilot-pull-request-reviewer", "queued", "", `[{"name":"Copilot code review","status":"COMPLETED","conclusion":"FAILURE"}]`, "waiting", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := activityRESTFixture(t, activityRESTPages(activityRESTRun(tc.provider, tc.status, tc.conclusion, activityRESTHead)), tc.rollup)
			got, err := c.GetReviewActivity("app", "7")
			if err != nil {
				t.Fatal(err)
			}
			if got.Reports[tc.index].State != tc.want || (len(got.Waiting) > 0) != (tc.want == "waiting") || len(got.Waiting) > 1 {
				t.Fatalf("want %s; got %s", tc.want, activityRaw(got))
			}
		})
	}
}

func TestReviewActivityRESTPaginationAndDuplicates(t *testing.T) {
	first := []map[string]any{}
	for i := 0; i < 100; i++ {
		first = append(first, activityRESTRun("build", "completed", "success", activityRESTHead))
	}
	active := activityRESTRun("copilot-pull-request-reviewer", "in_progress", "", activityRESTHead)
	pages := []any{
		map[string]any{"total_count": 102, "check_runs": first},
		map[string]any{"total_count": 102, "check_runs": []any{active, active}},
	}
	c, _ := activityRESTFixture(t, string(activityRaw(pages)), `[]`)
	got, err := c.GetReviewActivity("app", "7")
	if err != nil || len(got.Waiting) != 1 || got.Waiting[0] != "Copilot" {
		t.Fatalf("%s %v", activityRaw(got), err)
	}
}

func TestReviewActivityRESTActiveThenCompleted(t *testing.T) {
	c, dir := activityRESTFixture(t, activityRESTPages(activityRESTRun("copilot-pull-request-reviewer", "in_progress", "", activityRESTHead)), `[]`)
	got, err := c.GetReviewActivity("app", "7")
	if err != nil || len(got.Waiting) != 1 {
		t.Fatalf("%s %v", activityRaw(got), err)
	}
	fixtureFile(t, filepath.Join(dir, "rest.json"), activityRESTPages(activityRESTRun("copilot-pull-request-reviewer", "completed", "success", activityRESTHead)), 0600)
	got, err = c.GetReviewActivity("app", "7")
	if err != nil || len(got.Waiting) != 0 || got.Reports[0].State != "stale" {
		t.Fatalf("%s %v", activityRaw(got), err)
	}
}

func TestReviewActivityRESTRejectsUnavailablePartialAndWrongHead(t *testing.T) {
	for _, rest := range []string{
		`null`, `[]`, `{}`, `[null]`, `[{}]`, `[{"total_count":0}]`, `[{"total_count":0,"check_runs":null}]`,
		`[{"check_runs":[]}]`, `[{"total_count":-1,"check_runs":[]}]`, `[{"total_count":1,"check_runs":[]}]`,
		`[{"total_count":0,"check_runs":[]},{"total_count":1,"check_runs":[]}]`,
		activityRESTPages(activityRESTRun("copilot-pull-request-reviewer", "in_progress", "", strings.Repeat("b", 40))),
		activityRESTPages(activityRESTRun("copilot-pull-request-reviewer", "", "", activityRESTHead)),
		activityRESTPages(activityRESTRun("", "in_progress", "", activityRESTHead)),
		`[{"total_count":1,"check_runs":[`,
	} {
		t.Run(rest, func(t *testing.T) {
			c, _ := activityRESTFixture(t, rest, `[]`)
			if _, err := c.GetReviewActivity("app", "7"); err == nil {
				t.Fatalf("accepted incomplete evidence: %s", rest)
			}
		})
	}
	t.Run("nonzero-after-output", func(t *testing.T) {
		c, _ := activityRESTFixture(t, `[{"total_count":0,"check_runs":[]}]`, `[]`)
		t.Setenv("ACTIVITY_REST_EXIT", "1")
		if _, err := c.GetReviewActivity("app", "7"); err == nil {
			t.Fatal("accepted failed REST command")
		}
	})
}

func TestReviewActivityRESTHeadChangesBetweenReads(t *testing.T) {
	c, dir := activityRESTFixture(t, activityRESTPages(activityRESTRun("copilot-pull-request-reviewer", "completed", "success", activityRESTHead)), `[]`)
	for _, pages := range [][]any{
		{activityThreadPage(strings.Repeat("b", 40), false)},
		{activityThreadPage(activityRESTHead, true), activityThreadPage(strings.Repeat("b", 40), false)},
	} {
		fixtureFile(t, filepath.Join(dir, "threads.json"), string(activityRaw(pages)), 0600)
		if _, err := c.GetReviewActivity("app", "7"); err == nil || !strings.Contains(err.Error(), "head changed") {
			t.Fatalf("accepted changed PR head: %v", err)
		}
	}
	fixtureFile(t, filepath.Join(dir, "meta.json"), `{"headRefOid":"not-a-commit"}`, 0600)
	if _, err := c.GetReviewActivity("app", "7"); err == nil {
		t.Fatal("accepted invalid PR head")
	}
}

func TestReviewActivityRESTDefersHostedPreflight(t *testing.T) {
	c, dir := activityRESTFixture(t, activityRESTPages(activityRESTRun("copilot-pull-request-reviewer", "in_progress", "", activityRESTHead)), `[]`)
	bundle := filepath.Join(dir, "bundle")
	fixtureFile(t, filepath.Join(bundle, "manifest.json"), string(activityRaw(ReviewManifest{Repo: "app", RepoDir: filepath.Join(dir, "app"), Slug: "example/app", Forge: "github", PR: "7", HeadSHA: activityRESTHead, Round: 1})), 0600)
	if err := c.PrepareHostedReviewBundle(bundle); err == nil || !strings.Contains(err.Error(), "waiting for Copilot") {
		t.Fatalf("did not defer active REST-only review: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(bundle, "prior", "hosted-reviews.md"))
	if err != nil || !strings.Contains(string(raw), "Copilot: waiting") {
		t.Fatalf("missing waiting evidence: %s %v", raw, err)
	}
	t.Setenv("ACTIVITY_REST_EXIT", "1")
	if err := c.PrepareHostedReviewBundle(bundle); err == nil {
		t.Fatal("unavailable REST evidence allowed preflight")
	}
}
