package wtc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeLargeBB puts a bb on PATH that behaves like Node writing --json in one
// call and exiting: a regular file receives the whole body, a pipe only the
// first 64KiB. Each body is padded past that cap and bb also writes to
// stderr, which must stay out of the parsed JSON.
func fakeLargeBB(t *testing.T, bodies map[string]string) {
	t.Helper()
	bin := t.TempDir()
	padding := strings.Repeat("x", 80000)
	for name, body := range bodies {
		body = strings.Replace(body, "{", `{"description":"`+padding+`",`, 1)
		if err := os.WriteFile(filepath.Join(bin, name+".json"), []byte(body+"\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	script := `#!/bin/sh
case "$*" in
  *activity*) body=activity ;;
  *comments*) body=comments ;;
  *view*) body=view ;;
  *) exit 2 ;;
esac
echo "bb: notice on stderr" >&2
if [ -f /dev/stdout ]; then
  cat "` + bin + `/$body.json"
else
  head -c 65536 "` + bin + `/$body.json"
fi
`
	if err := os.WriteFile(filepath.Join(bin, "bb"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestStatusErrorsQuoteTheRightStream(t *testing.T) {
	script := "echo out; echo broken >&2; exit 3"
	_, err := statusJSON("sh", "-c", script)
	if err == nil || !strings.HasSuffix(err.Error(), ": broken") {
		t.Fatalf("forge error should quote stderr only: %v", err)
	}
	_, err = statusCommand(5*time.Second, "sh", "-c", script)
	if err == nil || !strings.HasSuffix(err.Error(), ": out\nbroken") {
		t.Fatalf("command error should quote combined output: %v", err)
	}
}

func TestStatusReadsBitbucketJSONPastPipeCap(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	statusHideBitbucketLogin(t)
	fakeLargeBB(t, map[string]string{
		"view":     `{"id":9,"state":"MERGED","updated_on":"2026-10-01T15:00:00Z","merge_commit":{"date":"2026-09-29T12:00:00Z"}}`,
		"activity": `{"activities":[{"update":{"state":"MERGED","date":"2026-09-30T12:00:00Z"}}]}`,
	})
	detail := statusEnrichRecord(PRRecord{Repo: "widget", Number: "9"}, "example/widget", "bitbucket.org")
	if detail.State != "MERGED" || detail.MergedOn != "2026-09-30T12:00:00Z" {
		t.Fatalf("large Bitbucket PR was not read: %+v", detail)
	}
}

func TestCatchUpReadsBitbucketJSONPastPipeCap(t *testing.T) {
	c := newWorkspaceFixture(t)
	owner := filepath.Join(c.Workspace, ".bare", "agent-harness.git")
	fixtureGit(t, "--git-dir="+owner, "remote", "set-url", "origin", "git@bitbucket.org:example/agent-harness.git")
	fixtureFile(t, filepath.Join(c.Collection, ".wtc-prs"), "agent-harness 7 topic - fixture\n", 0644)
	fakeLargeBB(t, map[string]string{
		"view": `{"state":"OPEN","draft":false,"source":{"commit":{"hash":"abc"}},"destination":{"branch":{"name":"main"}}}`,
	})
	target := catchUpTarget{collection: "main", path: c.Harness, repo: "agent-harness", harness: true}
	if got := c.catchUpPRState(target, "topic"); got.state != "OPEN" || got.base != "main" {
		t.Fatalf("large Bitbucket PR was not read: %+v", got)
	}
}

func TestReviewStatusReadsBitbucketJSONPastPipeCap(t *testing.T) {
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
	head := "1234567890abcdef1234567890abcdef12345678"
	fakeLargeBB(t, map[string]string{
		"view":     `{"source":{"commit":{"hash":"` + head + `"}}}`,
		"comments": `{"values":[{"id":301,"content":{"raw":"wtc-review v1 head=` + head + ` verdict=pass blockers=0 round=1"},"created_on":"2026-01-01T00:00:00Z","links":{"html":{"href":"https://bitbucket.org/example/app/pull-requests/7#comment-301"}}}]}`,
	})
	c, err := OpenCollection(collection)
	if err != nil {
		t.Fatal(err)
	}
	status, err := c.GetReviewStatus("app", "7", true)
	if err != nil || status.Verdict != "pass" || status.CommentID != "301" {
		t.Fatalf("large Bitbucket review status was not read: %+v %v", status, err)
	}
}
