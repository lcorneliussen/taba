package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"taba.sh/taba/internal/wtc"
)

// bitbucketReceiptFlow drives the native review lifecycle against a fake bb
// whose output follows @pilatos/bitbucket-cli: `pr comments add --json`
// prints {success, pullRequestId, comment: <REST comment>} (comment.command.ts
// in 1.8.3 and 2.2.1), and `pr comments list --json` prints the REST comments
// under "comments". addJSON overrides the add output; "" uses the real shape.
type bitbucketReceiptFlow struct {
	collection, bundle, head, log string
}

const syntheticBitbucketCommentID = "640123457"

func newBitbucketReceiptFlow(t *testing.T, addJSON string) bitbucketReceiptFlow {
	t.Helper()
	collection := filepath.Join(t.TempDir(), "sample")
	repo := filepath.Join(collection, "app")
	bundle := filepath.Join(collection, "bundle")
	for _, dir := range []string{filepath.Join(collection, "harness"), filepath.Join(repo, ".git"), bundle} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(collection, "harness", ".harness-repos.yml"), []byte("repos:\n  - name: app\n    remote: https://bitbucket.org/example/app.git\n"), 0644); err != nil {
		t.Fatal(err)
	}
	head := "1234567890abcdef1234567890abcdef12345678"
	manifest, _ := json.Marshal(wtc.ReviewManifest{Repo: "app", PR: "7", Public: true, Forge: "bitbucket", Slug: "example/app", URL: "https://bitbucket.org/example/app/pull-requests/7", HeadSHA: head, Round: 1, RepoDir: repo})
	if err := os.WriteFile(filepath.Join(bundle, "manifest.json"), manifest, 0644); err != nil {
		t.Fatal(err)
	}
	summary := "**Local review: pass**\n\n`wtc-review v1 head=" + head + " verdict=pass blockers=0 round=1 lead=test`\n"
	if err := os.WriteFile(filepath.Join(bundle, "summary.md"), []byte(summary), 0644); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	state := t.TempDir()
	if addJSON == "" {
		addJSON = `{"success":true,"pullRequestId":7,"comment":{"id":` + syntheticBitbucketCommentID + `,"content":{"raw":"posting"},"links":{"self":{"href":"https://api.bitbucket.org/2.0/repositories/example/app/pullrequests/7/comments/` + syntheticBitbucketCommentID + `"},"html":{"href":"https://bitbucket.org/example/app/pull-requests/7#comment-` + syntheticBitbucketCommentID + `"}},"created_on":"2026-01-01T00:00:00Z"}}`
	}
	if err := os.WriteFile(filepath.Join(state, "add.json"), []byte(addJSON+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// The list prints the REST comment exactly as stored, with a JSON number id
	// and the html link Bitbucket gives it; only the status line is copied in.
	launcher := `#!/bin/sh
if [ "$1" = --workspace ]; then
  [ "$2" = example ] && [ "$3" = --repo ] && [ "$4" = app ] || exit 90
  shift 4
fi
printf '%s\n' "$*" >> "$FAKE_BB_LOG"
case "$*" in
  'pr view 7 --json') printf '{"id":7,"state":"OPEN","draft":true,"source":{"commit":{"hash":"1234567890abcdef1234567890abcdef12345678"}}}\n' ;;
  'pr comments list 7 --limit 1000 --json') printf '{"pullRequestId":7,"count":0,"comments":[]}\n' ;;
  'pr comments add 7 '*'--json') printf '%s\n' "$5" > "$FAKE_BB_STATE/body"; /bin/cat "$FAKE_BB_STATE/add.json" ;;
  "pr comments edit 7 $FAKE_BB_CID "*) printf '%s\n' "$6" > "$FAKE_BB_STATE/body" ;;
  'pr comments list 7 --all --json')
    line=$(/usr/bin/grep -o 'wtc-review v1 head=[0-9a-f]* verdict=[a-z-]* blockers=[0-9]* round=[0-9]*' "$FAKE_BB_STATE/body" | /usr/bin/tail -n 1)
    printf '{"workspace":"example","repoSlug":"app","pullRequestId":7,"filters":{"resolution":null},"count":1,"comments":[{"id":%s,"content":{"raw":"%s"},"created_on":"2026-01-01T00:00:00Z","links":{"html":{"href":"https://bitbucket.org/example/app/pull-requests/7#comment-%s"}}}]}\n' "${FAKE_BB_LIST_ID:-$FAKE_BB_CID}" "$line" "${FAKE_BB_LINK_ID:-$FAKE_BB_CID}" ;;
  'pr ready 7') : ;;
  *) printf 'unexpected bb command: %s\n' "$*" >&2; exit 4 ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "bb"), []byte(launcher), 0755); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"BB_API_USER", "BB_API_PASS", "BB_USERNAME", "BB_API_TOKEN"} {
		t.Setenv(key, "")
	}
	for _, kv := range os.Environ() {
		if key, _, _ := strings.Cut(kv, "="); strings.HasPrefix(key, "WTC_") || strings.HasPrefix(key, "TABA_") {
			t.Setenv(key, "")
			os.Unsetenv(key)
		}
	}
	// This flow is the deprecated bb client's contract (#471): select it.
	t.Setenv("TABA_BITBUCKET_TRANSPORT", "bb")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+"/usr/bin:/bin")
	t.Setenv("FAKE_BB_LOG", filepath.Join(state, "calls.log"))
	t.Setenv("FAKE_BB_STATE", state)
	t.Setenv("FAKE_BB_CID", syntheticBitbucketCommentID)
	return bitbucketReceiptFlow{collection: collection, bundle: bundle, head: head, log: filepath.Join(state, "calls.log")}
}

func (f bitbucketReceiptFlow) run(t *testing.T, args ...string) error {
	t.Helper()
	oldArgs := os.Args
	t.Cleanup(func() { os.Args = oldArgs })
	os.Args = append([]string{"wtc"}, append(args, "--collection", f.collection)...)
	return run()
}

func (f bitbucketReceiptFlow) calls(t *testing.T) string {
	t.Helper()
	data, _ := os.ReadFile(f.log)
	return string(data)
}

// Issue 444: a real-shaped bb 2.2.1 create, then trusted status, then the
// default ready command, with no hand-written comment.id or receipt.
func TestBitbucketNativeReviewReceiptReachesReady(t *testing.T) {
	f := newBitbucketReceiptFlow(t, "")
	if err := f.run(t, "review", "post", f.bundle); err != nil {
		t.Fatalf("review post: %v\ncalls:\n%s", err, f.calls(t))
	}
	if saved, _ := os.ReadFile(filepath.Join(f.bundle, "comment.id")); strings.TrimSpace(string(saved)) != syntheticBitbucketCommentID {
		t.Fatalf("saved comment id %q", saved)
	}
	if _, err := os.Stat(filepath.Join(f.bundle, "comment.create-pending")); !os.IsNotExist(err) {
		t.Fatalf("known create left pending: %v", err)
	}
	c, err := wtc.OpenCollection(f.collection)
	if err != nil {
		t.Fatal(err)
	}
	status, err := c.GetReviewStatus("app", "7", true)
	if err != nil || status.State != "current" || status.Verdict != "pass" || status.CommentID != syntheticBitbucketCommentID {
		t.Fatalf("trusted status: %+v %v", status, err)
	}
	if err := f.run(t, "review", "ready", "7", "--repo", "app"); err != nil {
		t.Fatalf("review ready: %v\ncalls:\n%s", err, f.calls(t))
	}
	calls := f.calls(t)
	if strings.Count(calls, "pr comments add 7") != 1 || !strings.Contains(calls, "pr comments edit 7 "+syntheticBitbucketCommentID+" ") || !strings.HasSuffix(calls, "pr ready 7\n") {
		t.Fatalf("lifecycle calls:\n%s", calls)
	}
}

// A top-level REST-shaped create still persists; the receipt it writes must
// then match the id that status reads back from a JSON number and a
// #comment-<n> link.
func TestBitbucketTopLevelCreateReceiptMatchesStatus(t *testing.T) {
	f := newBitbucketReceiptFlow(t, `{"id":`+syntheticBitbucketCommentID+`,"links":{"html":{"href":"https://bitbucket.org/example/app/pull-requests/7#comment-`+syntheticBitbucketCommentID+`"}}}`)
	if err := f.run(t, "review", "post", f.bundle); err != nil {
		t.Fatalf("review post: %v", err)
	}
	c, err := wtc.OpenCollection(f.collection)
	if err != nil {
		t.Fatal(err)
	}
	status, err := c.GetReviewStatus("app", "7", true)
	if err != nil || status.State != "current" || status.CommentID != syntheticBitbucketCommentID {
		t.Fatalf("trusted status: %+v %v", status, err)
	}
	if err := f.run(t, "review", "ready", "7", "--repo", "app"); err != nil {
		t.Fatalf("review ready: %v", err)
	}
}

// A create whose response does not prove one id for this PR is uncertain: it
// stays pending, nothing is saved or receipted, a retry does not create
// again, and ready stays closed.
func TestBitbucketUncertainCreateStaysPending(t *testing.T) {
	cid := syntheticBitbucketCommentID
	link := `"links":{"html":{"href":"https://bitbucket.org/example/app/pull-requests/7#comment-` + cid + `"}}`
	for name, add := range map[string]string{
		"no-id":           `{"success":true,"pullRequestId":7,"comment":{"content":{"raw":"x"}}}`,
		"null-comment":    `{"success":true,"pullRequestId":7,"comment":null}`,
		"not-successful":  `{"success":false,"pullRequestId":7,"comment":{"id":` + cid + `}}`,
		"error-envelope":  `{"type":"error","error":{"message":"Bad request"}}`,
		"cli-error":       `{"success":true,"error":{"code":"API_ERROR"},"comment":{"id":` + cid + `}}`,
		"other-pr":        `{"success":true,"pullRequestId":8,"comment":{"id":` + cid + `,` + link + `}}`,
		"conflicting-ids": `{"id":640123458,"success":true,"pullRequestId":7,"comment":{"id":` + cid + `}}`,
		"id-contra-link":  `{"success":true,"pullRequestId":7,"comment":{"id":640123458,` + link + `}}`,
		"exponent-id":     `{"success":true,"pullRequestId":7,"comment":{"id":6.40123457e8}}`,
		"fraction-id":     `{"success":true,"pullRequestId":7,"comment":{"id":640123457.5}}`,
		"negative-id":     `{"success":true,"pullRequestId":7,"comment":{"id":-640123457}}`,
		"zero-id":         `{"id":0}`,
		"malformed":       `{"success":true,"comment":{"id":` + cid,
		"trailing":        `{"id":` + cid + `} {"id":1}`,
		"plain-text":      `Added comment to pull request #7`,
		"boolean-id":      `{"comment":{"id":true}}`,
	} {
		t.Run(name, func(t *testing.T) {
			f := newBitbucketReceiptFlow(t, add)
			if err := f.run(t, "review", "post", f.bundle); err == nil {
				t.Fatal("uncertain create accepted")
			}
			if _, err := os.Stat(filepath.Join(f.bundle, "comment.create-pending")); err != nil {
				t.Fatalf("pending marker: %v", err)
			}
			if _, err := os.Stat(filepath.Join(f.bundle, "comment.id")); !os.IsNotExist(err) {
				t.Fatalf("saved an unproven id: %v", err)
			}
			err := f.run(t, "review", "post", f.bundle)
			if err == nil || !strings.Contains(err.Error(), "comment.create-pending") {
				t.Fatalf("retry was not refused: %v", err)
			}
			if err := f.run(t, "review", "ready", "7", "--repo", "app"); err == nil {
				t.Fatal("ready after uncertain create")
			}
			calls := f.calls(t)
			if strings.Count(calls, "pr comments add") != 1 || strings.Contains(calls, "pr comments edit") || strings.Contains(calls, "pr ready") {
				t.Fatalf("calls:\n%s", calls)
			}
			if entries, _ := os.ReadDir(filepath.Join(f.collection, ".wtc-review-posted")); len(entries) != 0 {
				t.Fatalf("receipt written: %v", entries)
			}
		})
	}
}

// The receipt names one comment. A status comment that reads as another id,
// or whose id and link disagree, is not that comment.
func TestBitbucketReceiptRejectsOtherOrContradictoryStatusIDs(t *testing.T) {
	for name, env := range map[string][2]string{
		"other-id":           {"640123458", "640123458"},
		"id-contradicts-url": {syntheticBitbucketCommentID, "640123458"},
		"url-contradicts-id": {"640123458", syntheticBitbucketCommentID},
		"exponent-id":        {"6.40123457e8", syntheticBitbucketCommentID},
		"fraction-id":        {"640123457.0", syntheticBitbucketCommentID},
	} {
		t.Run(name, func(t *testing.T) {
			f := newBitbucketReceiptFlow(t, "")
			if err := f.run(t, "review", "post", f.bundle); err != nil {
				t.Fatal(err)
			}
			t.Setenv("FAKE_BB_LIST_ID", env[0])
			t.Setenv("FAKE_BB_LINK_ID", env[1])
			c, err := wtc.OpenCollection(f.collection)
			if err != nil {
				t.Fatal(err)
			}
			if status, err := c.GetReviewStatus("app", "7", true); err != nil || status.State != "untrusted" {
				t.Fatalf("status: %+v %v", status, err)
			}
			err = f.run(t, "review", "ready", "7", "--repo", "app")
			if err == nil || !strings.Contains(err.Error(), "untrusted") || strings.Contains(f.calls(t), "pr ready") {
				t.Fatalf("ready: %v\n%s", err, f.calls(t))
			}
		})
	}
}

// A saved id is updated, never recreated, and its known id survives into the
// receipt even when the update path is the only write.
func TestBitbucketSavedCommentIDUpdatesOnly(t *testing.T) {
	f := newBitbucketReceiptFlow(t, `{"success":true,"pullRequestId":7,"comment":{"id":1}}`)
	if err := os.WriteFile(filepath.Join(f.bundle, "comment.id"), []byte(syntheticBitbucketCommentID+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := f.run(t, "review", "post", f.bundle); err != nil {
		t.Fatal(err)
	}
	if err := f.run(t, "review", "ready", "7", "--repo", "app"); err != nil {
		t.Fatal(err)
	}
	if calls := f.calls(t); strings.Contains(calls, "pr comments add") || strings.Count(calls, "pr comments edit 7 "+syntheticBitbucketCommentID+" ") != 2 {
		t.Fatalf("calls:\n%s", calls)
	}
}
