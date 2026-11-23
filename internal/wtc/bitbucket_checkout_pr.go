package wtc

import (
	"context"
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
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	ctx = statusAuthOperation(bitbucketReadOperation(ctx, root))
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
			return nil, &bitbucketReadError{bitbucketCauseInvalidJSON}
		}
		best := 0
		for _, pr := range list.Values {
			// The query filters by source branch; the answer must agree.
			if pr.Source.Branch.Name != "" && pr.Source.Branch.Name != branch {
				return nil, &bitbucketReadError{bitbucketCauseIdentity}
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
