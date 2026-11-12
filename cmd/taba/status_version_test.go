package main

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"taba.sh/taba/internal/wtc"
)

func TestStatusTUIRunningVersionSurvivesCollectorStates(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "")
	oldVersion := wtc.CLIVersion
	t.Cleanup(func() { wtc.CLIVersion = oldVersion })
	wtc.CLIVersion = "1.2.3"
	model := statusTUIModel{width: 80, height: 24, focused: true, interval: time.Minute}
	model.startRefresh()
	assertVersion := func(state string) {
		t.Helper()
		view := model.View().Content
		header := strings.TrimRight(ansi.Strip(strings.Split(view, "\n")[0]), " ")
		if header != model.headerLine()+" · 1.2.3" || !strings.HasPrefix(header, "taba/wtc status · ") || !strings.Contains(view, ansi.SetHyperlink("https://github.com/lcorneliussen/taba/releases/tag/v1.2.3")) {
			t.Fatalf("%s lost running version/link: %q", state, view)
		}
		if strings.Contains(strings.Split(ansi.Strip(view), "\n")[0], "9.9.9") {
			t.Fatalf("%s claimed cached producer as running version", state)
		}
	}
	assertVersion("before first result")
	model.snapshot = wtc.StatusSnapshot{Collection: "fixture", CLIVersion: "9.9.9"}
	assertVersion("cached startup")
	updated, _ := model.Update(statusLoadedMsg{snapshot: model.snapshot, at: time.Now()})
	model = updated.(statusTUIModel)
	assertVersion("loaded")
	model.startRefresh()
	assertVersion("refresh")
	updated, _ = model.Update(statusLoadedMsg{err: errors.New("offline"), at: time.Now()})
	model = updated.(statusTUIModel)
	assertVersion("error")
	model.showLog = true
	assertVersion("log")
	for _, width := range []int{1, 8, 16, 20, 40, 80} {
		model.width = width
		for _, line := range strings.Split(model.View().Content, "\n") {
			if ansi.StringWidth(line) > width {
				t.Fatalf("version header exceeds width %d: %q", width, line)
			}
		}
		if width >= 16 && !strings.HasPrefix(ansi.Strip(model.View().Content), "taba/wtc status") {
			t.Fatalf("primary title lost at width %d", width)
		}
	}
}

func TestStatusTUIVersionLinkPolicyAndRefreshClick(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "1")
	oldVersion := wtc.CLIVersion
	t.Cleanup(func() { wtc.CLIVersion = oldVersion })
	for _, version := range []string{"dev", "0.1.62-dev.7", "1.2.3-rc.1", "", "unknown", "1.2.3\x1b\a\u009b"} {
		wtc.CLIVersion = version
		model := statusTUIModel{width: 100, height: 24, refreshing: true}
		view := model.View().Content
		if strings.Contains(view, "\x1b") || strings.Contains(view, "\a") || strings.Contains(view, "\u009b") {
			t.Fatalf("unsafe or invented version link for %q: %q", version, view)
		}
	}
	wtc.CLIVersion = "1.2.3"
	model := statusTUIModel{width: 100, height: 24, refreshing: true, noClick: true}
	view := model.View().Content
	if !strings.Contains(view, ansi.SetHyperlink(wtc.StatusVersionNotesURL(wtc.CLIVersion))) {
		t.Fatal("NO_COLOR or --no-click suppressed native terminal hyperlink")
	}
	refreshAt := ansi.StringWidth(strings.Split(model.headerLine(), "refreshing")[0])
	updated, _ := model.Update(tea.MouseClickMsg{X: refreshAt, Y: 0, Button: tea.MouseLeft})
	if !updated.(statusTUIModel).showLog {
		t.Fatal("version header displaced refresh-log click target")
	}
	updated, command := model.Update(tea.MouseClickMsg{X: ansi.StringWidth(model.headerLine()) + 3, Y: 0, Button: tea.MouseLeft})
	if updated.(statusTUIModel).showLog || command != nil {
		t.Fatal("version suffix click toggled the refresh log")
	}
	t.Setenv("TERM", "dumb")
	if strings.Contains(model.View().Content, "\x1b") {
		t.Fatal("dumb terminal received version hyperlink controls")
	}
}

func TestStatusTUIVersionSuffixIsMutedAndYieldsToPrimaryHeading(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "")
	oldVersion := wtc.CLIVersion
	t.Cleanup(func() { wtc.CLIVersion = oldVersion })
	for _, version := range []string{"1.2.3", "dev", ""} {
		wtc.CLIVersion = version
		model := statusTUIModel{width: 100, height: 24, refreshing: true,
			lastRefresh: time.Now().Add(-3 * time.Second),
			snapshot:    wtc.StatusSnapshot{Collection: "fixture"}}
		primary := model.headerLine()
		label := wtc.RunningCLIVersion()
		header := strings.Split(model.View().Content, "\n")[0]
		if !strings.HasPrefix(primary, "taba/wtc status · fixture · ") || !strings.HasSuffix(primary, "s old · refreshing") ||
			strings.TrimRight(ansi.Strip(header), " ") != primary+" · "+label ||
			!strings.Contains(header, "\x1b[2m · "+label) {
			t.Fatalf("version %q is not a muted suffix after timing: %q", version, header)
		}
		if !strings.HasPrefix(ansi.Strip(model.contentLines()[2]), "REPO") {
			t.Fatal("version suffix added a row ahead of the repository table")
		}
		model.width = ansi.StringWidth(primary)
		header = strings.Split(model.View().Content, "\n")[0]
		if ansi.Strip(header) != primary || strings.Contains(header, "]8;;https") {
			t.Fatalf("low-priority suffix displaced primary heading: %q", header)
		}
		model.width += 5
		header = strings.Split(model.View().Content, "\n")[0]
		if !strings.HasPrefix(ansi.Strip(header), primary) || ansi.StringWidth(header) > model.width {
			t.Fatalf("partially clipped suffix displaced timing: %q", header)
		}
		if version == "1.2.3" && (!strings.Contains(header, ansi.SetHyperlink(wtc.StatusVersionNotesURL(version))) || !strings.Contains(header, ansi.ResetHyperlink())) {
			t.Fatalf("partially clipped release link is not terminated: %q", header)
		}
	}
}

func TestStatusTableUsesSnapshotProducer(t *testing.T) {
	snapshot := wtc.StatusSnapshot{CLIVersion: "9.9.9", Collection: "fixture"}
	snapshot.GeneratedAt = "2026-10-28T00:00:00Z"
	if got := statusTable(snapshot, false, 80); !strings.HasPrefix(got, "taba/wtc status · fixture · 2026-10-28T00:00:00Z · 9.9.9") {
		t.Fatalf("table lost snapshot producer: %q", got)
	}
	primary := "taba/wtc status · fixture · " + snapshot.GeneratedAt
	header := strings.Split(statusTable(snapshot, false, ansi.StringWidth(primary)), "\n")[0]
	if header != primary {
		t.Fatalf("one-shot version suffix displaced primary metadata: %q", header)
	}
}
