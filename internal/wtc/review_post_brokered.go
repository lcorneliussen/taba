package wtc

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Brokered Bitbucket review posts (issue #471).
//
// On a gateway that keeps personal logins away from commands
// (BITBUCKET_PUBLISH=gateway), a review post cannot write. It leaves one
// request for the Bitbucket plugin's confirmed pull request writes
// (openclaw-bitbucket pr-writes) in the worktree's git directory: the final
// summary (create, or update of the saved comment) and every inline comment
// not posted yet. The person sees and confirms it in the Control UI; the
// plugin writes with their login and leaves a receipt. The next review post
// reads that receipt and completes what a direct post would have recorded:
// comment.id, the inline records and the review receipt.
//
// The intent id is derived from the bundle and its head, so asking again
// never creates a second request for the same review, and the plugin carries
// an intent out at most once. A summary create keeps comment.create-pending
// until the receipt names its id; an unknown outcome stays pending for
// reconciliation, and nothing is retried.

const (
	bitbucketWriteRequestFile = "bitbucket-pr-writes.json"
	bitbucketWriteReceiptsDir = "bitbucket-pr-writes"
	brokeredSidecarPrefix     = "bitbucket-pr-writes."
)

// errBrokeredIncomplete marks a receipt whose run stopped early: its
// recorded ids are applied and the rest can be asked for again.
var errBrokeredIncomplete = errors.New("incomplete brokered writes")

// ErrBitbucketAwaitingConfirmation reports a brokered write waiting for the
// person. It is not a failure of the review.
var ErrBitbucketAwaitingConfirmation = errors.New("Bitbucket writes await confirmation")

type brokeredRequest struct {
	Version    int                 `json:"version"`
	IntentID   string              `json:"intentId"`
	Owner      string              `json:"owner"`
	Repo       string              `json:"repo"`
	PR         int                 `json:"pr"`
	Head       string              `json:"head"`
	Operations []brokeredOperation `json:"operations"`
}

type brokeredOperation struct {
	Op      string          `json:"op"`
	Text    string          `json:"text,omitempty"`
	Comment int64           `json:"comment,omitempty"`
	Inline  *brokeredInline `json:"inline,omitempty"`
	Parent  int64           `json:"parent,omitempty"`
}

type brokeredInline struct {
	Path string `json:"path"`
	To   int    `json:"to"`
}

// brokeredSidecar maps each operation to what it is for, so the receipt can
// be applied without trusting anything but ids from it.
type brokeredSidecar struct {
	IntentID string               `json:"intentId"`
	Verdict  string               `json:"verdict"`
	Purposes []brokeredPurpose    `json:"purposes"`
	Inline   []ReviewInlineRecord `json:"inline"`
}

type brokeredPurpose struct {
	Kind string `json:"kind"` // "summary" or "inline"
	Key  string `json:"key,omitempty"`
}

type brokeredReceipt struct {
	Version   int    `json:"version"`
	IntentID  string `json:"intentId"`
	Owner     string `json:"owner"`
	Repo      string `json:"repo"`
	PR        int    `json:"pr"`
	Completed bool   `json:"completed"`
	Results   []struct {
		Index   int             `json:"index"`
		Op      string          `json:"op"`
		Status  string          `json:"status"`
		Comment json.RawMessage `json:"comment"`
	} `json:"results"`
}

// brokeredIntentID names attempt n of a review's brokered post. A stopped
// attempt keeps its id (the plugin carries an intent out once); the rest is
// asked for under the next one.
// brokeredSidecarPath is the local record of one attempt, kept per intent so
// a later attempt never replaces the record an earlier receipt needs.
func brokeredSidecarPath(bundle, intent string) string {
	return filepath.Join(bundle, brokeredSidecarPrefix+intent+".intent.json")
}

func brokeredIntentID(m ReviewManifest, bundle string, attempt int) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{"review-post-v1", m.Slug, m.PR, m.HeadSHA, strconv.Itoa(m.Round), bundle, strconv.Itoa(attempt)}, "\x00")))
	return "taba-review-" + hex.EncodeToString(sum[:])[:24]
}

// worktreeGitDir is the git directory of a worktree, read without running
// its hooks or fsmonitor.
func worktreeGitDir(worktree string) (string, error) {
	out, err := exec.Command("git", "-C", worktree, "-c", "core.fsmonitor=false", "rev-parse", "--absolute-git-dir").Output()
	if err != nil {
		return "", fmt.Errorf("cannot find the worktree's git directory: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// postReviewBrokered leaves or completes a brokered review post. cid is the
// saved summary comment id, if any; finalBody is the summary to publish.
func (c *Context) postReviewBrokered(m ReviewManifest, bundle, cid, finalBody string) (ReviewPostResult, error) {
	gitDir, err := worktreeGitDir(m.RepoDir)
	if err != nil {
		return ReviewPostResult{}, err
	}
	// Apply every receipt of earlier attempts first; a complete one ends
	// the post.
	intent := ""
	for attempt := 0; attempt < 20; attempt++ {
		candidate := brokeredIntentID(m, bundle, attempt)
		raw, err := os.ReadFile(filepath.Join(gitDir, bitbucketWriteReceiptsDir, candidate+".receipt.json"))
		if err != nil {
			intent = candidate
			break
		}
		result, err := c.reconcileBrokeredReview(m, bundle, gitDir, candidate, raw)
		if err == nil || !errors.Is(err, errBrokeredIncomplete) {
			return result, err
		}
		// Recorded ids are kept; ask for what is still missing.
		if result.CommentID != "" {
			cid = result.CommentID
		}
	}
	if intent == "" {
		return ReviewPostResult{}, fmt.Errorf("too many incomplete Bitbucket write attempts for this review; inspect the pull request")
	}
	requestPath := filepath.Join(gitDir, bitbucketWriteRequestFile)
	if raw, err := os.ReadFile(requestPath); err == nil {
		var pending brokeredRequest
		if err := brokeredStarted(gitDir, intent); err != nil {
			return ReviewPostResult{URL: m.URL}, err
		}
		if json.Unmarshal(raw, &pending) == nil && pending.IntentID == intent {
			return ReviewPostResult{URL: m.URL}, fmt.Errorf("%w: the review of PR #%s is waiting for the person to confirm it in the Control UI; post again afterwards", ErrBitbucketAwaitingConfirmation, m.PR)
		}
		return ReviewPostResult{}, fmt.Errorf("another Bitbucket write request in this worktree awaits confirmation; let the person confirm or discard it first")
	}

	workspace, repo, err := splitBitbucketSlug(m.Slug)
	if err != nil {
		return ReviewPostResult{}, err
	}
	pr, err := strconv.Atoi(m.PR)
	if err != nil {
		return ReviewPostResult{}, fmt.Errorf("invalid PR number")
	}
	request := brokeredRequest{Version: 1, IntentID: intent, Owner: workspace, Repo: repo, PR: pr, Head: m.HeadSHA}
	sidecar := brokeredSidecar{IntentID: intent}
	if match := reviewStatusLine.FindStringSubmatch(finalBody); match != nil {
		sidecar.Verdict = match[2]
	}
	if cid == "" {
		request.Operations = append(request.Operations, brokeredOperation{Op: "comment.create", Text: finalBody})
	} else {
		id, err := strconv.ParseInt(cid, 10, 64)
		if err != nil {
			return ReviewPostResult{}, fmt.Errorf("invalid saved comment id")
		}
		request.Operations = append(request.Operations, brokeredOperation{Op: "comment.update", Comment: id, Text: finalBody})
	}
	sidecar.Purposes = append(sidecar.Purposes, brokeredPurpose{Kind: "summary"})
	var queue []reviewInlineWrite
	_, failed, _ := postReviewInlineWith(m, bundle, &queue)
	if failed != 0 {
		return ReviewPostResult{}, fmt.Errorf("could not prepare inline comments; review remains pending")
	}
	for _, write := range queue {
		request.Operations = append(request.Operations, brokeredOperation{Op: "comment.create", Text: write.Body, Inline: &brokeredInline{Path: write.Row.File, To: write.Row.Line}})
		sidecar.Purposes = append(sidecar.Purposes, brokeredPurpose{Kind: "inline", Key: write.Row.Key})
		sidecar.Inline = append(sidecar.Inline, write.Row)
	}
	if len(request.Operations) > 100 {
		return ReviewPostResult{}, fmt.Errorf("the review has more than 100 comments to write; a confirmed request holds at most 100")
	}
	if err := writePrivateJSON(brokeredSidecarPath(bundle, intent), sidecar); err != nil {
		return ReviewPostResult{}, err
	}
	if cid == "" {
		// The summary create is in flight from here on: a direct post must
		// not create another one.
		pending, err := os.OpenFile(filepath.Join(bundle, "comment.create-pending"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil && !os.IsExist(err) {
			return ReviewPostResult{}, err
		}
		if err == nil {
			pending.Close()
		}
	}
	if err := writePrivateJSON(requestPath, request); err != nil {
		return ReviewPostResult{}, err
	}
	return ReviewPostResult{URL: m.URL}, fmt.Errorf("%w: %d Bitbucket writes for PR #%s (summary and %d inline) wait for the person to confirm them in the Control UI; post again afterwards to record them", ErrBitbucketAwaitingConfirmation, len(request.Operations), m.PR, len(queue))
}

// reconcileBrokeredReview applies a receipt: only ids, statuses and the
// request's identity are read from it.
func (c *Context) reconcileBrokeredReview(m ReviewManifest, bundle, gitDir, intent string, raw []byte) (ReviewPostResult, error) {
	var receipt brokeredReceipt
	if err := json.Unmarshal(raw, &receipt); err != nil || receipt.Version != 1 || receipt.IntentID != intent {
		return ReviewPostResult{}, fmt.Errorf("the Bitbucket write receipt is not for this review; inspect it before posting again")
	}
	workspace, repo, _ := splitBitbucketSlug(m.Slug)
	if !strings.EqualFold(receipt.Owner, workspace) || !strings.EqualFold(receipt.Repo, repo) || strconv.Itoa(receipt.PR) != m.PR {
		return ReviewPostResult{}, fmt.Errorf("the Bitbucket write receipt names another pull request")
	}
	sideRaw, err := os.ReadFile(brokeredSidecarPath(bundle, intent))
	var sidecar brokeredSidecar
	if err != nil || json.Unmarshal(sideRaw, &sidecar) != nil || sidecar.IntentID != intent || len(receipt.Results) != len(sidecar.Purposes) {
		return ReviewPostResult{}, fmt.Errorf("the brokered review's local record is missing or does not match its receipt; reconcile by reading the pull request")
	}
	inline := map[string]ReviewInlineRecord{}
	for _, row := range sidecar.Inline {
		inline[row.Key] = row
	}
	rows := readInlineRecords(bundle)
	result := ReviewPostResult{URL: m.URL}
	unknown := 0
	for i, purpose := range sidecar.Purposes {
		entry := receipt.Results[i]
		if entry.Index != i {
			return ReviewPostResult{}, fmt.Errorf("the Bitbucket write receipt is out of order")
		}
		id := ""
		if entry.Status == "done" {
			id, err = reviewCommentIDText(entry.Comment)
			if err != nil || id == "" || !prNumber.MatchString(id) {
				unknown++
				continue
			}
		}
		switch {
		case entry.Status == "unknown":
			unknown++
		case purpose.Kind == "summary" && entry.Status == "done":
			result.CommentID = id
			if err := os.WriteFile(filepath.Join(bundle, "comment.id"), []byte(id+"\n"), 0644); err != nil {
				return result, err
			}
			if err := os.Remove(filepath.Join(bundle, "comment.create-pending")); err != nil && !os.IsNotExist(err) {
				return result, err
			}
		case purpose.Kind == "inline" && entry.Status == "done":
			row := inline[purpose.Key]
			row.ID = id
			if rows, err = saveInlineRecord(bundle, rows, row); err != nil {
				return result, err
			}
			result.InlinePosted++
		case purpose.Kind == "inline":
			result.InlineFailed++
		}
	}
	if unknown != 0 {
		return result, fmt.Errorf("%d confirmed Bitbucket writes have an unknown outcome; the review remains pending until they are reconciled by reading the pull request", unknown)
	}
	// The request has been carried out; asking again would refuse its intent.
	_ = os.Remove(filepath.Join(gitDir, bitbucketWriteRequestFile))
	if result.CommentID == "" {
		// An earlier attempt may have recorded the summary.
		if saved, err := os.ReadFile(filepath.Join(bundle, "comment.id")); err == nil {
			result.CommentID = strings.TrimSpace(string(saved))
		}
	}
	if !receipt.Completed || result.CommentID == "" || result.InlineFailed != 0 {
		return result, fmt.Errorf("%w: the confirmed Bitbucket writes stopped before completing; the review remains pending", errBrokeredIncomplete)
	}
	// Only a complete post earns the review receipt, as a direct post does.
	if sidecar.Verdict != "" {
		path := reviewReceiptPath(c.Collection, m.Forge, m.Slug, m.PR, m.HeadSHA, result.CommentID, sidecar.Verdict)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return result, err
		}
		if err := os.WriteFile(path, nil, 0600); err != nil {
			return result, err
		}
	}
	return result, nil
}

func writePrivateJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".taba-write-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0600); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// brokeredWrites asks once for a short, self-contained set of writes (ready,
// resolve) under intent, and returns their receipt once the person has
// confirmed them. Until then it reports that they await confirmation.
func brokeredWrites(worktree, slug, number, head, intent string, operations []brokeredOperation) (*brokeredReceipt, error) {
	if !reviewHeadPattern.MatchString(head) {
		return nil, fmt.Errorf("cannot ask for Bitbucket writes on PR #%s without its current head", number)
	}
	gitDir, err := worktreeGitDir(worktree)
	if err != nil {
		return nil, err
	}
	workspace, repo, err := splitBitbucketSlug(slug)
	if err != nil {
		return nil, err
	}
	pr, err := strconv.Atoi(number)
	if err != nil {
		return nil, fmt.Errorf("invalid PR number")
	}
	if raw, err := os.ReadFile(filepath.Join(gitDir, bitbucketWriteReceiptsDir, intent+".receipt.json")); err == nil {
		var receipt brokeredReceipt
		if json.Unmarshal(raw, &receipt) != nil || receipt.Version != 1 || receipt.IntentID != intent ||
			!strings.EqualFold(receipt.Owner, workspace) || !strings.EqualFold(receipt.Repo, repo) || receipt.PR != pr || len(receipt.Results) != len(operations) {
			return nil, fmt.Errorf("the Bitbucket write receipt does not match this request; inspect it")
		}
		removeBrokeredRequest(gitDir, intent)
		return &receipt, nil
	}
	requestPath := filepath.Join(gitDir, bitbucketWriteRequestFile)
	if err := brokeredStarted(gitDir, intent); err != nil {
		return nil, err
	}
	if raw, err := os.ReadFile(requestPath); err == nil {
		var pending brokeredRequest
		if json.Unmarshal(raw, &pending) == nil && pending.IntentID == intent {
			return nil, fmt.Errorf("%w: PR #%s writes wait for the person to confirm them in the Control UI; run this again afterwards", ErrBitbucketAwaitingConfirmation, number)
		}
		return nil, fmt.Errorf("another Bitbucket write request in this worktree awaits confirmation; let the person confirm or discard it first")
	}
	request := brokeredRequest{Version: 1, IntentID: intent, Owner: workspace, Repo: repo, PR: pr, Head: head, Operations: operations}
	if err := writePrivateJSON(requestPath, request); err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("%w: %d Bitbucket writes for PR #%s wait for the person to confirm them in the Control UI; run this again afterwards", ErrBitbucketAwaitingConfirmation, len(operations), number)
}

var reviewHeadPattern = regexp.MustCompile(`^[0-9a-f]{12,40}$`)

func brokeredHash(label string, fields ...string) string {
	sum := sha256.Sum256([]byte(label + "\x00" + strings.Join(fields, "\x00")))
	return hex.EncodeToString(sum[:])[:24]
}

// brokeredStarted reports a confirmed run that began writing (the plugin's
// claim) but left no receipt: its outcome is unknown and is reconciled by
// reading the pull request, never by asking again.
func brokeredStarted(gitDir, intent string) error {
	if _, err := os.Lstat(filepath.Join(gitDir, bitbucketWriteReceiptsDir, intent+".claim")); err == nil {
		return fmt.Errorf("the confirmed Bitbucket writes (%s) were started but left no receipt; their outcome is unknown: reconcile by reading the pull request", intent)
	}
	return nil
}

// removeBrokeredRequest removes the worktree's pending request only when it is
// the given intent's: a later attempt's request is never removed.
func removeBrokeredRequest(gitDir, intent string) {
	path := filepath.Join(gitDir, bitbucketWriteRequestFile)
	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var pending brokeredRequest
	if json.Unmarshal(raw, &pending) == nil && pending.IntentID == intent {
		_ = os.Remove(path)
	}
}

// brokeredAttempts runs short self-contained writes (ready, resolve) under
// numbered intents of base: each earlier receipt is applied in order; a run
// that ended failed or stopped is asked for again under the next intent
// (the plugin carries an intent out once); an unknown outcome stops, to be
// reconciled by reading the pull request. apply reports whether the writes
// are complete; operations gives the writes still needed.
func brokeredAttempts(worktree, slug, number, head, base string, operations func() []brokeredOperation, apply func(*brokeredReceipt) (complete bool, err error)) error {
	for attempt := 0; attempt < 20; attempt++ {
		ops := operations()
		if len(ops) == 0 {
			return nil
		}
		receipt, err := brokeredWrites(worktree, slug, number, head, fmt.Sprintf("%s-%d", base, attempt), ops)
		if err != nil {
			return err
		}
		complete, err := apply(receipt)
		if err != nil || complete {
			return err
		}
		for _, entry := range receipt.Results {
			if entry.Status == "unknown" {
				return fmt.Errorf("a confirmed Bitbucket write for PR #%s has an unknown outcome; reconcile by reading the pull request before asking again", number)
			}
		}
	}
	return fmt.Errorf("too many incomplete Bitbucket write attempts for PR #%s; inspect the pull request", number)
}

// readBrokeredReceipt reads one intent's receipt, checked against the pull
// request; nil when there is none yet.
func readBrokeredReceipt(gitDir, slug, number, intent string) (*brokeredReceipt, error) {
	raw, err := os.ReadFile(filepath.Join(gitDir, bitbucketWriteReceiptsDir, intent+".receipt.json"))
	if err != nil {
		return nil, nil
	}
	workspace, repo, err := splitBitbucketSlug(slug)
	if err != nil {
		return nil, err
	}
	var receipt brokeredReceipt
	if json.Unmarshal(raw, &receipt) != nil || receipt.Version != 1 || receipt.IntentID != intent ||
		!strings.EqualFold(receipt.Owner, workspace) || !strings.EqualFold(receipt.Repo, repo) || strconv.Itoa(receipt.PR) != number {
		return nil, fmt.Errorf("the Bitbucket write receipt %s does not match this request; inspect it", intent)
	}
	removeBrokeredRequest(gitDir, intent)
	return &receipt, nil
}

// brokeredPending explains an asked-for intent without a receipt: started
// without one (outcome unknown), still waiting, or its request is gone.
func brokeredPending(gitDir, intent, number string) error {
	if err := brokeredStarted(gitDir, intent); err != nil {
		return err
	}
	raw, err := os.ReadFile(filepath.Join(gitDir, bitbucketWriteRequestFile))
	var pending brokeredRequest
	if err == nil && json.Unmarshal(raw, &pending) == nil && pending.IntentID == intent {
		return fmt.Errorf("%w: PR #%s writes wait for the person to confirm them in the Control UI; run this again afterwards", ErrBitbucketAwaitingConfirmation, number)
	}
	return fmt.Errorf("the Bitbucket write request %s is gone without a receipt; reconcile by reading the pull request", intent)
}
