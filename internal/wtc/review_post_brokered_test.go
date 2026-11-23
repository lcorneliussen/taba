package wtc

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type brokeredFixture struct {
	c      *Context
	m      ReviewManifest
	bundle string
	gitDir string
	h      noBBHost
}

func newBrokeredFixture(t *testing.T) brokeredFixture {
	t.Helper()
	h := newNoBBHost(t)
	unsetEnv(t, "BB_API_USER", "BB_API_PASS", "BB_USERNAME", "BB_API_TOKEN")
	t.Setenv("BITBUCKET_PUBLISH", "gateway")
	root := t.TempDir()
	repo := filepath.Join(root, "alpha")
	if out, err := exec.Command("git", "init", "-q", repo).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	bundle := filepath.Join(root, "bundle")
	if err := os.MkdirAll(filepath.Join(bundle, "findings"), 0o755); err != nil {
		t.Fatal(err)
	}
	finding := `{"concern":"correctness","findings":[{"severity":"major","file":"a.go","line":3,"title":"Synthetic finding","detail":"Detail."}]}`
	if err := os.WriteFile(filepath.Join(bundle, "findings", "correctness.json"), []byte(finding), 0o644); err != nil {
		t.Fatal(err)
	}
	m := ReviewManifest{Repo: "alpha", PR: "7", Forge: "bitbucket", Slug: "acme/alpha", URL: "https://bitbucket.org/acme/alpha/pull-requests/7", HeadSHA: "aaaaaaaaaaaa0000000000000000000000000000", Round: 1, RepoDir: repo}
	return brokeredFixture{c: &Context{Collection: root}, m: m, bundle: bundle, gitDir: filepath.Join(repo, ".git"), h: h}
}

const brokeredSummary = "**Local review: pass**\n\n`wtc-review v1 head=aaaaaaaaaaaa0000000000000000000000000000 verdict=pass blockers=0 round=1 lead=test`\n"

func (f brokeredFixture) request(t *testing.T) brokeredRequest {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(f.gitDir, bitbucketWriteRequestFile))
	if err != nil {
		t.Fatal(err)
	}
	var request brokeredRequest
	if err := json.Unmarshal(raw, &request); err != nil {
		t.Fatal(err)
	}
	return request
}

func (f brokeredFixture) receipt(t *testing.T, intent string, completed bool, results ...string) {
	t.Helper()
	type result struct {
		Index   int    `json:"index"`
		Op      string `json:"op"`
		Status  string `json:"status"`
		Comment *int   `json:"comment,omitempty"`
	}
	var rows []result
	for i, r := range results {
		status, id, _ := strings.Cut(r, ":")
		row := result{Index: i, Op: "comment.create", Status: status}
		if id != "" {
			n := 0
			for _, ch := range id {
				n = n*10 + int(ch-'0')
			}
			row.Comment = &n
		}
		rows = append(rows, row)
	}
	data, _ := json.Marshal(map[string]any{"version": 1, "intentId": intent, "owner": "acme", "repo": "alpha", "pr": 7, "completed": completed, "results": rows})
	dir := filepath.Join(f.gitDir, bitbucketWriteReceiptsDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, intent+".receipt.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestBrokeredReviewPostAsksOnceAndReconcilesItsReceipt(t *testing.T) {
	f := newBrokeredFixture(t)
	_, err := f.c.postReviewBrokered(f.m, f.bundle, "", brokeredSummary)
	if !errors.Is(err, ErrBitbucketAwaitingConfirmation) {
		t.Fatalf("err = %v", err)
	}
	request := f.request(t)
	if request.PR != 7 || request.Owner != "acme" || request.Repo != "alpha" || len(request.Operations) != 2 ||
		request.Operations[0].Op != "comment.create" || request.Operations[1].Inline == nil || request.Operations[1].Inline.Path != "a.go" {
		t.Fatalf("request = %+v", request)
	}
	if _, err := os.Stat(filepath.Join(f.bundle, "comment.create-pending")); err != nil {
		t.Fatal("the brokered summary create must stay pending until its receipt")
	}
	if strings.Contains(f.h.read(t, f.h.log), "comment create") {
		t.Fatal("a gateway post wrote directly")
	}
	// Asking again changes nothing.
	before, _ := os.ReadFile(filepath.Join(f.gitDir, bitbucketWriteRequestFile))
	if _, err := f.c.postReviewBrokered(f.m, f.bundle, "", brokeredSummary); !errors.Is(err, ErrBitbucketAwaitingConfirmation) {
		t.Fatalf("second ask = %v", err)
	}
	if after, _ := os.ReadFile(filepath.Join(f.gitDir, bitbucketWriteRequestFile)); string(after) != string(before) {
		t.Fatal("asking again rewrote the request")
	}
	// The person confirmed; the plugin left a receipt.
	f.receipt(t, request.IntentID, true, "done:900", "done:901")
	result, err := f.c.postReviewBrokered(f.m, f.bundle, "", brokeredSummary)
	if err != nil || result.CommentID != "900" || result.InlinePosted != 1 {
		t.Fatalf("reconcile = %+v, %v", result, err)
	}
	if id, _ := os.ReadFile(filepath.Join(f.bundle, "comment.id")); strings.TrimSpace(string(id)) != "900" {
		t.Fatal("summary id not recorded")
	}
	if _, err := os.Stat(filepath.Join(f.bundle, "comment.create-pending")); !os.IsNotExist(err) {
		t.Fatal("pending create not cleared")
	}
	if _, err := os.Stat(filepath.Join(f.gitDir, bitbucketWriteRequestFile)); !os.IsNotExist(err) {
		t.Fatal("carried-out request left behind")
	}
	if _, err := os.Stat(reviewReceiptPath(f.c.Collection, "bitbucket", "acme/alpha", "7", f.m.HeadSHA, "900", "pass")); err != nil {
		t.Fatal("review receipt not written")
	}
	rows := readInlineRecords(f.bundle)
	if len(rows) != 1 || rows[0].ID != "901" {
		t.Fatalf("inline records = %+v", rows)
	}
}

func TestBrokeredReviewPostAsksForTheRestAfterAStoppedRun(t *testing.T) {
	f := newBrokeredFixture(t)
	f.c.postReviewBrokered(f.m, f.bundle, "", brokeredSummary)
	first := f.request(t)
	f.receipt(t, first.IntentID, false, "done:900", "failed")
	if _, err := f.c.postReviewBrokered(f.m, f.bundle, "", brokeredSummary); !errors.Is(err, ErrBitbucketAwaitingConfirmation) {
		t.Fatalf("err = %v", err)
	}
	second := f.request(t)
	if second.IntentID == first.IntentID || len(second.Operations) != 2 || second.Operations[0].Op != "comment.update" || second.Operations[0].Comment != 900 || second.Operations[1].Inline == nil {
		t.Fatalf("follow-up request = %+v", second)
	}
	if _, err := os.Stat(reviewReceiptPath(f.c.Collection, "bitbucket", "acme/alpha", "7", f.m.HeadSHA, "900", "pass")); !os.IsNotExist(err) {
		t.Fatal("an incomplete post must not earn the review receipt")
	}
}

func TestBrokeredReviewPostKeepsUnknownOutcomesPending(t *testing.T) {
	f := newBrokeredFixture(t)
	f.c.postReviewBrokered(f.m, f.bundle, "", brokeredSummary)
	request := f.request(t)
	f.receipt(t, request.IntentID, false, "unknown", "skipped")
	_, err := f.c.postReviewBrokered(f.m, f.bundle, "", brokeredSummary)
	if err == nil || !strings.Contains(err.Error(), "unknown outcome") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.bundle, "comment.create-pending")); err != nil {
		t.Fatal("an unknown create must stay pending")
	}
	// A receipt for another pull request is refused.
	f2 := newBrokeredFixture(t)
	f2.c.postReviewBrokered(f2.m, f2.bundle, "", brokeredSummary)
	other := f2.request(t)
	data, _ := json.Marshal(map[string]any{"version": 1, "intentId": other.IntentID, "owner": "acme", "repo": "alpha", "pr": 8, "completed": true, "results": []any{}})
	os.MkdirAll(filepath.Join(f2.gitDir, bitbucketWriteReceiptsDir), 0o700)
	os.WriteFile(filepath.Join(f2.gitDir, bitbucketWriteReceiptsDir, other.IntentID+".receipt.json"), data, 0o600)
	if _, err := f2.c.postReviewBrokered(f2.m, f2.bundle, "", brokeredSummary); err == nil || !strings.Contains(err.Error(), "another pull request") {
		t.Fatalf("foreign receipt = %v", err)
	}
}
