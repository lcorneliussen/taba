package wtc

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBitbucketReviewPostResolveWithoutAPICredentials(t *testing.T) {
	collection := filepath.Join(t.TempDir(), "sample")
	harness := filepath.Join(collection, "harness")
	repo := filepath.Join(collection, "app")
	bundle := filepath.Join(collection, "bundle")
	for _, dir := range []string{harness, filepath.Join(repo, ".git"), filepath.Join(bundle, "findings")} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(harness, ".harness-repos.yml"), []byte("repos:\n  - name: app\n    remote: https://bitbucket.org/example/app.git\n"), 0644); err != nil {
		t.Fatal(err)
	}
	head := "1234567890abcdef1234567890abcdef12345678"
	manifest := ReviewManifest{Repo: "app", PR: "7", Public: true, Forge: "bitbucket", Slug: "example/app", URL: "https://bitbucket.org/example/app/pull-requests/7", HeadSHA: head, Round: 1, RepoDir: repo}
	if err := writeReviewManifest(bundle, manifest); err != nil {
		t.Fatal(err)
	}
	summary := "**Local review: pass**\n\n`wtc-review v1 head=" + head + " verdict=pass blockers=0 round=1 lead=test`\n"
	if err := os.WriteFile(filepath.Join(bundle, "summary.md"), []byte(summary), 0644); err != nil {
		t.Fatal(err)
	}
	finding := `{"concern":"code","status":"issues","findings":[{"severity":"major","file":"feature.go","line":4,"title":"Broken edge","detail":"Fails on empty input"}]}`
	if err := os.WriteFile(filepath.Join(bundle, "findings", "code.json"), []byte(finding), 0644); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	log := filepath.Join(bin, "calls.log")
	launcher := `#!/bin/sh
[ "$1" = --workspace ] && [ "$2" = example ] && [ "$3" = --repo ] && [ "$4" = app ] || exit 90
shift 4
printf '%s\n' "$*" >> "$FAKE_BB_LOG"
case "$*" in
  'pr view 7 --json') printf '{"source":{"commit":{"hash":"1234567890abcdef1234567890abcdef12345678"}}}\n' ;;
  'pr comments list 7 --limit 1000 --json') printf '{"count":0,"comments":[]}\n' ;;
  'pr comments add 7 '*'--file feature.go --line-to 4 --json') printf '{"id":401}\n' ;;
  'pr comments add 7 '*'--json') printf '{"id":301,"links":{"html":{"href":"https://bitbucket.org/example/app/pull-requests/7#comment-301"}}}\n' ;;
  'pr comments edit 7 301 '*) : ;;
  'pr comments reply 7 401 '*) : ;;
  'pr comments resolve 7 401') : ;;
  *) printf 'unexpected bb command: %s\n' "$*" >&2; exit 4 ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "bb"), []byte(launcher), 0755); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"BB_API_USER", "BB_API_PASS", "BB_USERNAME", "BB_API_TOKEN"} {
		t.Setenv(key, "")
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_BB_LOG", log)
	c, err := OpenCollection(collection)
	if err != nil {
		t.Fatal(err)
	}
	posted, err := c.PostReviewBundle(bundle, ReviewPostOptions{})
	if err != nil || posted.CommentID != "301" || posted.InlinePosted != 1 || posted.InlineFailed != 0 {
		t.Fatalf("Bitbucket post: %+v %v", posted, err)
	}
	if _, err := os.Stat(reviewReceiptPath(collection, "bitbucket", "example/app", "7", head, "301", "pass")); err != nil {
		t.Fatalf("missing posting receipt: %v", err)
	}
	rows := readInlineRecords(bundle)
	if len(rows) != 1 || rows[0].ID != "401" || rows[0].File != "feature.go" || rows[0].Line != 4 {
		t.Fatalf("inline receipt: %+v", rows)
	}
	resolved, err := c.ResolveReviewBundle(bundle, ReviewResolveOptions{Reply: "Fixed in the patch."})
	if err != nil || resolved.Resolved != 1 || !readInlineRecords(bundle)[0].Resolved {
		t.Fatalf("Bitbucket resolve: %+v %v", resolved, err)
	}
	calls, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	text := string(calls)
	for _, want := range []string{"pr comments add 7", "--file feature.go --line-to 4 --json", "pr comments edit 7 301", "pr comments reply 7 401 Fixed in the patch.", "pr comments resolve 7 401"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in Bitbucket calls:\n%s", want, text)
		}
	}
	if strings.Index(text, "pr comments reply 7 401") > strings.Index(text, "pr comments resolve 7 401") {
		t.Fatal("Bitbucket thread resolved before the reply was posted")
	}
}

type bitbucketTransport func(*http.Request) (*http.Response, error)

func (fn bitbucketTransport) RoundTrip(req *http.Request) (*http.Response, error) { return fn(req) }

func TestBitbucketReviewCommentAPIShapesAndAuthentication(t *testing.T) {
	old := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = old })
	t.Setenv("BB_API_USER", "synthetic-user")
	t.Setenv("BB_API_PASS", "synthetic-pass")
	requests := []struct {
		method string
		path   string
		body   map[string]any
	}{}
	http.DefaultTransport = bitbucketTransport(func(req *http.Request) (*http.Response, error) {
		user, pass, ok := req.BasicAuth()
		if !ok || user != "synthetic-user" || pass != "synthetic-pass" {
			t.Error("Bitbucket request omitted configured basic authentication")
		}
		if req.URL.Host != "api.bitbucket.org" || req.Header.Get("Content-Type") != "application/json" {
			t.Errorf("unexpected Bitbucket endpoint or content type: %s %s", req.URL, req.Header.Get("Content-Type"))
		}
		var body map[string]any
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		requests = append(requests, struct {
			method string
			path   string
			body   map[string]any
		}{req.Method, req.URL.Path, body})
		return &http.Response{StatusCode: 201, Status: "201 Created", Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"id":401,"links":{"html":{"href":"https://bitbucket.org/example/app/pull-requests/7#comment-401"}}}`))}, nil
	})
	m := ReviewManifest{Forge: "bitbucket", Slug: "example/app", PR: "7"}
	for _, call := range []struct {
		cid, body, file, parent string
		line                    int
		method, suffix          string
	}{
		{"", "Summary", "", "", 0, http.MethodPost, "/comments"},
		{"401", "Updated summary", "", "", 0, http.MethodPut, "/comments/401"},
		{"", "Inline", "feature.go", "", 4, http.MethodPost, "/comments"},
		{"", "Reply", "", "401", 0, http.MethodPost, "/comments"},
	} {
		result, ok, err := bitbucketReviewCommentAPI(m, call.cid, call.body, call.file, call.line, call.parent)
		if !ok || err != nil || result.CommentID != "401" {
			t.Fatalf("Bitbucket API result: %+v %v %v", result, ok, err)
		}
		got := requests[len(requests)-1]
		if got.method != call.method || got.path != "/2.0/repositories/example/app/pullrequests/7"+call.suffix || got.body["content"].(map[string]any)["raw"] != call.body {
			t.Fatalf("Bitbucket API request: %+v", got)
		}
	}
	if inline := requests[2].body["inline"].(map[string]any); inline["path"] != "feature.go" || inline["to"] != float64(4) {
		t.Fatalf("wrong inline position: %+v", inline)
	}
	if parent := requests[3].body["parent"].(map[string]any); parent["id"] != float64(401) {
		t.Fatalf("wrong reply target: %+v", parent)
	}
}

func TestBitbucketReviewStatusRequiresCurrentHeadAndLocalReceipt(t *testing.T) {
	collection := filepath.Join(t.TempDir(), "sample")
	harness := filepath.Join(collection, "harness")
	repo := filepath.Join(collection, "app")
	for _, dir := range []string{harness, filepath.Join(repo, ".git")} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(harness, ".harness-repos.yml"), []byte("repos:\n  - name: app\n    remote: https://bitbucket.org/example/app.git\n"), 0644); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	launcher := `#!/bin/sh
if [ "$1" = --workspace ]; then
  [ "$2" = example ] && [ "$3" = --repo ] && [ "$4" = app ] || exit 90
  shift 4
fi
case "$*" in
  'pr view 7 --json') printf '{"source":{"commit":{"hash":"%s"}}}\n' "$FAKE_BB_HEAD" ;;
  'pr comments list 7 --all --json') printf '{"values":[{"id":301,"content":{"raw":"wtc-review v1 head=%s verdict=pass blockers=0 round=1"},"created_on":"2026-01-01T00:00:00Z","links":{"html":{"href":"https://bitbucket.org/example/app/pull-requests/7#comment-301"}}}]}\n' "$FAKE_BB_REVIEW_HEAD" ;;
  *) exit 4 ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "bb"), []byte(launcher), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	head := "1234567890abcdef1234567890abcdef12345678"
	t.Setenv("FAKE_BB_HEAD", head)
	t.Setenv("FAKE_BB_REVIEW_HEAD", head)
	c, err := OpenCollection(collection)
	if err != nil {
		t.Fatal(err)
	}
	status, err := c.GetReviewStatus("app", "7", true)
	if err != nil || status.State != "untrusted" || status.Verdict != "pass" || status.CommentID != "301" {
		t.Fatalf("untrusted Bitbucket status: %+v %v", status, err)
	}
	receipt := reviewReceiptPath(collection, "bitbucket", "example/app", "7", head, "301", "pass")
	if err := os.MkdirAll(filepath.Dir(receipt), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(receipt, nil, 0600); err != nil {
		t.Fatal(err)
	}
	status, err = c.GetReviewStatus("app", "7", true)
	if err != nil || status.State != "current" {
		t.Fatalf("trusted Bitbucket status: %+v %v", status, err)
	}
	t.Setenv("FAKE_BB_HEAD", "abcdef1234567890abcdef1234567890abcdef12")
	status, err = c.GetReviewStatus("app", "7", true)
	if err != nil || status.State != "stale" {
		t.Fatalf("stale Bitbucket status: %+v %v", status, err)
	}
}

func TestBitbucketReviewDoesNotDowngradeUnsafeBodiesOrAPIError(t *testing.T) {
	m := ReviewManifest{Forge: "bitbucket", Slug: "example/app", PR: "7"}
	for _, key := range []string{"BB_API_USER", "BB_API_PASS", "BB_USERNAME", "BB_API_TOKEN"} {
		t.Setenv(key, "")
	}
	if _, err := postReviewComment(m, "", strings.Repeat("x", 8193)); err == nil || !strings.Contains(err.Error(), "requires API credentials") {
		t.Fatalf("long comment fell through to CLI: %v", err)
	}
	if err := replyReviewInline(m, "401", "-unsafe"); err == nil || !strings.Contains(err.Error(), "requires API credentials") {
		t.Fatalf("flag-like reply fell through to CLI: %v", err)
	}
	t.Setenv("BB_API_USER", "synthetic-user")
	t.Setenv("BB_API_PASS", "synthetic-pass")
	old := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = old })
	http.DefaultTransport = bitbucketTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 401, Status: "401 Unauthorized", Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":"denied"}`))}, nil
	})
	if _, err := postReviewComment(m, "", "Review body"); err == nil || !strings.Contains(err.Error(), "401 Unauthorized") {
		t.Fatalf("Bitbucket API error was hidden by CLI fallback: %v", err)
	}
}
