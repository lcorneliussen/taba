package wtc

import (
	"html"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

var reviewSourceLocation = regexp.MustCompile("`([^`\\n]+):([1-9][0-9]*)`")

// Presentation changes only the posted copy; the reviewer output and gate
// record remain available verbatim in the bundle.
func presentReviewSummary(body string, m ReviewManifest, files map[string]bool) string {
	if m.Forge == "bitbucket" {
		return presentBitbucketReview(body)
	}
	if m.Forge != "github" {
		return body
	}
	mark := ""
	if match := reviewStatusLine.FindStringSubmatch(body); len(match) > 2 {
		switch match[2] {
		case "pass":
			mark = "🟢 "
		case "pass-with-notes":
			mark = "🟡 "
		case "changes-requested", "error":
			mark = "🔴 "
		}
	}
	lines := strings.Split(strings.TrimRight(body, "\n"), "\n")
	var out []string
	fence, folded := "", false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		fenceRun := ""
		if strings.HasPrefix(trimmed, "`") || strings.HasPrefix(trimmed, "~") {
			marker := trimmed[0]
			for i := 0; i < len(trimmed) && trimmed[i] == marker; i++ {
				fenceRun += string(marker)
			}
		}
		if fence != "" {
			out = append(out, line)
			if strings.HasPrefix(fenceRun, fence) && strings.TrimSpace(strings.TrimPrefix(trimmed, fenceRun)) == "" {
				fence = ""
			}
			continue
		}
		if len(fenceRun) >= 3 {
			fence = fenceRun
			out = append(out, line)
			continue
		}
		if strings.HasPrefix(line, "### ") || strings.Contains(line, "wtc-review v1 head=") {
			if folded {
				out = append(out, "", "</details>", "")
				folded = false
			}
		}
		title := strings.TrimPrefix(line, "### ")
		if strings.HasPrefix(line, "### ") {
			switch title {
			case "Concerns", "Addressed since previous round", "Run stats":
				out = append(out, "<details>", "<summary>"+html.EscapeString(title)+"</summary>", "")
				folded = true
				continue
			}
		}
		if strings.HasPrefix(line, "**Local review:") {
			line = mark + line
		}
		line = reviewSourceLocation.ReplaceAllStringFunc(line, func(location string) string {
			parts := reviewSourceLocation.FindStringSubmatch(location)
			if !files[parts[1]] {
				return location
			}
			var segments []string
			for _, part := range strings.Split(parts[1], "/") {
				segments = append(segments, url.PathEscape(part))
			}
			link := "https://github.com/" + m.Slug + "/blob/" + m.HeadSHA + "/" + strings.Join(segments, "/") + "#L" + parts[2]
			label := strings.NewReplacer("[", `\[`, "]", `\]`).Replace(location)
			return "[" + label + "](" + link + ")"
		})
		out = append(out, line)
	}
	if folded {
		out = append(out, "", "</details>")
	}
	return strings.Join(out, "\n") + "\n"
}

// Bitbucket has no details folds. Compact only recognized generated sections;
// unfamiliar prose and tables must stay visible rather than lose review evidence.
func presentBitbucketReview(body string) string {
	lines := strings.Split(strings.TrimRight(body, "\n"), "\n")
	outside := make([]bool, len(lines))
	fence := ""
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		run := ""
		if strings.HasPrefix(trimmed, "`") || strings.HasPrefix(trimmed, "~") {
			// Count the opening run, not marker characters at the end of an info string.
			n := 0
			for n < len(trimmed) && trimmed[n] == trimmed[0] {
				n++
			}
			run = trimmed[:n]
		}
		if fence != "" {
			if strings.HasPrefix(run, fence) && strings.TrimSpace(strings.TrimPrefix(trimmed, run)) == "" {
				fence = ""
			}
		} else if len(run) >= 3 {
			fence = run
		} else {
			outside[i] = true
		}
	}
	mark := ""
	if match := reviewStatusLine.FindStringSubmatch(body); len(match) > 2 {
		switch match[2] {
		case "pass":
			mark = "🟢 "
		case "pass-with-notes":
			mark = "🟡 "
		case "changes-requested", "error":
			mark = "🔴 "
		}
	}
	gate := -1
	if len(reviewStatusLine.FindAllString(body, -1)) == 1 {
		for i, line := range lines {
			trimmed := strings.TrimSpace(line)
			if outside[i] && strings.HasPrefix(trimmed, "`wtc-review v1 head=") && strings.HasSuffix(trimmed, "`") && strings.Count(trimmed, "`") == 2 && reviewStatusLine.MatchString(trimmed) {
				gate = i
			}
		}
	}
	var out []string
	for i := 0; i < len(lines); {
		line := lines[i]
		if i == gate {
			i++
			continue
		}
		if outside[i] && (line == "### Concerns" || line == "### Run stats") {
			end := i + 1
			for end < len(lines) && end != gate && !(outside[end] && strings.HasPrefix(lines[end], "#")) {
				end++
			}
			section := lines[i+1 : end]
			var compact []string
			if line == "### Concerns" {
				compact = compactBitbucketConcerns(section)
			} else {
				compact = compactBitbucketRun(section)
			}
			if compact != nil {
				out = append(out, compact...)
				i = end
				continue
			}
		}
		if outside[i] && strings.HasPrefix(line, "**Local review:") {
			line = mark + line
		}
		out = append(out, line)
		i++
	}
	if gate >= 0 {
		for len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" {
			out = out[:len(out)-1]
		}
		out = append(out, "", "### Gate record", "", lines[gate])
	}
	return strings.Join(out, "\n") + "\n"
}

func reviewPresentationCells(line string, count int) []string {
	line = strings.TrimSpace(line)
	if len(line) < 2 || !strings.HasPrefix(line, "|") || !strings.HasSuffix(line, "|") {
		return nil
	}
	cells := strings.Split(line[1:len(line)-1], "|")
	if len(cells) != count {
		return nil
	}
	for i := range cells {
		cells[i] = strings.TrimSpace(cells[i])
	}
	return cells
}

func reviewPresentationRows(lines []string, header []string) [][]string {
	var rows [][]string
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		cells := reviewPresentationCells(line, len(header))
		if cells == nil {
			return nil
		}
		rows = append(rows, cells)
	}
	if len(rows) < 3 {
		return nil
	}
	for i, name := range header {
		if !strings.EqualFold(rows[0][i], name) || strings.Trim(rows[1][i], "-:") != "" || strings.Count(rows[1][i], "-") < 3 {
			return nil
		}
	}
	return rows[2:]
}

func compactBitbucketConcerns(lines []string) []string {
	header := []string{"Concern", "Status", "Open findings", "Note"}
	rows := reviewPresentationRows(lines, header)
	if rows == nil {
		return nil
	}
	var clean, skipped, failed, open []string
	for _, row := range rows {
		count, err := strconv.Atoi(row[2])
		if err != nil || count < 0 || row[0] == "" {
			return nil
		}
		note := ""
		if row[3] != "" && row[3] != "-" {
			note = " — " + row[3]
		}
		switch strings.ToLower(row[1]) {
		case "ok":
			if count != 0 {
				return nil
			}
			clean = append(clean, "🟢 "+row[0]+note)
		case "skipped":
			if count != 0 || note == "" {
				return nil
			}
			skipped = append(skipped, "⚪ "+row[0]+note)
		case "issues", "error":
			if count > 0 {
				open = append(open, "| "+strings.Join(row, " | ")+" |")
			} else if strings.EqualFold(row[1], "error") {
				failed = append(failed, "🔴 "+row[0]+" — error"+note)
			} else {
				return nil
			}
		default:
			return nil
		}
	}
	out := []string{"### Concerns", ""}
	if len(open) > 0 {
		out = append(out, "| Concern | Status | Open findings | Note |", "|---|---|---|---|")
		out = append(out, open...)
		out = append(out, "")
	}
	if len(clean) > 0 {
		out = append(out, strings.Join(clean, " · "), "")
	}
	for _, line := range skipped {
		out = append(out, line, "")
	}
	for _, line := range failed {
		out = append(out, line, "")
	}
	return out
}

var reviewPresentationWall = regexp.MustCompile(`^Wall-clock for the whole run: (\S+) \(agent time summed: (\S+)\)\.$`)

func compactBitbucketRun(lines []string) []string {
	end := len(lines)
	for end > 0 && strings.TrimSpace(lines[end-1]) == "" {
		end--
	}
	if end == 0 {
		return nil
	}
	wall := reviewPresentationWall.FindStringSubmatch(lines[end-1])
	if wall == nil {
		return nil
	}
	rows := reviewPresentationRows(lines[:end-1], []string{"Concern", "Agent", "Time", "Tokens in / out (cache read)", "Cost"})
	if rows == nil {
		return nil
	}
	total := rows[len(rows)-1]
	if total[0] != "**Total**" || total[1] != "" || total[2] != wall[2] || total[3] == "" || total[4] == "" {
		return nil
	}
	for _, row := range rows[:len(rows)-1] {
		if row[0] == "**Total**" {
			return nil
		}
	}
	return []string{"### Run", "", wall[1] + " wall · " + total[2] + " agents · " + total[3] + " tokens in / out (cache read) · " + total[4], ""}
}

func reviewSourceFiles(m ReviewManifest) map[string]bool {
	files := map[string]bool{}
	if m.Forge == "github" {
		if raw, err := reviewGit(m.RepoDir, "ls-tree", "-r", "-z", "--name-only", m.HeadSHA); err == nil {
			for _, path := range strings.Split(string(raw), "\x00") {
				if path != "" {
					files[path] = true
				}
			}
		}
	}
	return files
}
