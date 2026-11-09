package wtc

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"taba.sh/taba/internal/envcompat"
)

type statusPRDetail struct {
	Number          string
	State           string
	Checks          string
	Merge           string
	Review          string
	Title           string
	MergedOn        string
	Base            string
	MergeCommit     string
	HeadCommit      string
	BaseCommit      string
	MergeConflicts  []string
	MergeSource     string
	ChecksUnsettled bool
}

func statusCheckResult(rollup []struct {
	Conclusion string `json:"conclusion"`
	State      string `json:"state"`
	Status     string `json:"status"`
}) (string, bool) {
	failed, pending, passed := false, false, false
	for _, check := range rollup {
		conclusion := strings.ToUpper(check.Conclusion)
		if conclusion == "" {
			conclusion = strings.ToUpper(check.State)
		}
		switch conclusion {
		case "FAILURE", "TIMED_OUT", "CANCELLED", "ACTION_REQUIRED", "STARTUP_FAILURE", "STALE", "ERROR":
			failed = true
		case "SUCCESS", "NEUTRAL", "SKIPPED":
			passed = true
		default:
			// A nonempty rollup with an unknown or absent conclusion is not final.
			pending = true
		}
		if check.Status != "" && !strings.EqualFold(check.Status, "COMPLETED") {
			pending = true
		}
		if conclusion == "PENDING" || conclusion == "IN_PROGRESS" || conclusion == "QUEUED" {
			pending = true
		}
	}
	switch {
	case failed:
		return "FAILURE", pending
	case pending:
		return "PENDING", true
	case passed:
		return "SUCCESS", false
	default:
		return "NONE", false
	}
}

func statusGHDetail(raw []byte, fallback PRRecord) (statusPRDetail, error) {
	var p struct {
		Number           int    `json:"number"`
		State            string `json:"state"`
		Title            string `json:"title"`
		IsDraft          bool   `json:"isDraft"`
		ReviewDecision   string `json:"reviewDecision"`
		MergeStateStatus string `json:"mergeStateStatus"`
		Mergeable        string `json:"mergeable"`
		MergedAt         string `json:"mergedAt"`
		BaseRefName      string `json:"baseRefName"`
		HeadRefOid       string `json:"headRefOid"`
		MergeCommit      *struct {
			OID string `json:"oid"`
		} `json:"mergeCommit"`
		ReviewRequests    []json.RawMessage `json:"reviewRequests"`
		LatestReviews     []json.RawMessage `json:"latestReviews"`
		StatusCheckRollup []struct {
			Conclusion string `json:"conclusion"`
			State      string `json:"state"`
			Status     string `json:"status"`
		} `json:"statusCheckRollup"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return statusPRDetail{}, err
	}
	if p.Number == 0 || p.State == "" {
		return statusPRDetail{}, fmt.Errorf("missing GitHub PR identity or state")
	}
	checks, unsettled := statusCheckResult(p.StatusCheckRollup)
	d := statusPRDetail{Number: fmt.Sprint(p.Number), State: strings.ToUpper(p.State),
		Checks: checks, ChecksUnsettled: unsettled, Merge: p.MergeStateStatus,
		Title: cleanPRField(p.Title), MergedOn: p.MergedAt, Base: p.BaseRefName, HeadCommit: p.HeadRefOid}
	if strings.EqualFold(p.Mergeable, "CONFLICTING") {
		d.Merge = "CONFLICTING"
	}
	if d.Title == "" {
		d.Title = fallback.Title
	}
	if p.MergeCommit != nil {
		d.MergeCommit = p.MergeCommit.OID
	}
	if d.State == "OPEN" && p.IsDraft {
		d.State = "DRAFT"
	}
	if d.State == "MERGED" {
		d.Merge = "MERGED"
		d.Review = "merged"
	} else {
		switch strings.ToUpper(p.ReviewDecision) {
		case "APPROVED":
			d.Review = "approved"
		case "CHANGES_REQUESTED":
			d.Review = "changes"
		default:
			switch {
			case p.IsDraft && len(p.ReviewRequests) == 0 && len(p.LatestReviews) == 0:
				d.Review = "none"
			case len(p.LatestReviews) != 0:
				d.Review = "commented"
			case len(p.ReviewRequests) != 0 || strings.EqualFold(p.ReviewDecision, "REVIEW_REQUIRED"):
				d.Review = "waiting"
			case p.IsDraft:
				d.Review = "none"
			default:
				d.Review = "noreviewers"
			}
		}
	}
	if p.IsDraft && d.Checks == "NONE" {
		d.Checks = "draft"
	}
	return d, nil
}

func statusBBDetail(raw []byte, fallback PRRecord) (statusPRDetail, error) {
	var p struct {
		ID          int    `json:"id"`
		State       string `json:"state"`
		Draft       bool   `json:"draft"`
		Title       string `json:"title"`
		MergedOn    string `json:"merged_on"`
		MergeCommit *struct {
			Hash string `json:"hash"`
		} `json:"merge_commit"`
		Destination struct {
			Commit struct {
				Hash string `json:"hash"`
			} `json:"commit"`
			Branch struct {
				Name string `json:"name"`
			} `json:"branch"`
		} `json:"destination"`
		Source struct {
			Commit struct {
				Hash string `json:"hash"`
			} `json:"commit"`
		} `json:"source"`
		Participants []struct {
			Approved bool   `json:"approved"`
			State    string `json:"state"`
		} `json:"participants"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return statusPRDetail{}, err
	}
	if p.ID == 0 || p.State == "" {
		return statusPRDetail{}, fmt.Errorf("missing Bitbucket PR identity or state")
	}
	d := statusPRDetail{Number: fmt.Sprint(p.ID), State: strings.ToUpper(p.State),
		Checks: "NONE", Merge: "UNKNOWN", Review: "none", Title: cleanPRField(p.Title), Base: p.Destination.Branch.Name,
		HeadCommit: p.Source.Commit.Hash, BaseCommit: p.Destination.Commit.Hash}
	if d.Title == "" {
		d.Title = fallback.Title
	}
	if p.Draft && d.State == "OPEN" {
		d.State = "DRAFT"
		d.Checks = "draft"
	}
	for _, participant := range p.Participants {
		if participant.Approved {
			d.Review = "approved"
			break
		}
		if strings.EqualFold(participant.State, "changes_requested") {
			d.Review = "changes"
		}
	}
	if d.State == "MERGED" {
		d.Merge = "MERGED"
		d.Review = "merged"
		d.MergedOn = p.MergedOn
		if p.MergeCommit != nil {
			d.MergeCommit = p.MergeCommit.Hash
		}
	}
	return d, nil
}

func statusWeekdayHours(start, end time.Time) float64 {
	if !end.After(start) {
		return 0
	}
	var hours float64
	for at := start.UTC(); at.Before(end); {
		next := time.Date(at.Year(), at.Month(), at.Day()+1, 0, 0, 0, 0, time.UTC)
		if next.After(end) {
			next = end
		}
		if at.Weekday() != time.Saturday && at.Weekday() != time.Sunday {
			hours += next.Sub(at).Hours()
		}
		at = next
	}
	return hours
}

func statusArchived(mergedOn string, now time.Time) bool {
	when, err := time.Parse(time.RFC3339, mergedOn)
	hours := 48.0
	if configured, parseErr := strconv.ParseFloat(envcompat.Getenv("WTC_PR_ARCHIVE_HOURS"), 64); parseErr == nil && configured > 0 {
		hours = configured
	}
	return err == nil && statusWeekdayHours(when, now) >= hours
}
