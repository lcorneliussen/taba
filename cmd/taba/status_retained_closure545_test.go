package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"taba.sh/taba/internal/wtc"
)

// retained545Flow is one synthetic collection whose provider answers are
// switched between collector passes: the fake gh/bb read their reply from
// files, so each pass runs the production collector against new provider
// state without any network.
type retained545Flow struct {
	t      *testing.T
	c      *wtc.Context
	dir    string
	forge  string
	target string
}

func newRetained545Flow(t *testing.T, forge string) *retained545Flow {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("TMPDIR", t.TempDir())
	for _, name := range []string{"WTC_CONFIG_ROOT", "TABA_CONFIG_ROOT", "WTC_FORGE_MIN_INTERVAL", "TABA_FORGE_MIN_INTERVAL",
		"GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN", "GH_HOST", "GH_REPO", "XDG_CONFIG_HOME",
		"BB_API_USER", "BB_API_PASS", "BB_USERNAME", "BB_API_TOKEN", "ATLASSIAN_ACCOUNT_EMAIL", "ATLASSIAN_API_TOKEN"} {
		t.Setenv(name, "")
	}
	// gh's default storage for one github.com account: a known credential
	// context, so passes share their observations as a running pane does.
	ghConfig := t.TempDir()
	for name, body := range map[string]string{"hosts.yml": "github.com:\n    git_protocol: https\n    user: synthetic-user\n", "config.yml": "version: 1\n"} {
		if err := os.WriteFile(filepath.Join(ghConfig, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("GH_CONFIG_DIR", ghConfig)
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "")
	root := t.TempDir()
	config := filepath.Join(root, wtc.AdHocConfigDir)
	if err := os.MkdirAll(config, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(config, "repos.yml"), []byte("repos:\n  - name: widget\n    remote: https://"+forge+"/example/widget.git\n    default_ref: origin/main\n"), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := wtc.OpenCollection(root)
	if err != nil {
		t.Fatal(err)
	}
	segment := map[string]string{"github.com": "pull", "bitbucket.org": "pull-requests"}[forge]
	target := "https://" + forge + "/example/widget/" + segment + "/7"
	if err := os.WriteFile(c.PRFile(), []byte("widget 7 - "+target+" Saved title\n"), 0600); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0700); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$RETAINED545/calls\"\n" +
		"if [ -f \"$RETAINED545/fail\" ]; then echo 'synthetic provider failure' >&2; exit 7; fi\n" +
		"if [ \"$2\" = list ]; then cat \"$RETAINED545/list\"; exit 0; fi\n" +
		"cat \"$RETAINED545/view\"\n"
	for _, name := range []string{"gh", "bb"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("RETAINED545", dir)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	f := &retained545Flow{t: t, c: c, dir: dir, forge: forge, target: target}
	f.provider("[]", "")
	return f
}

func (f *retained545Flow) provider(list, view string) {
	f.t.Helper()
	for name, body := range map[string]string{"list": list, "view": view} {
		if err := os.WriteFile(filepath.Join(f.dir, name), []byte(body), 0600); err != nil {
			f.t.Fatal(err)
		}
	}
}

func (f *retained545Flow) enlist(number, title string) {
	f.t.Helper()
	target := strings.TrimSuffix(f.target, "/7") + "/" + number
	file, err := os.OpenFile(f.c.PRFile(), os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		f.t.Fatal(err)
	}
	defer file.Close()
	if _, err := file.WriteString("widget " + number + " - " + target + " " + title + "\n"); err != nil {
		f.t.Fatal(err)
	}
}

// budget exhausts (or restores) the shared request allowance.
func (f *retained545Flow) budget(exhausted bool) {
	interval := ""
	if exhausted {
		interval = "3600"
	}
	f.t.Setenv("WTC_FORGE_MIN_INTERVAL", interval)
	f.t.Setenv("TABA_FORGE_MIN_INTERVAL", interval)
}

func (f *retained545Flow) failing(on bool) {
	f.t.Helper()
	path := filepath.Join(f.dir, "fail")
	if on {
		if err := os.WriteFile(path, nil, 0600); err != nil {
			f.t.Fatal(err)
		}
		return
	}
	_ = os.Remove(path)
}

func (f *retained545Flow) foreground() wtc.StatusSnapshot {
	f.t.Helper()
	snapshot, err := f.c.StatusForegroundSnapshot(context.Background())
	if err != nil {
		f.t.Fatal(err)
	}
	return snapshot
}

func (f *retained545Flow) background() wtc.StatusSnapshot {
	f.t.Helper()
	snapshot, _, err := f.c.StatusLiveSnapshotContext(context.Background(), false, true, time.Hour)
	if err != nil {
		f.t.Fatal(err)
	}
	return snapshot
}

func retained545Row(t *testing.T, snapshot wtc.StatusSnapshot, number string) wtc.StatusPRRow {
	t.Helper()
	for _, row := range snapshot.PRs {
		if row.Number == number {
			return row
		}
	}
	t.Fatalf("PR %s missing: %+v", number, snapshot.PRs)
	return wtc.StatusPRRow{}
}

// retained545Expect is what one collector pass must show for PR 7.
type retained545Expect struct {
	state, freshness, reason string
}

// retained545Render checks PR 7 on every presentation surface: the native
// table at narrow and wide widths, with and without color, the Markdown
// status and the help detail. A known closure keeps its lifecycle label and
// strike, its freshness mark leads the title, and it shows no active check,
// merge or review signals; anything else is never closed.
func retained545Render(t *testing.T, step string, snapshot wtc.StatusSnapshot, want retained545Expect) wtc.StatusPRRow {
	t.Helper()
	row := retained545Row(t, snapshot, "7")
	if row.State != want.state || row.Freshness != want.freshness || row.ObservationReason != want.reason {
		t.Fatalf("%s: collector row state=%s freshness=%s reason=%s, want %+v", step, row.State, row.Freshness, row.ObservationReason, want)
	}
	if row.Draft || row.Archived || len(snapshot.Orphans) != 0 {
		t.Fatalf("%s: closure changed draft/archive/orphan facts: %+v orphans=%v", step, row, snapshot.Orphans)
	}
	closed := want.state == "CLOSED" || want.state == "DECLINED"
	if got := wtc.StatusPRKnownClosure(row, time.Now()); got != closed {
		t.Fatalf("%s: known closure %v, want %v", step, got, closed)
	}
	label := strings.ToLower(want.state)
	mark := wtc.StatusPRFreshnessMark(row.Freshness, row.ObservedAt, time.Now())
	for _, width := range []int{34, 80, 120} {
		for _, noColor := range []string{"", "1"} {
			t.Setenv("NO_COLOR", noColor)
			line, state := statusClosedStrikeRow(t, row, width)
			plain := ansi.Strip(line)
			if state != label {
				t.Fatalf("%s width=%d: STATE %q, want %q: %q", step, width, state, label, plain)
			}
			if struck := statusStrikeSGR.MatchString(line); struck != (closed && noColor == "") {
				t.Fatalf("%s width=%d NO_COLOR=%q: strike %v: %q", step, width, noColor, struck, line)
			}
			if mark != "" && !strings.Contains(plain, mark+" ") {
				t.Fatalf("%s width=%d: freshness %q missing: %q", step, width, mark, plain)
			}
			if width == 120 {
				if row.Title == "" || !strings.Contains(plain, row.Title) {
					t.Fatalf("%s: title displaced: %q", step, plain)
				}
				l := statusTUIPRLayout(width, row)
				start := l.number + 1 + l.repo + 1 + l.state + 1
				signals := strings.TrimSpace(ansi.Cut(plain, start, start+l.signalWidths[0]+l.signalWidths[1]+l.signalWidths[2]+3))
				// Only the neutral placeholder, as for completed merges.
				if closed && strings.Trim(signals, "· ") != "" {
					t.Fatalf("%s: closed row shows active signals %q: %q", step, signals, plain)
				}
			}
		}
	}
	markdown := snapshot.Markdown()
	if strings.Contains(markdown, " "+label+" ") != closed || strings.Contains(markdown, "◇ draft") {
		t.Fatalf("%s: Markdown lifecycle disagrees with the table:\n%s", step, markdown)
	}
	detail := wtc.StatusPRObservationDetail(row)
	if !strings.Contains(detail, "last "+label) || want.reason != "" && !strings.Contains(detail, want.reason) {
		t.Fatalf("%s: detail %q", step, detail)
	}
	return row
}

// #545: a known provider closure survives a deferred, stale or failed
// recheck as the last known lifecycle, with truthful freshness, until a real
// observation of a reopen replaces it. The provider's draft flag on a closed
// PR never makes it a draft, and closure grants no archive or branch action.
func TestStatusRetainedClosure545CollectorToRender(t *testing.T) {
	t.Run("github.com", func(t *testing.T) {
		f := newRetained545Flow(t, "github.com")
		closedAt := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
		f.provider("[]", `{"number":7,"state":"CLOSED","isDraft":true,"title":"Synthetic closure","closedAt":"`+closedAt+`"}`)
		live := f.foreground()
		retained545Render(t, "live", live, retained545Expect{"CLOSED", "live", ""})

		f.budget(true)
		f.enlist("8", "Never observed")
		deferred := f.foreground()
		row := retained545Render(t, "deferred", deferred, retained545Expect{"CLOSED", "deferred", "shared-budget"})
		if row.StateSource != "detail-cache" || !strings.Contains(wtc.StatusPRObservationDetail(row), "recheck reason shared-budget") {
			t.Fatalf("deferred closure provenance: %+v", row)
		}
		never := retained545Row(t, deferred, "8")
		if wtc.StatusPRKnownClosure(never, time.Now()) || never.ObservedAt != "" {
			t.Fatalf("never-observed row: %+v", never)
		}
		frame := strings.Join(statusTUIPRLines(deferred, 120, true, false), "\n")
		for _, line := range strings.Split(frame, "\n") {
			if strings.HasPrefix(line, "#8 ") && (strings.Contains(line, "closed") || !strings.Contains(line, "never")) {
				t.Fatalf("never-observed row claims a lifecycle: %q", line)
			}
		}
		// A pane holding the live closure keeps it through the deferred pass.
		retained545Render(t, "pane-deferred", wtc.StatusPreferNewerObservations(deferred, live), retained545Expect{"CLOSED", "deferred", "shared-budget"})

		stale := deferred
		stale.PRs = append([]wtc.StatusPRRow(nil), deferred.PRs...)
		for i := range stale.PRs {
			if stale.PRs[i].Number == "7" {
				stale.PRs[i].Freshness, stale.PRs[i].ObservationReason = "stale", "observation-expired"
				stale.PRs[i].ObservedAt = time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339Nano)
			}
		}
		retained545Render(t, "stale", stale, retained545Expect{"CLOSED", "stale", "observation-expired"})

		f.budget(false)
		f.failing(true)
		failed := f.foreground()
		retained545Render(t, "failed", failed, retained545Expect{"CLOSED", "unavailable", "refresh-failed"})

		f.failing(false)
		f.provider(`[{"number":7,"state":"OPEN","title":"Synthetic closure","isDraft":false}]`, `{"number":7,"state":"OPEN","title":"Synthetic closure","isDraft":false}`)
		reopened := f.foreground()
		retained545Render(t, "reopened", reopened, retained545Expect{"OPEN", "live", ""})
		retained545Render(t, "pane-reopened", wtc.StatusPreferNewerObservations(reopened, failed), retained545Expect{"OPEN", "live", ""})
	})
	t.Run("bitbucket.org", func(t *testing.T) {
		// A Bitbucket decline is recorded in the PR registry once observed and
		// holds through refused and failed passes until a read sees it open.
		f := newRetained545Flow(t, "bitbucket.org")
		f.provider("[]", `{"id":7,"state":"DECLINED","title":"Synthetic closure","updated_on":"`+time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)+`"}`)
		retained545Render(t, "live", f.foreground(), retained545Expect{"DECLINED", "live", ""})
		f.budget(true)
		retained545Render(t, "deferred", f.background(), retained545Expect{"DECLINED", "recorded", ""})
		f.budget(false)
		f.failing(true)
		retained545Render(t, "failed", f.foreground(), retained545Expect{"DECLINED", "recorded", ""})
		f.failing(false)
		f.provider("[]", `{"id":7,"state":"OPEN","title":"Synthetic closure"}`)
		retained545Render(t, "reopened", f.foreground(), retained545Expect{"OPEN", "live", ""})
	})
}
