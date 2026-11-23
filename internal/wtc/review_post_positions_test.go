package wtc

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func reviewPositionFixture(t *testing.T, findings string) (*Context, string, string, string) {
	t.Helper()
	collection := filepath.Join(t.TempDir(), "sample")
	repo, bundle := filepath.Join(collection, "app"), filepath.Join(collection, "bundle")
	for _, dir := range []string{filepath.Join(collection, "harness"), filepath.Join(repo, ".git"), filepath.Join(bundle, "findings")} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(collection, "harness", ".harness-repos.yml"), []byte("repos:\n  - name: app\n    remote: https://github.com/example/app.git\n"), 0644); err != nil {
		t.Fatal(err)
	}
	head := "1234567890abcdef1234567890abcdef12345678"
	if err := writeReviewManifest(bundle, ReviewManifest{Repo: "app", PR: "7", Public: true, Forge: "github", Slug: "example/app", HeadSHA: head, Round: 1, RepoDir: repo}); err != nil {
		t.Fatal(err)
	}
	summary := "**Local review: pass**\n\nFindings remain visible in this summary.\n\n`wtc-review v1 head=" + head + " verdict=pass blockers=0 round=1 lead=test:`\n"
	for name, data := range map[string]string{
		"summary.md":         summary,
		"diff.patch":         "diff --git a/feature.go b/feature.go\n--- a/feature.go\n+++ b/feature.go\n@@ -3,2 +3,3 @@\n context\n-old\n+new\n+added\n",
		"findings/code.json": `{"concern":"code","status":"issues","findings":` + findings + `}`,
	} {
		if err := os.WriteFile(filepath.Join(bundle, name), []byte(data), 0644); err != nil {
			t.Fatal(err)
		}
	}
	bin := t.TempDir()
	log, body := filepath.Join(bin, "requests.log"), filepath.Join(bin, "body.json")
	launcher := `#!/bin/sh
printf '%s\n' "$*" >> "$FAKE_GH_LOG"
case "$*" in
  *'--json headRefOid'*) echo '{"headRefOid":"1234567890abcdef1234567890abcdef12345678"}' ;;
  *'api graphql'*'nameWithOwner'*) echo '{"data":{"repository":{"id":"R_app","nameWithOwner":"example/app","pullRequest":{"number":7,"baseRepository":{"id":"R_app","nameWithOwner":"example/app"}}}}}' ;;
  *'--paginate --slurp'*) echo '[[]]' ;;
  *'api -X POST repos/example/app/pulls/7/comments'*)
    case "${FAKE_INLINE_MODE:-}" in
      position) echo '{"errors":["Line could not be resolved"]}'; echo 'gh: Validation Failed (HTTP 422)' >&2; exit 1 ;;
      auth) echo '{"message":"Resource not accessible"}'; echo 'gh: Forbidden (HTTP 403)' >&2; exit 1 ;;
      network) echo 'connection reset by peer' >&2; exit 1 ;;
      server) echo '{"message":"Service unavailable"}'; echo 'gh: Unavailable (HTTP 503)' >&2; exit 1 ;;
      body) echo '{"errors":[{"field":"body","code":"blank"}]}'; echo 'gh: Validation Failed (HTTP 422)' >&2; exit 1 ;;
      *) echo '{"id":401}' ;;
    esac ;;
  *'issues/comments/301'*|*'issues/7/comments'*) cat > "$FAKE_GH_BODY"; echo '{"id":301}' ;;
  *) exit 4 ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(launcher), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_GH_LOG", log)
	t.Setenv("FAKE_GH_BODY", body)
	c, err := OpenCollection(collection)
	if err != nil {
		t.Fatal(err)
	}
	return c, bundle, log, body
}

func TestReviewPostUnplaceableFindingsFinalizeAndRepost(t *testing.T) {
	c, bundle, log, body := reviewPositionFixture(t, `[
{"severity":"minor","file":"pr.md","line":50,"title":"Clarify description"},
{"severity":"minor","file":"feature.go","line":70,"title":"Outside hunk"}]`)
	for i := 0; i < 2; i++ {
		result, err := c.PostReviewBundle(bundle, ReviewPostOptions{})
		if err != nil || result.InlineFailed != 0 || result.InlinePosted != 0 || result.InlineSkipped != 2 {
			t.Fatalf("unplaceable findings blocked final status: %+v %v", result, err)
		}
		final, err := os.ReadFile(body)
		if err != nil || !strings.Contains(string(final), "verdict=pass") || !strings.Contains(string(final), "Findings remain visible") {
			t.Fatalf("summary not finalized: %s %v", final, err)
		}
	}
	rows := readInlineRecords(bundle)
	if len(rows) != 2 || rows[0].Skipped != "not in diff" || rows[1].Skipped != "not in diff" || rows[0].Error != "" {
		t.Fatalf("skipped records wrong or duplicated: %+v", rows)
	}
	requests, _ := os.ReadFile(log)
	if strings.Contains(string(requests), "api -X POST repos/example/app/pulls/7/comments") {
		t.Fatal("attempted to post a comment outside the diff")
	}
	if _, err := os.Stat(reviewReceiptPath(c.Collection, "github", "example/app", "7", "1234567890abcdef1234567890abcdef12345678", "301", "pass")); err != nil {
		t.Fatal("final review has no trusted receipt", err)
	}
}

func TestReviewPostPositionRejectionSkipsButRealFailuresStayPending(t *testing.T) {
	for _, tc := range []struct{ mode, message string }{
		{"position", ""}, {"auth", "Resource not accessible"}, {"network", "connection reset"},
		{"server", "Service unavailable"}, {"body", "blank"},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			c, bundle, _, body := reviewPositionFixture(t, `[{"severity":"minor","file":"feature.go","line":4,"title":"Changed line"}]`)
			t.Setenv("FAKE_INLINE_MODE", tc.mode)
			result, err := c.PostReviewBundle(bundle, ReviewPostOptions{})
			rows := readInlineRecords(bundle)
			if len(rows) != 1 {
				t.Fatalf("missing attempt record: %+v", rows)
			}
			final, _ := os.ReadFile(body)
			if tc.mode == "position" {
				if err != nil || result.InlineSkipped != 1 || result.InlineFailed != 0 || rows[0].Error != "" || rows[0].Skipped != "not in diff" || !strings.Contains(string(final), "verdict=pass") {
					t.Fatalf("422 position rejection blocked summary: %+v %+v %s %v", result, rows, final, err)
				}
			} else {
				if err == nil || result.InlineFailed != 1 || result.InlineSkipped != 0 || !strings.Contains(rows[0].Error, tc.message) || !strings.Contains(string(final), "verdict=pending") {
					t.Fatalf("real failure was hidden: %+v %+v %s %v", result, rows, final, err)
				}
			}
		})
	}
}

func TestReviewPostRetryReplacesFailedRecord(t *testing.T) {
	c, bundle, _, _ := reviewPositionFixture(t, `[{"severity":"minor","file":"feature.go","line":4,"title":"Changed line"}]`)
	t.Setenv("FAKE_INLINE_MODE", "network")
	if _, err := c.PostReviewBundle(bundle, ReviewPostOptions{}); err == nil {
		t.Fatal("network failure did not fail the post")
	}
	t.Setenv("FAKE_INLINE_MODE", "")
	result, err := c.PostReviewBundle(bundle, ReviewPostOptions{})
	if err != nil || result.InlinePosted != 1 {
		t.Fatal(result, err)
	}
	data, _ := os.ReadFile(filepath.Join(bundle, "inline-comments.json"))
	var rows []ReviewInlineRecord
	if err := json.Unmarshal(data, &rows); err != nil || len(rows) != 1 || rows[0].ID != "401" || rows[0].Error != "" {
		t.Fatalf("failed attempt was not replaced: %s %v", data, err)
	}
}
