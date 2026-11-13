package openclaw

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var markerDiffHunk = regexp.MustCompile(`^@@ -[0-9]+(?:,[0-9]+)? \+[0-9]+(?:,[0-9]+)? @@`)

// A path header is metadata only before its hunks. Added file content can
// itself begin with +++ and must never replace that patch's file identity.
func parseAddedConflictMarkers(diff string) (map[string]string, error) {
	const (
		outside = iota
		headers
		pathHeader
		hunk
	)
	stage, file := outside, ""
	markers := map[string]string{}
	for _, line := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			stage, file = headers, ""
		case strings.HasPrefix(line, "diff --"):
			return nil, fmt.Errorf("unsupported conflict-marker diff format")
		case stage != hunk && strings.HasPrefix(line, "+++ "):
			if stage != headers {
				return nil, fmt.Errorf("unexpected conflict-marker path header")
			}
			name, _, _ := strings.Cut(strings.TrimPrefix(line, "+++ "), "\t")
			if strings.HasPrefix(name, `"`) {
				var err error
				name, err = strconv.Unquote(name)
				if err != nil {
					return nil, fmt.Errorf("invalid quoted conflict-marker path: %w", err)
				}
			}
			if name == "/dev/null" {
				file = ""
			} else {
				var ok bool
				file, ok = strings.CutPrefix(name, "b/")
				if !ok || file == "" || strings.ContainsRune(file, 0) {
					return nil, fmt.Errorf("invalid conflict-marker new-side path")
				}
			}
			stage = pathHeader
		case strings.HasPrefix(line, "@@"):
			if stage < pathHeader || !markerDiffHunk.MatchString(line) {
				return nil, fmt.Errorf("invalid conflict-marker diff hunk")
			}
			stage = hunk
		case strings.HasPrefix(line, "+") && isConflictMarker(line[1:]):
			if stage != hunk || file == "" {
				return nil, fmt.Errorf("conflict-marker line has no verified file identity")
			}
			markers[file+"\x00"+line[1:]] = file
		}
	}
	return markers, nil
}

// Both recovery and sandbox continuation use the same two-baseline check.
// Diff failures leave verification incomplete; callers must retain their state.
func newConflictMarkerFiles(dir, first, second string, cached bool) ([]string, error) {
	added := func(base string) (map[string]string, error) {
		args := []string{"diff", "--no-color", "--no-ext-diff", "--no-textconv", "--no-renames", "--src-prefix=a/", "--dst-prefix=b/", "-U0"}
		if cached {
			args = append(args, "--cached")
		}
		args = append(args, base, "--")
		out, err := git(dir, args...)
		if err != nil {
			return nil, err
		}
		return parseAddedConflictMarkers(out)
	}
	a, err := added(first)
	if err != nil {
		return nil, err
	}
	b, err := added(second)
	if err != nil {
		return nil, err
	}
	files := map[string]bool{}
	for key, file := range a {
		if _, ok := b[key]; ok {
			files[file] = true
		}
	}
	names := []string{}
	for file := range files {
		names = append(names, file)
	}
	sort.Strings(names)
	return names, nil
}
