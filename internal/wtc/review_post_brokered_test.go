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
	// The person confirms the rest: the next post applies both receipts.
	f.receipt(t, second.IntentID, true, "done:900", "done:902")
	result, err := f.c.postReviewBrokered(f.m, f.bundle, "", brokeredSummary)
	if err != nil || result.CommentID != "900" || result.InlinePosted != 1 {
		t.Fatalf("after the second receipt = %+v, %v", result, err)
	}
	if _, err := os.Stat(reviewReceiptPath(f.c.Collection, "bitbucket", "acme/alpha", "7", f.m.HeadSHA, "900", "pass")); err != nil {
		t.Fatal("a completed second attempt must earn the review receipt")
	}
	if rows := readInlineRecords(f.bundle); len(rows) != 1 || rows[0].ID != "902" {
		t.Fatalf("inline records = %+v", rows)
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

func TestBrokeredResolveAndReadyFollowTheirReceipts(t *testing.T) {
	f := newBrokeredFixture(t)
	rows := []ReviewInlineRecord{{Key: "k1", Concern: "correctness", File: "a.go", Line: 3, ID: "901"}, {Key: "k2", File: "b.go", Line: 1, ID: "902", Resolved: true}}
	if err := writeInlineRecords(f.bundle, rows); err != nil {
		t.Fatal(err)
	}
	_, err := f.c.resolveReviewBrokered(f.m, f.bundle, rows, ReviewResolveOptions{Reply: "Fixed in the next commit."})
	if !errors.Is(err, ErrBitbucketAwaitingConfirmation) {
		t.Fatalf("err = %v", err)
	}
	request := f.request(t)
	if len(request.Operations) != 2 || request.Operations[0].Parent != 901 || request.Operations[1].Op != "comment.resolve" || request.Operations[1].Comment != 901 {
		t.Fatalf("request = %+v", request)
	}
	f.receipt(t, request.IntentID, true, "done:950", "done:901")
	result, err := f.c.resolveReviewBrokered(f.m, f.bundle, readInlineRecords(f.bundle), ReviewResolveOptions{Reply: "Fixed in the next commit."})
	if err != nil || result.Resolved != 1 || !readInlineRecords(f.bundle)[0].Resolved {
		t.Fatalf("resolve = %+v, %v", result, err)
	}
	if strings.Contains(f.h.read(t, f.h.log), "comment resolve") {
		t.Fatal("a gateway resolve wrote directly")
	}

	// Ready: asked once, done from its receipt.
	if err := BitbucketMarkReady("", f.m.RepoDir, "acme/alpha", "7", "aaaaaaaaaaaa"); !errors.Is(err, ErrBitbucketAwaitingConfirmation) {
		t.Fatalf("ready = %v", err)
	}
	ready := f.request(t)
	if len(ready.Operations) != 1 || ready.Operations[0].Op != "pr.ready" || ready.Head != "aaaaaaaaaaaa" {
		t.Fatalf("ready request = %+v", ready)
	}
	f.receipt(t, ready.IntentID, true, "done")
	if err := BitbucketMarkReady("", f.m.RepoDir, "acme/alpha", "7", "aaaaaaaaaaaa"); err != nil {
		t.Fatalf("ready after confirmation = %v", err)
	}
	if strings.Contains(f.h.read(t, f.h.log), "pull-requests update") {
		t.Fatal("a gateway ready wrote directly")
	}
}

func TestBrokeredWritesStartedWithoutReceiptAreReconciledNotAskedAgain(t *testing.T) {
	f := newBrokeredFixture(t)
	f.c.postReviewBrokered(f.m, f.bundle, "", brokeredSummary)
	request := f.request(t)
	dir := filepath.Join(f.gitDir, bitbucketWriteReceiptsDir)
	os.MkdirAll(dir, 0o700)
	os.WriteFile(filepath.Join(dir, request.IntentID+".claim"), []byte("2026-11-23T00:00:00Z\n"), 0o600)
	_, err := f.c.postReviewBrokered(f.m, f.bundle, "", brokeredSummary)
	if err == nil || errors.Is(err, ErrBitbucketAwaitingConfirmation) || !strings.Contains(err.Error(), "outcome is unknown") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.bundle, "comment.create-pending")); err != nil {
		t.Fatal("the summary create must stay pending")
	}
}

// One review uses one client selection: the collection's control root.
func TestBitbucketSelectionFollowsTheControlRootEverywhere(t *testing.T) {
	h := newNoBBHost(t)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "taba.env"), []byte("TABA_BITBUCKET_TRANSPORT=bb\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Review reads given the collection's control root use its selection.
	if got := reviewRepositoryAudience(root, t.TempDir(), "bitbucket", "acme/alpha"); got != "unknown" {
		t.Fatalf("audience = %q", got)
	}
	if strings.Contains(h.read(t, h.log), "repo get") {
		t.Fatal("a review read ignored the control root's bb selection and ran twg")
	}
	if !strings.Contains(h.read(t, h.networkLog), "network-tool:bb") {
		t.Fatal("the review read did not use the selected bb client")
	}
	if err := os.WriteFile(h.networkLog, nil, 0o600); err != nil { // the bb launch above was intended
		t.Fatal(err)
	}
	// Writes find the control root through the legacy alias as well.
	unsetEnv(t, "BB_API_USER", "BB_API_PASS", "BB_USERNAME", "BB_API_TOKEN", "BITBUCKET_PUBLISH", "TABA_CONFIG_ROOT")
	t.Setenv("WTC_CONFIG_ROOT", root)
	if route, err := selectedBitbucketWrite("", true); err != nil || route != bitbucketWriteBB {
		t.Fatalf("write route = %s, %v", route, err)
	}
	// An unreadable or unsupported selection is a client refusal, not transient.
	for _, err := range []error{
		&bitbucketReadError{cause: bitbucketSelectionCause(errBitbucketClientDefaults, "taba.env")},
		&bitbucketReadError{cause: bitbucketSelectionCause(errBitbucketClientSelection, "environment")},
	} {
		if refusal := ClassifyBitbucketRefusal(err, ""); refusal.Cause != RefusalClient || !refusal.Access() {
			t.Fatalf("%v -> %+v", err, refusal)
		}
	}
}

func TestBrokeredReadyAndResolveAskAgainAfterAFailedReceipt(t *testing.T) {
	f := newBrokeredFixture(t)
	// Ready: refused once, asked again under the next intent, then done.
	if err := BitbucketMarkReady("", f.m.RepoDir, "acme/alpha", "7", "aaaaaaaaaaaa"); !errors.Is(err, ErrBitbucketAwaitingConfirmation) {
		t.Fatalf("ready = %v", err)
	}
	first := f.request(t)
	f.receipt(t, first.IntentID, false, "failed")
	if err := BitbucketMarkReady("", f.m.RepoDir, "acme/alpha", "7", "aaaaaaaaaaaa"); !errors.Is(err, ErrBitbucketAwaitingConfirmation) {
		t.Fatalf("ready after a failed receipt = %v", err)
	}
	second := f.request(t)
	if second.IntentID == first.IntentID {
		t.Fatal("a failed ready must be asked again under a new intent")
	}
	f.receipt(t, second.IntentID, true, "done")
	if err := BitbucketMarkReady("", f.m.RepoDir, "acme/alpha", "7", "aaaaaaaaaaaa"); err != nil {
		t.Fatalf("ready after confirmation = %v", err)
	}

	// Resolve: one of two threads failed; only that one is asked for again.
	rows := []ReviewInlineRecord{{Key: "k1", File: "a.go", Line: 3, ID: "901"}, {Key: "k2", File: "b.go", Line: 4, ID: "902"}}
	if err := writeInlineRecords(f.bundle, rows); err != nil {
		t.Fatal(err)
	}
	if _, err := f.c.resolveReviewBrokered(f.m, f.bundle, readInlineRecords(f.bundle), ReviewResolveOptions{}); !errors.Is(err, ErrBitbucketAwaitingConfirmation) {
		t.Fatalf("resolve = %v", err)
	}
	r1 := f.request(t)
	if len(r1.Operations) != 2 {
		t.Fatalf("first resolve request = %+v", r1)
	}
	f.receipt(t, r1.IntentID, false, "done:901", "failed")
	if _, err := f.c.resolveReviewBrokered(f.m, f.bundle, readInlineRecords(f.bundle), ReviewResolveOptions{}); !errors.Is(err, ErrBitbucketAwaitingConfirmation) {
		t.Fatalf("resolve after a partial receipt = %v", err)
	}
	r2 := f.request(t)
	if r2.IntentID == r1.IntentID || len(r2.Operations) != 1 || r2.Operations[0].Comment != 902 {
		t.Fatalf("follow-up resolve request = %+v", r2)
	}
	if got := readInlineRecords(f.bundle); !got[0].Resolved || got[1].Resolved {
		t.Fatalf("rows after the partial receipt = %+v", got)
	}
	f.receipt(t, r2.IntentID, true, "done:902")
	result, err := f.c.resolveReviewBrokered(f.m, f.bundle, readInlineRecords(f.bundle), ReviewResolveOptions{})
	if err != nil || result.Resolved != 1 || !readInlineRecords(f.bundle)[1].Resolved {
		t.Fatalf("final resolve = %+v, %v", result, err)
	}
	// An unknown outcome stops instead of asking again.
	g := newBrokeredFixture(t)
	BitbucketMarkReady("", g.m.RepoDir, "acme/alpha", "7", "aaaaaaaaaaaa")
	g.receipt(t, g.request(t).IntentID, false, "unknown")
	if err := BitbucketMarkReady("", g.m.RepoDir, "acme/alpha", "7", "aaaaaaaaaaaa"); err == nil || !strings.Contains(err.Error(), "unknown outcome") {
		t.Fatalf("ready after an unknown receipt = %v", err)
	}
}

func TestBrokeredResolveRepliesOnceAndRecordsOnlyAskedAttempts(t *testing.T) {
	f := newBrokeredFixture(t)
	rows := []ReviewInlineRecord{{Key: "k1", File: "a.go", Line: 3, ID: "901"}}
	if err := writeInlineRecords(f.bundle, rows); err != nil {
		t.Fatal(err)
	}
	opt := ReviewResolveOptions{Reply: "Fixed."}
	// Another request waits in the worktree: the ask is refused and nothing
	// is recorded, so a later run asks normally.
	other := brokeredRequest{Version: 1, IntentID: "taba-other-0000000000000000", Owner: "acme", Repo: "alpha", PR: 7, Head: "aaaaaaaaaaaa", Operations: []brokeredOperation{{Op: "pr.ready"}}}
	if err := writePrivateJSON(filepath.Join(f.gitDir, bitbucketWriteRequestFile), other); err != nil {
		t.Fatal(err)
	}
	if _, err := f.c.resolveReviewBrokered(f.m, f.bundle, readInlineRecords(f.bundle), opt); err == nil || errors.Is(err, ErrBitbucketAwaitingConfirmation) {
		t.Fatalf("refused ask = %v", err)
	}
	if records, _ := filepath.Glob(filepath.Join(f.bundle, "bitbucket-resolve.*.json")); len(records) != 0 {
		t.Fatalf("a refused ask was recorded: %v", records)
	}
	os.Remove(filepath.Join(f.gitDir, bitbucketWriteRequestFile))
	if _, err := f.c.resolveReviewBrokered(f.m, f.bundle, readInlineRecords(f.bundle), opt); !errors.Is(err, ErrBitbucketAwaitingConfirmation) {
		t.Fatalf("ask = %v", err)
	}
	first := f.request(t)
	if len(first.Operations) != 2 || first.Operations[0].Parent != 901 {
		t.Fatalf("first request = %+v", first)
	}
	// The reply was made, the resolve failed: the retry asks only to resolve.
	f.receipt(t, first.IntentID, false, "done:950", "failed")
	if _, err := f.c.resolveReviewBrokered(f.m, f.bundle, readInlineRecords(f.bundle), opt); !errors.Is(err, ErrBitbucketAwaitingConfirmation) {
		t.Fatalf("retry = %v", err)
	}
	second := f.request(t)
	if second.IntentID == first.IntentID || len(second.Operations) != 1 || second.Operations[0].Op != "comment.resolve" {
		t.Fatalf("retry request must not repeat the reply: %+v", second)
	}
}

func TestBrokeredPostClaimWithoutRequestAndLaterRequestsSurvive(t *testing.T) {
	f := newBrokeredFixture(t)
	f.c.postReviewBrokered(f.m, f.bundle, "", brokeredSummary)
	request := f.request(t)
	// The plugin claimed the intent and the request file is gone: unknown outcome.
	dir := filepath.Join(f.gitDir, bitbucketWriteReceiptsDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, request.IntentID+".claim"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(f.gitDir, bitbucketWriteRequestFile)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.c.postReviewBrokered(f.m, f.bundle, "", brokeredSummary); err == nil || !strings.Contains(err.Error(), "outcome is unknown") {
		t.Fatalf("claim without request = %v", err)
	}
	// Reconciling one intent never removes another intent's pending request.
	later := brokeredRequest{Version: 1, IntentID: "taba-later-0000000000000000", Owner: "acme", Repo: "alpha", PR: 7, Head: "aaaaaaaaaaaa", Operations: []brokeredOperation{{Op: "pr.ready"}}}
	if err := writePrivateJSON(filepath.Join(f.gitDir, bitbucketWriteRequestFile), later); err != nil {
		t.Fatal(err)
	}
	removeBrokeredRequest(f.gitDir, request.IntentID)
	if got := f.request(t); got.IntentID != later.IntentID {
		t.Fatalf("a later intent's request was removed: %+v", got)
	}
}

func TestStoredReviewManifestUsesItsCollectionsControlRoot(t *testing.T) {
	c := newWorkspaceFixture(t)
	if got := (ReviewManifest{RepoDir: c.Harness}).bitbucketControlRoot(); got != c.ConfigRoot {
		t.Fatalf("control root from a stored manifest = %q, want %q", got, c.ConfigRoot)
	}
	if got := (ReviewManifest{RepoDir: c.Harness, ConfigRoot: "/explicit"}).bitbucketControlRoot(); got != "/explicit" {
		t.Fatalf("explicit control root = %q", got)
	}
}

// A resolve request the person discarded (no receipt, no claim, request
// file gone) was never started: the next run asks again.
func TestBrokeredResolveAsksAgainAfterADiscardedRequest(t *testing.T) {
	f := newBrokeredFixture(t)
	if err := writeInlineRecords(f.bundle, []ReviewInlineRecord{{Key: "k1", File: "a.go", Line: 3, ID: "901"}}); err != nil {
		t.Fatal(err)
	}
	opt := ReviewResolveOptions{Reply: "Fixed."}
	if _, err := f.c.resolveReviewBrokered(f.m, f.bundle, readInlineRecords(f.bundle), opt); !errors.Is(err, ErrBitbucketAwaitingConfirmation) {
		t.Fatalf("ask = %v", err)
	}
	first := f.request(t)
	if err := os.Remove(filepath.Join(f.gitDir, bitbucketWriteRequestFile)); err != nil { // discarded
		t.Fatal(err)
	}
	if _, err := f.c.resolveReviewBrokered(f.m, f.bundle, readInlineRecords(f.bundle), opt); !errors.Is(err, ErrBitbucketAwaitingConfirmation) {
		t.Fatalf("after a discarded request = %v, want a fresh ask", err)
	}
	second := f.request(t)
	if second.IntentID == first.IntentID || len(second.Operations) != 2 {
		t.Fatalf("fresh ask = %+v", second)
	}
	// A claimed request that is gone is never asked again: its outcome is unknown.
	dir := filepath.Join(f.gitDir, bitbucketWriteReceiptsDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, second.IntentID+".claim"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(f.gitDir, bitbucketWriteRequestFile)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.c.resolveReviewBrokered(f.m, f.bundle, readInlineRecords(f.bundle), opt); err == nil || !strings.Contains(err.Error(), "outcome is unknown") {
		t.Fatalf("claimed and gone = %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.gitDir, bitbucketWriteRequestFile)); !os.IsNotExist(err) {
		t.Fatal("a claimed intent must not be asked again")
	}
}

// A receipt found for the attempt being asked (its record was lost after
// confirmation) is applied like any other: unknown stops, failed threads
// are asked for again, and only nothing left to do is success.
func TestBrokeredResolveReceiptFoundWhileAskingIsNotSuccessUnlessComplete(t *testing.T) {
	for _, tc := range []struct {
		name     string
		statuses []string
		want     string // "unknown", "ask" or "done"
	}{
		{"failed thread", []string{"done:901", "failed"}, "ask"},
		{"unknown thread", []string{"done:901", "unknown"}, "unknown"},
		{"all done", []string{"done:901", "done:902"}, "done"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newBrokeredFixture(t)
			if err := writeInlineRecords(f.bundle, []ReviewInlineRecord{{Key: "k1", File: "a.go", Line: 3, ID: "901"}, {Key: "k2", File: "b.go", Line: 4, ID: "902"}}); err != nil {
				t.Fatal(err)
			}
			if _, err := f.c.resolveReviewBrokered(f.m, f.bundle, readInlineRecords(f.bundle), ReviewResolveOptions{}); !errors.Is(err, ErrBitbucketAwaitingConfirmation) {
				t.Fatalf("ask = %v", err)
			}
			first := f.request(t)
			// The record of the attempt is lost; the plugin left a receipt.
			records, _ := filepath.Glob(filepath.Join(f.bundle, "bitbucket-resolve.*.json"))
			for _, r := range records {
				os.Remove(r)
			}
			f.receipt(t, first.IntentID, tc.want == "done", tc.statuses...)
			_, err := f.c.resolveReviewBrokered(f.m, f.bundle, readInlineRecords(f.bundle), ReviewResolveOptions{})
			switch tc.want {
			case "done":
				if err != nil {
					t.Fatalf("complete receipt = %v", err)
				}
			case "unknown":
				if err == nil || !strings.Contains(err.Error(), "unknown outcome") {
					t.Fatalf("unknown receipt = %v", err)
				}
			case "ask":
				if !errors.Is(err, ErrBitbucketAwaitingConfirmation) {
					t.Fatalf("failed receipt = %v, want a new ask", err)
				}
				if next := f.request(t); next.IntentID == first.IntentID || len(next.Operations) != 1 || next.Operations[0].Comment != 902 {
					t.Fatalf("new ask = %+v", next)
				}
			}
			if rows := readInlineRecords(f.bundle); !rows[0].Resolved {
				t.Fatalf("done thread not recorded: %+v", rows)
			}
		})
	}
}

// Repeated failed resolves after a successful reply: the reply is asked for
// once; every retry asks only to resolve, under a new intent each time.
func TestBrokeredResolveRepeatedRetriesNeverRepeatTheReply(t *testing.T) {
	f := newBrokeredFixture(t)
	if err := writeInlineRecords(f.bundle, []ReviewInlineRecord{{Key: "k1", File: "a.go", Line: 3, ID: "901"}}); err != nil {
		t.Fatal(err)
	}
	opt := ReviewResolveOptions{Reply: "Fixed."}
	seen := map[string]bool{}
	replies := 0
	for round, statuses := range [][]string{{"done:950", "failed"}, {"failed"}, {"done:901"}} {
		if _, err := f.c.resolveReviewBrokered(f.m, f.bundle, readInlineRecords(f.bundle), opt); !errors.Is(err, ErrBitbucketAwaitingConfirmation) {
			t.Fatalf("round %d ask = %v", round, err)
		}
		request := f.request(t)
		if seen[request.IntentID] {
			t.Fatalf("round %d reused intent %s", round, request.IntentID)
		}
		seen[request.IntentID] = true
		for _, op := range request.Operations {
			if op.Op == "comment.create" {
				replies++
			}
		}
		if len(request.Operations) != len(statuses) {
			t.Fatalf("round %d request = %+v", round, request)
		}
		f.receipt(t, request.IntentID, statuses[len(statuses)-1] != "failed", statuses...)
	}
	if replies != 1 {
		t.Fatalf("the reply was asked for %d times", replies)
	}
	result, err := f.c.resolveReviewBrokered(f.m, f.bundle, readInlineRecords(f.bundle), opt)
	if err != nil || !readInlineRecords(f.bundle)[0].Resolved {
		t.Fatalf("final = %+v, %v", result, err)
	}
}
