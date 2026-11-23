package wtc

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Synthetic GitHub: the registry names example/old-widget; the forge may know
// it as example/widget. Reads follow a rename; a write to the old name's REST
// path is answered with HTTP 307, which gh does not replay.
const reviewIdentityHead = "1234567890abcdef1234567890abcdef12345678"

const reviewIdentityGH = `#!/bin/sh
printf '%s\n' "$*" >> "$FAKE_GH_LOG"
node() { printf '{"id":"%s","nameWithOwner":"%s"}' "$1" "$2"; }
case "$*" in
  *'api graphql'*'reviewThreads'*) printf '{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[{"id":"THREAD_1","isResolved":false,"comments":{"nodes":[{"databaseId":401}]}}]}}}}}\n' ;;
  *'api graphql'*'resolveReviewThread'*) printf '{"data":{"resolveReviewThread":{"thread":{"isResolved":true}}}}\n' ;;
  *'api graphql'*'pullRequest(number'*' name=old-widget '*)
    case "${FAKE_GQL:-}" in
      offline) echo 'error connecting to api.github.com' >&2; exit 1 ;;
      auth) echo 'gh: Bad credentials (HTTP 401)' >&2; exit 1 ;;
      errors) echo '{"errors":[{"type":"NOT_FOUND"}],"data":{"repository":null}}'; exit 0 ;;
    esac
    printf '{"data":{"repository":{"id":"%s","nameWithOwner":"%s","pullRequest":{"number":%s,"baseRepository":%s}}}}\n' \
      "$FAKE_OLD_ID" "$FAKE_OLD_NAME" "${FAKE_PR_NUMBER:-7}" "$(node "${FAKE_BASE_ID:-$FAKE_OLD_ID}" "${FAKE_BASE_NAME:-$FAKE_OLD_NAME}")" ;;
  *'api graphql'*'repository(owner'*)
    [ "${FAKE_CANON_GQL:-}" = offline ] && { echo 'error connecting to api.github.com' >&2; exit 1; }
    printf '{"data":{"repository":%s}}\n' "$(node "${FAKE_CANON_ID:-$FAKE_OLD_ID}" "${FAKE_CANON_NAME:-$FAKE_OLD_NAME}")" ;;
  *'--json headRefOid,statusCheckRollup'*) printf '{"headRefOid":"%s","statusCheckRollup":[]}\n' "$FAKE_HEAD" ;;
  *'--json headRefOid'*) printf '{"headRefOid":"%s"}\n' "$FAKE_HEAD" ;;
  *'--paginate --slurp'*) printf '[[]]\n' ;;
  *'-X POST repos/example/old-widget/'*|*'-X PATCH repos/example/old-widget/'*)
    cat >/dev/null; echo 'gh: HTTP 307: Temporary Redirect' >&2; exit 1 ;;
  *'-X POST repos/'*'/issues/7/comments'*)
    cat >/dev/null
    [ "${FAKE_POST:-}" = ambiguous ] && { echo 'gh: unexpected EOF' >&2; exit 1; }
    printf '{"id":301,"html_url":"https://github.com/%s/pull/7#issuecomment-301"}\n' "$FAKE_OLD_NAME" ;;
  *'-X PATCH repos/'*'/issues/comments/301'*)
    cat >/dev/null; printf '{"id":301,"html_url":"https://github.com/%s/pull/7#issuecomment-301"}\n' "$FAKE_OLD_NAME" ;;
  *'-X POST repos/'*'/pulls/7/comments'*)
    cat >/dev/null; printf '{"id":401,"html_url":"https://github.com/%s/pull/7#discussion_r401"}\n' "$FAKE_OLD_NAME" ;;
  *) printf 'unexpected args: %s\n' "$*" >&2; exit 4 ;;
esac
`

type reviewIdentityFixture struct {
	collection, bundle, log string
	c                       *Context
	manifest                ReviewManifest
}

// The forge state defaults to a legitimate rename of R_widget.
func newReviewIdentityFixture(t *testing.T, recordedID string) reviewIdentityFixture {
	t.Helper()
	collection := filepath.Join(t.TempDir(), "sample")
	repo := filepath.Join(collection, "widget")
	bundle := filepath.Join(collection, "bundle")
	for _, dir := range []string{filepath.Join(collection, "harness"), filepath.Join(repo, ".git"), filepath.Join(bundle, "findings")} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	fixtureFile(t, filepath.Join(collection, "harness", ".harness-repos.yml"), "repos:\n  - name: widget\n    remote: https://github.com/example/old-widget.git\n", 0644)
	manifest := ReviewManifest{Repo: "widget", PR: "7", Public: true, Forge: "github", Slug: "example/old-widget", RepositoryID: recordedID, HeadSHA: reviewIdentityHead, Round: 1, RepoDir: repo}
	if err := writeReviewManifest(bundle, manifest); err != nil {
		t.Fatal(err)
	}
	fixtureFile(t, filepath.Join(bundle, "summary.md"), "**Local review: pass**\n\n`wtc-review v1 head="+reviewIdentityHead+" verdict=pass blockers=0 round=1 lead=test:`\n", 0644)
	fixtureFile(t, filepath.Join(bundle, "diff.patch"), "diff --git a/feature.go b/feature.go\n--- a/feature.go\n+++ b/feature.go\n@@ -1,2 +1,3 @@\n one\n+two\n three\n", 0644)
	fixtureFile(t, filepath.Join(bundle, "findings", "code.json"), `{"concern":"code","status":"issues","findings":[{"severity":"major","file":"feature.go","line":2,"title":"Edge","detail":"Synthetic"}]}`, 0644)
	bin := t.TempDir()
	fixtureFile(t, filepath.Join(bin, "gh"), reviewIdentityGH, 0755)
	log := filepath.Join(bin, "requests.log")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_GH_LOG", log)
	t.Setenv("WTC_FORGE_MIN_INTERVAL", "0")
	t.Setenv("GH_HOST", "")
	for key, value := range map[string]string{"FAKE_HEAD": reviewIdentityHead, "FAKE_OLD_ID": "R_widget", "FAKE_OLD_NAME": "example/widget", "FAKE_GQL": "", "FAKE_CANON_GQL": "", "FAKE_POST": "", "FAKE_PR_NUMBER": "", "FAKE_BASE_ID": "", "FAKE_BASE_NAME": "", "FAKE_CANON_ID": "", "FAKE_CANON_NAME": ""} {
		t.Setenv(key, value)
	}
	c, err := OpenCollection(collection)
	if err != nil {
		t.Fatal(err)
	}
	return reviewIdentityFixture{collection, bundle, log, c, manifest}
}

func (f reviewIdentityFixture) requests(t *testing.T) string {
	data, err := os.ReadFile(f.log)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(data)
}

func reviewIdentityWrites(requests string) []string {
	var writes []string
	for _, line := range strings.Split(requests, "\n") {
		if strings.Contains(line, "-X POST") || strings.Contains(line, "-X PATCH") || strings.Contains(line, "resolveReviewThread") {
			writes = append(writes, line)
		}
	}
	return writes
}

func TestReviewPostFollowsVerifiedRenameWithRecordedIdentity(t *testing.T) {
	f := newReviewIdentityFixture(t, "R_widget")
	manifestBefore, _ := os.ReadFile(filepath.Join(f.bundle, "manifest.json"))
	posted, err := f.c.PostReviewBundle(f.bundle, ReviewPostOptions{})
	if err != nil || posted.CommentID != "301" || posted.InlinePosted != 1 {
		t.Fatalf("post: %+v %v\n%s", posted, err, f.requests(t))
	}
	writes := reviewIdentityWrites(f.requests(t))
	if len(writes) != 3 {
		t.Fatalf("want create, inline and final update, got %q", writes)
	}
	for _, w := range writes {
		if !strings.Contains(w, "repos/example/widget/") || !strings.HasSuffix(w, "--hostname github.com") {
			t.Fatalf("write not pinned to the verified canonical repository: %q", w)
		}
	}
	// The bundle is evidence of what was reviewed; recovery never edits it.
	if after, _ := os.ReadFile(filepath.Join(f.bundle, "manifest.json")); string(after) != string(manifestBefore) {
		t.Fatalf("manifest rewritten:\n%s\n%s", manifestBefore, after)
	}
	if _, err := os.Stat(filepath.Join(f.bundle, "comment.create-pending")); !os.IsNotExist(err) {
		t.Fatalf("confirmed create left its intent marker: %v", err)
	}
	// Receipts and the trusted gate stay keyed by the configured slug.
	if !reviewReceiptExists(f.collection, "github", "example/old-widget", "7", reviewIdentityHead, "301", "pass") {
		t.Fatal("receipt not recorded under the configured repository identity")
	}
	if reviewReceiptExists(f.collection, "github", "example/widget", "7", reviewIdentityHead, "301", "pass") {
		t.Fatal("receipt recorded under a name the status gate does not read")
	}
	comments := `[{"body":"` + "`wtc-review v1 head=" + reviewIdentityHead + " verdict=pass blockers=0 round=1 lead=test:`" + `","html_url":"https://github.com/example/widget/pull/7#issuecomment-301","created_at":"2026-01-01T00:00:00Z"}]`
	status, err := ParseReviewStatusComments([]byte(comments), 7, reviewIdentityHead, "github", "example/old-widget", f.collection, true)
	if err != nil || status.State != "current" {
		t.Fatalf("trusted gate after rename: %+v %v", status, err)
	}
	// Repeating the post updates the same comment rather than creating one.
	before := len(writes)
	if _, err := f.c.PostReviewBundle(f.bundle, ReviewPostOptions{}); err != nil {
		t.Fatal(err)
	}
	writes = reviewIdentityWrites(f.requests(t))
	for _, w := range writes[before:] {
		if strings.Contains(w, "-X POST") {
			t.Fatalf("repeat post created a new comment: %q", writes[before:])
		}
	}
}

func TestReviewPostRenameRefusals(t *testing.T) {
	for _, tc := range []struct {
		name, recorded string
		env            map[string]string
		want           string
	}{
		// An old bundle has no identity to carry forward, even though old,
		// canonical and PR metadata agree today.
		{"old bundle without recorded identity", "", nil, "recorded no repository identity"},
		// The old name now belongs to another repository.
		{"reused name", "R_widget", map[string]string{"FAKE_OLD_ID": "R_other", "FAKE_OLD_NAME": "example/old-widget"}, "name may have been reused"},
		{"reused name after a second rename", "R_widget", map[string]string{"FAKE_OLD_ID": "R_other"}, "name may have been reused"},
		{"canonical name is another repository", "R_widget", map[string]string{"FAKE_CANON_ID": "R_other"}, "not one verified repository"},
		{"canonical lookup names another repository", "R_widget", map[string]string{"FAKE_CANON_NAME": "example/widget-fork"}, "not one verified repository"},
		{"PR base is another repository", "R_widget", map[string]string{"FAKE_BASE_ID": "R_fork"}, "disagree"},
		{"PR number mismatch", "R_widget", map[string]string{"FAKE_PR_NUMBER": "8"}, "disagree"},
		{"transfer to another owner", "R_widget", map[string]string{"FAKE_OLD_NAME": "elsewhere/widget"}, "another owner"},
		{"unsafe canonical name", "R_widget", map[string]string{"FAKE_OLD_NAME": "example/../widget"}, "disagree"},
		{"canonical name on another host", "R_widget", map[string]string{"FAKE_OLD_NAME": "evil.example/widget/x"}, "disagree"},
		{"offline", "R_widget", map[string]string{"FAKE_GQL": "offline"}, "check gh auth status and network access"},
		{"auth failure", "R_widget", map[string]string{"FAKE_GQL": "auth"}, "check gh auth status and network access"},
		{"provider errors", "R_widget", map[string]string{"FAKE_GQL": "errors"}, "no repository identity"},
		{"canonical lookup offline", "R_widget", map[string]string{"FAKE_CANON_GQL": "offline"}, "cannot verify canonical"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newReviewIdentityFixture(t, tc.recorded)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			_, err := f.c.PostReviewBundle(f.bundle, ReviewPostOptions{})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want refusal %q, got %v\n%s", tc.want, err, f.requests(t))
			}
			if writes := reviewIdentityWrites(f.requests(t)); len(writes) != 0 {
				t.Fatalf("refused identity still wrote: %q", writes)
			}
			if _, err := os.Stat(filepath.Join(f.bundle, "comment.create-pending")); !os.IsNotExist(err) {
				t.Fatalf("refusal created an intent marker: %v", err)
			}
			// Progress and failed posts are writes too.
			for _, mode := range []string{"progress", "failed"} {
				if _, err := f.c.PostReviewBundle(f.bundle, ReviewPostOptions{Mode: mode}); err == nil {
					t.Fatalf("%s post bypassed identity verification", mode)
				}
			}
			if writes := reviewIdentityWrites(f.requests(t)); len(writes) != 0 {
				t.Fatalf("refused identity still wrote: %q", writes)
			}
		})
	}
}

// A create whose outcome is unknown, such as one that failed with the redirect
// under the old name, keeps blocking reposts after a verified rename: the
// marker refuses the create before any write and is left as is.
func TestReviewPostUncertainCreateSurvivesRename(t *testing.T) {
	f := newReviewIdentityFixture(t, "R_widget")
	pending := filepath.Join(f.bundle, "comment.create-pending")
	fixtureFile(t, pending, "prior attempt\n", 0600)
	old := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := os.Chtimes(pending, old, old); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := f.c.PostReviewBundle(f.bundle, ReviewPostOptions{}); err == nil || !strings.Contains(err.Error(), "comment.create-pending") {
			t.Fatalf("existing uncertain create not honoured: %v", err)
		}
	}
	info, err := os.Stat(pending)
	data, _ := os.ReadFile(pending)
	if err != nil || string(data) != "prior attempt\n" || !info.ModTime().Equal(old) {
		t.Fatalf("existing intent marker changed: %q %v %v", data, info, err)
	}
	if writes := reviewIdentityWrites(f.requests(t)); len(writes) != 0 {
		t.Fatalf("posted despite an unreconciled create: %q", writes)
	}
}

func TestReviewPostAmbiguousCanonicalCreateIsNotRetried(t *testing.T) {
	f := newReviewIdentityFixture(t, "R_widget")
	t.Setenv("FAKE_POST", "ambiguous")
	for i := 0; i < 3; i++ {
		if _, err := f.c.PostReviewBundle(f.bundle, ReviewPostOptions{}); err == nil {
			t.Fatal("ambiguous create reported success")
		}
	}
	writes := reviewIdentityWrites(f.requests(t))
	if len(writes) != 1 || !strings.Contains(writes[0], "-X POST repos/example/widget/issues/7/comments") {
		t.Fatalf("want exactly one create attempt, got %q", writes)
	}
	if _, err := os.Stat(filepath.Join(f.bundle, "comment.create-pending")); err != nil {
		t.Fatalf("ambiguous create lost its intent marker: %v", err)
	}
	if reviewReceiptExists(f.collection, "github", "example/old-widget", "7", reviewIdentityHead, "301", "pass") {
		t.Fatal("ambiguous create left a trusted receipt")
	}
}

func TestReviewPostUnrenamedRepository(t *testing.T) {
	// Old bundles keep posting while the configured name is still the
	// repository's name, including a case-only spelling difference.
	for _, name := range []string{"example/old-widget", "Example/Old-Widget"} {
		t.Run(name, func(t *testing.T) {
			f := newReviewIdentityFixture(t, "")
			t.Setenv("FAKE_OLD_NAME", name)
			t.Setenv("FAKE_GH_LOG", f.log)
			// Make the old REST path accept writes: no rename happened.
			script := strings.Replace(reviewIdentityGH, `  *'-X POST repos/example/old-widget/'*|*'-X PATCH repos/example/old-widget/'*)
    cat >/dev/null; echo 'gh: HTTP 307: Temporary Redirect' >&2; exit 1 ;;
`, "", 1)
			fixtureFile(t, filepath.Join(filepath.Dir(f.log), "gh"), script, 0755)
			if _, err := f.c.PostReviewBundle(f.bundle, ReviewPostOptions{}); err != nil {
				t.Fatalf("unrenamed post: %v\n%s", err, f.requests(t))
			}
			for _, w := range reviewIdentityWrites(f.requests(t)) {
				if !strings.Contains(w, "repos/example/old-widget/") {
					t.Fatalf("unrenamed repository written elsewhere: %q", w)
				}
			}
		})
	}
}

func TestReviewResolveReplyFollowsVerifiedRename(t *testing.T) {
	f := newReviewIdentityFixture(t, "R_widget")
	if err := writeInlineRecords(f.bundle, []ReviewInlineRecord{{Key: "012345abcd", ID: "401"}}); err != nil {
		t.Fatal(err)
	}
	result, err := f.c.ResolveReviewBundle(f.bundle, ReviewResolveOptions{Reply: "Fixed."})
	if err != nil || result.Resolved != 1 {
		t.Fatalf("resolve: %+v %v\n%s", result, err, f.requests(t))
	}
	writes := reviewIdentityWrites(f.requests(t))
	if len(writes) != 2 || !strings.Contains(writes[0], "-X POST repos/example/widget/pulls/7/comments") || !strings.Contains(writes[1], "resolveReviewThread") {
		t.Fatalf("reply not sent to the verified repository: %q", writes)
	}
	for _, line := range strings.Split(strings.TrimSpace(f.requests(t)), "\n") {
		if strings.HasPrefix(line, "api ") && !strings.HasSuffix(line, "--hostname github.com") {
			t.Fatalf("review API call not pinned to github.com: %q", line)
		}
	}
	g := newReviewIdentityFixture(t, "")
	if err := writeInlineRecords(g.bundle, []ReviewInlineRecord{{Key: "012345abcd", ID: "401"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.c.ResolveReviewBundle(g.bundle, ReviewResolveOptions{Reply: "Fixed."}); err == nil || !strings.Contains(err.Error(), "recorded no repository identity") {
		t.Fatalf("old bundle reply gained rename authority: %v", err)
	}
	if writes := reviewIdentityWrites(g.requests(t)); len(writes) != 0 {
		t.Fatalf("refused reply still wrote: %q", writes)
	}
}

func TestReviewManifestRepositoryIDCompatibility(t *testing.T) {
	dir := t.TempDir()
	// A manifest written before repository IDs were recorded.
	fixtureFile(t, filepath.Join(dir, "manifest.json"), `{"repo":"widget","pr":"7","forge":"github","slug":"example/old-widget","head_sha":"`+reviewIdentityHead+`","round":1}`, 0644)
	m, err := readReviewManifest(dir)
	if err != nil || m.RepositoryID != "" || m.Slug != "example/old-widget" {
		t.Fatalf("old manifest: %+v %v", m, err)
	}
	data, _ := json.Marshal(m)
	if strings.Contains(string(data), "repository_id") {
		t.Fatalf("empty repository ID serialized: %s", data)
	}
	m.RepositoryID = "R_widget"
	if err := writeReviewManifest(dir, m); err != nil {
		t.Fatal(err)
	}
	again, err := readReviewManifest(dir)
	if err != nil || again.RepositoryID != "R_widget" {
		t.Fatalf("round trip: %+v %v", again, err)
	}
	// Receipt names do not include the repository ID: receipts written by
	// older versions keep proving their posts.
	if reviewReceiptPath("/c", "github", "example/old-widget", "7", reviewIdentityHead, "301", "pass") != reviewReceiptPaths("/c", "github", "example/old-widget", "7", reviewIdentityHead, "301", "pass")[0] {
		t.Fatal("receipt path changed")
	}
}

func TestReviewBundleRecordsRepositoryID(t *testing.T) {
	f := newReviewIdentityFixture(t, "")
	if got := reviewRecordedRepositoryID(f.manifest.RepoDir, "example/old-widget", "7"); got != "R_widget" {
		t.Fatalf("recorded ID %q", got)
	}
	t.Setenv("FAKE_GQL", "offline")
	if got := reviewRecordedRepositoryID(f.manifest.RepoDir, "example/old-widget", "7"); got != "" {
		t.Fatalf("unverifiable identity recorded %q", got)
	}
	t.Setenv("FAKE_GQL", "")
	t.Setenv("FAKE_CANON_ID", "R_other")
	if got := reviewRecordedRepositoryID(f.manifest.RepoDir, "example/old-widget", "7"); got != "" {
		t.Fatalf("contradictory identity recorded %q", got)
	}
}

func TestReviewWriteTargetLeavesBitbucketAlone(t *testing.T) {
	f := newReviewIdentityFixture(t, "")
	m := f.manifest
	m.Forge, m.Slug = "bitbucket", "example/old-widget"
	got, err := reviewWriteTarget(m)
	if err != nil || got != m || f.requests(t) != "" {
		t.Fatalf("Bitbucket target changed or queried GitHub: %+v %v %q", got, err, f.requests(t))
	}
}
