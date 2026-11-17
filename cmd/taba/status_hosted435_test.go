package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"taba.sh/taba/internal/wtc"
)

func hosted435FrameSnapshots(terminal bool) (wtc.StatusSnapshot, wtc.StatusSnapshot) {
	url := "https://github.com/example/widget/pull/7"
	head := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	now := time.Now()
	checks, merge, review := "FAILURE", "CONFLICTING", "changes-requested"
	old := wtc.StatusPRRow{Repo: "widget", Slug: "example/widget", Number: "7", URL: &url, State: "DRAFT", Draft: true, HeadCommit: head, Title: "Retained title", Checks: &checks, Merge: &merge, Review: &review, Freshness: "live", ObservedAt: now.Format(time.RFC3339Nano)}
	repo := wtc.StatusRepo{Collection: "fixture", Repo: "widget", Dir: "widget", Worktree: "checkout/widget", Slug: "example/widget", Forge: "github.com", BranchKind: "branch", Branch: "feature", BranchDisplay: "feature", Tree: "clean", PR: &wtc.StatusPRFacts{Number: "7", URL: url, Head: head, Checks: "FAILURE", Merge: merge, Review: review, Draft: true, Freshness: "live", ObservedAt: old.ObservedAt}}
	current := wtc.StatusSnapshot{Collection: "fixture", Repos: []wtc.StatusRepo{repo}, PRs: []wtc.StatusPRRow{old}}
	incomingRepo := repo
	incomingFacts := *repo.PR
	incomingFacts.Checks = "SUCCESS"
	incomingFacts.Merge = "CLEAN"
	incomingFacts.Review = "approved"
	incomingFacts.Draft = false
	incomingFacts.ObservedAt = now.Add(-time.Hour).Format(time.RFC3339Nano)
	incomingRepo.PR = &incomingFacts
	incoming := wtc.StatusSnapshot{Collection: "fixture", Repos: []wtc.StatusRepo{incomingRepo}, PRs: []wtc.StatusPRRow{{Repo: "widget", Slug: "example/widget", Number: "7", URL: &url, State: "OPEN", HeadCommit: head, Title: "Old completion", Freshness: "live", ObservedAt: incomingFacts.ObservedAt}}}
	if terminal {
		merged := "MERGED"
		current.PRs[0].State = "MERGED"
		current.PRs[0].Draft = false
		current.PRs[0].Merge = &merged
		current.PRs[0].Freshness = "recorded"
		current.Repos[0].PR = nil
		incoming.PRs[0].Freshness = "deferred"
		incoming.PRs[0].ObservationReason = "shared-budget"
	}
	return incoming, current
}

func TestStatusHosted435NativeSectionsKeepSameFactsAndTerminal(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	for _, terminal := range []bool{false, true} {
		for _, width := range []int{34, 100} {
			t.Run(fmt.Sprintf("terminal%v/width%d", terminal, width), func(t *testing.T) {
				incoming, current := hosted435FrameSnapshots(terminal)
				snapshot := wtc.StatusPreferNewerObservations(incoming, current)
				model := statusTUIModel{width: width, height: 28, focused: true, snapshot: snapshot, lastRefresh: time.Now()}
				frame := model.View().Content
				plain := ansi.Strip(frame)
				parts := strings.Split(plain, "\n")
				split := -1
				for i, line := range parts {
					if strings.TrimSpace(line) == "PRs" {
						split = i
						break
					}
				}
				if split < 0 {
					t.Fatal("missing PR section")
				}
				repos := strings.Join(parts[:split], "\n")
				prs := strings.Join(parts[split+1:], "\n")
				if dir := os.Getenv("WTC_HOSTED435_FRAME_DIR"); dir != "" {
					raw, _ := json.Marshal(map[string]any{"columns": width, "terminal": terminal, "content": frame})
					if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("terminal%v-%d.json", terminal, width)), raw, 0600); err != nil {
						t.Fatal(err)
					}
				}
				if !strings.Contains(repos, "widget") || !strings.Contains(prs, "Retai") {
					t.Fatalf("missing native sections: %s", plain)
				}
				if terminal {
					if strings.Contains(repos, "#7") || !strings.Contains(strings.ToLower(prs), "merged") || strings.Contains(prs, "d:") {
						t.Fatalf("terminal authority/association lost: %s", plain)
					}
				} else {
					if !strings.Contains(repos, "✗") || !strings.Contains(prs, "confl") {
						t.Fatalf("repo and PR signals disagree: %s", plain)
					}
				}
				for _, line := range strings.Split(frame, "\n") {
					if ansi.StringWidth(line) > width {
						t.Fatal("native frame would wrap")
					}
				}
			})
		}
	}
}

func TestStatusHosted435NativeConfirmedMergeAgeStaysInDetail(t *testing.T) {
	for _, freshness := range []string{"stale", "deferred", "unavailable"} {
		for _, width := range []int{34, 100} {
			t.Run(fmt.Sprintf("%s/%d", freshness, width), func(t *testing.T) {
				incoming, current := hosted435FrameSnapshots(true)
				current.PRs[0].Freshness = freshness
				current.PRs[0].ObservedAt = time.Now().Add(-time.Hour).Format(time.RFC3339Nano)
				incoming.PRs[0].Freshness = "unavailable"
				incoming.PRs[0].ObservationReason = "refresh-failed"
				snapshot := wtc.StatusPreferNewerObservations(incoming, current)
				model := statusTUIModel{width: width, height: 28, focused: true, snapshot: snapshot, lastRefresh: time.Now()}
				frame := model.View().Content
				plain := ansi.Strip(frame)
				lines := strings.Split(plain, "\n")
				split := -1
				for i, line := range lines {
					if strings.TrimSpace(line) == "PRs" {
						split = i
						break
					}
				}
				if split < 0 || !strings.Contains(strings.Join(lines[:split], "\n"), "widget") || strings.Contains(strings.Join(lines[:split], "\n"), "#7") {
					t.Fatalf("merged repo association is not empty: %s", plain)
				}
				if dir := os.Getenv("WTC_HOSTED435_FRAME_DIR"); dir != "" {
					raw, _ := json.Marshal(map[string]any{"columns": width, "case": "merged-" + freshness, "content": frame, "detail": wtc.StatusPRObservationDetail(snapshot.PRs[0])})
					if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("merged-%s-%d.json", freshness, width)), raw, 0600); err != nil {
						t.Fatal(err)
					}
				}
				if !strings.Contains(strings.ToLower(plain), "merged") || strings.Contains(plain, "d:1h") || strings.Contains(plain, "u:1h") || strings.Contains(plain, "s:1h") {
					t.Fatalf("merge hidden by mark: %s", plain)
				}
				detail := wtc.StatusPRObservationDetail(snapshot.PRs[0])
				if !strings.Contains(detail, "last merged") || !strings.Contains(detail, "recheck reason refresh-failed") || !strings.Contains(detail, current.PRs[0].ObservedAt) {
					t.Fatal(detail)
				}
				for _, line := range strings.Split(frame, "\n") {
					if ansi.StringWidth(line) > width {
						t.Fatal("frame wraps")
					}
				}
			})
		}
	}
}

func TestStatusHosted435NativeNewHeadRemainsUnassociated(t *testing.T) {
	for _, width := range []int{34, 100} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			incoming, current := hosted435FrameSnapshots(false)
			incoming.Repos[0].PR.Head = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
			snapshot := wtc.StatusPreferNewerObservations(incoming, current)
			model := statusTUIModel{width: width, height: 28, focused: true, snapshot: snapshot, lastRefresh: time.Now()}
			frame := model.View().Content
			lines := strings.Split(ansi.Strip(frame), "\n")
			split := -1
			for i, line := range lines {
				if strings.TrimSpace(line) == "PRs" {
					split = i
					break
				}
			}
			if split < 0 {
				t.Fatal("missing PR section")
			}
			repos, prs := strings.Join(lines[:split], "\n"), strings.Join(lines[split+1:], "\n")
			if strings.Contains(repos, "#7") || !strings.Contains(repos, "widget") || !strings.Contains(prs, "Retai") || !strings.Contains(prs, "confl") || snapshot.Repos[0].PR != nil || !snapshot.Repos[0].PRUnconfirmed {
				t.Fatalf("cross-head facts escaped into native repo section: %s", ansi.Strip(frame))
			}
			if dir := os.Getenv("WTC_HOSTED435_FRAME_DIR"); dir != "" {
				raw, _ := json.Marshal(map[string]any{"columns": width, "case": "new-head", "content": frame})
				if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("new-head-%d.json", width)), raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			for _, line := range strings.Split(frame, "\n") {
				if ansi.StringWidth(line) > width {
					t.Fatal("frame wraps")
				}
			}
		})
	}
}

func TestStatusHosted435NativeConfirmedBranchWarningSurvivesAge(t *testing.T) {
	for _, width := range []int{34, 100} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			_, current := hosted435FrameSnapshots(true)
			current.PRs[0].OnBranch = true
			current.PRs[0].Landing = &wtc.StatusLanding{State: "unknown", Reason: "inspect retained branch"}
			current.PRs[0].Freshness = "deferred"
			current.PRs[0].DisplayTitle = "MERGED — landing unknown; inspect branch"
			lines := statusTUIPRLines(current, width, false, false)
			text := strings.ToLower(strings.Join(lines, "\n"))
			if !strings.Contains(text, "merged") || !strings.Contains(text, "landing unknown") || strings.Contains(text, "d:") {
				t.Fatal(text)
			}
			for _, line := range lines {
				if ansi.StringWidth(line) > width {
					t.Fatal("branch-warning row wraps")
				}
			}
		})
	}
}
