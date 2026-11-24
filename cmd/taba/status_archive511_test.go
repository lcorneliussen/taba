package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"taba.sh/taba/internal/wtc"
)

func archive511TUIRow(repo, number, state, at string) wtc.StatusPRRow {
	url := "https://github.com/example/" + repo + "/pull/" + number
	row := wtc.StatusPRRow{Repo: repo, Number: number, State: state, Title: "synthetic " + number, Archived: true, Freshness: "live", URL: &url}
	if at != "" {
		if state == "MERGED" {
			row.MergedOn = &at
		} else {
			row.ClosedAt = &at
		}
	}
	return row
}

func archive511Positions(snapshot wtc.StatusSnapshot, showArchived bool) []string {
	var keys []string
	statusTUIPRLines(snapshot, 120, showArchived, true, func(_ int, row wtc.StatusPRRow) {
		if row.URL == nil || !strings.HasSuffix(*row.URL, "/"+row.Repo+"/pull/"+row.Number) {
			keys = append(keys, "mismatched-link:"+row.Repo+"#"+row.Number)
			return
		}
		keys = append(keys, row.Repo+"#"+row.Number)
	})
	return keys
}

func TestStatusArchive511TUIOrderToggleAndLinks(t *testing.T) {
	active := archive511TUIRow("widget", "40", "OPEN", "")
	active.Archived = false
	rows := []wtc.StatusPRRow{
		archive511TUIRow("widget", "9", "CLOSED", "2020-01-06T10:00:00Z"),
		active,
		archive511TUIRow("gadget", "20", "CLOSED", "2020-02-03T10:00:00Z"),
		archive511TUIRow("widget", "3", "MERGED", "2020-03-10T10:00:00Z"),
		archive511TUIRow("gadget", "5", "MERGED", "2020-02-03T10:00:00Z"),
		archive511TUIRow("tool", "4", "DECLINED", ""),
	}
	snapshot := wtc.StatusSnapshot{PRs: rows}
	hidden, count := statusTUIVisiblePRs(snapshot, false)
	if len(hidden) != 1 || hidden[0].Number != "40" || count != 5 {
		t.Fatalf("hidden archive: %+v %d", hidden, count)
	}
	want := "widget#40 widget#3 gadget#5 gadget#20 widget#9 tool#4"
	if got := strings.Join(archive511Positions(snapshot, true), " "); got != want {
		t.Fatalf("archive rows and link targets\n got: %s\nwant: %s", got, want)
	}
	// A refresh that returns the same facts in another inventory order keeps
	// every row and link target in place.
	shuffled := wtc.StatusSnapshot{PRs: []wtc.StatusPRRow{rows[5], rows[3], rows[0], rows[4], rows[1], rows[2]}}
	if got := strings.Join(archive511Positions(shuffled, true), " "); got != want {
		t.Fatalf("refresh reshuffled rows: %s", got)
	}
	if got := strings.Join(archive511Positions(shuffled, false), " "); got != "widget#40" {
		t.Fatalf("toggle off: %s", got)
	}
}

// The collector carries GitHub closedAt into the row, so a cached snapshot
// orders by it without another forge read.
func TestStatusArchive511CollectorCarriesClosedAt(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("WTC_CONFIG_ROOT", "")
	t.Setenv("TABA_CONFIG_ROOT", "")
	t.Setenv("WTC_FORGE_MIN_INTERVAL", "")
	t.Setenv("TABA_FORGE_MIN_INTERVAL", "")
	root := t.TempDir()
	config := filepath.Join(root, wtc.AdHocConfigDir)
	if err := os.MkdirAll(config, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(config, "repos.yml"), []byte("repos:\n  - name: widget\n    remote: https://github.com/example/widget.git\n    default_ref: origin/main\n"), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := wtc.OpenCollection(root)
	if err != nil {
		t.Fatal(err)
	}
	prs := "widget 9 - https://github.com/example/widget/pull/9 old closure\nwidget 12 - https://github.com/example/widget/pull/12 recent closure\n"
	if err := os.WriteFile(c.PRFile(), []byte(prs), 0600); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	script := `#!/bin/sh
case "$1 $2" in "pr list") echo '[]'; exit 0;; esac
case "$3" in
9) printf '%s' '{"number":9,"state":"CLOSED","title":"old closure","closedAt":"2020-01-06T10:00:00Z"}';;
12) printf '%s' '{"number":12,"state":"CLOSED","title":"recent closure","closedAt":"2020-03-02T10:00:00Z"}';;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	snapshot, err := c.StatusForegroundSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range snapshot.PRs {
		if !row.Archived || row.ClosedAt == nil {
			t.Fatalf("closure time lost in collection: %+v", row)
		}
	}
	if got := strings.Join(archive511Positions(snapshot, true), " "); got != "widget#12 widget#9" {
		t.Fatalf("collected archive order: %s", got)
	}
}

// A Bitbucket decline is dated by its activity-log decline event, not
// updated_on, and orders against a GitHub closure from the cached snapshot.
func TestStatusArchive511CollectorDatesBitbucketDecline(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("TMPDIR", t.TempDir())
	for _, name := range []string{"WTC_CONFIG_ROOT", "TABA_CONFIG_ROOT", "WTC_FORGE_MIN_INTERVAL", "TABA_FORGE_MIN_INTERVAL",
		"BB_API_USER", "BB_API_PASS", "BB_USERNAME", "BB_API_TOKEN", "ATLASSIAN_ACCOUNT_EMAIL", "ATLASSIAN_API_TOKEN"} {
		t.Setenv(name, "")
	}
	root := t.TempDir()
	config := filepath.Join(root, wtc.AdHocConfigDir)
	if err := os.MkdirAll(config, 0700); err != nil {
		t.Fatal(err)
	}
	repos := "repos:\n  - name: widget\n    remote: https://github.com/example/widget.git\n    default_ref: origin/main\n" +
		"  - name: gadget\n    remote: https://bitbucket.org/example/gadget.git\n    default_ref: origin/main\n"
	if err := os.WriteFile(filepath.Join(config, "repos.yml"), []byte(repos), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := wtc.OpenCollection(root)
	if err != nil {
		t.Fatal(err)
	}
	prs := "widget 9 - https://github.com/example/widget/pull/9 closure\ngadget 4 - https://bitbucket.org/example/gadget/pull-requests/4 decline\n"
	if err := os.WriteFile(c.PRFile(), []byte(prs), 0600); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	gh := `#!/bin/sh
case "$1 $2" in "pr list") echo '[]'; exit 0;; esac
printf '%s' '{"number":9,"state":"CLOSED","title":"closure","closedAt":"2020-02-01T00:00:00Z"}'
`
	// The decline sits before the GitHub closure; updated_on and a later
	// edit while declined come after it.
	bb := `#!/bin/sh
case "$*" in
*"pr view 4"*) printf '%s' '{"id":4,"state":"DECLINED","title":"decline","updated_on":"2020-06-01T00:00:00.000000+00:00"}';;
*"pr activity 4"*) printf '%s' '{"activities":[{"update":{"state":"OPEN","date":"2020-01-01T00:00:00Z"}},{"update":{"state":"DECLINED","date":"2020-01-15T00:00:00.000000+00:00"}},{"update":{"state":"DECLINED","date":"2020-05-01T00:00:00Z"}}]}';;
*) echo '{"values":[]}';;
esac
`
	for name, script := range map[string]string{"gh": gh, "bb": bb} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	snapshot, err := c.StatusForegroundSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, row := range wtc.StatusPRsByPriority(snapshot.PRs) {
		if !row.Archived || row.ClosedAt == nil {
			t.Fatalf("closure time lost in collection: %+v", row)
		}
		order = append(order, row.Repo+"#"+row.Number+"@"+*row.ClosedAt)
	}
	// updated_on or the later DECLINED edit would put the decline first.
	if got := strings.Join(order, " "); got != "widget#9@2020-02-01T00:00:00Z gadget#4@2020-01-15T00:00:00Z" {
		t.Fatalf("collected archive order: %s", got)
	}
}
