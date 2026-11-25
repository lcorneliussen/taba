package wtc

import "strings"

// StatusLanding is an observation, never authorization to detach or delete refs.
// Unknown retains incomplete evidence; follow-up identifies commits after the
// recorded PR head without claiming their content is absent from every squash.
type StatusLanding struct {
	State  string `json:"state"`
	Reason string `json:"reason"`
}

func statusMergedLanding(row StatusRepo, detail statusPRDetail) *StatusLanding {
	unknown := &StatusLanding{State: "unknown", Reason: "full landing not established; inspect retained branch"}
	if row.baseRef.IsZero() {
		return unknown
	}
	if catchUpLanded(catchUpTarget{path: row.Worktree}, row.baseRef.String(), catchUpPR{state: "MERGED", mergeCommit: detail.MergeCommit, head: detail.HeadCommit}) {
		return &StatusLanding{State: "landed", Reason: "full landing established; ahead can include residual history"}
	}
	if detail.HeadCommit != "" && detail.MergeCommit != "" &&
		catchUpGitOK(row.Worktree, "merge-base", "--is-ancestor", detail.MergeCommit, row.baseRef.String()) &&
		catchUpGitOK(row.Worktree, "merge-base", "--is-ancestor", detail.HeadCommit, "HEAD") {
		if n, err := statusCount(row.Worktree, detail.HeadCommit+"..HEAD"); err == nil && n > 0 {
			return &StatusLanding{State: "follow-up", Reason: "commits after recorded PR head; full landing not established"}
		}
	}
	return unknown
}

// LandingLabel also handles older cached snapshots that lack the observation.
func (r StatusPRRow) LandingLabel() string {
	if r.Landing == nil {
		return "still on branch; catch-up"
	}
	switch r.Landing.State {
	case "landed":
		return "fully landed; catch-up"
	case "follow-up":
		return "follow-up; inspect unlanded work"
	default:
		return "landing unknown; inspect branch"
	}
}

func parseMergedHead(line string) (repo, number, merge, head string, ok bool) {
	f := strings.Fields(line)
	if len(f) != 6 || f[0] != "#" || f[1] != "merged-head" || ValidatePRIdentity(f[2], f[3]) != nil || !statusCommitHash.MatchString(f[4]) || !statusCommitHash.MatchString(f[5]) {
		return "", "", "", "", false
	}
	return f[2], f[3], f[4], f[5], true
}
