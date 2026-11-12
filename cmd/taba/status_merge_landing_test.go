package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"taba.sh/taba/internal/wtc"
)

// Canonical snapshots from synthetic forge responses preserve two separate
// observations: the PR merged, but full branch landing could not be established.
func TestStatusMergedUnknownCanonicalSnapshots(t *testing.T) {
	for _, forge := range []string{"github-com", "bitbucket-org"} {
		t.Run(forge, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("testdata", "merge-landing", forge+".json"))
			if err != nil {
				t.Fatal(err)
			}
			var snapshot wtc.StatusSnapshot
			if err := json.Unmarshal(data, &snapshot); err != nil {
				t.Fatal(err)
			}
			row := snapshot.PRs[0]
			if row.State != "MERGED" || !row.OnBranch || row.Landing == nil || row.Landing.State != "unknown" {
				t.Fatalf("fixture lost independent merge/landing facts: %+v", row)
			}
			for _, width := range []int{32, 40, 52, 80, 140, 220} {
				for _, styled := range []bool{false, true} {
					lines := statusTUIPRLines(snapshot, width, false, styled)
					layout := statusTUIPRLayout(width, row)
					start := layout.number + 1 + layout.repo + 1
					cell := strings.TrimSpace(ansi.Cut(ansi.Strip(lines[3]), start, start+layout.state))
					if cell != "merged" {
						t.Errorf("width=%d styled=%v: known merge rendered as %q", width, styled, cell)
					}
					out := ansi.Strip(strings.Join(lines, "\n"))
					if !strings.Contains(out, "landing unknown") {
						t.Errorf("width=%d styled=%v: missing landing uncertainty:\n%s", width, styled, out)
					}
					for _, line := range lines {
						if ansi.StringWidth(line) > width {
							t.Errorf("width=%d: wrapping row %q", width, line)
						}
					}
				}
				if out := statusTable(snapshot, false, width); !strings.Contains(out, "merged") || !strings.Contains(out, "landing unknown") {
					t.Errorf("width=%d: plain table lost independent facts:\n%s", width, out)
				}
			}
			if snapshot.PRs[0].State != "MERGED" || snapshot.PRs[0].Landing.State != "unknown" {
				t.Fatal("renderer mutated canonical facts")
			}
		})
	}
}

func TestStatusMergeLandingStateControls(t *testing.T) {
	merged := "MERGED"
	for _, tc := range []struct {
		name string
		row  wtc.StatusPRRow
		want string
	}{
		{"provider-unknown", wtc.StatusPRRow{State: "UNKNOWN"}, "unknown"},
		{"draft", wtc.StatusPRRow{State: "DRAFT", Draft: true}, "◇ draft"},
		{"landed", wtc.StatusPRRow{State: "MERGED", Merge: &merged, OnBranch: true, Landing: &wtc.StatusLanding{State: "landed"}}, "⚠ catch-up"},
		{"follow-up", wtc.StatusPRRow{State: "MERGED", Merge: &merged, OnBranch: true, Landing: &wtc.StatusLanding{State: "follow-up"}}, "⚠ extra"},
		{"off-branch", wtc.StatusPRRow{State: "MERGED", Merge: &merged}, "merged"},
		{"off-branch-build", wtc.StatusPRRow{State: "MERGED", Merge: &merged, Follow: &wtc.StatusFollow{Stage: "prod", Checks: "PENDING"}}, "● prod"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.row.Repo, tc.row.Number, tc.row.Title = "widget", "7", "Synthetic control"
			lines := statusTUIPRLines(wtc.StatusSnapshot{PRs: []wtc.StatusPRRow{tc.row}}, 140, false, false)
			layout := statusTUIPRLayout(140, tc.row)
			start := layout.number + 1 + layout.repo + 1
			if cell := strings.TrimSpace(ansi.Cut(lines[3], start, start+layout.state)); cell != tc.want {
				t.Fatalf("state=%q want=%q", cell, tc.want)
			}
		})
	}
}

func TestStatusMergedLandingDetailGeometry(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	firstURL, secondURL := "https://github.com/example/widget/pull/7", "https://github.com/example/widget/pull/8"
	first := wtc.StatusPRRow{Repo: "widget", Number: "7", State: "MERGED", OnBranch: true, URL: &firstURL,
		Landing: &wtc.StatusLanding{State: "unknown"}, DisplayTitle: "MERGED — landing unknown; inspect branch; still on topic"}
	second := wtc.StatusPRRow{Repo: "widget", Number: "8", State: "MERGED", OnBranch: true, URL: &secondURL,
		Landing: &wtc.StatusLanding{State: "landed"}, Title: "Synthetic next PR"}
	for _, width := range []int{32, 40, 52, 80, 100, 140, 220} {
		snapshot := wtc.StatusSnapshot{PRs: []wtc.StatusPRRow{first, second}}
		var positions []int
		lines := statusTUIPRLines(snapshot, width, false, false, func(line int, row wtc.StatusPRRow) {
			positions = append(positions, line)
		})
		wantGap := 1
		if width < 80 {
			wantGap++
		}
		if len(positions) != 2 || positions[0] != 3 || positions[1]-positions[0] != wantGap || len(lines) != positions[1]+1 {
			t.Fatalf("width=%d: positions=%v lines=%d; want row gap %d", width, positions, len(lines), wantGap)
		}
		model := statusTUIModel{snapshot: snapshot, width: width, height: 40}
		content := model.contentLayout()
		if len(content.rows) != 2 {
			t.Fatalf("width=%d: row locations=%+v", width, content.rows)
		}
		for i, target := range []string{firstURL, secondURL} {
			row := content.rows[i]
			if row.end != row.start+1 || !strings.Contains(ansi.Strip(content.lines[row.start]), "#"+row.pr.Number) {
				t.Fatalf("width=%d: PR hit geometry lost: %+v", width, row)
			}
			if got := model.buildClickTarget(1, row.start); got != target {
				t.Fatalf("width=%d: PR hit=%q want=%q", width, got, target)
			}
		}
		if wantGap > 1 {
			if got := model.buildClickTarget(1, content.rows[0].end); got != "" {
				t.Fatalf("width=%d: explanation inherited PR hit target %q", width, got)
			}
		}
		// Scrolling to the second PR must use its actual rendered position.
		model.height = 6
		model.scroll = content.rows[1].start - model.viewport(content).header
		secondY := content.rows[1].start - model.viewport(content).scroll
		if got := model.buildClickTarget(1, secondY); got != secondURL {
			t.Fatalf("width=%d: scrolled PR hit=%q want=%q", width, got, secondURL)
		}
	}
}
