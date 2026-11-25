package main

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"taba.sh/taba/internal/wtc"
)

// statusDenseWidths covers narrow (no sync columns), normal and wide layouts.
var statusDenseWidths = []int{24, 40, 54, 60, 72, 80, 100, 120, 200}

var statusOSC8Target = regexp.MustCompile("\x1b]8;[^;\x07\x1b]*;([^\x07\x1b]*)(?:\x07|\x1b\\\\)")

// assertStatusDenseSection is the shared dense-table invariant (#534): a
// section is one heading plus exactly one physical line per item, items occupy
// consecutive lines, and no line exceeds the width.
func assertStatusDenseSection(t *testing.T, label string, lines []string, starts []int, items, width int) {
	t.Helper()
	if len(starts) != items {
		t.Fatalf("%s: %d item positions for %d items", label, len(starts), items)
	}
	if len(lines) != 1+items {
		t.Fatalf("%s: %d physical lines for %d items:\n%s", label, len(lines), items, ansi.Strip(strings.Join(lines, "\n")))
	}
	for i, start := range starts {
		if start != 1+i {
			t.Fatalf("%s: item %d starts at line %d, want %d", label, i, start, 1+i)
		}
	}
	for _, line := range lines {
		if strings.Contains(line, "\n") || ansi.StringWidth(line) > width {
			t.Fatalf("%s: line exceeds width %d or wraps: %q", label, width, line)
		}
	}
}

func statusDenseLinkTargets(lines []string) []string {
	var targets []string
	for _, line := range lines {
		for _, m := range statusOSC8Target.FindAllStringSubmatch(line, -1) {
			if m[1] != "" {
				targets = append(targets, m[1])
			}
		}
	}
	return targets
}

// statusDenseBaseFixture is ten synthetic repositories that all carry a
// collection-local base override with the same long reason.
func statusDenseBaseFixture(reason string) wtc.StatusSnapshot {
	snapshot := wtc.StatusSnapshot{Collection: "fixture"}
	for i := range 10 {
		name := fmt.Sprintf("repo-%02d", i)
		url := fmt.Sprintf("https://example.invalid/acme/%s/pull/%d", name, 100+i)
		row := wtc.StatusRepo{Collection: "fixture", Repo: name, Dir: name, Slug: "acme/" + name, Forge: "github",
			Branch: "topic", BranchKind: "branch", BranchDisplay: "topic", Tree: "clean", Behind: i % 3, ComparisonRef: "origin/βeta",
			DevelopmentBase: &wtc.RepositoryBase{Repository: name, RegistryRef: "origin/main", EffectiveRef: "origin/βeta", Source: "collection", Reason: reason}}
		if i%2 == 0 {
			row.PR = &wtc.StatusPRFacts{Number: fmt.Sprint(100 + i), URL: url, Checks: "SUCCESS", Merge: "MERGEABLE", Review: "approved"}
		}
		snapshot.Repos = append(snapshot.Repos, row)
	}
	return snapshot
}

func statusDenseWithoutBases(snapshot wtc.StatusSnapshot) wtc.StatusSnapshot {
	snapshot.Repos = slices.Clone(snapshot.Repos)
	for i := range snapshot.Repos {
		snapshot.Repos[i].DevelopmentBase = nil
	}
	return snapshot
}

var statusDenseReasons = []string{
	"Feature staging",
	strings.Repeat("Coordinated beta stabilisation for the release train; keep catch-up on βeta until it lands 界 🐾. ", 6),
	"cached\x1b]8;;https://bad.invalid\acontrol\x1b]8;;\a\nreason",
}

// #534 acceptance: ten repositories with long collection base reasons still
// take exactly ten item lines, and the reason never reaches the table.
func TestStatusDenseRepoRows534(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	for _, reason := range statusDenseReasons {
		snapshot := statusDenseBaseFixture(reason)
		plain := statusDenseWithoutBases(snapshot)
		for _, width := range statusDenseWidths {
			for _, styled := range []bool{false, true} {
				label := fmt.Sprintf("width %d styled %v", width, styled)
				var starts []int
				lines := statusTUIRepoLines(snapshot, width, styled, func(i int, _ wtc.StatusRepo) { starts = append(starts, i) })
				assertStatusDenseSection(t, label, lines, starts, 10, width)
				var plainStarts []int
				without := statusTUIRepoLines(plain, width, styled, func(i int, _ wtc.StatusRepo) { plainStarts = append(plainStarts, i) })
				if !slices.Equal(starts, plainStarts) || len(lines) != len(without) {
					t.Fatalf("%s: base metadata changed row geometry", label)
				}
				if got, want := statusDenseLinkTargets(lines), statusDenseLinkTargets(without); !slices.Equal(got, want) {
					t.Fatalf("%s: base metadata changed link targets: %v vs %v", label, got, want)
				}
				if styled && width >= 40 && len(statusDenseLinkTargets(lines)) == 0 {
					t.Fatalf("%s: fixture produced no links to compare", label)
				}
				text := ansi.Strip(strings.Join(lines, "\n"))
				for _, leak := range []string{"Feature staging", "Coordinated", "cached", "bad.invalid", "(collection)", "repo base show"} {
					if strings.Contains(text, leak) {
						t.Fatalf("%s: row carries base prose %q:\n%s", label, leak, text)
					}
				}
				layout := statusTUIRepoLayout(snapshot, width)
				for i, line := range lines[1:] {
					marked := strings.Contains(ansi.Strip(line), statusTUIBaseMarker)
					if marked != layout.showSync {
						t.Fatalf("%s: repo %d base marker %v with sync columns %v: %q", label, i, marked, layout.showSync, ansi.Strip(line))
					}
				}
			}
		}
	}
}

// The marker belongs to the ↓ count only while that count uses the override.
func TestStatusDenseBaseMarkerFollowsComparison534(t *testing.T) {
	row := statusDenseBaseFixture("r").Repos[1]
	if got := statusTUIBehindCell(row); got != "1"+statusTUIBaseMarker {
		t.Fatalf("override count = %q", got)
	}
	row.ComparisonRef = ""
	if got := statusTUIBehindCell(row); got != "1"+statusTUIBaseMarker {
		t.Fatalf("legacy snapshot without comparison ref = %q", got)
	}
	row.ComparisonRef = "origin/release"
	if got := statusTUIBehindCell(row); got != "1" {
		t.Fatalf("count against a PR base kept the override marker: %q", got)
	}
	row.DevelopmentBase.Source = "registry"
	row.ComparisonRef = "origin/βeta"
	if got := statusTUIBehindCell(row); got != "1" {
		t.Fatalf("registry base marked: %q", got)
	}
	row.DevelopmentBase, row.Behind, row.Conflict = nil, 0, true
	if got := statusTUIBehindCell(row); got != "✗" {
		t.Fatalf("conflict cell changed: %q", got)
	}
}

// Full reasons stay reachable by keyboard (? help), once per distinct reason,
// and toggling help adds no line inside the repository section.
func TestStatusDenseBaseDetailsInHelp534(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	reason := statusDenseReasons[1]
	snapshot := statusDenseBaseFixture(reason)
	snapshot.Repos[9].DevelopmentBase = &wtc.RepositoryBase{Source: "collection", EffectiveRef: "origin/hotfix", Reason: "Hotfix validation"}
	snapshot.Repos[9].ComparisonRef = "origin/hotfix"
	snapshot.Repos[8].ComparisonRef = "origin/release"
	details := statusTUIBaseOverrideDetails(snapshot)
	if len(details) != 2 {
		t.Fatalf("details not grouped by ref and reason: %q", details)
	}
	if !strings.Contains(details[0], strings.TrimSpace(reason)) || !strings.Contains(details[0], "repo-00, repo-01") ||
		!strings.Contains(details[0], `repo-08 ↓ vs PR base "origin/release"`) || !strings.Contains(details[1], "repo-09") {
		t.Fatalf("details lost values: %q", details)
	}
	hostile := statusTUIBaseOverrideDetails(statusDenseBaseFixture(statusDenseReasons[2]))
	if joined := strings.Join(hostile, ""); strings.ContainsAny(joined, "\x1b\a\n") {
		t.Fatalf("details carry cached controls: %q", joined)
	}
	for _, width := range []int{40, 80, 120} {
		closed := statusTUIModel{snapshot: snapshot, width: width, height: 200}
		open := closed
		open.showHelp = true
		before, after := closed.contentLayout(), open.contentLayout()
		helpText := strings.ReplaceAll(ansi.Strip(strings.Join(after.lines, "")), " ", "")
		squash := func(s string) string { return strings.ReplaceAll(s, " ", "") }
		if strings.Count(helpText, squash(`"origin/βeta" (collection) for`)) != 1 || !strings.Contains(helpText, squash(strings.TrimSpace(reason))) {
			t.Fatalf("width %d: help repeats or omits the shared reason", width)
		}
		if !strings.Contains(helpText, "repobaseshow") {
			t.Fatalf("width %d: help lacks the full-value command", width)
		}
		repoStarts := func(c statusTUIContent) []int {
			var starts []int
			for _, row := range c.rows {
				if row.repo != nil {
					if row.end-row.start != 1 {
						t.Fatalf("repo row spans %d lines", row.end-row.start)
					}
					starts = append(starts, row.start)
				}
			}
			return starts
		}
		a, b := repoStarts(before), repoStarts(after)
		if len(a) != 10 || len(b) != 10 {
			t.Fatalf("width %d: repo rows %d/%d", width, len(a), len(b))
		}
		for i := range a {
			if a[i]-a[0] != i || b[i]-b[0] != i {
				t.Fatalf("width %d: repo rows not consecutive: %v / %v", width, a, b)
			}
		}
	}
}

// Before #534 each override added up to three lines, so a 24-line terminal
// showed five of ten repositories. All ten now fit the first viewport.
func TestStatusDenseViewportShowsAllRepos534(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	snapshot := statusDenseBaseFixture(statusDenseReasons[1])
	for _, width := range []int{40, 80, 120} {
		model := statusTUIModel{snapshot: snapshot, width: width, height: 24}
		content := model.contentLayout()
		v := model.viewport(content)
		visible := 0
		for _, row := range content.rows {
			if row.repo != nil && row.start >= v.header && row.start < v.header+v.body {
				visible++
			}
		}
		if visible != 10 {
			t.Fatalf("width %d: %d of 10 repositories in the first 24-line viewport", width, visible)
		}
		view := ansi.Strip(model.View().Content)
		if strings.Contains(view, "Coordinated") {
			t.Fatalf("width %d: viewport shows base reason prose", width)
		}
	}
}

// Non-interactive output has no ? key: it gets one bounded pointer line, and
// Markdown and JSON keep the complete values.
func TestStatusDensePlainAndStructuredOutput534(t *testing.T) {
	reason := statusDenseReasons[1]
	snapshot := statusDenseBaseFixture(reason)
	for _, width := range statusDenseWidths {
		table := statusTable(snapshot, true, width)
		lines := strings.Split(table, "\n")
		notes := 0
		for _, line := range lines {
			if ansi.StringWidth(line) > width {
				t.Fatalf("width %d: plain line too wide: %q", width, line)
			}
			if strings.HasPrefix(line, statusTUIBaseMarker+" collection base") {
				notes++
			}
		}
		if notes != 1 || strings.Contains(table, "Coordinated") || strings.Contains(table, "\x1b") {
			t.Fatalf("width %d: plain output notes=%d:\n%s", width, notes, table)
		}
	}
	if note := statusTUIBaseOverrideNote(snapshot, 200); len(note) != 2 || !strings.Contains(note[1], "repo base show <repo>") || !strings.Contains(note[1], "(10)") {
		t.Fatalf("plain note = %q", note)
	}
	if statusTUIBaseOverrideNote(statusDenseWithoutBases(snapshot), 80) != nil || statusTUIBaseOverrideDetails(statusDenseWithoutBases(snapshot)) != nil {
		t.Fatal("rows without overrides gained base output")
	}
	if md := snapshot.ReposMarkdown(); strings.Count(md, strings.TrimSpace(reason)) != 10 {
		t.Fatal("Markdown no longer carries each full reason")
	}
	data, err := json.Marshal(snapshot)
	if err != nil || strings.Count(string(data), `"reason":`) < 10 || !strings.Contains(string(data), "Coordinated beta") {
		t.Fatalf("JSON lost base reasons: %v", err)
	}
}

func TestRepositoryBaseStatusMappingAndScrolling(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	url := "https://example.invalid/pull/7"
	snapshot := wtc.StatusSnapshot{Collection: "fixture", Repos: []wtc.StatusRepo{
		{Collection: "fixture", Repo: "first", Dir: "first", BranchDisplay: "topic", DevelopmentBase: &wtc.RepositoryBase{Source: "collection", EffectiveRef: "origin/βeta", Reason: strings.Repeat("界 staging ", 40)}},
		{Collection: "fixture", Repo: "widget", Dir: "widget", BranchDisplay: "topic", PR: &wtc.StatusPRFacts{Number: "7", URL: url}, Runtime: "ready"},
	}, Runtime: []wtc.RuntimeItem{{Collection: "fixture", Repo: "widget", Dir: "widget", Path: "widget/web/server", Group: "widget/web", Kind: "service", State: "ready"}, {Collection: "fixture", Repo: "widget", Dir: "widget", Path: "widget/web/tunnel", Group: "widget/web", Kind: "endpoint", State: "ready", URL: "https://example.invalid", Exposure: wtc.ExposureProtected, ExposureBasis: wtc.ExposureDeclared}}, PRs: []wtc.StatusPRRow{{Repo: "widget", Number: "7", URL: &url}}}
	for _, width := range []int{24, 40, 80, 120} {
		for _, styled := range []bool{false, true} {
			indices := map[string]int{}
			var starts []int
			lines := statusTUIRepoLines(snapshot, width, styled, func(i int, r wtc.StatusRepo) { indices[r.Repo] = i; starts = append(starts, i) })
			assertStatusDenseSection(t, fmt.Sprintf("mapping width %d", width), lines, starts, 2, width)
			if indices["widget"] != indices["first"]+1 {
				t.Fatalf("following repo shifted: %+v", indices)
			}
		}
		model := statusTUIModel{snapshot: snapshot, width: width, height: 8}
		content := model.contentLayout()
		for scroll := 0; scroll <= len(content.lines); scroll++ {
			model.scroll = scroll
			v := model.viewport(content)
			for _, row := range content.rows {
				y := row.start - v.scroll
				if y < v.header || y >= v.header+v.body {
					continue
				}
				got, ok := model.visibleRow(1, y)
				if !ok || got.key != row.key {
					t.Fatalf("scroll mapping %s => %+v", row.key, got)
				}
				if row.pr != nil && model.buildClickTarget(1, y) != url {
					t.Fatal("following PR click shifted")
				}
				if row.repo != nil && row.repo.Repo == "widget" {
					layout := statusTUIRepoLayout(snapshot, width)
					if model.buildClickTarget(layout.prStart(), y) != url {
						t.Fatal("following repository PR click shifted")
					}
					if layout.showRuntime {
						if item, ok := model.runtimeItemAt(layout.runtimeStart(), y); !ok || item.Kind != "service" {
							t.Fatal("following repository service click shifted")
						}
					}
				}
				if row.service != nil {
					layout := statusTUIRuntimeLayout(snapshot, width)
					if target := model.buildClickTarget(layout.tunnelStart("")+2, y); target != "https://example.invalid" && width >= 80 {
						t.Fatalf("service link shifted: %s", target)
					}
				}
			}
		}
	}
}
