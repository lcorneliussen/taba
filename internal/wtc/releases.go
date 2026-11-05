package wtc

import (
	"bytes"
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Masterminds/semver/v3"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
	"taba.sh/taba/internal/branding"
)

// The unreleased directory always holds README.md, so the pattern matches
// even when no change is pending. Embedding the directory takes every file
// name except dot and underscore files, so a fragment without the .md suffix
// fails reads the same way it fails cut and check, and dot or underscore
// drafts and OS files never reach the binary. A subdirectory would be
// embedded too, so reads reject one.
//
//go:embed release-notes/*.md release-notes/unreleased
var releaseNotes embed.FS

// UnreleasedDir holds one fragment per pending change, relative to the
// release-notes directory.
const UnreleasedDir = "unreleased"

const unreleasedReadme = "README.md"

// ReleaseNote is a committed release document embedded in this binary.
type ReleaseNote struct {
	Version string `json:"version"`
	Content string `json:"content"`
}

func releaseVersion(value string) (*semver.Version, error) {
	v, err := semver.StrictNewVersion(strings.TrimPrefix(value, "v"))
	if err != nil {
		return nil, fmt.Errorf("invalid release version %q: use a full semantic version such as 0.1.38", value)
	}
	return v, nil
}

// EmbeddedReleaseNotes returns releases in semantic version order, followed by
// unreleased changes. It neither discovers a collection nor contacts a forge.
func EmbeddedReleaseNotes() ([]ReleaseNote, error) {
	root, err := fs.Sub(releaseNotes, "release-notes")
	if err != nil {
		return nil, err
	}
	return readReleaseNotes(root)
}

func readReleaseNotes(root fs.FS) ([]ReleaseNote, error) {
	entries, err := fs.ReadDir(root, ".")
	if err != nil {
		return nil, err
	}
	notes := make([]ReleaseNote, 0, len(entries)+1)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), ".md")
		if _, err := releaseVersion(name); err != nil {
			return nil, err
		}
		content, err := fs.ReadFile(root, entry.Name())
		if err != nil {
			return nil, err
		}
		notes = append(notes, ReleaseNote{Version: name, Content: string(content)})
	}
	sort.Slice(notes, func(i, j int) bool {
		a, _ := releaseVersion(notes[i].Version)
		b, _ := releaseVersion(notes[j].Version)
		return a.LessThan(b)
	})
	fragments, err := ReadReleaseFragments(root)
	if err != nil {
		return nil, err
	}
	pending := "# " + branding.Product + " unreleased\n\nNo unreleased changes.\n"
	if len(fragments) > 0 {
		pending = AssembleReleaseNotes("unreleased", fragments)
	}
	return append(notes, ReleaseNote{Version: "unreleased", Content: pending}), nil
}

// ReleaseFragment is one pending change's notes: level-two topic headings,
// each followed by its entries.
type ReleaseFragment struct {
	Name     string
	Sections []ReleaseSection
}

// ReleaseSection is one topic heading and the Markdown body below it.
type ReleaseSection struct {
	Heading string
	Body    string
}

// ReadReleaseFragments reads and validates the unreleased fragments of a
// release-notes directory, ordered by issue number, then file name.
func ReadReleaseFragments(root fs.FS) ([]ReleaseFragment, error) {
	entries, err := fs.ReadDir(root, UnreleasedDir)
	if err != nil {
		return nil, err
	}
	var fragments []ReleaseFragment
	for _, entry := range entries {
		name := entry.Name()
		// Dot and underscore files (.DS_Store, editor swap files, drafts) are
		// not embedded; skipping them on disk keeps cut and check in step.
		if name == unreleasedReadme || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
			continue
		}
		path := UnreleasedDir + "/" + name
		if entry.IsDir() {
			// Its files would be embedded but never assembled, cut or checked.
			return nil, fmt.Errorf("%s: fragments live directly in %s/, not in subdirectories", path, UnreleasedDir)
		}
		if !strings.HasSuffix(name, ".md") {
			return nil, fmt.Errorf("%s: release note fragments are Markdown files", path)
		}
		content, err := fs.ReadFile(root, path)
		if err != nil {
			return nil, err
		}
		fragment, err := ParseReleaseFragment(name, string(content))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		fragments = append(fragments, fragment)
	}
	sort.SliceStable(fragments, func(i, j int) bool {
		a, b := fragmentNumber(fragments[i].Name), fragmentNumber(fragments[j].Name)
		if a != b {
			return a < b
		}
		return fragments[i].Name < fragments[j].Name
	})
	return fragments, nil
}

// fragmentNumber orders "<issue>-<slug>.md" fragments by issue number, so #117
// follows #46. Names without a leading number sort after numbered ones.
func fragmentNumber(name string) int {
	n, digits := 0, 0
	for _, r := range name {
		if r < '0' || r > '9' {
			break
		}
		n, digits = n*10+int(r-'0'), digits+1
	}
	if digits == 0 {
		return int(^uint(0) >> 1)
	}
	return n
}

// ParseReleaseFragment splits a fragment into its topic sections. A fragment
// starts with a "## " heading and every heading has at least one entry. The
// fragment is parsed as CommonMark, so only top-level headings start
// sections; "#" lines in code blocks, fences and list items are body text.
func ParseReleaseFragment(name, content string) (ReleaseFragment, error) {
	source := []byte(strings.ReplaceAll(content, "\r\n", "\n"))
	doc := goldmark.DefaultParser().Parse(text.NewReader(source))
	type heading struct {
		title      string
		start, end int // the heading's own line
	}
	var headings []heading
	for node := doc.FirstChild(); node != nil; node = node.NextSibling() {
		h, ok := node.(*ast.Heading)
		if !ok || h.Level > 2 {
			continue // "###" subheadings belong to the section body
		}
		lines := h.Lines()
		if lines.Len() == 0 {
			return ReleaseFragment{}, fmt.Errorf("empty section heading")
		}
		segment := lines.At(0)
		start := bytes.LastIndexByte(source[:segment.Start], '\n') + 1
		end := len(source)
		if i := bytes.IndexByte(source[segment.Stop:], '\n'); i >= 0 {
			end = segment.Stop + i + 1
		}
		marker := strings.TrimLeft(string(source[start:segment.Start]), " ")
		if h.Level != 2 || !strings.HasPrefix(marker, "## ") {
			return ReleaseFragment{}, fmt.Errorf("use \"## <topic>\" headings; the release heading is added when notes are assembled")
		}
		title := strings.TrimSpace(string(segment.Value(source)))
		headings = append(headings, heading{title: title, start: start, end: end})
	}
	if len(headings) == 0 {
		return ReleaseFragment{}, fmt.Errorf("no \"## <topic>\" section")
	}
	if strings.TrimSpace(string(source[:headings[0].start])) != "" {
		return ReleaseFragment{}, fmt.Errorf("start with a \"## <topic>\" heading")
	}
	fragment := ReleaseFragment{Name: name}
	for i, h := range headings {
		stop := len(source)
		if i+1 < len(headings) {
			stop = headings[i+1].start
		}
		if h.title == "" {
			return ReleaseFragment{}, fmt.Errorf("empty section heading")
		}
		body := strings.TrimRight(string(source[h.end:stop]), "\n")
		for strings.Contains(body, "\n") && strings.TrimSpace(body[:strings.Index(body, "\n")]) == "" {
			body = body[strings.Index(body, "\n")+1:] // blank separator lines, spaces included
		}
		if strings.TrimSpace(body) == "" {
			body = ""
		}
		if body == "" {
			return ReleaseFragment{}, fmt.Errorf("section %q has no entries", h.title)
		}
		if body[0] == ' ' || body[0] == '\t' {
			return ReleaseFragment{}, fmt.Errorf("section %q: start its entries at the left margin, so they cannot nest under another change's entries when sections are merged", h.title)
		}
		if !visibleContent(body) {
			return ReleaseFragment{}, fmt.Errorf("section %q has no entries", h.title)
		}
		if referenceDefinitions(body) {
			return ReleaseFragment{}, fmt.Errorf("section %q: use inline links; reference definitions are document-wide and would collide with other fragments' labels", h.title)
		}
		if !closedBlocks(body) {
			return ReleaseFragment{}, fmt.Errorf("section %q leaves a code fence or HTML block open; close it so it cannot swallow the sections after it", h.title)
		}
		fragment.Sections = append(fragment.Sections, ReleaseSection{Heading: h.title, Body: body})
	}
	return fragment, nil
}

// closedBlocks reports whether body ends with every block closed: a heading
// placed after it still parses as a top-level heading.
func closedBlocks(body string) bool {
	source := body + "\n\n## wtc-release-notes-sentinel\n"
	last, ok := parseMarkdown(source).LastChild().(*ast.Heading)
	return ok && last.Level == 2 && string(last.Lines().Value([]byte(source))) == "wtc-release-notes-sentinel"
}

// referenceDefinitions reports whether body defines link references anywhere,
// including inside lists.
func referenceDefinitions(body string) bool {
	found := false
	_ = ast.Walk(parseMarkdown(body), func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if _, ok := node.(*ast.LinkReferenceDefinition); ok && entering {
			found = true
			return ast.WalkStop, nil
		}
		return ast.WalkContinue, nil
	})
	return found
}

func parseMarkdown(source string) ast.Node {
	return goldmark.DefaultParser().Parse(text.NewReader([]byte(source)))
}

// visibleContent reports whether body holds an entry: comments, link
// reference definitions and subheadings alone do not count.
func visibleContent(body string) bool {
	for node := parseMarkdown(body).FirstChild(); node != nil; node = node.NextSibling() {
		switch node.(type) {
		case *ast.HTMLBlock, *ast.LinkReferenceDefinition, *ast.Heading:
		default:
			return true
		}
	}
	return false
}

// AssembleReleaseNotes renders fragments as one release document headed
// "# <branding.Product> <version>". Sections with the same heading are
// merged; headings keep the order in which fragments, in ReadReleaseFragments
// order, first use them.
func AssembleReleaseNotes(version string, fragments []ReleaseFragment) string {
	var order []string
	bodies := map[string][]string{}
	for _, fragment := range fragments {
		for _, section := range fragment.Sections {
			if _, seen := bodies[section.Heading]; !seen {
				order = append(order, section.Heading)
			}
			bodies[section.Heading] = append(bodies[section.Heading], section.Body)
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# %s %s\n", branding.Product, version)
	for _, heading := range order {
		fmt.Fprintf(&b, "\n## %s\n\n", heading)
		// A blank line keeps each change's entries a block of their own: a
		// tighter join can merge, nest or swallow another change's list.
		b.WriteString(strings.Join(bodies[heading], "\n\n"))
		b.WriteString("\n")
	}
	return b.String()
}

// SelectReleaseNotes selects one version (latest by default), or all published
// notes newer than since. Unreleased changes require an explicit selection.
func SelectReleaseNotes(version, since string) ([]ReleaseNote, error) {
	if version != "" && since != "" {
		return nil, fmt.Errorf("choose a version or --since, not both")
	}
	var baseline *semver.Version
	if since != "" {
		var err error
		baseline, err = releaseVersion(since)
		if err != nil {
			return nil, err
		}
	}
	if version != "" && version != "unreleased" {
		v, err := releaseVersion(version)
		if err != nil {
			return nil, err
		}
		version = v.String()
	}
	notes, err := EmbeddedReleaseNotes()
	if err != nil {
		return nil, err
	}
	selected := make([]ReleaseNote, 0)
	for _, note := range notes {
		if version != "" {
			if note.Version == version {
				return []ReleaseNote{note}, nil
			}
			continue
		}
		if note.Version == "unreleased" {
			continue
		}
		if baseline != nil {
			v, _ := releaseVersion(note.Version)
			if v.GreaterThan(baseline) {
				selected = append(selected, note)
			}
		} else {
			selected = []ReleaseNote{note}
		}
	}
	if version != "" {
		return nil, fmt.Errorf(branding.Text("release notes for %q are not embedded in this binary; run {{command}} release-notes --list"), version)
	}
	return selected, nil
}

// CutReleaseNotes writes <version>.md in dir from the unreleased fragments and
// deletes them in one working-tree change, for the caller to commit. It
// refuses an existing release file and an empty fragment set.
func CutReleaseNotes(dir, version string) (string, error) {
	v, err := releaseVersion(version)
	if err != nil {
		return "", err
	}
	fragments, err := ReadReleaseFragments(os.DirFS(dir))
	if err != nil {
		return "", err
	}
	if len(fragments) == 0 {
		return "", fmt.Errorf("no unreleased fragments in %s", filepath.Join(dir, UnreleasedDir))
	}
	path := filepath.Join(dir, v.String()+".md")
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return "", err
	}
	_, err = file.WriteString(AssembleReleaseNotes(v.String(), fragments))
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return "", err
	}
	for _, fragment := range fragments {
		if err := os.Remove(filepath.Join(dir, UnreleasedDir, fragment.Name)); err != nil {
			return "", err
		}
	}
	return path, nil
}
