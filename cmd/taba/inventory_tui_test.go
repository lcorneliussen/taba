package main

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestInventoryTUIFitsTerminalWithoutWrapping(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	rows := []inventoryTUIRow{
		{name: "certificates/2026/CertificateSigningRequest.certSigningRequest", scope: "machine", state: "control-only", detail: "scope: all collections (machine config)", extra: "target: none"},
		{name: ".env.collection.local", scope: "local", state: "present", detail: "scope: this collection", extra: "target: all worktrees"},
	}
	for _, width := range []int{28, 40, 58, 80, 112} {
		m := inventoryTUIModel{title: "wtc secrets · example", summary: "2 file paths", heading: "PATH", rows: rows, width: width, height: 12}
		lines := strings.Split(m.View().Content, "\n")
		if len(lines) != 12 {
			t.Fatalf("width %d: got %d lines, want 12", width, len(lines))
		}
		for i, line := range lines {
			if got := ansi.StringWidth(line); got > width {
				t.Fatalf("width %d line %d wraps: %d cells: %q", width, i, got, line)
			}
		}
		if !strings.Contains(m.View().Content, "2 file paths") || !strings.Contains(m.View().Content, "q quit") {
			t.Fatalf("width %d: summary or footer missing", width)
		}
	}
}

func TestInventoryTUISelectionKeepsRowAndDetailsVisible(t *testing.T) {
	rows := make([]inventoryTUIRow, 20)
	for i := range rows {
		rows[i] = inventoryTUIRow{name: "row", detail: "scope", extra: "target"}
	}
	m := inventoryTUIModel{title: "inventory", heading: "PATH", rows: rows, width: 50, height: 10, selected: 15}
	m.keepSelectionVisible()
	if m.top > m.selected || m.selected >= m.top+m.bodyHeight() {
		t.Fatalf("selected row not visible: top=%d selected=%d body=%d", m.top, m.selected, m.bodyHeight())
	}
	if !strings.Contains(m.View().Content, "16/20") || !strings.Contains(m.View().Content, "target") {
		t.Fatal("selection count or details missing")
	}
	updated, _ := m.Update(tea.KeyPressMsg(tea.Key{Code: 'j', Text: "j"}))
	m = updated.(inventoryTUIModel)
	if m.selected != 16 || m.top > m.selected || m.selected >= m.top+m.bodyHeight() {
		t.Fatalf("down movement lost selection: top=%d selected=%d", m.top, m.selected)
	}
	m.height = 18
	m.keepSelectionVisible()
	if want := len(rows) - m.bodyHeight(); m.top != want {
		t.Fatalf("larger viewport left blank space: top=%d want=%d", m.top, want)
	}
}

func TestInventoryTUINarrowStateHeadingAligns(t *testing.T) {
	for _, width := range []int{32, 40, 56} {
		m := inventoryTUIModel{title: "inventory", heading: "PATH", width: width, height: 10,
			rows: []inventoryTUIRow{{name: "example", state: "present"}}}
		lines := strings.Split(ansi.Strip(m.View().Content), "\n")
		got := ansi.StringWidth(lines[2][:strings.Index(lines[2], "STATE")])
		want := ansi.StringWidth(lines[3][:strings.Index(lines[3], "present")])
		if got != want {
			t.Fatalf("width %d: heading column %d, state column %d", width, got, want)
		}
	}
}
