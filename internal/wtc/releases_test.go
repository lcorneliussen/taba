package wtc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/Masterminds/semver/v3"
	"taba.sh/taba/internal/branding"
)

func TestEmbeddedReleaseNotes(t *testing.T) {
	notes, err := EmbeddedReleaseNotes()
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) < 40 || notes[0].Version != "0.1.0" || notes[len(notes)-1].Version != "unreleased" {
		t.Fatalf("missing historical or upcoming notes: %d documents", len(notes))
	}
	for i, note := range notes {
		// 0.1.x notes keep their original "# wtc" heading; from 0.2.0-0 on,
		// pre-releases included, notes are headed with branding.Product.
		header := "# " + branding.Product + " " + note.Version + "\n"
		if v, err := releaseVersion(note.Version); err == nil && v.LessThan(semver.MustParse("0.2.0-0")) {
			header = "# wtc " + note.Version + "\n"
		}
		if !strings.HasPrefix(note.Content, header) || strings.TrimSpace(strings.TrimPrefix(note.Content, header)) == "" {
			t.Fatalf("missing heading or body for %s", note.Version)
		}
		if note.Version == "unreleased" {
			continue
		}
		v, err := releaseVersion(note.Version)
		if err != nil || v.String() != note.Version {
			t.Fatalf("noncanonical archive version %q", note.Version)
		}
		if i > 0 {
			previous, _ := releaseVersion(notes[i-1].Version)
			if !v.GreaterThan(previous) {
				t.Fatalf("archive out of order: %s, %s", previous, v)
			}
		}
	}
}

func TestSelectReleaseNotes(t *testing.T) {
	all, err := EmbeddedReleaseNotes()
	if err != nil {
		t.Fatal(err)
	}
	latest := all[len(all)-2].Version
	tests := []struct {
		name, version, since string
		want                 []string
	}{
		{"latest", "", "", []string{latest}},
		{"exact with v", "v0.1.9", "", []string{"0.1.9"}},
		{"unreleased explicit", "unreleased", "", []string{"unreleased"}},
		{"empty range", "", latest, []string{}},
		{"future baseline", "", "9.0.0", []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			notes, err := SelectReleaseNotes(tt.version, tt.since)
			if err != nil {
				t.Fatal(err)
			}
			if notes == nil || len(notes) != len(tt.want) {
				t.Fatalf("got %v, want %v", notes, tt.want)
			}
			for i, want := range tt.want {
				if notes[i].Version != want {
					t.Fatalf("got %s, want %s", notes[i].Version, want)
				}
			}
		})
	}

	notes, err := SelectReleaseNotes("", "v0.1.8")
	if err != nil || len(notes) < 30 || notes[0].Version != "0.1.9" || notes[1].Version != "0.1.10" || notes[len(notes)-1].Version != latest {
		t.Fatal("upgrade range must exclude baseline and unreleased, and sort numerically", err)
	}
	for _, args := range [][2]string{
		{"0.1.38", "0.1.35"}, {"9.0.0", ""}, {"../README", ""},
		{"0.1", ""}, {"", "unreleased"}, {"", "garbage"}, {"", "0.1"},
	} {
		if _, err := SelectReleaseNotes(args[0], args[1]); err == nil {
			t.Fatalf("expected error for %q", args)
		}
	}
}

func TestParseReleaseFragment(t *testing.T) {
	fragment, err := ParseReleaseFragment("7-x.md", "\n## Review\n\n- one\n  continued\n\n## Retire\n\n- two\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(fragment.Sections) != 2 || fragment.Sections[0].Heading != "Review" || fragment.Sections[0].Body != "- one\n  continued" || fragment.Sections[1].Body != "- two" {
		t.Fatalf("unexpected sections: %#v", fragment.Sections)
	}
	fenced := "## Upgrade\n\n- Refresh:\n\n  ```sh\n  # Refresh the environment\n  ## not a heading\n  wtc env setup\n  ```\n\n~~~~\n# also code\n~~~\n## still code\n~~~~\n\n## Next\n\n- after\n"
	fragment, err = ParseReleaseFragment("8-fence.md", fenced)
	if err != nil {
		t.Fatal(err)
	}
	if len(fragment.Sections) != 2 || fragment.Sections[1].Heading != "Next" || !strings.Contains(fragment.Sections[0].Body, "  ## not a heading\n") || !strings.Contains(fragment.Sections[0].Body, "## still code\n~~~~") {
		t.Fatalf("headings inside fences must stay code: %#v", fragment.Sections)
	}
	listed := "## Upgrade\n\n- ~~~sh\n  wtc env setup\n  ~~~\n- 1. ```\n     # comment\n     ```\n"
	fragment, err = ParseReleaseFragment("9-list-fence.md", listed)
	if err != nil || len(fragment.Sections) != 1 || !strings.Contains(fragment.Sections[0].Body, "     # comment") {
		t.Fatalf("a fence opened on a list marker must parse: %#v, %v", fragment, err)
	}
	for name, content := range map[string]string{
		"indented code":         "## Upgrade\n\nRun:\n\n    ~~~sh\n",
		"indented in list item": "## Upgrade\n\n- step\n\n      ```\n",
		"code on marker line":   "## Upgrade\n\n-     ~~~sh\n",
		"nested marker fence":   "## Upgrade\n\n- a\n    - ~~~sh\n      code\n      ~~~\n",
		"third-level fence":     "## Upgrade\n\n- outer\n  - inner\n    - ~~~sh\n      ## echo ok\n      ~~~\n",
		"subheading":            "## Upgrade\n\n### Steps\n\n- one\n",
		"space separator":       "## Upgrade\n  \n- one\n",
		"tab separator":         "## Upgrade\n\t\n\n- one\n",
	} {
		fragment, err := ParseReleaseFragment(name, content)
		if err != nil || len(fragment.Sections) != 1 || fragment.Sections[0].Heading != "Upgrade" {
			t.Fatalf("%s: one Upgrade section expected: %#v, %v", name, fragment, err)
		}
	}
	fragment, err = ParseReleaseFragment("10-item-fence.md", "## Upgrade\n\n- step\n\n  ```\n  ## code\n  ```\n\n## Next\n\n- x\n")
	if err != nil || len(fragment.Sections) != 2 || !strings.Contains(fragment.Sections[0].Body, "## code") {
		t.Fatalf("a fence inside a list item must parse: %#v, %v", fragment, err)
	}
	for name, content := range map[string]string{
		"fence first":      "```\n## inside\n```\n",
		"unclosed fence":   "## Upgrade\n\n```\n## inside to the end\n",
		"indented entries": "## Review\n\n  - nests under the previous change\n",
		"comment only":     "## Review\n\n<!-- todo -->\n",
		"reference only":   "## Review\n\n[x]: https://example.com\n",
		"subheading only":  "## Review\n\n### Only\n",
		"reference link":   "## Review\n\n- [change][issue]\n\n[issue]: https://example.com/1\n",
		"nested reference": "## Review\n\n- one\n\n  [issue]: https://example.com/1\n",
		"no heading":       "- loose entry\n",
		"release heading":  "# wtc unreleased\n\n## Review\n\n- x\n",
		"empty section":    "## Review\n\n## Retire\n\n- x\n",
		"empty heading":    "## \n\n- x\n",
		"setext heading":   "Topic\n-----\n\n- x\n",
		"nothing":          "\n",
	} {
		if _, err := ParseReleaseFragment(name, content); err == nil {
			t.Fatalf("%s: expected an error", name)
		}
	}
}

func writeFragments(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, UnreleasedDir), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, UnreleasedDir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestAssembleReleaseNotesMergesSectionsInIssueOrder(t *testing.T) {
	dir := writeFragments(t, map[string]string{
		"README.md":    "# Not a fragment\n",
		"117-later.md": "## Review\n\n- later review\n",
		"46-first.md":  "## Review\n\n- first review\n",
		"56-retire.md": "## Retire\n\n- retire\n\n## Review\n\n- middle review\n",
		"misc.md":      "## Docs\n\n- unnumbered\n",
	})
	fragments, err := ReadReleaseFragments(os.DirFS(dir))
	if err != nil {
		t.Fatal(err)
	}
	got := AssembleReleaseNotes("1.2.3", fragments)
	want := "# taba/wtc 1.2.3\n\n## Review\n\n- first review\n\n- middle review\n\n- later review\n\n## Retire\n\n- retire\n\n## Docs\n\n- unnumbered\n"
	if got != want {
		t.Fatalf("assembled notes:\n%s\nwant:\n%s", got, want)
	}
}

func TestAssembleReleaseNotesKeepsEachChangeABlock(t *testing.T) {
	fragments := []ReleaseFragment{
		{Name: "1.md", Sections: []ReleaseSection{{Heading: "Steps", Body: "- first\n  continued"}}},
		{Name: "2.md", Sections: []ReleaseSection{{Heading: "Steps", Body: "2. second"}}},
		{Name: "3.md", Sections: []ReleaseSection{{Heading: "Steps", Body: "Prose."}}},
	}
	want := "# taba/wtc 1.0.0\n\n## Steps\n\n- first\n  continued\n\n2. second\n\nProse.\n"
	if got := AssembleReleaseNotes("1.0.0", fragments); got != want {
		t.Fatalf("got:\n%q\nwant:\n%q", got, want)
	}
}

func TestReleaseNotesWithoutFragments(t *testing.T) {
	notes, err := readReleaseNotes(fstest.MapFS{
		"0.1.0.md":             {Data: []byte("# wtc 0.1.0\n\n- first\n")},
		"unreleased/README.md": {Data: []byte("# Unreleased\n")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 2 || notes[1].Version != "unreleased" || notes[1].Content != "# taba/wtc unreleased\n\nNo unreleased changes.\n" {
		t.Fatalf("got %#v", notes)
	}
}

func TestReleaseFragmentsSkipFilesEmbedSkips(t *testing.T) {
	dir := writeFragments(t, map[string]string{".DS_Store": "\x00binary", "_draft.md": "no heading", "1-x.md": "## A\n\n- x\n"})
	fragments, err := ReadReleaseFragments(os.DirFS(dir))
	if err != nil || len(fragments) != 1 {
		t.Fatalf("got %v, %v", fragments, err)
	}
}

// The unreleased directory must be embedded as a directory: that takes every
// non-.md fragment (so it fails reads) and leaves out dot and underscore files.
func TestEmbedPatternIncludesEveryFragmentFile(t *testing.T) {
	source, err := os.ReadFile("releases.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(source), "//go:embed release-notes/*.md release-notes/unreleased\n") {
		t.Fatal("go:embed must take release-notes/unreleased as a directory")
	}
}

func TestReadReleaseFragmentsNamesTheBrokenFile(t *testing.T) {
	dir := writeFragments(t, map[string]string{"9-bad.md": "- no heading\n"})
	if _, err := ReadReleaseFragments(os.DirFS(dir)); err == nil || !strings.Contains(err.Error(), "unreleased/9-bad.md") {
		t.Fatalf("expected an error naming the fragment, got %v", err)
	}
	dir = writeFragments(t, map[string]string{"README.md": "# Unreleased\n"})
	if err := os.MkdirAll(filepath.Join(dir, UnreleasedDir, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, UnreleasedDir, "nested", "123-change.md"), []byte("## Review\n\n- x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadReleaseFragments(os.DirFS(dir)); err == nil || !strings.Contains(err.Error(), "unreleased/nested") {
		t.Fatalf("expected a nested fragment directory to be rejected, got %v", err)
	}
	dir = writeFragments(t, map[string]string{"9-notes.txt": "## Review\n\n- x\n"})
	if _, err := ReadReleaseFragments(os.DirFS(dir)); err == nil {
		t.Fatal("expected a non-Markdown fragment to be rejected")
	}
}

func TestCutReleaseNotes(t *testing.T) {
	dir := writeFragments(t, map[string]string{
		"README.md":   "# Unreleased\n",
		"2-second.md": "## Status\n\n- second\n",
		"1-first.md":  "## Status\n\n- first\n",
	})
	path, err := CutReleaseNotes(dir, "v1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(dir, "1.2.3.md") {
		t.Fatalf("wrote %s", path)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "# taba/wtc 1.2.3\n\n## Status\n\n- first\n\n- second\n" {
		t.Fatalf("release file %q: %v", data, err)
	}
	left, _ := os.ReadDir(filepath.Join(dir, UnreleasedDir))
	if len(left) != 1 || left[0].Name() != "README.md" {
		t.Fatalf("fragments left behind: %v", left)
	}
	if _, err := CutReleaseNotes(dir, "1.2.4"); err == nil {
		t.Fatal("expected an error with no fragments")
	}
	dir = writeFragments(t, map[string]string{"1-x.md": "## Status\n\n- x\n"})
	if err := os.WriteFile(filepath.Join(dir, "1.2.3.md"), []byte("# wtc 1.2.3\n\nkept\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := CutReleaseNotes(dir, "1.2.3"); err == nil {
		t.Fatal("expected an existing release file to be refused")
	}
	if _, err := os.Stat(filepath.Join(dir, UnreleasedDir, "1-x.md")); err != nil {
		t.Fatal("a refused cut must keep its fragments")
	}
	if _, err := CutReleaseNotes(dir, "1.2"); err == nil {
		t.Fatal("expected an invalid version to be refused")
	}
}
