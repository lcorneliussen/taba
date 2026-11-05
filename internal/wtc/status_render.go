package wtc

import (
	"fmt"
	"sort"
	"strings"
	"taba.sh/taba/internal/branding"
)

// StatusPRsByPriority keeps enlisted order within each visible group while
// putting review-ready PRs ahead of drafts and completed work.
func StatusPRsByPriority(rows []StatusPRRow) []StatusPRRow {
	ordered := append([]StatusPRRow(nil), rows...)
	priority := func(row StatusPRRow) int {
		if row.Draft || row.State == "DRAFT" {
			return 1
		}
		if row.OnBranch {
			return 2
		}
		if row.State == "MERGED" || row.Archived || row.MergedOn != nil ||
			(row.Merge != nil && *row.Merge == "MERGED") {
			return 3
		}
		return 0
	}
	sort.SliceStable(ordered, func(i, j int) bool { return priority(ordered[i]) < priority(ordered[j]) })
	return ordered
}

var statusGlyphs = map[string]string{
	"SUCCESS": "✓", "FAILURE": "✗", "ERROR": "✗", "PENDING": "●",
	"EXPECTED": "●", "draft": "D", "NONE": "·", "approved": "✓",
	"changes": "!", "waiting": "…", "commented": "✎", "noreviewers": "∅",
	"merged": "·", "FOLLOW": "→", "MERGED": "·",
}

var statusReviewLabels = map[string]string{
	"approved": "approved", "changes": "changes requested", "waiting": "waiting on reviewers",
	"commented": "reviewer commented", "noreviewers": "∅ no reviewers", "merged": "merged",
}

func statusGlyph(value string) string {
	if glyph, ok := statusGlyphs[value]; ok {
		return glyph
	}
	return value
}

func statusShownGlyph(value string) string {
	glyph := statusGlyph(value)
	if glyph == "·" || glyph == " " {
		return ""
	}
	return glyph
}

func statusMergeGlyph(value string) string {
	switch value {
	case "BEHIND":
		return "↓"
	case "CONFLICTING", "DIRTY":
		return "✗ conflict"
	case "BLOCKED":
		return "⊘"
	case "FOLLOW":
		return "→"
	}
	return ""
}

func statusMarkdownLink(url *string, label string) string {
	if url != nil && *url != "" {
		return "[" + label + "](" + *url + ")"
	}
	return label
}

func statusBuildLabel(build *StatusBuild) string {
	if build == nil {
		return ""
	}
	if build.Error != "" {
		return "error: " + build.Error
	}
	glyph := ""
	if build.Checks != nil {
		glyph = statusGlyph(*build.Checks)
	}
	label := glyph
	if build.Build != nil && *build.Build != "" {
		label = strings.TrimSpace(glyph + " " + statusMarkdownLink(build.URL, "#"+*build.Build))
	}
	if label != "" && build.HeadInBuild != nil && !*build.HeadInBuild {
		label += " (HEAD not in build)"
	}
	return label
}

// Markdown renders the agent-facing view from the same structured snapshot
// used by JSON and the future live status pane.
func (s StatusSnapshot) Markdown() string {
	return s.markdown(true)
}

// ReposMarkdown omits the collection's enlisted PR section.
func (s StatusSnapshot) ReposMarkdown() string {
	return s.markdown(false)
}

func (s StatusSnapshot) markdown(includePRs bool) string {
	var b strings.Builder
	collection := s.Collection
	if collection == "" {
		collection = "(all)"
	}
	fmt.Fprintf(&b, "# %s\n\nGenerated: %s\nCLI: %s %s\n\n## Repos\n\n", collection, s.GeneratedAt, branding.Product, StatusVersionLabel(s.CLIVersion))
	if len(s.Repos) == 0 {
		b.WriteString("- (none)\n")
	}
	for _, row := range s.Repos {
		branch := row.BranchDisplay
		if branch == "" {
			branch = row.Branch
		}
		if branch == "" {
			branch = "?"
		}
		tree := row.Tree
		if tree == "" {
			tree = "clean"
		}
		name := row.Dir
		if s.ShowCollectionColumn {
			name = row.Collection + "/" + row.Dir
		}
		parts := []string{fmt.Sprintf("**%s** (`%s`) — %s", name, branch, tree)}
		if row.RenamedTo != "" {
			parts = append(parts, "renamed to "+row.RenamedTo)
		}
		if row.Runtime != "" {
			parts = append(parts, "runtime "+row.Runtime)
		}
		if row.Ahead > 0 {
			label := fmt.Sprintf("↑%d", row.Ahead)
			if row.AheadRef != "" {
				label += " vs " + row.AheadRef
			}
			if row.Landing != nil {
				label += " (" + row.Landing.State + ")"
			}
			parts = append(parts, label)
		}
		if row.Behind > 0 {
			parts = append(parts, fmt.Sprintf("↓%d", row.Behind))
		}
		if row.Conflict {
			parts = append(parts, "✗ unresolved conflicts")
		} else if row.Operation != "" {
			parts = append(parts, "⚠ in-progress "+row.Operation)
		}
		if row.PR != nil && row.PR.Number != "" {
			bits := []string{"#" + row.PR.Number}
			if row.PR.Draft {
				bits = append(bits, "◇ draft")
			}
			if g := statusShownGlyph(row.PR.Checks); g != "" {
				bits = append(bits, g)
			}
			if g := statusMergeGlyph(row.PR.Merge); g != "" {
				bits = append(bits, g)
			}
			if label := statusReviewLabels[row.PR.Review]; label != "" {
				bits = append(bits, label)
			} else if g := statusShownGlyph(row.PR.Review); g != "" {
				bits = append(bits, g)
			}
			parts = append(parts, "PR "+strings.Join(bits, " "))
		}
		builds := []string{}
		if label := statusBuildLabel(row.Tip); label != "" {
			builds = append(builds, "tip "+label)
		}
		if label := statusBuildLabel(row.Prod); label != "" {
			builds = append(builds, "prod "+label)
		}
		if len(builds) != 0 {
			parts = append(parts, strings.Join(builds, "; "))
		}
		b.WriteString("- " + strings.Join(parts, "; ") + "\n")
	}
	if s.StaleCount != 0 {
		fmt.Fprintf(&b, "\n_%d worktree(s) behind remote — catch-up needed._\n", s.StaleCount)
	}
	if len(s.Runtime) != 0 {
		b.WriteString("\n## Runtime\n\n```text\n" + RuntimeText(s.Runtime) + "```\n")
	}
	if len(s.Retired) != 0 {
		b.WriteString("\n## Retired leftovers\n\n")
		for _, row := range s.Retired {
			fmt.Fprintf(&b, branding.Text("- **%s** — %s; `{{command}} retire %s` finishes once they are gone\n"), row.Folder, statusRetiredHolding(row), row.Folder)
		}
	}
	if len(s.Retirement) > 0 {
		b.WriteString("\n## Retirement\n\n")
		for _, a := range s.Retirement {
			fmt.Fprintf(&b, branding.Text("- **%s** — %s (%d blockers, %d unknown checks); inspect with `{{command}} retire %s --dry-run`\n"), runtimeTextSafe(a.Collection), a.State, len(a.Blockers), len(a.Unknowns), runtimeTextSafe(a.Collection))
		}
	}
	if s.ShowCollectionColumn || !includePRs {
		b.WriteString("\n")
		return b.String()
	}
	b.WriteString("\n## PRs\n\n")
	active, archived := []StatusPRRow{}, []StatusPRRow{}
	for _, row := range StatusPRsByPriority(s.PRs) {
		if row.Archived {
			archived = append(archived, row)
		} else {
			active = append(active, row)
		}
	}
	if len(active) == 0 && len(archived) == 0 {
		if s.PRsEmptyHint {
			b.WriteString(branding.Text("- (none enlisted — `{{command}} pr enlist <repo> <n>`)\n"))
		} else {
			b.WriteString("- (none)\n")
		}
	} else {
		if len(active) == 0 {
			b.WriteString("- (none open)\n")
		}
		for _, row := range active {
			bits := []string{fmt.Sprintf("**%s**", row.Repo), "#" + row.Number}
			if row.OnBranch {
				bits = append(bits, "⚠ MERGED — "+row.LandingLabel())
			} else if row.Draft {
				bits = append(bits, "◇ draft")
			}
			merge := ""
			if row.Merge != nil {
				merge = *row.Merge
			}
			if !row.OnBranch && (merge == "FOLLOW" || merge == "MERGED") {
				bits = append(bits, "_merged_")
			}
			if !row.OnBranch {
				if row.Checks != nil {
					if g := statusShownGlyph(*row.Checks); g != "" {
						bits = append(bits, g)
					}
				}
				if merge != "FOLLOW" && merge != "MERGED" && merge != "UNKNOWN" {
					if g := statusMergeGlyph(merge); g != "" {
						bits = append(bits, g)
					}
				}
				if row.Review != nil {
					if label := statusReviewLabels[*row.Review]; label != "" {
						bits = append(bits, label)
					}
				}
			}
			if row.URL != nil && row.Number != "" {
				label := row.Title
				if label == "" {
					label = "PR #" + row.Number
				}
				bits = append(bits, statusMarkdownLink(row.URL, label))
			} else if row.Title != "" {
				bits = append(bits, row.Title)
			}
			if !row.OnBranch {
				follow := []string{}
				if row.FollowTipBuild != nil && *row.FollowTipBuild != "" {
					follow = append(follow, "tip "+*row.FollowTipBuild)
				}
				if row.FollowProdBuild != nil && *row.FollowProdBuild != "" {
					follow = append(follow, "prod "+*row.FollowProdBuild)
				}
				if len(follow) != 0 {
					bits = append(bits, strings.Join(follow, " "))
				}
			}
			b.WriteString("- " + strings.Join(bits, " ") + "\n")
		}
		if len(archived) != 0 {
			b.WriteString("\n## Archived\n\n_Merged PRs past 48 weekday-hours (weekends excluded)._\n\n")
			for _, row := range archived {
				bits := []string{fmt.Sprintf("**%s**", row.Repo), "#" + row.Number}
				if row.URL != nil && row.Number != "" {
					label := row.Title
					if label == "" {
						label = "PR #" + row.Number
					}
					bits = append(bits, statusMarkdownLink(row.URL, label))
				} else if row.Title != "" {
					bits = append(bits, row.Title)
				}
				if row.MergedOn != nil && *row.MergedOn != "" {
					bits = append(bits, "merged "+*row.MergedOn)
				}
				b.WriteString("- " + strings.Join(bits, " ") + "\n")
			}
		}
	}
	if len(s.Orphans) != 0 {
		b.WriteString("\n## Orphans\n\n")
		for _, row := range s.Orphans {
			fmt.Fprintf(&b, "- **%s** on `%s` — PR %s; catch-up returns it to the tip\n", row.Repo, row.Branch, row.State)
		}
	}
	b.WriteString("\n")
	return b.String()
}

func statusRetiredHolding(row StatusRetired) string {
	if len(row.BlockingPaths) == 0 {
		return "nothing left"
	}
	return "holds " + strings.Join(row.BlockingPaths, ", ")
}
