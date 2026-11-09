package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/lcorneliussen/wtc-cli/internal/wtc"
	"github.com/spf13/cobra"
)

func hoursTestLocal(t *testing.T) {
	t.Helper()
	t.Setenv("OPENCLAW_STATE_DIR", "")
	t.Setenv("OPENCLAW_SHELL", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
}
func TestWorkingHoursCLIRequiresExplicitSetupAndSharesProject(t *testing.T) {
	hoursTestLocal(t)
	c := &wtc.Context{Collection: t.TempDir(), ConfigRoot: t.TempDir()}
	invoke := func(args ...string) error {
		root := &cobra.Command{Use: "wtc", SilenceUsage: true, SilenceErrors: true}
		asJSON := true
		addWorkingHoursCommand(root, &asJSON, func() (*wtc.Context, error) { return c, nil })
		root.SetArgs(append([]string{"hours"}, args...))
		return root.Execute()
	}
	if err := invoke("setup"); err == nil {
		t.Fatal("setup guessed a schedule")
	}
	path := filepath.Join(t.TempDir(), "synthetic.json")
	if err := os.WriteFile(path, []byte(`{"timezone":"Etc/UTC","windows":[{"days":[1,3],"start":"10:00","end":"16:00"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := invoke("setup", "--file", path); err != nil {
		t.Fatal(err)
	}
	if err := invoke("extend"); err == nil {
		t.Fatal("extension guessed a duration")
	}
	if err := invoke("extend", "--for", "5h"); err == nil {
		t.Fatal("unbounded extension")
	}
	if err := invoke("extend", "--for", "30m"); err != nil {
		t.Fatal(err)
	}
	sibling := &wtc.Context{Collection: t.TempDir(), ConfigRoot: c.ConfigRoot}
	f, err := localHoursFacts(sibling, time.Now())
	if err != nil || f.Phase != "extended" {
		t.Fatal(f, err)
	}
	if err := invoke("end-extension"); err != nil {
		t.Fatal(err)
	}
	if err := invoke("edit", "--project", "--file", path); err != nil {
		t.Fatal(err)
	}
	if err := invoke("clear", "--project"); err != nil {
		t.Fatal(err)
	}
	if err := invoke("clear"); err != nil {
		t.Fatal(err)
	}
	f, err = localHoursFacts(c, time.Now())
	if err != nil || f.Configured {
		t.Fatal(f, err)
	}
}
func TestWorkingHoursOpenClawNeverUsesGatewayPerson(t *testing.T) {
	hoursTestLocal(t)
	c := &wtc.Context{Collection: t.TempDir(), ConfigRoot: t.TempDir()}
	store, err := localHoursStore(c)
	if err != nil {
		t.Fatal(err)
	}
	p := wtc.WorkingSchedule{Timezone: "Etc/UTC", Windows: []wtc.WorkingWindow{{Days: []int{1}, Start: "10:00", End: "16:00"}}}
	if err := store.Setup(p, false); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"OPENCLAW_STATE_DIR", "OPENCLAW_SHELL"} {
		t.Run(key, func(t *testing.T) {
			value := "synthetic"
			if key == "OPENCLAW_SHELL" {
				value = "exec"
			}
			t.Setenv(key, value)
			if _, err := localHoursStore(c); err == nil {
				t.Fatal("gateway identity accepted")
			}
			f, err := localHoursFacts(c, time.Now())
			if err != nil || f.Configured || f.Phase != "unconfigured" || f.Timezone != "" || strings.Contains(f.Summary(), "setup") || f.UnavailableReason == "" {
				t.Fatal("leaked gateway schedule", f, err)
			}
		})
	}
}
func TestWorkingHoursTUIRefreshesPhaseWithoutForgeRefresh(t *testing.T) {
	hoursTestLocal(t)
	c := &wtc.Context{Collection: t.TempDir(), ConfigRoot: t.TempDir()}
	m := statusTUIModel{context: c, width: 120}
	m.refreshHours(time.Now())
	if m.hoursHint != "Set working hours…" {
		t.Fatal("setup affordance should stay compact", m.hoursHint)
	}
	if got := strings.Join(m.contentLines(), "\n"); !strings.Contains(got, "Set working hours") {
		t.Fatal(got)
	}
	store, err := localHoursStore(c)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Setup(wtc.WorkingSchedule{Timezone: "Etc/UTC", Windows: []wtc.WorkingWindow{{Days: []int{1}, Start: "10:00", End: "16:00"}}}, false); err != nil {
		t.Fatal(err)
	}
	if err := store.Extend(time.Now(), time.Hour); err != nil {
		t.Fatal(err)
	}
	m.refreshHours(time.Now())
	if m.hoursHint != "Working hours: extended" {
		t.Fatal("phase affordance should stay compact", m.hoursHint)
	}
	if got := strings.Join(m.contentLines(), "\n"); !strings.Contains(got, "extended") {
		t.Fatal(got)
	}
	if err := store.Clear(false); err != nil {
		t.Fatal(err)
	}
	m.refreshHours(time.Now())
	if got := strings.Join(m.contentLines(), "\n"); !strings.Contains(got, "Set working hours") {
		t.Fatal(got)
	}
}

func TestWorkingHoursHintPreservesTUIRowAndClickMapping(t *testing.T) {
	hoursTestLocal(t)
	t.Setenv("TERM", "xterm-256color")
	c := &wtc.Context{Collection: t.TempDir(), ConfigRoot: t.TempDir()}
	first, second := "https://github.com/example/first", "https://github.com/example/second"
	m := statusTUIModel{context: c, width: 120, height: 24, snapshot: wtc.StatusSnapshot{Collection: "synthetic", Repos: []wtc.StatusRepo{
		{Dir: "first", Slug: "example/first", Forge: "github.com", BranchDisplay: "main"}, {Dir: "second", Slug: "example/second", Forge: "github.com", BranchDisplay: "main"},
	}}}
	m.refreshHours(time.Now())
	for _, help := range []bool{false, true} {
		m.showHelp = help
		row := 3
		if help {
			row += 4
		}
		lines := m.contentLines()
		if !strings.Contains(lines[row], "first") || !strings.Contains(lines[row+1], "second") {
			t.Fatal("hint shifted rows", lines)
		}
		if got := m.buildClickTarget(1, row); got != first {
			t.Fatalf("first click = %q", got)
		}
		if got := m.buildClickTarget(1, row+1); got != second {
			t.Fatalf("second click = %q", got)
		}
		if got := m.buildClickTarget(1, row-1); got != "" {
			t.Fatalf("header clickable: %q", got)
		}
		m.startRefresh()
		if !strings.Contains(m.contentLines()[row], "first") {
			t.Fatal("progress shifted table")
		}
		m.refreshing = false
	}
}

func TestWorkingHoursSecondaryRowAlignmentAndProgressPriority(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")
	hours := "Set working hours…"
	for _, tc := range []struct {
		name, progress, hours string
		width                 int
		visible               bool
	}{
		{"wide", "", hours, 80, true},
		{"exact", "", hours, ansi.StringWidth(hours), true},
		{"too narrow", "", hours, ansi.StringWidth(hours) - 1, false},
		{"progress fits", "↻ Checking", hours, 80, true},
		{"two cell gap", "↻ Checking", hours, ansi.StringWidth("↻ Checking") + ansi.StringWidth(hours) + 2, true},
		{"one cell gap hides", "↻ Checking", hours, ansi.StringWidth("↻ Checking") + ansi.StringWidth(hours) + 1, false},
		{"long progress", strings.Repeat("x", 80), hours, 80, false},
		{"wide glyph and ANSI", "↻ 確認", "\x1b[31mHours: 確認\x1b[0m", 40, true},
		{"no hours", "↻ Checking", "", 80, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			line := statusTUISecondaryLine(tc.progress, tc.hours, tc.width)
			plain := ansi.Strip(line)
			if !strings.HasPrefix(plain, tc.progress) {
				t.Fatalf("progress changed: %q", plain)
			}
			if tc.visible {
				if ansi.StringWidth(line) != tc.width || !strings.HasSuffix(plain, statusTUISafe(tc.hours)) {
					t.Fatalf("hours not right aligned: %q (width %d)", line, ansi.StringWidth(line))
				}
				if !strings.HasSuffix(line, statusTUIStyle(statusTUISafe(tc.hours), statusToneDim)) {
					t.Fatalf("hours not muted: %q", line)
				}
			} else if plain != tc.progress {
				t.Fatalf("hours should yield: %q", plain)
			}
			if strings.ContainsAny(plain, "\n\r") {
				t.Fatalf("row wrapped: %q", plain)
			}
		})
	}
}

func TestWorkingHoursSecondaryRowKeepsPrimaryHeaderAndTable(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")
	for _, width := range []int{16, 40, 80, 120} {
		m := statusTUIModel{width: width, snapshot: wtc.StatusSnapshot{Collection: "synthetic"}}
		before := m.contentLines()
		m.hoursHint = "Set working hours…"
		after := m.contentLines()
		if before[0] != after[0] || len(before) != len(after) || strings.Join(before[2:], "\n") != strings.Join(after[2:], "\n") {
			t.Fatalf("hours changed primary header or table at width %d", width)
		}
		m.refreshing, m.progressStep = true, "Checking remote refs"
		withHours := m.contentLines()
		m.hoursHint = ""
		withoutHours := m.contentLines()
		progress := ansi.Strip(withoutHours[1])
		if !strings.HasPrefix(ansi.Strip(withHours[1]), progress) {
			t.Fatalf("hours displaced progress at width %d", width)
		}
	}
}

func TestWorkingHoursStayOutOfStatusMarkdownAndDefaultCapturedOutput(t *testing.T) {
	hoursTestLocal(t)
	t.Setenv("WTC_CONFIG_ROOT", t.TempDir())
	t.Setenv("TABA_CONFIG_ROOT", "")
	collection := filepath.Join(t.TempDir(), "synthetic")
	if _, err := wtc.InitAdHoc(collection); err != nil {
		t.Fatal(err)
	}
	t.Chdir(collection)
	c, err := wtc.Discover(collection)
	if err != nil {
		t.Fatal(err)
	}
	store, err := localHoursStore(c)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Setup(wtc.WorkingSchedule{Timezone: "Etc/UTC", Windows: []wtc.WorkingWindow{{Days: []int{1}, Start: "10:00", End: "16:00"}}}, false); err != nil {
		t.Fatal(err)
	}
	for _, md := range []bool{false, true} {
		root := &cobra.Command{Use: "wtc", SilenceUsage: true, SilenceErrors: true}
		asJSON := false
		addStatusCommand(root, &asJSON)
		args := []string{"status", "--local", "--no-watch"}
		if md {
			args = append(args, "--md")
		}
		root.SetArgs(args)
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		original := os.Stdout
		os.Stdout = w
		err = root.Execute()
		os.Stdout = original
		w.Close()
		out, readErr := io.ReadAll(r)
		r.Close()
		if err != nil || readErr != nil {
			t.Fatal(err, readErr)
		}
		if !strings.HasPrefix(string(out), "# ") || strings.Contains(string(out), "Working hours:") || strings.Contains(string(out), "Etc/UTC") {
			t.Fatalf("private facts entered status Markdown: %s", out)
		}
	}
}
