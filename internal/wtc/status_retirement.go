package wtc

import (
	"fmt"
	"path/filepath"
	"sort"
	"time"
)

func (c *Context) appendRetirement(snapshot *StatusSnapshot, name string) {
	report, err := c.InspectRetirement(name, nil)
	if err != nil {
		snapshot.Retirement = append(snapshot.Retirement, RetirementAssessment{Collection: name, State: "unknown", ObservedAt: time.Now().UTC().Format(time.RFC3339Nano), Blockers: []string{}, Unknowns: []string{"Local retirement evidence could not be collected; inspect with retire --dry-run."}})
		return
	}
	snapshot.Retirement = append(snapshot.Retirement, report.Assessment)
}

func (c *Context) statusRetirement(snapshot *StatusSnapshot, all bool) {
	names := map[string]bool{}
	if !all {
		names[filepath.Base(c.Collection)] = true
	} else {
		for _, repo := range snapshot.Repos {
			names[repo.Collection] = true
		}
		for _, retired := range snapshot.Retired {
			names[retired.Folder] = true
		}
	}
	ordered := make([]string, 0, len(names))
	for name := range names {
		ordered = append(ordered, name)
	}
	sort.Strings(ordered)
	for _, name := range ordered {
		c.appendRetirement(snapshot, name)
	}
	statusRetirementFacts(snapshot)
}

// Status facts can add blockers but cannot close unverified source coverage.
func statusRetirementFacts(snapshot *StatusSnapshot) {
	for i := range snapshot.Retirement {
		a := &snapshot.Retirement[i]
		add := func(reason string) {
			for _, old := range a.Blockers {
				if old == reason {
					return
				}
			}
			a.Blockers = append(a.Blockers, reason)
		}
		for _, repo := range snapshot.Repos {
			if repo.Collection == a.Collection && repo.PR != nil {
				if statusObservationUnavailable(repo.PR.Freshness) {
					add(fmt.Sprintf("%s: pull request #%s state is unconfirmed in status.", repo.Dir, repo.PR.Number))
				} else {
					add(fmt.Sprintf("%s: pull request #%s remains open in status.", repo.Dir, repo.PR.Number))
				}
			}
		}
		if !snapshot.ShowCollectionColumn && snapshot.Collection == a.Collection {
			for _, pr := range snapshot.PRs {
				represented := false
				for _, repo := range snapshot.Repos {
					if repo.PR != nil && repo.PR.Number == pr.Number && (repo.Repo == pr.Repo || repo.Dir == pr.Repo) {
						represented = true
					}
				}
				if !represented {
					if statusObservationUnavailable(pr.Freshness) {
						add(fmt.Sprintf("%s: pull request #%s state is unconfirmed in status.", pr.Repo, pr.Number))
					} else if pr.State == "OPEN" || pr.State == "DRAFT" {
						add(fmt.Sprintf("%s: pull request #%s remains open in status.", pr.Repo, pr.Number))
					}
				}
				if pr.Follow != nil && pr.Follow.Stage != "done" {
					add(fmt.Sprintf("%s: pull request #%s still has delivery follow-up.", pr.Repo, pr.Number))
				}
			}
		}
		for _, resource := range snapshot.Runtime {
			if resource.Collection != a.Collection {
				continue
			}
			switch resource.State {
			case "ready", "running", "starting", "provision-hook-completed":
				add(fmt.Sprintf("%s: resource %s requires lifecycle follow-up (%s).", resource.Repo, resource.Path, resource.State))
			}
		}
		a.classify()
	}
}
