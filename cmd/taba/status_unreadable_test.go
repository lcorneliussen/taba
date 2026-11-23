package main

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"taba.sh/taba/internal/wtc"
)

func TestStatusTableShowsUnreadableCollections(t *testing.T) {
	snapshot := wtc.StatusSnapshot{Schema: 1, ShowCollectionColumn: true,
		Unreadable: []wtc.StatusUnreadable{{Collection: "conflict", Reason: "collection context could not be opened"}, {Collection: "evil\x1b[2J\nname", Reason: "x"}}}
	for _, reposOnly := range []bool{false, true} {
		table := statusTable(snapshot, reposOnly, 120)
		if !strings.Contains(table, "unreadable conflict: collection context could not be opened") {
			t.Fatalf("reposOnly=%v table omits unreadable collection:\n%s", reposOnly, table)
		}
		if strings.Contains(table, "\x1b[2J") || strings.Contains(table, "evil\nname") {
			t.Fatalf("control characters reached the terminal: %q", table)
		}
	}
	md := snapshot.Markdown()
	if !strings.Contains(md, "## Unreadable collections") || strings.Contains(md, "\x1b[2J") {
		t.Fatalf("Markdown omits or leaks unreadable rows:\n%q", md)
	}
}

// The live view of a workspace sweep shows unreadable collections too; a
// partial snapshot must not look complete.
func TestStatusTUIShowsUnreadableCollections(t *testing.T) {
	snapshot := wtc.StatusSnapshot{Schema: 1, ShowCollectionColumn: true,
		Repos:      []wtc.StatusRepo{{Collection: "clean", Dir: "harness"}},
		Unreadable: []wtc.StatusUnreadable{{Collection: "conflict", Reason: "collection context could not be opened"}, {Collection: "evil\x1b[2J\nname", Reason: "x"}}}
	for _, reposOnly := range []bool{false, true} {
		model := statusTUIModel{snapshot: snapshot, width: 120, height: 40, reposOnly: reposOnly}
		raw := strings.Join(model.contentLayout().lines, "\n")
		if !strings.Contains(ansi.Strip(raw), "unreadable conflict: collection context could not be opened") {
			t.Fatalf("reposOnly=%v live view omits unreadable collection:\n%s", reposOnly, ansi.Strip(raw))
		}
		if strings.Contains(raw, "\x1b[2J") || strings.Contains(raw, "evil\nname") {
			t.Fatalf("control characters reached the live view: %q", raw)
		}
	}
}

// Every output mode writes the full report first; a partial sweep then exits
// nonzero, and a complete one does not.
func TestWriteStatusReportRendersThenExitsForPartialSweep(t *testing.T) {
	complete := wtc.StatusSnapshot{Schema: 1, ShowCollectionColumn: true, Repos: []wtc.StatusRepo{{Collection: "clean", Dir: "harness"}}}
	partial := complete
	partial.Unreadable = []wtc.StatusUnreadable{{Collection: "conflict", Reason: "collection context could not be opened"}, {Collection: "other", Reason: "x"}}
	for _, mode := range []struct {
		name                     string
		asJSON, table, reposOnly bool
		want                     string
	}{
		{"json", true, false, false, `"unreadable":[{"collection":"conflict"`},
		{"table", false, true, false, "unreadable conflict: collection context could not be opened"},
		{"markdown", false, false, false, "**conflict** — collection context could not be opened"},
		{"repos-markdown", false, false, true, ""},
	} {
		var out strings.Builder
		err := writeStatusReport(&out, partial, mode.asJSON, mode.table, mode.reposOnly, 120)
		if err == nil || err.Error() != "partial status: 2 collection(s) unreadable" {
			t.Fatalf("%s: partial sweep exit: %v", mode.name, err)
		}
		if out.Len() == 0 || !strings.Contains(out.String(), mode.want) {
			t.Fatalf("%s: report missing before exit:\n%s", mode.name, out.String())
		}
		out.Reset()
		if err := writeStatusReport(&out, complete, mode.asJSON, mode.table, mode.reposOnly, 120); err != nil || out.Len() == 0 {
			t.Fatalf("%s: complete sweep: %v %q", mode.name, err, out.String())
		}
	}
}
