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
	pinFile, err := pinInput(c.Harness)
	if err != nil {
		return result, err
	}
	configFile, err := configInput(c.Harness)
	if err != nil {
		return result, err
	}
	result.Files = []string{filepath.Base(pinFile), filepath.Base(configFile)}
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
	if dirty, err := gitOutput("-C", c.Harness, "status", "--porcelain", "--", result.Files[0], result.Files[1]); err != nil {
		return result, err
	} else if dirty != "" {
		return result, fmt.Errorf("the harness pin or %s has uncommitted changes; commit or discard them first", result.Files[1])
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
// including to, oldest first.
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
		if err != nil || item.Draft || item.Prerelease || !v.GreaterThan(from) || v.GreaterThan(to) {
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
