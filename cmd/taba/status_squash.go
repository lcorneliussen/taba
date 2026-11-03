package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"taba.sh/taba/internal/wtc"
)

// Keep observations below the dense tables, whose cells remain raw counts.
func statusSyncNotes(snapshot wtc.StatusSnapshot, width int) []string {
	var lines []string
	for _, row := range snapshot.Repos {
		if row.Ahead > 0 && row.AheadBasis == "development-tip" {
			name := row.Dir
			if snapshot.ShowCollectionColumn {
				name = row.Collection + "/" + name
			}
			text := fmt.Sprintf("%s: ↑%d vs %s (commit history", name, row.Ahead, row.AheadRef)
			if row.UpstreamState == "gone" {
				text += "; upstream gone"
			}
			if row.Landing != nil && row.Landing.State == "landed" {
				text += "; fully landed residuals"
			}
			text += ")"
			lines = append(lines, strings.Split(ansi.Wrap(statusTUISafe(text), max(1, width), ""), "\n")...)
		}
	}
	if len(lines) > 0 {
		lines = append([]string{""}, lines...)
	}
	return lines
}
