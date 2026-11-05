package main

import (
	"fmt"
	"strings"

	"github.com/mattn/go-runewidth"
	"taba.sh/taba/internal/branding"
	"taba.sh/taba/internal/wtc"
)

func statusTable(snapshot wtc.StatusSnapshot, reposOnly bool, width int) string {
	name := snapshot.Collection
	if name == "" {
		name = "(all)"
	}
	header := fmt.Sprintf("%s status · %s · %s", branding.Product, name, snapshot.GeneratedAt)
	if remaining := width - runewidth.StringWidth(header); remaining > 0 {
		header += statusTUIFit(" · "+wtc.StatusVersionLabel(snapshot.CLIVersion), remaining)
	}
	lines := []string{statusTUIFit(header, width), ""}
	lines = append(lines, statusTUIRepoLines(snapshot, width, false)...)
	lines = append(lines, statusTUIRuntimeLines(snapshot, width, false)...)
	if !reposOnly {
		lines = append(lines, statusTUIPRLines(snapshot, width, false, false)...)
	}
	lines = append(lines, statusTUIRetiredLines(snapshot, width, false)...)
	lines = append(lines, statusNextLines(snapshot, width, false, -1)...)
	if snapshot.StaleCount > 0 {
		lines = append(lines, "", statusTUIFit(fmt.Sprintf("%d worktree(s) behind the development tip", snapshot.StaleCount), width))
	}
	lines = append(lines, statusRetirementLines(snapshot, width)...)
	lines = append(lines, statusSyncNotes(snapshot, width)...)
	return strings.Join(lines, "\n") + "\n"
}

func statusRetirementLines(snapshot wtc.StatusSnapshot, width int) []string {
	var lines []string
	for _, a := range snapshot.Retirement {
		lines = append(lines, statusTUIFit(fmt.Sprintf("Retirement %s: %s (%d blockers, %d unknown) · retire --dry-run", statusTUISafe(a.Collection), a.State, len(a.Blockers), len(a.Unknowns)), width))
	}
	if len(lines) > 0 {
		lines = append([]string{""}, lines...)
	}
	return lines
}
