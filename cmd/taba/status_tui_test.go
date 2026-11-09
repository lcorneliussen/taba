package main

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"
	"taba.sh/taba/internal/wtc"
)

func statusTestMessage(t *testing.T, command tea.Cmd) tea.Msg {
	t.Helper()
	messages := make(chan tea.Msg, 1)
	go func() { messages <- command() }()
	select {
	case message := <-messages:
		return message
	case <-time.After(3 * time.Second):
		t.Fatal("TUI refresh event did not arrive")
		return nil
	}
}

func TestStatusTUILinksColorsAndClickTargets(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "")
	prURL := "https://github.com/example/widget/pull/7"
	buildURL := "https://example.invalid/build/42"
	passed := "SUCCESS"
	merged := "MERGED"
	model := statusTUIModel{width: 100, height: 24, snapshot: wtc.StatusSnapshot{Collection: "fixture",
		Repos: []wtc.StatusRepo{{Dir: "widget", BranchDisplay: "feature", Tree: "clean",
			PR:  &wtc.StatusPRFacts{Number: "7", URL: prURL, Checks: "SUCCESS"},
			Tip: &wtc.StatusBuild{Checks: &passed, URL: &buildURL}}},
		PRs: []wtc.StatusPRRow{{Repo: "widget", Number: "7", Title: "Change widget", URL: &prURL, Merge: &merged}}}}
	view := model.View().Content
	if strings.Count(view, ansi.SetHyperlink(prURL)) < 2 || !strings.Contains(view, ansi.SetHyperlink(buildURL)) ||
		!strings.Contains(view, "\x1b[36m") || !strings.Contains(view, "\x1b[1m") {
		t.Fatalf("TUI lost terminal links or visual hierarchy: %q", view)
	}
	for _, line := range strings.Split(view, "\n") {
		if ansi.StringWidth(line) > model.width {
			t.Fatalf("styled line exceeded terminal width: %q", line)
		}
	}
	if got := model.buildClickTarget(54, 3); got != prURL {
		t.Fatalf("repo PR cell click = %q", got)
	}
	if got := model.buildClickTarget(83, 3); got != buildURL {
		t.Fatalf("build cell click = %q", got)
	}
	if got := model.buildClickTarget(5, 7); got != prURL {
		t.Fatalf("PR list row click = %q", got)
	}
	branchOnly := statusTUIModel{width: 80, height: 16, snapshot: wtc.StatusSnapshot{Collection: "fixture",
		Repos: []wtc.StatusRepo{{Dir: "widget", BranchDisplay: "feature", PR: &wtc.StatusPRFacts{Number: "7", URL: prURL}}}}}
	if branchOnly.View().MouseMode != tea.MouseModeCellMotion || branchOnly.buildClickTarget(statusTUIRepoLayout(branchOnly.snapshot, 80).prStart(), 3) != prURL {
		t.Fatal("discovered branch PR did not enable its ordinary click target")
	}
	t.Setenv("NO_COLOR", "1")
	view = model.View().Content
	if strings.Contains(view, "\x1b[36m") || !strings.Contains(view, ansi.SetHyperlink(prURL)) {
		t.Fatal("NO_COLOR suppressed links or retained styling")
	}
	t.Setenv("TERM", "dumb")
	dumbView := model.View()
	if strings.Contains(dumbView.Content, "\x1b") || dumbView.MouseMode != tea.MouseModeNone || dumbView.ReportFocus || model.buildClickTarget(54, 3) != "" {
		t.Fatalf("TERM=dumb emitted terminal controls or enabled mouse input: %+v", dumbView)
	}
}

func TestStatusTUIShowsOpenPRsBeforeDrafts(t *testing.T) {
	snapshot := wtc.StatusSnapshot{PRs: []wtc.StatusPRRow{
		{Number: "1", State: "DRAFT", Draft: true},
		{Number: "2", State: "OPEN"},
		{Number: "3", State: "DRAFT", Draft: true},
		{Number: "4", State: "OPEN"},
	}}
	visible, _ := statusTUIVisiblePRs(snapshot, false)
	for i, want := range []string{"2", "4", "1", "3"} {
		if visible[i].Number != want {
			t.Fatalf("visible PR order = %+v", visible)
		}
	}
}

func TestStatusTUIRejectsTerminalControlsInLinks(t *testing.T) {
	if got := statusTUIURL("https://example.invalid/\u009bunsafe"); got != "" {
		t.Fatalf("C1 control in terminal link target: %q", got)
	}
	if got := statusTUISafe("label\u009bcell"); got != "label cell" {
		t.Fatalf("C1 control in terminal label: %q", got)
	}
}

func TestStatusTUIRepoBranchAndBuildLinksForBothForges(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "")
	passed, failed, pending := "SUCCESS", "FAILURE", "PENDING"
	githubBuild := "https://github.com/example/widget/actions/runs/42"
	githubPending := "https://github.com/example/widget/actions/runs/43"
	bitbucketBuild := "https://bitbucket.org/example/gadget/pipelines/results/687"
	snapshot := wtc.StatusSnapshot{Collection: "fixture", Repos: []wtc.StatusRepo{
		{Dir: "widget", Slug: "example/widget", Forge: "github.com", Branch: "feature/topic", BranchDisplay: "feature/topic",
			Tip:  &wtc.StatusBuild{Checks: &passed, Build: statusStringPtr("42"), URL: &githubBuild},
			Prod: &wtc.StatusBuild{Checks: &pending, Build: statusStringPtr("43"), URL: &githubPending}},
		{Dir: "gadget", Slug: "example/gadget", Forge: "bitbucket.org", Branch: "main", BranchDisplay: "⌂ main", BranchKind: "detached",
			Tip: &wtc.StatusBuild{Checks: &failed, Build: statusStringPtr("687"), URL: &bitbucketBuild}},
	}}
	model := statusTUIModel{width: 100, height: 20, snapshot: snapshot}
	view := model.View().Content
	for _, target := range []string{"https://github.com/example/widget", "https://github.com/example/widget/tree/feature/topic",
		"https://bitbucket.org/example/gadget", "https://bitbucket.org/example/gadget/src/main/", githubBuild, githubPending, bitbucketBuild} {
		if !strings.Contains(view, ansi.SetHyperlink(target)) {
			t.Fatalf("missing terminal link %q", target)
		}
	}
	if strings.Contains(view, ";4;") || !strings.Contains(view, "T✓#42") || !strings.Contains(view, "P●#43") || !strings.Contains(view, "T✗#687") ||
		!strings.Contains(view, ansi.SetHyperlink("https://github.com/example/widget")+"\x1b[1m") ||
		!strings.Contains(view, ansi.SetHyperlink("https://github.com/example/widget/tree/feature/topic")+"\x1b[0m") ||
		!strings.Contains(view, ansi.SetHyperlink("https://bitbucket.org/example/gadget/src/main/")+"\x1b[2m") ||
		!strings.Contains(view, ansi.SetHyperlink(githubBuild)+"\x1b[32m") ||
		!strings.Contains(view, ansi.SetHyperlink(githubPending)+"\x1b[33m") ||
		!strings.Contains(view, ansi.SetHyperlink(bitbucketBuild)+"\x1b[31m") {
		t.Fatalf("secondary links or build result tones missing: %q", view)
	}
	layout := statusTUIRepoLayout(snapshot, 100)
	for _, check := range []struct {
		x, y int
		url  string
	}{{1, 3, "https://github.com/example/widget"}, {layout.name + 1, 3, "https://github.com/example/widget/tree/feature/topic"},
		{layout.tipStart(), 3, githubBuild}, {1, 4, "https://bitbucket.org/example/gadget"},
		{layout.name + 1, 4, "https://bitbucket.org/example/gadget/src/main/"}, {layout.tipStart(), 4, bitbucketBuild}} {
		if got := model.buildClickTarget(check.x, check.y); got != check.url {
			t.Fatalf("click (%d,%d) = %q, want %q", check.x, check.y, got, check.url)
		}
	}
	bad := wtc.StatusRepo{Slug: "example/widget", Forge: "unknown.example", Branch: "main"}
	if statusTUIRepoURL(bad) != "" || statusTUIBranchURL(bad) != "" {
		t.Fatal("unsupported forge produced a link")
	}
}

func statusStringPtr(value string) *string { return &value }

func TestStatusTUIHelpKeepsClickTargetsAligned(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	repoURL := "https://github.com/example/widget"
	branchURL := repoURL + "/tree/topic"
	prURL := repoURL + "/pull/7"
	buildURL := repoURL + "/actions/runs/42"
	snapshot := wtc.StatusSnapshot{Collection: "fixture",
		Repos: []wtc.StatusRepo{{Dir: "widget", Slug: "example/widget", Forge: "github.com", Branch: "topic", BranchDisplay: "topic",
			PR: &wtc.StatusPRFacts{Number: "7", URL: prURL}, Tip: &wtc.StatusBuild{URL: &buildURL}}},
		PRs: []wtc.StatusPRRow{{Repo: "widget", Number: "7", URL: &prURL, Title: "Synthetic change"}}}
	model := statusTUIModel{snapshot: snapshot, width: 100, height: 20, showHelp: true}
	layout := statusTUIRepoLayout(snapshot, 100)
	for _, check := range []struct {
		x, y int
		url  string
	}{{1, 7, repoURL}, {layout.name + 1, 7, branchURL}, {layout.prStart(), 7, prURL},
		{layout.tipStart(), 7, buildURL}, {1, 11, prURL}, {1, 6, ""}} {
		if got := model.buildClickTarget(check.x, check.y); got != check.url {
			t.Fatalf("click (%d,%d) = %q, want %q", check.x, check.y, got, check.url)
		}
	}
}

func TestStatusTUIMouseModeRequiresVisibleValidTarget(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	valid := "https://example.invalid/pull/7"
	merged := "MERGED"
	model := statusTUIModel{width: 80, height: 16, snapshot: wtc.StatusSnapshot{Collection: "fixture",
		PRs: []wtc.StatusPRRow{{Repo: "widget", Number: "7", URL: &valid, Merge: &merged, Archived: true}}}}
	if model.View().MouseMode != tea.MouseModeNone {
		t.Fatal("hidden archived PR captured mouse input")
	}
	model.showArchived = true
	if model.View().MouseMode != tea.MouseModeCellMotion {
		t.Fatal("visible PR link did not enable mouse input")
	}
	model.reposOnly = true
	if model.View().MouseMode != tea.MouseModeNone {
		t.Fatal("hidden PR section captured mouse input")
	}
	model.reposOnly = false
	invalid := "javascript:alert(1)"
	model.snapshot.PRs[0].URL = &invalid
	if model.View().MouseMode != tea.MouseModeNone {
		t.Fatal("invalid PR URL captured mouse input")
	}
	model.snapshot.PRs = nil
	model.snapshot.Repos = []wtc.StatusRepo{{Dir: "widget", Tip: &wtc.StatusBuild{}}}
	if model.View().MouseMode != tea.MouseModeNone {
		t.Fatal("build cell without URL captured mouse input")
	}
	model.width = 25
	model.snapshot.Repos = []wtc.StatusRepo{{Dir: "widget", PR: &wtc.StatusPRFacts{Number: "7", URL: valid}}}
	if model.View().MouseMode != tea.MouseModeCellMotion || model.buildClickTarget(21, 3) != valid {
		t.Fatalf("visible PR cell in a narrow terminal was not clickable: %q", model.View().Content)
	}
	model.width = 80
	model.snapshot.Repos[0].PR.URL = "HTTPS://example.invalid/pull/7"
	if model.View().MouseMode != tea.MouseModeCellMotion {
		t.Fatal("valid uppercase URL scheme did not enable visible PR click")
	}
}

func TestStatusTUIClickTargetExcludesFooterAndOffscreenPR(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	first := "https://example.invalid/pull/7"
	second := "https://example.invalid/pull/8"
	m := statusTUIModel{width: 80, height: 10, snapshot: wtc.StatusSnapshot{Collection: "fixture",
		Repos: []wtc.StatusRepo{{Dir: "widget"}},
		PRs:   []wtc.StatusPRRow{{Repo: "widget", Number: "7", URL: &first}, {Repo: "widget", Number: "8", URL: &second}}}}
	if m.View().MouseMode != tea.MouseModeCellMotion || m.buildClickTarget(2, 7) != first {
		t.Fatal("visible PR row was not clickable")
	}
	if got := m.buildClickTarget(2, 8); got != "" {
		t.Fatalf("footer click opened offscreen PR: %q", got)
	}
}

func TestStatusTUIRefreshStreamsCollectorProgressAndCompletion(t *testing.T) {
	model := statusTUIModel{snapshot: wtc.StatusSnapshot{Collection: "fixture"}, width: 80, height: 16}
	model.startRefresh()
	command := statusTUIRefreshWithCollector(&wtc.Context{}, false, func(c *wtc.Context) statusLoadedMsg {
		c.StatusProgress("Checking remote refs")
		return statusLoadedMsg{snapshot: wtc.StatusSnapshot{Collection: "fixture", Schema: 1}, at: time.Now()}
	})
	started := statusTestMessage(t, command)
	updated, wait := model.Update(started)
	model = updated.(statusTUIModel)
	progress := statusTestMessage(t, wait)
	updated, wait = model.Update(progress)
	model = updated.(statusTUIModel)
	if !strings.Contains(model.View().Content, "Checking remote refs") {
		t.Fatalf("collector progress did not reach the TUI: %s", model.View().Content)
	}
	loaded := statusTestMessage(t, wait)
	updated, _ = model.Update(loaded)
	model = updated.(statusTUIModel)
	if model.refreshing || model.snapshot.Schema != 1 {
		t.Fatalf("collector completion did not reach the TUI: %+v", model)
	}

	model.startRefresh()
	command = statusTUIRefreshWithCollector(&wtc.Context{}, false, func(*wtc.Context) statusLoadedMsg {
		return statusLoadedMsg{err: errors.New("fixture failure"), at: time.Now()}
	})
	started = statusTestMessage(t, command)
	updated, wait = model.Update(started)
	model = updated.(statusTUIModel)
	loaded = statusTestMessage(t, wait)
	updated, _ = model.Update(loaded)
	model = updated.(statusTUIModel)
	if model.refreshing || !strings.Contains(model.errorText, "fixture failure") ||
		!strings.Contains(strings.Join(model.progressLog, "\n"), "Refresh failed") {
		t.Fatalf("collector failure did not reach the TUI: %+v", model)
	}
}

func TestStatusTUIRepoRowsKeepSignalsWithinWidth(t *testing.T) {
	snapshot := wtc.StatusSnapshot{Collection: "fixture", Repos: []wtc.StatusRepo{{
		Dir: "widget", BranchDisplay: "topic", Tree: "±2", Ahead: 1, Behind: 3,
		PR: &wtc.StatusPRFacts{Number: "7", Checks: "SUCCESS", Merge: "BEHIND", Review: "waiting"},
	}}}
	lines := statusTUIRepoLines(snapshot, 60, false)
	if len(lines) != 2 || !strings.Contains(lines[1], "#7 ✓ ↓ …") ||
		!strings.Contains(lines[0], "±    ↑   ↓") || !strings.Contains(lines[1], "±2   1   3") {
		t.Fatalf("repo signals missing: %q", lines)
	}
	if fitted := statusTUIFit("⌂ development-tip", 8); runewidth.StringWidth(fitted) != 8 || !strings.HasSuffix(fitted, "…") {
		t.Fatalf("wide branch clipping failed: %q", fitted)
	}
}

func TestStatusTUIConflictsUseThemeColorsWithoutExtraColumn(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "")
	conflicting := "DIRTY"
	other := "CONFLICTING"
	if got := statusTUIPRGlyph(&other); got != "✗" {
		t.Fatalf("conflicting merge glyph = %q", got)
	}
	snapshot := wtc.StatusSnapshot{Collection: "fixture", Repos: []wtc.StatusRepo{{
		Dir: "widget", BranchDisplay: "topic", Behind: 3, Tree: "±2", Conflict: true, Operation: "MERGE_HEAD",
		PR: &wtc.StatusPRFacts{Number: "7", Merge: conflicting},
	}}, PRs: []wtc.StatusPRRow{{Repo: "widget", Number: "7", Merge: &conflicting}}}
	lines := statusTUIRepoLines(snapshot, 60, true)
	if !strings.Contains(ansi.Strip(lines[1]), "#7 ✗ conflict") || !strings.Contains(ansi.Strip(lines[1]), "✗3") ||
		strings.Count(lines[1], "\x1b[31m") < 2 || strings.Contains(lines[1], "38;5;") {
		t.Fatalf("repo conflict missing or color not themeable: %q", lines)
	}
	prs := statusTUIPRLines(snapshot, 60, false, true)
	if !strings.Contains(ansi.Strip(prs[3]), "✗ conflict") || !strings.Contains(prs[3], "\x1b[31m") {
		t.Fatalf("PR conflict missing: %q", prs)
	}
}

func TestStatusPRConflictVisibleWithGreenChecksAndCleanCheckout(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "")
	conflicting, passed := "CONFLICTING", "SUCCESS"
	snapshot := wtc.StatusSnapshot{Collection: "fixture", Repos: []wtc.StatusRepo{{
		Dir: "widget", BranchDisplay: "topic", Tree: "clean", Behind: 1234,
		PR: &wtc.StatusPRFacts{Number: "7", URL: "https://github.com/example/widget/pull/7", Checks: passed, Merge: conflicting},
	}}, PRs: []wtc.StatusPRRow{{Repo: "widget", Number: "7", Checks: &passed, Merge: &conflicting}}}
	for _, styled := range []bool{false, true} {
		for _, width := range []int{32, 60, 100} {
			lines := statusTUIRepoLines(snapshot, width, styled)
			row := ansi.Strip(lines[1])
			if !strings.Contains(row, "✗") || !strings.Contains(row, "#7") {
				t.Fatalf("PR conflict hidden at width %d, styled %t: %q", width, styled, row)
			}
			if width >= 60 && (!strings.Contains(row, "✗ conflict ✓") || !strings.Contains(row, "✗1234")) {
				t.Fatalf("conflict or count hidden: %q", row)
			}
			if ansi.StringWidth(lines[1]) > width {
				t.Fatalf("row exceeds width %d: %q", width, row)
			}
			if strings.Contains(row, "✗") && strings.Contains(row, "±") {
				t.Fatalf("PR conflict fabricated a dirty tree: %q", row)
			}
		}
	}
	prs := strings.Join(statusTUIPRLines(snapshot, 80, false, false), "\n")
	if !strings.Contains(prs, "✗ conflict") || !strings.Contains(prs, "✓ ✗") {
		t.Fatalf("passing checks hidden in PR list: %q", prs)
	}
	for _, styled := range []bool{false, true} {
		cell := statusTUIRepoPRCell(&wtc.StatusPRFacts{Number: "1234567", URL: snapshot.Repos[0].PR.URL, Merge: conflicting}, 8, styled)
		if !strings.HasPrefix(ansi.Strip(cell), "✗ ") || ansi.StringWidth(cell) > 8 {
			t.Fatalf("compact cell hides conflict: %q", cell)
		}
	}
	snapshot.Repos[0].Behind = 0
	if got := statusTUIBehind(snapshot.Repos[0]); got != "✗" {
		t.Fatalf("zero-behind PR conflict hidden: %q", got)
	}
}

func TestStatusLongConflictingPRFitsAndKeepsClickTarget(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "")
	prURL := "https://github.com/example/widget/pull/1234567"
	pr := &wtc.StatusPRFacts{Number: "1234567", URL: prURL, Merge: "CONFLICTING", Checks: "SUCCESS"}
	plain := statusTUIPR(pr)
	plainWidth := runewidth.StringWidth(plain)
	if got := statusTUIRepoPRCell(pr, plainWidth, false); got != plain {
		t.Fatalf("plain conflict cell should fit exactly: %q", got)
	}
	if got := ansi.Strip(statusTUIRepoPRCell(pr, plainWidth, true)); !strings.HasPrefix(got, "✗ ") {
		t.Fatalf("hyperlink arrow should trigger compact conflict cell: %q", got)
	}
	for _, number := range []string{"1234567", "123456789012345"} {
		snapshot := wtc.StatusSnapshot{Collection: "fixture", Repos: []wtc.StatusRepo{{
			Dir: "widget", BranchDisplay: "topic", Tree: "±1234", Ahead: 1234, Behind: 1234,
			PR: &wtc.StatusPRFacts{Number: number, URL: prURL, Merge: "CONFLICTING", Checks: "SUCCESS"},
		}}}
		for width := 20; width <= 120; width++ {
			layout := statusTUIRepoLayout(snapshot, width)
			for _, styled := range []bool{false, true} {
				lines := statusTUIRepoLines(snapshot, width, styled)
				for _, line := range lines {
					if ansi.StringWidth(line) > width {
						t.Fatalf("PR %s overflows width %d (styled %t): %q", number, width, styled, line)
					}
				}
				if !strings.Contains(ansi.Strip(lines[1]), "✗") {
					t.Fatalf("conflict hidden at width %d: %q", width, lines[1])
				}
			}
			model := statusTUIModel{snapshot: snapshot, width: width, height: 20}
			if got := model.buildClickTarget(layout.prStart(), 3); got != prURL {
				t.Fatalf("PR click at width %d: %q", width, got)
			}
		}
	}
}

func TestStatusTUIHidesBuildColumnsWithoutFactsAndMutesMergedPRs(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "")
	merged, passed := "MERGED", "SUCCESS"
	url := "https://example.invalid/pull/8"
	snapshot := wtc.StatusSnapshot{Collection: "fixture",
		Repos: []wtc.StatusRepo{{Dir: "widget", BranchDisplay: "main", Tree: "clean"}},
		PRs: []wtc.StatusPRRow{{Repo: "widget", Number: "8", Title: "Finished work", URL: &url,
			Merge: &merged, Checks: &passed}}}
	repos := statusTUIRepoLines(snapshot, 100, false)
	if !strings.Contains(repos[0], "±") || !strings.Contains(repos[0], "↑") ||
		!strings.Contains(repos[0], "↓") || strings.Contains(repos[0], "TEST") ||
		strings.Contains(repos[0], "PROD") || runewidth.StringWidth(repos[0]) > 100 {
		t.Fatalf("build columns shown without build facts: %q", repos[0])
	}
	prodOnly := snapshot
	prodOnly.Repos = []wtc.StatusRepo{{Dir: "widget", BranchDisplay: "main", Tree: "clean",
		Prod: &wtc.StatusBuild{Branch: "main", Checks: &passed}}}
	repos = statusTUIRepoLines(prodOnly, 100, false)
	if strings.Contains(repos[0], "TEST") || !strings.Contains(repos[0], "PROD") || !strings.Contains(repos[1], "P✓") {
		t.Fatalf("a production-only provider should show only PROD: %q", repos)
	}
	prs := statusTUIPRLines(snapshot, 100, false, true)
	if len(prs) != 4 || !strings.Contains(ansi.Strip(prs[2]), "PR") ||
		!strings.Contains(ansi.Strip(prs[2]), "STATE") ||
		!strings.Contains(prs[3], "\x1b[2m") ||
		!strings.Contains(prs[3], "\x1b[2m") ||
		!strings.Contains(prs[3], ansi.SetHyperlink(url)) ||
		strings.Contains(ansi.Strip(prs[3]), "✓") {
		t.Fatalf("merged PR row was not aligned, muted and linked: %q", prs)
	}
}

func TestStatusTUIResponsiveLayoutKeepsCountsAndClickColumns(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	prURL, buildURL := "https://example.invalid/pull/7", "https://example.invalid/build/7"
	snapshot := wtc.StatusSnapshot{Collection: "fixture", Repos: []wtc.StatusRepo{{
		Dir: "widget", BranchDisplay: "topic", Tree: "±1234", Ahead: 1234, Behind: 2345,
		PR: &wtc.StatusPRFacts{Number: "7", URL: prURL}, Tip: &wtc.StatusBuild{URL: &buildURL},
	}}}
	wide := statusTUIRepoLines(snapshot, 100, false)
	if !strings.Contains(wide[1], "±1234") || !strings.Contains(wide[1], "1234") ||
		!strings.Contains(wide[1], "2345") || strings.Contains(wide[1], "12…") {
		t.Fatalf("large counts were truncated: %q", wide[1])
	}
	for _, width := range []int{72, 55, 45} {
		layout := statusTUIRepoLayout(snapshot, width)
		lines := statusTUIRepoLines(snapshot, width, false)
		if runewidth.StringWidth(lines[0]) > width || runewidth.StringWidth(lines[1]) > width {
			t.Fatalf("repo table overflows %d columns: %q", width, lines)
		}
		model := statusTUIModel{snapshot: snapshot, width: width, height: 20}
		if got := model.buildClickTarget(layout.prStart(), 3); got != prURL {
			t.Fatalf("PR click at width %d = %q", width, got)
		}
		if layout.showBuilds {
			if got := model.buildClickTarget(layout.tipStart(), 3); got != buildURL {
				t.Fatalf("build click at width %d = %q", width, got)
			}
		} else if got := model.buildClickTarget(layout.tipStart(), 3); got != "" {
			t.Fatalf("hidden build column at width %d was clickable: %q", width, got)
		}
	}
	breakpoints := []struct {
		width, name  int
		builds, sync bool
	}{{100, 20, true, true}, {72, 16, true, true}, {71, 16, false, true},
		{55, 16, false, true}, {54, 16, false, false}, {45, 10, false, false}, {44, 10, false, false}}
	plain := snapshot
	plain.Repos = []wtc.StatusRepo{{Dir: "widget", BranchDisplay: "topic", Tree: "clean",
		PR: &wtc.StatusPRFacts{Number: "7", URL: prURL}, Tip: &wtc.StatusBuild{URL: &buildURL}}}
	for _, want := range breakpoints {
		layout := statusTUIRepoLayout(plain, want.width)
		header := statusTUIRepoLines(plain, want.width, false)[0]
		if layout.name != want.name || layout.showBuilds != want.builds || layout.showSync != want.sync ||
			(strings.Contains(header, "TEST") != want.builds) || (strings.Contains(header, "↑") != want.sync) {
			t.Fatalf("unexpected layout at %d: %+v %q", want.width, layout, header)
		}
	}
}

func TestStatusTUIPRTablePlacesActiveRowsFirstAndWarnsOnBranch(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "")
	merged, passed := "MERGED", "SUCCESS"
	oldURL, activeURL, staleURL := "https://example.invalid/pull/1", "https://example.invalid/pull/2", "https://example.invalid/pull/3"
	snapshot := wtc.StatusSnapshot{Collection: "fixture", Repos: []wtc.StatusRepo{{Dir: "widget"}},
		PRs: []wtc.StatusPRRow{
			{Repo: "widget", Number: "1", Title: "Finished", URL: &oldURL, Merge: &merged},
			{Repo: "widget", Number: "2", Title: "In progress", DisplayTitle: "Display title", URL: &activeURL, Draft: true, Checks: &passed},
			{Repo: "widget", Number: "3", Title: "Needs update", URL: &staleURL, Merge: &merged, Checks: &passed, OnBranch: true},
		}}
	lines := statusTUIPRLines(snapshot, 100, false, true)
	if !strings.Contains(ansi.Strip(lines[3]), "#2") || !strings.Contains(ansi.Strip(lines[3]), "Display title") ||
		!strings.Contains(ansi.Strip(lines[3]), "◇ draft") ||
		!strings.Contains(ansi.Strip(lines[4]), "#3") || !strings.Contains(ansi.Strip(lines[4]), "⚠ catch-up") ||
		!strings.Contains(lines[4], "\x1b[32m✓") ||
		!strings.Contains(ansi.Strip(lines[5]), "#1") {
		t.Fatalf("PR table did not prioritize active work: %q", lines)
	}
	rowText, headerText := ansi.Strip(lines[3]), ansi.Strip(lines[2])
	if runewidth.StringWidth(strings.Split(rowText, "✓")[0]) != runewidth.StringWidth(strings.Split(headerText, "C M R")[0]) {
		t.Fatalf("PR check glyph does not align with table header: %q", lines)
	}
	model := statusTUIModel{snapshot: snapshot, width: 100, height: 20}
	if model.buildClickTarget(1, 7) != activeURL || model.buildClickTarget(1, 8) != staleURL ||
		model.buildClickTarget(1, 9) != oldURL || model.buildClickTarget(8, 7) != "" {
		t.Fatal("PR table click targets disagree with rendered row order or number column")
	}
}

func TestStatusTUIPRTableShowsUnavailableForgeState(t *testing.T) {
	snapshot := wtc.StatusSnapshot{Collection: "fixture", PRs: []wtc.StatusPRRow{{Repo: "widget", Number: "7", State: "UNKNOWN", Title: "Synthetic change"}}}
	lines := statusTUIPRLines(snapshot, 80, false, false)
	if len(lines) != 4 || !strings.Contains(lines[3], "unknown") || strings.Contains(lines[3], "open") {
		t.Fatalf("unavailable forge state was shown as open: %q", lines)
	}
}

func TestStatusTUIPRTableFitsNarrowOneShotWidths(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	url := "https://example.invalid/pull/7"
	snapshot := wtc.StatusSnapshot{Collection: "fixture", PRs: []wtc.StatusPRRow{{
		Repo: "widget", Number: "7", Title: "A longer change title", URL: &url,
	}}}
	for _, width := range []int{24, 33, 34, 39, 40, 51, 52, 80, 100} {
		lines := statusTUIPRLines(snapshot, width, false, false)
		for _, line := range lines[2:] {
			if runewidth.StringWidth(line) > width {
				t.Fatalf("PR table line overflows %d columns: %q", width, line)
			}
		}
		if !strings.Contains(lines[3], "#7") || statusTUIPRLayout(width).title < 1 {
			t.Fatalf("PR number or title column disappeared at %d: %q", width, lines)
		}
	}
	model := statusTUIModel{snapshot: snapshot, width: 24, height: 20}
	if model.buildClickTarget(1, 7) != url || model.buildClickTarget(5, 7) != "" {
		t.Fatal("narrow PR number click guard disagreed with rendered table")
	}
}

func TestStatusOneShotTablesFitNarrowTerminals(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	url := "https://example.invalid/pull/7"
	snapshot := wtc.StatusSnapshot{Collection: "fixture", GeneratedAt: "2026-10-01T00:00:00Z", StaleCount: 1,
		Repos: []wtc.StatusRepo{{Dir: "widget", BranchDisplay: "feature/topic", Tree: "±2",
			PR: &wtc.StatusPRFacts{Number: "7", URL: url}}},
		PRs: []wtc.StatusPRRow{{Repo: "widget", Number: "7", Title: "Synthetic change", URL: &url},
			{Repo: "widget", Number: "6", Title: "Older change", Archived: true}},
		Orphans: []wtc.StatusOrphan{{Repo: "widget", Branch: "old-topic", State: "MERGED"}}}
	for _, width := range []int{24, 25, 30, 31, 39, 40} {
		for _, line := range strings.Split(strings.TrimSuffix(statusTable(snapshot, false, width), "\n"), "\n") {
			if runewidth.StringWidth(line) > width {
				t.Fatalf("one-shot table overflows %d columns: %q", width, line)
			}
		}
		layout := statusTUIRepoLayout(snapshot, width)
		model := statusTUIModel{snapshot: snapshot, width: width, height: 20}
		if got := model.buildClickTarget(layout.prStart(), 3); got != url {
			t.Fatalf("narrow repo PR click at width %d = %q", width, got)
		}
		if width == 31 || width == 40 {
			wideTree := snapshot
			wideTree.Repos = append([]wtc.StatusRepo(nil), snapshot.Repos...)
			wideTree.Repos[0].Tree = "±1000"
			for _, line := range strings.Split(strings.TrimSuffix(statusTable(wideTree, false, width), "\n"), "\n") {
				if runewidth.StringWidth(line) > width {
					t.Fatalf("large tree count overflows %d columns: %q", width, line)
				}
			}
		}
	}
	compact := strings.Join(statusTUIPRLines(snapshot, 24, false, false), "\n")
	expanded := strings.Join(statusTUIPRLines(snapshot, 24, true, false), "\n")
	if !strings.Contains(compact, "archived (1) · a") || strings.Contains(expanded, "archived (1) · a") ||
		!strings.Contains(expanded, "#6") {
		t.Fatalf("archive hint or toggle wrong: compact=%q expanded=%q", compact, expanded)
	}
}

func TestStatusTUIModelShowsCachedRowsAndControls(t *testing.T) {
	merged := "MERGED"
	model := statusTUIModel{snapshot: wtc.StatusSnapshot{Collection: "fixture", Repos: []wtc.StatusRepo{{Dir: "widget", BranchDisplay: "⌂ main", Tree: "clean"}},
		PRs: []wtc.StatusPRRow{{Repo: "widget", Number: "7", Title: "Old change", Merge: &merged, Archived: true}}},
		width: 90, height: 20, focused: true, interval: 30 * time.Second, background: 5 * time.Minute,
		lastRefresh: time.Now().Add(-time.Minute), nextRefresh: time.Now().Add(time.Minute)}
	view := model.View().Content
	if !strings.Contains(ansi.Strip(view), "taba/wtc status · fixture") || !strings.Contains(view, "widget") || !strings.Contains(view, "archived (1)") || strings.Contains(view, "Old change") {
		t.Fatalf("cached view wrong: %s", view)
	}
	updated, _ := model.Update(tea.KeyPressMsg(tea.Key{Code: 'a', Text: "a"}))
	model = updated.(statusTUIModel)
	if !model.showArchived || !strings.Contains(model.View().Content, "Old change") {
		t.Fatal("archive toggle did not reveal merged PR")
	}
	updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: '?', Text: "?"}))
	model = updated.(statusTUIModel)
	if !model.showHelp || !strings.Contains(model.View().Content, "PgUp/PgDn") ||
		!strings.Contains(model.View().Content, "C checks · M mergeability · R reviews") {
		t.Fatal("help toggle failed")
	}
	model.refreshing = true
	updated, _ = model.Update(statusLoadedMsg{snapshot: wtc.StatusSnapshot{Collection: "fixture"}, at: time.Now()})
	model = updated.(statusTUIModel)
	if model.refreshing || len(model.snapshot.Repos) != 0 || model.nextRefresh.IsZero() {
		t.Fatalf("refresh state did not advance: %+v", model)
	}
}

func TestStatusTUIRefreshLogUpdatesWhileCollectorRuns(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	model := statusTUIModel{snapshot: wtc.StatusSnapshot{Collection: "fixture"}, width: 80, height: 16}
	model.startRefresh()
	events := make(chan tea.Msg)
	updated, next := model.Update(statusProgressMsg{message: "Checked pull requests 2/4", at: model.startedAt.Add(2 * time.Second), events: events})
	model = updated.(statusTUIModel)
	if next == nil || !strings.Contains(model.View().Content, "Checked pull requests 2/4") ||
		model.View().MouseMode != tea.MouseModeCellMotion {
		t.Fatalf("refresh progress was not visible or clickable: %s", model.View().Content)
	}
	refreshAt := runewidth.StringWidth(strings.Split(model.headerLine(), "refreshing")[0])
	updated, _ = model.Update(tea.MouseClickMsg{X: refreshAt, Y: 0})
	model = updated.(statusTUIModel)
	if !model.showLog || !strings.Contains(model.View().Content, "2s  Checked pull requests 2/4") {
		t.Fatalf("refresh click did not open log: %s", model.View().Content)
	}
	updated, _ = model.Update(statusLoadedMsg{snapshot: wtc.StatusSnapshot{Collection: "fixture"}, at: model.startedAt.Add(3 * time.Second)})
	model = updated.(statusTUIModel)
	if model.refreshing || !strings.Contains(model.View().Content, "3s  Refresh finished") {
		t.Fatalf("refresh completion did not appear in log: %s", model.View().Content)
	}
}

func TestStatusTUIRefreshKeepsTablePositionAndCoalescesCounts(t *testing.T) {
	model := statusTUIModel{snapshot: wtc.StatusSnapshot{Collection: "fixture", Repos: []wtc.StatusRepo{{Dir: "widget", BranchDisplay: "main"}}}, width: 36, height: 14}
	before := model.View().Content
	if !strings.Contains(strings.Split(before, "\n")[3], "widget") {
		t.Fatalf("repository did not start on the expected row: %q", before)
	}
	model.startRefresh()
	events := make(chan tea.Msg)
	for i := 1; i <= 15; i++ {
		updated, _ := model.Update(statusProgressMsg{message: fmt.Sprintf("Checked pull requests %d/15", i), at: model.startedAt.Add(time.Duration(i) * time.Second), events: events})
		model = updated.(statusTUIModel)
	}
	during := model.View().Content
	if !strings.Contains(strings.Split(during, "\n")[3], "widget") || len(model.progressLog) != 2 ||
		!strings.Contains(model.progressLog[1], "15/15") || strings.Contains(strings.Join(model.progressLog, "\n"), "1/15") {
		t.Fatalf("refresh shifted the table or repeated count entries: %q; log=%q", during, model.progressLog)
	}
	for _, line := range strings.Split(during, "\n") {
		if ansi.StringWidth(line) > model.width {
			t.Fatalf("refresh line exceeds terminal width: %q", line)
		}
	}
	model.width = 24
	if line := strings.Split(model.View().Content, "\n")[1]; !strings.Contains(line, "15/15") || ansi.StringWidth(line) > model.width {
		t.Fatalf("narrow progress hid its count or wrapped: %q", line)
	}
	updated, _ := model.Update(statusLoadedMsg{snapshot: model.snapshot, at: model.startedAt.Add(16 * time.Second)})
	model = updated.(statusTUIModel)
	if !strings.Contains(strings.Split(model.View().Content, "\n")[3], "widget") {
		t.Fatalf("repository shifted after refresh: %q", model.View().Content)
	}
}

func TestStatusTUIRefreshLogSeparatesCountedStages(t *testing.T) {
	model := statusTUIModel{width: 40, height: 12}
	model.startRefresh()
	events := make(chan tea.Msg)
	for i, message := range []string{"Fetching remote refs 1", "Fetching remote refs 2", "Reading worktrees 1/2", "Reading worktrees 2/2"} {
		updated, _ := model.Update(statusProgressMsg{message: message, at: model.startedAt.Add(time.Duration(i+1) * time.Second), events: events})
		model = updated.(statusTUIModel)
	}
	if len(model.progressLog) != 3 || !strings.Contains(model.progressLog[1], "refs 2") ||
		!strings.Contains(model.progressLog[2], "worktrees 2/2") {
		t.Fatalf("counted stages were not kept separately: %q", model.progressLog)
	}
}

func TestStatusTUIRefreshLogKeepsNewestEntryVisible(t *testing.T) {
	model := statusTUIModel{snapshot: wtc.StatusSnapshot{Collection: "fixture"}, width: 80, height: 12, showLog: true, showHelp: true}
	model.startRefresh()
	for i := 0; i < 20; i++ {
		model.progressLog = append(model.progressLog, "step")
		model.focusLogTail()
	}
	model.progressLog = append(model.progressLog, "latest progress")
	model.focusLogTail()
	if !strings.Contains(model.View().Content, "latest progress") {
		t.Fatalf("latest progress hidden while refreshing: %s", model.View().Content)
	}
	updated, _ := model.Update(statusLoadedMsg{snapshot: wtc.StatusSnapshot{Collection: "fixture"}, at: model.startedAt.Add(time.Second)})
	model = updated.(statusTUIModel)
	if !strings.Contains(model.View().Content, "Refresh finished") {
		t.Fatalf("completion hidden after refresh: %s", model.View().Content)
	}
}

func TestStatusTUIRefreshLogReportsFetchFallback(t *testing.T) {
	model := statusTUIModel{snapshot: wtc.StatusSnapshot{Collection: "fixture"}, width: 80, height: 12, showLog: true}
	model.startRefresh()
	updated, _ := model.Update(statusLoadedMsg{snapshot: wtc.StatusSnapshot{Collection: "fixture"},
		fetched: wtc.StatusFetchReport{Attempted: 2, Failed: 1}, at: model.startedAt.Add(time.Second)})
	model = updated.(statusTUIModel)
	if !strings.Contains(model.View().Content, "1 ref refresh failed; showing local refs") {
		t.Fatalf("failed fetch was hidden from refresh log: %s", model.View().Content)
	}
}

func TestStatusTUIClicksOnlyBuildCells(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	tipURL, prodURL := "https://example.invalid/build/7", "https://example.invalid/build/8"
	snapshot := wtc.StatusSnapshot{Collection: "fixture", Repos: []wtc.StatusRepo{{Dir: "widget", BranchDisplay: "topic",
		Tip: &wtc.StatusBuild{Branch: "main", URL: &tipURL}, Prod: &wtc.StatusBuild{Branch: "prod", URL: &prodURL}}}}
	m := statusTUIModel{snapshot: snapshot, width: 80, height: 20}
	if m.View().MouseMode != tea.MouseModeCellMotion {
		t.Fatal("build URLs did not enable mouse input")
	}
	if got := m.buildClickTarget(63, 3); got != tipURL {
		t.Fatalf("tip cell target: %q", got)
	}
	if got := m.buildClickTarget(72, 3); got != prodURL {
		t.Fatalf("prod cell target: %q", got)
	}
	for _, x := range []int{0, 20, 40, 62} {
		if got := m.buildClickTarget(x, 3); got != "" {
			t.Fatalf("non-build cell opened URL at %d: %q", x, got)
		}
	}
	m.scroll = 999
	if got := m.buildClickTarget(63, 3); got != tipURL {
		t.Fatalf("click map disagreed with clamped view scroll: %q", got)
	}
	m.noClick = true
	if m.View().MouseMode != tea.MouseModeNone {
		t.Fatal("--no-click did not disable mouse mode")
	}
}

func TestStatusOneShotTableCanHidePRSection(t *testing.T) {
	snapshot := wtc.StatusSnapshot{Collection: "fixture", GeneratedAt: "2026-09-30T00:00:00Z",
		Repos: []wtc.StatusRepo{{Dir: "widget", BranchDisplay: "topic", Tree: "clean"}},
		PRs:   []wtc.StatusPRRow{{Repo: "widget", Number: "7", Title: "Synthetic PR"}}}
	full := statusTable(snapshot, false, 80)
	repos := statusTable(snapshot, true, 80)
	if !strings.Contains(full, "Synthetic PR") || strings.Contains(repos, "Synthetic PR") || !strings.Contains(repos, "widget") {
		t.Fatalf("repository-only table wrong: full=%s repos=%s", full, repos)
	}
}

func statusOverflowModel() statusTUIModel {
	m := statusTUIModel{width: 100, height: 12, snapshot: wtc.StatusSnapshot{Collection: "overflow"}}
	for i := 0; i < 36; i++ {
		name := fmt.Sprintf("repo-%02d", i)
		m.snapshot.Repos = append(m.snapshot.Repos, wtc.StatusRepo{Collection: "overflow", Repo: name, Dir: name,
			Slug: "example/" + name, Forge: "github.com", BranchDisplay: "main"})
	}
	for i := 0; i < 20; i++ {
		url := fmt.Sprintf("https://example.test/pull/%d", 100+i)
		m.snapshot.PRs = append(m.snapshot.PRs, wtc.StatusPRRow{Repo: "repo-00", Number: fmt.Sprint(100 + i),
			Title: fmt.Sprintf("Synthetic change %d", i), URL: &url})
	}
	return m
}

func statusScrollEvent(t *testing.T, m statusTUIModel, event tea.Msg) statusTUIModel {
	t.Helper()
	updated, command := m.Update(event)
	if command != nil {
		t.Fatal("scroll, resize or snapshot update dispatched work")
	}
	return updated.(statusTUIModel)
}

func TestStatusTUIOverflowKeyboardAndWheel(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	m := statusOverflowModel()
	initial := m.View().Content
	m = statusScrollEvent(t, m, tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	if m.View().Content == initial {
		t.Fatal("wheel did not scroll overflowing content")
	}
	for i := 0; i < 150; i++ {
		m = statusScrollEvent(t, m, tea.KeyPressMsg{Code: 'j'})
	}
	bottom := m.View().Content
	if !strings.Contains(ansi.Strip(bottom), "#119") || !strings.Contains(strings.Split(ansi.Strip(bottom), "\n")[0], "status · overflow") {
		t.Fatalf("last PR or pinned status header is inaccessible: %s", bottom)
	}
	m = statusScrollEvent(t, m, tea.KeyPressMsg{Code: 'k'})
	if m.View().Content == bottom {
		t.Fatal("excess down presses left scroll beyond the bottom")
	}
	m = statusScrollEvent(t, m, tea.KeyPressMsg{Code: tea.KeyHome})
	if m.View().Content != initial {
		t.Fatal("Home did not return to the initial viewport")
	}
	m = statusScrollEvent(t, m, tea.KeyPressMsg{Code: tea.KeyPgDown})
	if m.View().Content == initial {
		t.Fatal("Page Down did not move the viewport")
	}
	m = statusScrollEvent(t, m, tea.KeyPressMsg{Code: tea.KeyPgUp})
	if m.View().Content != initial {
		t.Fatal("Page Up did not undo one page movement")
	}
	m = statusScrollEvent(t, m, tea.KeyPressMsg{Code: tea.KeyEnd})
	if m.View().Content != bottom {
		t.Fatal("End did not reach the last viewport")
	}
	m = statusScrollEvent(t, m, tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	if m.View().Content == bottom {
		t.Fatal("wheel could not leave the bottom viewport")
	}
}

func TestStatusTUIOverflowRowsAndClicksStayTogether(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	m := statusOverflowModel()
	m.showHelp = true
	m.snapshot.Runtime = []wtc.RuntimeItem{{Collection: "overflow", Repo: "repo-00", Dir: "repo-00", Path: "repo-00/web/server",
		Group: "repo-00/web", Kind: "service", State: "ready", URL: "http://localhost:43000"}}
	wanted, seen := map[string]string{}, map[string]bool{}
	for _, r := range m.snapshot.Repos {
		wanted[r.Dir] = "https://github.com/" + r.Slug
	}
	for _, r := range m.snapshot.PRs {
		wanted["#"+r.Number] = *r.URL
	}
	serviceSeen := false
	for step := 0; step < 110; step++ {
		for y, line := range strings.Split(ansi.Strip(m.View().Content), "\n") {
			words := strings.Fields(line)
			if len(words) > 0 && wanted[words[0]] != "" {
				if got := m.buildClickTarget(1, y); got != wanted[words[0]] {
					t.Fatalf("visible %s opens %q, want %q", words[0], got, wanted[words[0]])
				}
				seen[words[0]] = true
			}
			if at := strings.Index(line, ":43000"); at >= 0 {
				if got := m.buildClickTarget(runewidth.StringWidth(line[:at]), y); got != "http://localhost:43000" {
					t.Fatalf("scrolled service URL opened %q", got)
				}
				serviceSeen = true
			}
		}
		if m.buildClickTarget(1, 0) != "" || m.buildClickTarget(1, m.height-2) != "" {
			t.Fatal("header or footer opened an offscreen row")
		}
		m = statusScrollEvent(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	}
	if len(seen) != len(wanted) || !serviceSeen {
		t.Fatalf("unreachable rows: saw %d of %d, service %v", len(seen), len(wanted), serviceSeen)
	}
}

func TestStatusTUIOverflowRefreshAnchorAndResize(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	m := statusOverflowModel()
	for i := 0; i < 15; i++ {
		m = statusScrollEvent(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	}
	before := strings.Split(ansi.Strip(m.View().Content), "\n")
	name := strings.Fields(before[2])[0]
	fresh := m.snapshot
	fresh.Repos = append([]wtc.StatusRepo{{Collection: "overflow", Repo: "inserted", Dir: "inserted"}}, fresh.Repos...)
	m = statusScrollEvent(t, m, statusLoadedMsg{snapshot: fresh, at: time.Now()})
	if after := strings.Split(ansi.Strip(m.View().Content), "\n"); !strings.HasPrefix(after[2], name+" ") {
		t.Fatalf("refresh lost the visible repository anchor %q: %s", name, m.View().Content)
	}
	for _, size := range []struct{ width, height int }{{25, 5}, {80, 3}, {8, 1}, {80, 2}, {100, 12}, {100, 80}} {
		m = statusScrollEvent(t, m, tea.WindowSizeMsg{Width: size.width, Height: size.height})
		lines := strings.Split(m.View().Content, "\n")
		if len(lines) > size.height {
			t.Fatalf("%dx%d view has %d physical rows", size.width, size.height, len(lines))
		}
		for _, line := range lines {
			if ansi.StringWidth(line) > size.width {
				t.Fatalf("view wraps outside %d columns: %q", size.width, line)
			}
		}
	}
	if !strings.Contains(ansi.Strip(m.View().Content), "inserted") {
		t.Fatal("expanded viewport did not clamp back to the beginning")
	}
}

func TestStatusTUIOverflowNoClickAndWrappedLog(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	m := statusOverflowModel()
	m.noClick = true
	if m.View().MouseMode == tea.MouseModeNone {
		t.Fatal("--no-click disabled overflow wheel input")
	}
	m = statusScrollEvent(t, m, tea.MouseClickMsg{X: 1, Y: 3, Button: tea.MouseLeft})
	m = statusScrollEvent(t, m, tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	m.showLog, m.width, m.height = true, 30, 8
	m.progressLog = []string{strings.Repeat("synthetic diagnostic ", 20) + "final-marker"}
	m.focusLogTail()
	if !strings.Contains(ansi.Strip(m.View().Content), "final-marker") {
		t.Fatal("wrapped diagnostic tail is inaccessible")
	}
	t.Setenv("TERM", "dumb")
	if m.View().MouseMode != tea.MouseModeNone || strings.Contains(m.View().Content, "\x1b") {
		t.Fatal("dumb terminal received mouse or styling controls")
	}
	short := statusTUIModel{width: 80, height: 40, snapshot: wtc.StatusSnapshot{Collection: "empty"}}
	initial := short.View().Content
	for i := 0; i < 5; i++ {
		short = statusScrollEvent(t, short, tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	}
	if short.View().Content != initial {
		t.Fatal("short view overscrolled")
	}
}

func TestStatusTUIOverflowProcessView(t *testing.T) {
	m := statusTUIModel{procs: true, width: 80, height: 8}
	for i := 0; i < 40; i++ {
		m.processes = append(m.processes, wtc.StatusProcess{PID: 100 + i, Command: fmt.Sprintf("synthetic-process-%02d", i)})
	}
	m = statusScrollEvent(t, m, tea.KeyPressMsg{Code: tea.KeyEnd})
	text := ansi.Strip(m.View().Content)
	if !strings.Contains(text, "status · processes") || !strings.Contains(text, "synthetic-process-39") {
		t.Fatalf("process rows/header are inaccessible: %s", text)
	}
	m = statusScrollEvent(t, m, tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	if ansi.Strip(m.View().Content) == text || m.buildClickTarget(1, 3) != "" {
		t.Fatal("process scrolling stayed stuck or offered a repository action")
	}
}

func TestStatusTUIFollowStateNamesWaitingTier(t *testing.T) {
	for _, tc := range []struct {
		follow      *wtc.StatusFollow
		state, tone string
	}{
		{nil, "merged", statusToneDim},
		{&wtc.StatusFollow{Stage: "tip", Checks: "WAITING"}, "→ test", statusToneWarning},
		{&wtc.StatusFollow{Stage: "prod", Checks: "PENDING"}, "● prod", statusToneWarning},
		{&wtc.StatusFollow{Stage: "tip", Checks: "FAILURE"}, "✗ test", statusToneFailure},
		{&wtc.StatusFollow{Stage: "prod", Checks: "ERROR"}, "✗ prod", statusToneFailure},
		{&wtc.StatusFollow{Stage: "tip", Checks: "UNKNOWN"}, "? test", statusToneDim},
		{&wtc.StatusFollow{Stage: "done", Checks: "SUCCESS"}, "✓ built", statusToneSuccess},
	} {
		if state, tone := statusTUIFollowState(tc.follow); state != tc.state || tone != tc.tone {
			t.Fatalf("%+v: got %q/%q, want %q/%q", tc.follow, state, tone, tc.state, tc.tone)
		}
	}
	merged := "MERGED"
	snapshot := wtc.StatusSnapshot{Collection: "fixture", PRs: []wtc.StatusPRRow{{Repo: "widget", Number: "8",
		State: "MERGED", Merge: &merged, Title: "Shipped", Follow: &wtc.StatusFollow{Stage: "prod", Checks: "WAITING"}}}}
	if prs := statusTUIPRLines(snapshot, 100, false, false); !strings.Contains(prs[3], "→ prod") {
		t.Fatalf("PR table should show the tier the merge waits on: %q", prs)
	}
}

func TestStatusTableShowsRetiredLeftoversInWorkspaceSweep(t *testing.T) {
	snapshot := wtc.StatusSnapshot{Schema: 1, ShowCollectionColumn: true,
		Retired: []wtc.StatusRetired{{Folder: "old-task", BlockingPaths: []string{"design", "evil\x1b[2J\nname"}}}}
	for _, reposOnly := range []bool{false, true} {
		if table := statusTable(snapshot, reposOnly, 80); !strings.Contains(table, "retired old-task: leftovers design") {
			t.Fatalf("reposOnly=%v table omits leftovers:\n%s", reposOnly, table)
		}
		if table := statusTable(snapshot, reposOnly, 80); strings.Contains(table, "\x1b[2J") || strings.Contains(table, "evil\nname") {
			t.Fatalf("control characters reached the terminal: %q", table)
		}
	}
}
