package wtc

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Joint contract test (#471): taba writes a gateway write request, the merged
// openclaw-bitbucket pr-writes module (pinned copy, see testdata/…/SOURCE)
// asks, confirms and writes its receipt against an injected fake Bitbucket,
// and taba reconciles that receipt. No bb, no network, no real credential:
// the driver poisons fetch and the package network guard stays in place.

const jointPluginDir = "testdata/openclaw-bitbucket-a0d4305"

type jointRun struct {
	Asked struct {
		OK     bool   `json:"ok"`
		Error  string `json:"error"`
		Result struct {
			Status string `json:"status"`
		} `json:"result"`
	} `json:"asked"`
	Confirmed *struct {
		OK     bool `json:"ok"`
		Result struct {
			Status  string          `json:"status"`
			Receipt json.RawMessage `json:"receipt"`
		} `json:"result"`
	} `json:"confirmed"`
	Calls []struct {
		Method string         `json:"method"`
		Route  string         `json:"route"`
		Body   map[string]any `json:"body"`
	} `json:"calls"`
	PR struct {
		Draft bool `json:"draft"`
	} `json:"pr"`
	Comments []struct {
		ID       int  `json:"id"`
		Resolved bool `json:"resolved"`
		Parent   *struct {
			ID int `json:"id"`
		} `json:"parent"`
	} `json:"comments"`
}

// jointEnv is one fixture's plugin side: node, the plugin's state directory
// and the fake Bitbucket's state, kept across the plugin runs of a test.
type jointEnv struct {
	node, state, fake string
}

// jointNode finds node before a synthetic host narrows PATH.
func jointNode(t *testing.T) string {
	t.Helper()
	if node := os.Getenv("WTC_TEST_NODE"); node != "" {
		return node
	}
	node, err := exec.LookPath("node")
	if err != nil {
		if os.Getenv("WTC_TEST_REQUIRE_RUNTIME") == "1" {
			t.Fatal("the joint contract test needs node: set WTC_TEST_NODE")
		}
		t.Skip("the joint contract test needs node (CI sets WTC_TEST_NODE)")
	}
	return node
}

// jointPinned checks that the pinned plugin files are the ones SOURCE names.
func jointPinned(t *testing.T) {
	t.Helper()
	source, err := os.ReadFile(filepath.Join(jointPluginDir, "SOURCE"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(source), "commit: a0d4305ead109681ea79bd0f6815d1a26597d088") {
		t.Fatal("SOURCE does not name the pinned plugin commit")
	}
	for _, line := range strings.Split(string(source), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || len(fields[0]) != 64 {
			continue
		}
		data, err := os.ReadFile(filepath.Join(jointPluginDir, fields[1]))
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != fields[0] {
			t.Fatalf("pinned %s does not match its recorded sha256", fields[1])
		}
		t.Logf("plugin %s sha256 %s", fields[1], fields[0])
	}
}

func jointPlugin(t *testing.T, env jointEnv, f brokeredFixture, fail string) jointRun {
	t.Helper()
	dir, err := filepath.Abs(jointPluginDir)
	if err != nil {
		t.Fatal(err)
	}
	driver, _ := filepath.Abs("testdata/joint-contract-driver.mjs")
	cmd := exec.Command(env.node, driver, dir, env.state, f.m.RepoDir, env.fake, fail)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("plugin driver: %v\n%s", err, stderr.String())
	}
	var run jointRun
	if err := json.Unmarshal(out, &run); err != nil {
		t.Fatalf("plugin driver output: %v\n%s", err, out)
	}
	if strings.Contains(string(out), "synthetic-joint-token") {
		t.Fatal("the plugin answer or its calls reveal the login token")
	}
	return run
}

func jointFixture(t *testing.T, node string) (brokeredFixture, jointEnv) {
	t.Helper()
	jointPinned(t)
	env := jointEnv{node: node, state: t.TempDir()}
	env.fake = filepath.Join(t.TempDir(), "fake-bitbucket.json")
	f := newBrokeredFixture(t)
	if out, err := exec.Command("git", "-C", f.m.RepoDir, "remote", "add", "origin", "https://bitbucket.org/acme/alpha.git").CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	return f, env
}

func TestJointContractReviewPostResolveAndReadyWithoutBB(t *testing.T) {
	f, node := jointFixture(t, jointNode(t))

	// Review post: summary + one inline comment, confirmed by the plugin.
	if _, err := f.c.postReviewBrokered(f.m, f.bundle, "", brokeredSummary); !errors.Is(err, ErrBitbucketAwaitingConfirmation) {
		t.Fatalf("taba ask = %v", err)
	}
	run := jointPlugin(t, node, f, "")
	if !run.Asked.OK || run.Confirmed == nil || run.Confirmed.Result.Status != "done" {
		t.Fatalf("plugin did not carry out taba's request: %+v", run)
	}
	posts := 0
	for _, c := range run.Calls {
		if c.Method == "POST" && strings.HasSuffix(c.Route, "/comments") {
			posts++
		}
	}
	if posts != 2 {
		t.Fatalf("plugin calls = %+v", run.Calls)
	}
	result, err := f.c.postReviewBrokered(f.m, f.bundle, "", brokeredSummary)
	if err != nil || result.CommentID != "950" || result.InlinePosted != 1 {
		t.Fatalf("taba reconcile of the plugin receipt = %+v, %v", result, err)
	}
	rows := readInlineRecords(f.bundle)
	if len(rows) != 1 || rows[0].ID != "951" {
		t.Fatalf("inline record = %+v", rows)
	}
	// What the plugin sent is what taba planned: the inline anchor and text.
	anchored := false
	for _, c := range run.Calls {
		inline, _ := c.Body["inline"].(map[string]any)
		if c.Method == "POST" && inline != nil {
			anchored = inline["path"] == rows[0].File && inline["to"] == float64(rows[0].Line)
		}
	}
	if !anchored {
		t.Fatalf("the plugin's inline comment is not anchored where taba planned %s:%d: %+v", rows[0].File, rows[0].Line, run.Calls)
	}

	// Resolve with a reply: the plugin replies to and resolves the thread.
	if _, err := f.c.resolveReviewBrokered(f.m, f.bundle, rows, ReviewResolveOptions{Reply: "Fixed."}); !errors.Is(err, ErrBitbucketAwaitingConfirmation) {
		t.Fatalf("taba resolve ask = %v", err)
	}
	run = jointPlugin(t, node, f, "")
	if run.Confirmed == nil || run.Confirmed.Result.Status != "done" {
		t.Fatalf("plugin resolve: %+v", run)
	}
	replied, resolved := false, false
	for _, c := range run.Comments {
		replied = replied || c.Parent != nil && c.Parent.ID == 951
		resolved = resolved || c.ID == 951 && c.Resolved
	}
	if !replied || !resolved {
		t.Fatalf("Bitbucket after the plugin's resolve: %+v", run.Comments)
	}
	if got, err := f.c.resolveReviewBrokered(f.m, f.bundle, readInlineRecords(f.bundle), ReviewResolveOptions{Reply: "Fixed."}); err != nil || got.Resolved != 1 || !readInlineRecords(f.bundle)[0].Resolved {
		t.Fatalf("taba resolve reconcile = %+v, %v", got, err)
	}

	// Ready: un-drafted by the plugin, recorded by taba.
	if err := BitbucketMarkReady("", f.m.RepoDir, "acme/alpha", "7", "aaaaaaaaaaaa"); !errors.Is(err, ErrBitbucketAwaitingConfirmation) {
		t.Fatalf("taba ready ask = %v", err)
	}
	run = jointPlugin(t, node, f, "")
	if run.Confirmed == nil || run.Confirmed.Result.Status != "done" || run.PR.Draft {
		t.Fatalf("plugin ready: %+v", run)
	}
	if err := BitbucketMarkReady("", f.m.RepoDir, "acme/alpha", "7", "aaaaaaaaaaaa"); err != nil {
		t.Fatalf("taba ready reconcile = %v", err)
	}
}

func TestJointContractFailedAndUnknownWritesStayHonest(t *testing.T) {
	// A refused resolve: the plugin records it failed; taba asks again for
	// that thread only and never repeats the reply that was made.
	node := jointNode(t)
	f, env := jointFixture(t, node)
	if err := writeInlineRecords(f.bundle, []ReviewInlineRecord{{Key: "k1", File: "a.go", Line: 3, ID: "901"}}); err != nil {
		t.Fatal(err)
	}
	opt := ReviewResolveOptions{Reply: "Fixed."}
	if _, err := f.c.resolveReviewBrokered(f.m, f.bundle, readInlineRecords(f.bundle), opt); !errors.Is(err, ErrBitbucketAwaitingConfirmation) {
		t.Fatalf("ask = %v", err)
	}
	if run := jointPlugin(t, env, f, "POST /comments/901/resolve 403"); run.Confirmed == nil || run.Confirmed.Result.Status != "stopped" {
		t.Fatalf("plugin with a refused resolve: %+v", run)
	}
	if _, err := f.c.resolveReviewBrokered(f.m, f.bundle, readInlineRecords(f.bundle), opt); !errors.Is(err, ErrBitbucketAwaitingConfirmation) {
		t.Fatalf("retry = %v", err)
	}
	if retry := f.request(t); len(retry.Operations) != 1 || retry.Operations[0].Op != "comment.resolve" {
		t.Fatalf("retry must only resolve: %+v", retry)
	}

	// An unanswered write: the plugin records it unknown; taba stops and
	// never asks again.
	g, genv := jointFixture(t, node)
	if _, err := g.c.postReviewBrokered(g.m, g.bundle, "", brokeredSummary); !errors.Is(err, ErrBitbucketAwaitingConfirmation) {
		t.Fatalf("ask = %v", err)
	}
	if run := jointPlugin(t, genv, g, "POST /comments drop"); run.Confirmed == nil || run.Confirmed.Result.Status != "stopped" {
		t.Fatalf("plugin with an unanswered write: %+v", run)
	}
	_, err := g.c.postReviewBrokered(g.m, g.bundle, "", brokeredSummary)
	if err == nil || !strings.Contains(err.Error(), "unknown outcome") {
		t.Fatalf("taba after an unknown write = %v", err)
	}
	if _, err := os.Stat(filepath.Join(g.bundle, "comment.create-pending")); err != nil {
		t.Fatal("the summary create must stay pending")
	}
}
