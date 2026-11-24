package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
// updated_on, and orders against a GitHub closure. This also pins the
// refresh budget under twg with a known credential: a merged PR's activity is
// read once and then recorded, and a declined PR's once per updated_on, even
// after the response cache expires. A TWG profile, which has no local
// identity, leaves the decline unknown and reads no activity.
func TestStatusArchive511CollectorDatesBitbucketDecline(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("TMPDIR", t.TempDir())
	for _, name := range []string{"WTC_CONFIG_ROOT", "TABA_CONFIG_ROOT", "TABA_BITBUCKET_TRANSPORT", "WTC_BITBUCKET_TRANSPORT", "TWG_BBC_TOKEN", "TWG_BBC_USER",
		"BB_API_USER", "BB_API_PASS", "BB_USERNAME", "BB_API_TOKEN", "ATLASSIAN_ACCOUNT_EMAIL", "ATLASSIAN_API_TOKEN"} {
		t.Setenv(name, "")
	}
	t.Setenv("WTC_FORGE_MIN_INTERVAL", "0")
	t.Setenv("WTC_FORGE_CACHE_AGE", "1")
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
	prs := "widget 9 - https://github.com/example/widget/pull/9 closure\n" +
		"gadget 4 - https://bitbucket.org/example/gadget/pull-requests/4 decline\n" +
		"gadget 5 - https://bitbucket.org/example/gadget/pull-requests/5 merge\n"
	if err := os.WriteFile(c.PRFile(), []byte(prs), 0600); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	log := filepath.Join(bin, "twg.log")
	gh := `#!/bin/sh
case "$1 $2" in "pr list") echo '[]'; exit 0;; esac
printf '%s' '{"number":9,"state":"CLOSED","title":"closure","closedAt":"2020-02-01T00:00:00Z"}'
`
	// The decline sits before the GitHub closure; updated_on and a later
	// edit while declined come after it.
	pr := func(id, state string) string {
		return `{"items":[{"input":"` + id + `","ok":true,"data":{"id":` + id + `,"state":"` + state + `","title":"t","updated_on":"2020-06-01T00:00:00.000000+00:00","destination":{"branch":{"name":"main"},"repository":{"full_name":"example/gadget"}},"source":{"commit":{"hash":"aaaaaaaaaaaa"}}}}]}`
	}
	twg := `#!/bin/sh
if [ "$1" = "--version" ]; then echo 1.4.4; exit 0; fi
echo "$*" >> "` + log + `"
case "$*" in
*"pull-requests get 4 "*) cat "` + filepath.Join(bin, "pr4.json") + `";;
*"pull-requests get 5 "*) printf '%s' '` + pr("5", "MERGED") + `';;
*"pull-requests activity 4 "*) cat "` + filepath.Join(bin, "activity4.json") + `";;
*"pull-requests activity 5 "*) printf '%s' '{"items":[{"input":"5","ok":true,"data":[{"update":{"state":"MERGED","date":"2020-01-10T00:00:00Z"}}]}]}';;
*"pull-requests query"*) echo '{"values":[]}';;
*) echo '{"ok":false,"error":{"code":"UNKNOWN_COMMAND"}}'; exit 2;;
esac
`
	pr4 := filepath.Join(bin, "pr4.json")
	if err := os.WriteFile(pr4, []byte(pr("4", "DECLINED")), 0600); err != nil {
		t.Fatal(err)
	}
	activity4 := filepath.Join(bin, "activity4.json")
	dated := `{"items":[{"input":"4","ok":true,"data":[{"update":{"state":"OPEN","date":"2020-01-01T00:00:00Z"}},{"update":{"state":"DECLINED","date":"2020-01-15T00:00:00.000000+00:00"}},{"update":{"state":"DECLINED","date":"2020-05-01T00:00:00Z"}}]}]}`
	if err := os.WriteFile(activity4, []byte(dated), 0600); err != nil {
		t.Fatal(err)
	}
	for name, script := range map[string]string{"gh": gh, "twg": twg} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if git, err := exec.LookPath("git"); err == nil {
		if err := os.Symlink(git, filepath.Join(bin, "git")); err != nil {
			t.Fatal(err)
		}
	}
	// /bin and /usr/bin stay for sh and coreutils; neither holds bb.
	t.Setenv("PATH", bin+":/bin:/usr/bin")
	reads := func(command string) int {
		raw, _ := os.ReadFile(log)
		return strings.Count(string(raw), command+" ")
	}
	collect := func() []string {
		t.Helper()
		snapshot, err := c.StatusForegroundSnapshot(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		var order []string
		for _, row := range wtc.StatusPRsByPriority(snapshot.PRs) {
			if !row.Archived {
				t.Fatalf("row left the archive: %+v", row)
			}
			at := "unknown"
			if when, known := wtc.StatusPRTerminalAt(row); known {
				at = when.Format("2006-01-02")
			}
			order = append(order, row.Repo+"#"+row.Number+"@"+at)
		}
		return order
	}

	// TWG profile: no identity, so no decline time and no activity read.
	if got := strings.Join(collect(), " "); got != "widget#9@2020-02-01 gadget#5@2020-01-10 gadget#4@unknown" {
		t.Fatalf("profile archive order: %s", got)
	}
	if reads("pull-requests activity 4") != 0 {
		t.Fatalf("a TWG profile read decline activity")
	}

	t.Setenv("TWG_BBC_TOKEN", "synthetic-twg-token")
	merged := reads("pull-requests activity 5")
	for refresh := 1; refresh <= 3; refresh++ {
		// updated_on or the later DECLINED edit would put the decline first.
		if got := strings.Join(collect(), " "); got != "widget#9@2020-02-01 gadget#4@2020-01-15 gadget#5@2020-01-10" {
			t.Fatalf("refresh %d: collected archive order: %s", refresh, got)
		}
		if declined, prReads := reads("pull-requests activity 4"), reads("pull-requests get 4"); declined != 1 || prReads < refresh {
			t.Fatalf("refresh %d: %d decline activity reads for %d PR reads; want one in all", refresh, declined, prReads)
		}
		if reads("pull-requests activity 5") != merged {
			t.Fatalf("refresh %d: a recorded merge read its activity again", refresh)
		}
		time.Sleep(1100 * time.Millisecond) // past WTC_FORGE_CACHE_AGE
	}

	// A background collection completes a decline inside its detail read; the
	// collector must not read it again. A malformed reply is not cached, so
	// each run retries it exactly once.
	// A later decline (new updated_on) whose log is malformed.
	redeclined := strings.Replace(pr("4", "DECLINED"), "2020-06-01T", "2020-07-01T", 1)
	if err := os.WriteFile(pr4, []byte(redeclined), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(activity4, []byte(`{"items":[{"input":"4","ok":true,"data":{"values":"not a list"}}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(c.PRFile(), []byte("gadget 4 - https://bitbucket.org/example/gadget/pull-requests/4 decline\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for run := 1; run <= 2; run++ {
		before := reads("pull-requests activity 4")
		if _, err := c.StatusForgePreview(); err != nil {
			t.Fatal(err)
		}
		if got := reads("pull-requests activity 4") - before; got != 1 {
			t.Fatalf("background run %d: %d decline activity reads for a malformed reply; want 1", run, got)
		}
		time.Sleep(1100 * time.Millisecond)
	}
	// A readable log for that decline is dated by a background run.
	if err := os.WriteFile(activity4, []byte(dated), 0600); err != nil {
		t.Fatal(err)
	}
	before := reads("pull-requests activity 4")
	snapshot, err := c.StatusForgePreview()
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.PRs) != 1 || snapshot.PRs[0].ClosedAt == nil || *snapshot.PRs[0].ClosedAt != "2020-01-15T00:00:00Z" || reads("pull-requests activity 4")-before > 1 {
		t.Fatalf("background run did not date the decline in one read: %+v after %d reads", snapshot.PRs, reads("pull-requests activity 4")-before)
	}
}
