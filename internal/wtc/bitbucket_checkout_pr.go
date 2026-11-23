package wtc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

// BitbucketCheckoutPR is a branch's pull request as a gateway's Collection
// card and its pull request news see it (#471).
type BitbucketCheckoutPR struct {
	Number      int
	URL         string
	State       string // open, merged or closed (declined or superseded)
	Draft       bool
	Title       string
	Checks      string // success, failure, pending, none, or unknown when not read
	Review      string // approved, changes_requested, or empty
	Head        string
	MergeCommit string
}

// BitbucketBranchPullRequest finds branch's pull request in slug — the open
// one, else the latest merged, declined or superseded one — and reads its
// state, review and head checks through the selected client and read
// credential, in one read operation. root is the control root. nil with a
// nil error means the branch has no pull request; an error means it could
// not be read, which is never reported as "none".
func BitbucketBranchPullRequest(root, slug, branch string) (*BitbucketCheckoutPR, error) {
	read := BitbucketBranchRead(root, slug, branch)
	return read.PR, read.Err
}

// BitbucketBranchReadResult is one branch lookup with the non-secret identity
// of the read context that made it and when it completed, so news can tell
// one read identity from another and drop a result older than one already
// seen (#471).
type BitbucketBranchReadResult struct {
	PR         *BitbucketCheckoutPR
	Err        error
	Identity   string // "k:<digest>" for a fingerprinted context, "u:<digest>" for an unknown one
	ObservedAt time.Time
}

// BitbucketBranchRead is BitbucketBranchPullRequest with its read identity.
func BitbucketBranchRead(root, slug, branch string) BitbucketBranchReadResult {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	ctx = statusAuthOperation(bitbucketReadOperation(ctx, root))
	pr, err := bitbucketBranchPullRequest(ctx, slug, branch)
	return BitbucketBranchReadResult{PR: pr, Err: err, Identity: bitbucketReadIdentity(ctx, slug), ObservedAt: time.Now().UTC()}
}

// bitbucketReadIdentity names the read context of slug without revealing
// it: a digest of the entry's authenticated scope when that context is
// fingerprinted (an unchanged host token, a token from the environment, a
// bb login), otherwise a digest of the selection only, marked unknown, which
// never equals a known identity.
func bitbucketReadIdentity(ctx context.Context, slug string) string {
	entry := statusAuthEntry(ctx, "pr", "bitbucket.org", slug)
	if entry.known {
		sum := sha256.Sum256([]byte("read-identity-v1\x00" + entry.scope))
		return "k:" + hex.EncodeToString(sum[:])[:16]
	}
	sum := sha256.Sum256([]byte("read-identity-v1\x00unknown\x00" + bitbucketReadFor(ctx).selection.fingerprint()))
	return "u:" + hex.EncodeToString(sum[:])[:16]
}

func bitbucketBranchPullRequest(ctx context.Context, slug, branch string) (*BitbucketCheckoutPR, error) {
	if bitbucketReadFor(ctx).selection.Client == bitbucketClientBB {
		// bb lists pull requests without a source-branch filter: a lookup
		// through it could only guess, so it is reported, never "no PR".
		return nil, &bitbucketReadError{cause: bitbucketCauseBranchLookup}
	}
	number := ""
	for _, state := range []string{"OPEN", "MERGED", "DECLINED", "SUPERSEDED"} {
		raw, err := bitbucketReadPRsByState(ctx, slug, branch, state, 10)
		if err != nil {
			return nil, err
		}
		var list struct {
			Values []struct {
				ID     json.Number `json:"id"`
				Source struct {
					Branch struct {
						Name string `json:"name"`
					} `json:"branch"`
				} `json:"source"`
			} `json:"values"`
		}
		decoder := json.NewDecoder(strings.NewReader(string(raw)))
		decoder.UseNumber()
		if decoder.Decode(&list) != nil {
			return nil, &bitbucketReadError{cause: bitbucketCauseInvalidJSON}
		}
		best := 0
		for _, pr := range list.Values {
			// The query filters by source branch; the answer must agree.
			if pr.Source.Branch.Name != "" && pr.Source.Branch.Name != branch {
				return nil, &bitbucketReadError{cause: bitbucketCauseIdentity}
			}
			if n, err := strconv.Atoi(pr.ID.String()); err == nil && n > best {
				best = n
			}
		}
		if best > 0 {
			number = strconv.Itoa(best)
			break
		}
	}
	if number == "" {
		return nil, nil
	}
	raw, err := bitbucketReadPR(ctx, slug, number)
	if err != nil {
		return nil, err
	}
	detail, err := statusBBDetail(raw, PRRecord{Number: number})
	if err != nil {
		return nil, err
	}
	n, _ := strconv.Atoi(number)
	pr := &BitbucketCheckoutPR{Number: n, URL: "https://bitbucket.org/" + slug + "/pull-requests/" + number, Title: detail.Title,
		Head: detail.HeadCommit, MergeCommit: detail.MergeCommit, Checks: "unknown"}
	switch detail.State {
	case "DRAFT":
		pr.State, pr.Draft = "open", true
	case "DECLINED", "SUPERSEDED", "CLOSED":
		// The card's word for a pull request closed without merging.
		pr.State = "closed"
	default:
		pr.State = strings.ToLower(detail.State)
	}
	switch detail.Review {
	case "approved":
		pr.Review = "approved"
	case "changes":
		pr.Review = "changes_requested"
	}
	if pr.State == "open" && detail.HeadCommit != "" {
		evidence, err := statusBBHeadChecksContext(ctx, StatusRefreshBackground, slug, detail.HeadCommit, false)
		if err == nil && evidence.Complete {
			switch evidence.Result {
			case "SUCCESS":
				pr.Checks = "success"
			case "FAILURE":
				pr.Checks = "failure"
			case "PENDING":
				pr.Checks = "pending"
			case "NONE":
				pr.Checks = "none"
			}
		}
	}
	return pr, nil
}
