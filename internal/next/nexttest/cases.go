// Package nexttest supplies synthetic, deterministic status cases for tests
// and development previews. It never reads a checkout or contacts a forge.
package nexttest

import (
	"fmt"

	"taba.sh/taba/internal/next"
)

// Case is fixture input, not a new public status API. Facts deliberately use
// the existing planner's type; proposed review/job facts are not invented here.
type Case struct {
	ID    string     `json:"id"`
	Facts next.Facts `json:"facts"`
}

// Cases builds reachable combinations from lifecycle profiles, then varies
// orthogonal observations. A conflicting PR needs a divergent base; dirty files
// may coexist with any remote check/review result. Unpublished commits are
// present only on branches with committed work. Every case owns its PR value.
func Cases() []Case {
	var cases []Case
	for _, host := range []string{next.HostLocal, next.HostOpenClaw} {
		base := func() next.Facts {
			return next.Facts{Collection: "sample", Repo: "api", Host: host,
				Base: "origin/main", Head: "head-1", BaseHead: "base-1"}
		}
		add := func(id string, f next.Facts) {
			if f.Branch == "" && f.Ahead == 0 && f.Behind == 0 && f.InProgress == "" {
				f.Head = f.BaseHead
			}
			cases = append(cases, Case{ID: host + "/" + id, Facts: f})
		}
		for _, dirty := range []int{0, 2} {
			for _, behind := range []int{0, 3} {
				for _, ahead := range []int{0, 2} {
					f := base()
					f.Dirty, f.Behind, f.Ahead = dirty, behind, ahead
					add(fmt.Sprintf("detached/dirty=%d/behind=%d/ahead=%d", dirty, behind, ahead), f)
				}
			}
		}
		for _, lifecycle := range []string{"no-pr", "open", "merged", "merged-followup", "closed"} {
			for _, dirty := range []int{0, 2} {
				for _, behind := range []int{0, 3} {
					for _, unpublished := range []int{0, 2} {
						f := base()
						f.Branch, f.Dirty, f.Behind, f.ToPush = "42-topic", dirty, behind, unpublished
						f.Ahead = 2
						if host == next.HostLocal {
							// The local adapter counts ahead against tracking,
							// and uses that same count for publication.
							f.Ahead = unpublished
						}
						if lifecycle == "merged" || lifecycle == "merged-followup" {
							// A completed branch has no new work to publish. Keep
							// stale/contradictory counters in an adversarial corpus.
							in := true
							f.Ahead = 0
							f.PR = &next.PR{Number: 7, State: "merged", MergedIn: &in}
							if lifecycle == "merged" && unpublished > 0 {
								continue
							}
							if lifecycle == "merged-followup" {
								f.Ahead, f.AfterMerge, f.ToPush = 2, 2, 2
								if unpublished == 0 { // identical state, not another case
									continue
								}
							}
						} else if lifecycle == "closed" {
							f.PR = &next.PR{Number: 7, State: "closed"}
						}
						prefix := fmt.Sprintf("%s/dirty=%d/behind=%d/unpublished=%d", lifecycle, dirty, behind, unpublished)
						if lifecycle != "open" {
							add(prefix, f)
							continue
						}
						for _, checks := range []string{"none", "pending", "success", "failure"} {
							for _, review := range []string{"", "review_required", "approved", "changes_requested"} {
								for _, draft := range []bool{false, true} {
									for _, mergeable := range []string{"mergeable", "unknown", "conflicting"} {
										if mergeable == "conflicting" && behind == 0 {
											continue
										}
										v := f
										v.PR = &next.PR{Number: 7, State: "open", Checks: checks, Review: review, Draft: draft, Mergeable: mergeable}
										if mergeable == "conflicting" {
											v.MergeConflicts = []string{"contract.go"}
										}
										v.MergeUnknown = behind > 0 && mergeable == "unknown"
										add(fmt.Sprintf("%s/checks=%s/review=%s/draft=%t/merge=%s", prefix, checks, review, draft, mergeable), v)
									}
								}
							}
						}
					}
				}
			}
		}
		for _, operation := range []string{"merge", "rebase", "cherry-pick"} {
			f := base()
			f.Branch, f.InProgress, f.Conflicted, f.Dirty = "42-topic", operation, 1, 2
			add("in-progress/"+operation, f)
		}
	}
	return cases
}
