package main

import (
	"context"
	"fmt"
	"github.com/charmbracelet/x/ansi"
	"strings"
	"testing"
	"time"

	"taba.sh/taba/internal/wtc"
)

func TestStatusResourceNamedViewKeepsInvokingDevAuthority(t *testing.T) {
	t.Setenv("WTC_CONFIG_ROOT", "")
	t.Setenv("TABA_CONFIG_ROOT", "")
	t.Setenv("WTC_DEV_BUILD", "off")
	t.Setenv("TABA_DEV_BUILD", "off")
	invoking, _ := batchFixture(t)
	viewed, _ := batchFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for _, tc := range []struct{ cwd, want string }{{invoking.Collection, invoking.Collection}, {t.TempDir(), ""}} {
		t.Chdir(tc.cwd)
		m := newStatusTUIModel(viewed, false, false, false, true, true, false, time.Minute, 5*time.Minute, time.Hour, ctx, cancel)
		if m.devCollection != tc.want {
			t.Errorf("viewed collection changed invoking authority: got %q want %q", m.devCollection, tc.want)
		}
	}
}

func TestStatusResourceProcessesDateActualObservation(t *testing.T) {
	at := time.Now().Add(-2 * time.Second)
	m := statusTUIModel{procs: true, refreshing: true, interval: time.Minute, background: 5 * time.Minute}
	next, _ := m.Update(statusLoadedMsg{at: at})
	got := next.(statusTUIModel)
	if !got.lastRefresh.Equal(at) || strings.Contains(got.headerLine(), "age unknown") {
		t.Fatal("completed process observation lost its age")
	}
}

func TestStatusResourceUnknownAgeFocusExpiryKeepsBoundedSchedule(t *testing.T) {
	for _, stamp := range []string{"", "invalid", time.Now().Add(time.Hour).Format(time.RFC3339Nano)} {
		m := statusTUIModel{watcherPending: true, focused: true, observedUntil: time.Now().Add(-time.Second), lastRefresh: statusSnapshotTime(wtc.StatusSnapshot{GeneratedAt: stamp}), background: 5 * time.Minute}
		before := time.Now()
		next, _ := m.Update(statusTickMsg(before))
		got := next.(statusTUIModel)
		if !got.lastRefresh.IsZero() {
			t.Fatal("invalid saved age became fresh")
		}
		if got.nextRefresh.Before(before.Add(got.background)) {
			t.Fatal("unknown age scheduled background refresh in the past")
		}
	}
}

func TestStatusResourceViewKeepsRowsAndStyledTrailingCells(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")
	for _, width := range []int{34, 100} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			m := statusTUIModel{context: &wtc.Context{Collection: t.TempDir()}, width: width, height: 30, snapshot: wtc.StatusSnapshot{Collection: "fixture", Repos: []wtc.StatusRepo{{Repo: "widget", Dir: "widget", BranchDisplay: "topic", Tree: "clean"}}, PRs: []wtc.StatusPRRow{{Repo: "widget", Number: "7", State: "MERGED", Freshness: "recorded", Title: "Confirmed"}}}}
			m.hover = "sentinel   "
			plain := strings.Split(m.View().Content, "\n")
			m.hover = "\x1b[31msentinel   \x1b[0m"
			styled := strings.Split(m.View().Content, "\n")
			if len(plain) > m.height || len(plain) < 2 || len(styled) != len(plain) {
				t.Fatal("outer padding changed physical row count")
			}
			for i := range plain {
				if ansi.StringWidth(plain[i]) > width || ansi.StringWidth(styled[i]) > width {
					t.Fatal("padding wrapped a physical row")
				}
				if i < len(plain)-1 && plain[i] != styled[i] {
					t.Fatalf("footer styling shifted body row %d", i)
				}
			}
			for _, frame := range [][]string{plain, styled} {
				footer := frame[len(frame)-1]
				if !strings.HasPrefix(ansi.Strip(footer), "sentinel   ") || !strings.HasSuffix(footer, "\x1b[0m") {
					t.Fatalf("styled trailing cells or reset lost: %q", footer)
				}
				body := ansi.Strip(strings.Join(frame[:len(frame)-1], "\n"))
				if !strings.Contains(body, "widget") || !strings.Contains(body, "PRs") || !strings.Contains(body, "merged") {
					t.Fatal("padding removed repository/PR sections")
				}
			}
		})
	}
}
