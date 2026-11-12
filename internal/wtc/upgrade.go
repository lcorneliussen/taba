package wtc

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/Masterminds/semver/v3"
	"taba.sh/taba/internal/branding"
)

// cliReleaseRepo is the GitHub repository that publishes wtc releases, and
// cliMiseTool the mise tool a harness pin renders to.
const (
	cliReleaseRepo = "lcorneliussen/taba"
	cliMiseTool    = "github:" + cliReleaseRepo
	// legacyCLIMiseTool is the pin key before the repository rename. 0.1.x
	// binaries still render it; GitHub's rename redirect is expected to keep it
	// installable, which is checked once a release is published.
	legacyCLIMiseTool = "github:lcorneliussen/wtc-cli"
)

// UpgradeOptions selects the release a harness moves its pin to. An empty
// Version means the latest published release.
type UpgradeOptions struct {
	Version string
	DryRun  bool
}

// UpgradeResult records one pin transition and the release notes it spans.
type UpgradeResult struct {
	From        string   `json:"from"`
	To          string   `json:"to"`
	Requires    string   `json:"requires"`
	Files       []string `json:"files"`
	DryRun      bool     `json:"dry_run"`
	NotesSource string   `json:"notes_source"`
	NotesError  string   `json:"notes_error,omitempty"`
	// NotInstalled is set when the pin was written on a machine without
	// mise, so the release itself still has to be installed.
	NotInstalled bool          `json:"not_installed,omitempty"`
	Notes        []ReleaseNote `json:"notes"`
}

// CompatibilityRange is the harness range a pin implies: the pinned release up
// to, not including, the next minor version.
func CompatibilityRange(version string) (string, error) {
	v, err := releaseVersion(version)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(">=%s,<%d.%d", v.String(), v.Major(), v.Minor()+1), nil
}

var compatibilityHeader = regexp.MustCompile(`^\s*\[compatibility\]\s*(#.*)?$`)
var tomlHeader = regexp.MustCompile(`^\s*\[`)
var requiresLine = regexp.MustCompile(`^(\s*requires\s*=\s*)"[^"]*"(.*)$`)

// replaceCompatibility rewrites the requires line of the harness config's
// [compatibility] table and leaves every other line as written.
func replaceCompatibility(config, requires string) (string, error) {
	lines := strings.SplitAfter(config, "\n")
	inTable := false
	for i, line := range lines {
		body := strings.TrimRight(line, "\r\n")
		if tomlHeader.MatchString(body) {
			inTable = compatibilityHeader.MatchString(body)
			continue
		}
		if !inTable {
			continue
		}
		if m := requiresLine.FindStringSubmatch(body); m != nil {
			lines[i] = m[1] + fmt.Sprintf("%q", requires) + m[2] + line[len(body):]
			return strings.Join(lines, ""), nil
		}
	}
	return "", errors.New("harness configuration has no [compatibility] requires line to update")
}

// Upgrade moves the harness pin and compatibility range to a newer release,
// refreshes this collection's environment for it, and returns the release
// notes in between. It never commits: the change is the harness's to review.
func (c *Context) Upgrade(opt UpgradeOptions) (UpgradeResult, error) {
	result := UpgradeResult{DryRun: opt.DryRun, Files: []string{".wtc-cli-version", "wtc.toml"}}
	if c.AdHoc {
		return result, errors.New(branding.Text("{{command}} upgrade needs a harness collection; an ad-hoc collection uses the installed wtc"))
	}
	if operation := catchUpOperation(catchUpTarget{path: c.Harness}); operation != "" {
		// The shared guard also fails closed when Git cannot inspect a marker.
		// Preserve that error instead of diagnosing an operation that may not exist.
		if _, err := gitOutput("-C", c.Harness, "rev-parse", "--git-path", operation); err != nil {
			return result, err
		}
		return result, fmt.Errorf("the harness has an in-progress %s; finish or abort it before upgrading", operation)
	}
	pinFile, err := pinInput(c.Harness)
	if err != nil {
		return result, err
	}
	configFile, err := configInput(c.Harness)
	if err != nil {
		return result, err
	}
	result.Files = []string{filepath.Base(pinFile), filepath.Base(configFile)}
	// Follow aliases before reading or writing, and check every link as well as
	// its destination: Git reports target edits under the target's own name.
	pinFile, err = upgradeInput(c.Harness, pinFile)
	if err != nil {
		return result, err
	}
	configFile, err = upgradeInput(c.Harness, configFile)
	if err != nil {
		return result, err
	}
	pin, err := os.ReadFile(pinFile)
	if err != nil {
		return result, fmt.Errorf("cannot read the harness pin: %w", err)
	}
	from, err := releaseVersion(strings.TrimSpace(string(pin)))
	if err != nil {
		return result, err
	}
	result.From = from.String()
	target := opt.Version
	if target == "" {
		if target, err = latestCLIRelease(); err != nil {
			return result, err
		}
	}
	to, err := releaseVersion(target)
	if err != nil {
		return result, err
	}
	result.To = to.String()
	if !to.GreaterThan(from) {
		return result, fmt.Errorf("the harness already pins %s; %s is not newer", result.From, result.To)
	}
	if opt.Version != "" {
		// Checked before anything is written, so a typo leaves no pin behind
		// that mise cannot install.
		if err := exec.Command("gh", "api", "repos/"+cliReleaseRepo+"/releases/tags/v"+result.To, "--jq", ".tag_name").Run(); err != nil {
			return result, fmt.Errorf(branding.Text("{{command}} %s is not a published release (checked with gh): %w"), result.To, err)
		}
	}
	if result.Requires, err = CompatibilityRange(result.To); err != nil {
		return result, err
	}
	config, err := os.ReadFile(configFile)
	if err != nil {
		return result, fmt.Errorf("cannot read %s: %w", filepath.Base(configFile), err)
	}
	updated, err := replaceCompatibility(string(config), result.Requires)
	if err != nil {
		return result, err
	}
	if !opt.DryRun {
		if err := os.WriteFile(pinFile, []byte(result.To+"\n"), 0644); err != nil {
			return result, err
		}
		if err := os.WriteFile(configFile, []byte(updated), 0644); err != nil {
			return result, err
		}
		if err := c.refreshEnv(); err != nil {
			return result, fmt.Errorf("pin updated, but the collection environment was not: %w", err)
		}
		if _, err := exec.LookPath("mise"); err != nil {
			result.NotInstalled = true
		} else if err := c.InstallMise(); err != nil {
			return result, fmt.Errorf("pin updated, but the new release was not installed: %w", err)
		}
	}
	result.Notes, result.NotesSource, err = upgradeNotes(c.Collection, from, to, opt.DryRun)
	if err != nil && !opt.DryRun {
		// The pin is written, and installed when mise is present (see
		// NotInstalled); missing notes undo neither.
		result.NotesError = err.Error()
		return result, nil
	}
	return result, err
}

// upgradeInput resolves a selected input within the harness and requires each
// symlink and the final regular file to be tracked and clean. Resolving one
// component at a time also protects intermediate aliases and directory links.
func upgradeInput(harness, input string) (string, error) {
	harnessPath, err := filepath.Abs(harness)
	if err != nil {
		return "", err
	}
	root, err := filepath.EvalSymlinks(harnessPath)
	if err != nil {
		return "", err
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return "", err
	}
	current := root
	pending := []string{filepath.Base(input)}
	links := 0
	for len(pending) > 0 {
		part := pending[0]
		pending = pending[1:]
		if part == "" || part == "." {
			continue
		}
		if part == ".." {
			if current == root {
				return "", fmt.Errorf("upgrade input %s escapes the harness", filepath.Base(input))
			}
			current = filepath.Dir(current)
			continue
		}
		path := filepath.Join(current, part)
		info, err := os.Lstat(path)
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 || len(pending) == 0 {
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return "", err
			}
			spec := ":(literal)" + filepath.ToSlash(rel)
			if _, err := gitOutput("-C", root, "ls-files", "--error-unmatch", "--", spec); err != nil {
				return "", fmt.Errorf("upgrade input %s must be tracked by the harness", rel)
			}
			if dirty, err := gitOutput("-C", root, "status", "--porcelain", "--untracked-files=all", "--", spec); err != nil {
				return "", err
			} else if dirty != "" {
				return "", fmt.Errorf("upgrade input %s has uncommitted changes; commit or discard them first", rel)
			}
		}
		if info.Mode()&os.ModeSymlink != 0 {
			links++
			if links > 40 {
				return "", fmt.Errorf("too many symlinks in upgrade input %s", filepath.Base(input))
			}
			target, err := os.Readlink(path)
			if err != nil {
				return "", err
			}
			if filepath.IsAbs(target) {
				// Accept either spelling of a symlinked harness root, retaining target
				// components so a symlink followed by ".." is resolved in filesystem order.
				relative := ""
				for _, prefix := range []string{root, harnessPath} {
					if target == prefix {
						relative = "."
						break
					}
					if strings.HasPrefix(target, prefix+string(filepath.Separator)) {
						relative = strings.TrimPrefix(target, prefix+string(filepath.Separator))
						break
					}
				}
				if relative == "" {
					return "", fmt.Errorf("upgrade input %s escapes the harness", filepath.Base(input))
				}
				current, target = root, relative
			}
			pending = append(strings.Split(target, string(filepath.Separator)), pending...)
			continue
		}
		if len(pending) > 0 && !info.IsDir() || len(pending) == 0 && !info.Mode().IsRegular() {
			return "", fmt.Errorf("upgrade input %s must resolve to a regular file", filepath.Base(input))
		}
		current = path
	}
	info, err := os.Stat(current)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("upgrade input %s must resolve to a regular file", filepath.Base(input))
	}
	return current, nil
}

// upgradeNotes prefers the target binary's own archive, which carries later
// corrections to earlier notes. A dry run, or a machine without mise, reads the
// published GitHub release bodies, which are the numbered notes as released.
func upgradeNotes(dir string, from, to *semver.Version, dryRun bool) ([]ReleaseNote, string, error) {
	if !dryRun {
		if mise, err := exec.LookPath("mise"); err == nil {
			cmd := exec.Command(mise, "exec", cliMiseTool+"@"+to.String(), "--", "wtc", "release-notes", "--since", from.String(), "--json")
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "WTC_DEV_BUILD=off")
			if out, err := cmd.Output(); err == nil {
				var envelope struct {
					Data []ReleaseNote `json:"data"`
				}
				if json.Unmarshal(out, &envelope) == nil {
					return envelope.Data, "wtc " + to.String() + " release-notes", nil
				}
			}
		}
	}
	notes, err := publishedReleaseNotes(from, to)
	return notes, "GitHub releases", err
}

func latestCLIRelease() (string, error) {
	out, err := exec.Command("gh", "api", "repos/"+cliReleaseRepo+"/releases/latest", "--jq", ".tag_name").Output()
	if err != nil {
		return "", fmt.Errorf("cannot resolve the latest wtc release with gh; pass --version: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// publishedReleaseNotes returns the published releases after from, up to and
// including to, oldest first. Prereleases are skipped unless one is the
// target itself, so `--version <v>-rc.N` finds its own notes.
func publishedReleaseNotes(from, to *semver.Version) ([]ReleaseNote, error) {
	out, err := exec.Command("gh", "api", "--paginate", "repos/"+cliReleaseRepo+"/releases?per_page=100",
		"--jq", ".[] | {tag_name, body, draft, prerelease}").Output()
	if err != nil {
		return nil, fmt.Errorf("cannot read published wtc releases with gh: %w", err)
	}
	type release struct {
		version *semver.Version
		note    ReleaseNote
	}
	var releases []release
	scanner := bufio.NewScanner(bytes.NewReader(out))
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		var item struct {
			Tag        string `json:"tag_name"`
			Body       string `json:"body"`
			Draft      bool   `json:"draft"`
			Prerelease bool   `json:"prerelease"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &item); err != nil {
			return nil, fmt.Errorf("unexpected gh release output: %w", err)
		}
		v, err := releaseVersion(item.Tag)
		if err != nil || item.Draft || !releaseNoteInRange(v, from, to, item.Prerelease) {
			continue
		}
		releases = append(releases, release{v, ReleaseNote{Version: v.String(), Content: item.Body}})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	sort.Slice(releases, func(i, j int) bool { return releases[i].version.LessThan(releases[j].version) })
	notes := make([]ReleaseNote, 0, len(releases))
	for _, r := range releases {
		notes = append(notes, r.note)
	}
	if len(notes) == 0 || notes[len(notes)-1].Version != to.String() {
		return notes, fmt.Errorf(branding.Text("{{command}} %s is not a published release"), to)
	}
	return notes, nil
}
