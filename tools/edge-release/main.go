// Command edge-release names, gates and verifies opt-in edge builds of the
// CLI from the repository root (docs/edge-builds.md):
//
//	go run ./tools/edge-release version  -sha <commit> -time <unix seconds>
//	go run ./tools/edge-release evidence -sha <commit> -repo <owner/name> -runs <runs.json>
//	go run ./tools/edge-release verify   -dist dist -version <v> -sha <commit> -manifest <edge.json> -notes <notes.md>
//
// Edge builds are not releases. Their version is a prerelease of the
// upcoming release (see Version) carrying the source commit, and is also the tag: it has no "v" prefix,
// so the stable/RC tag workflow, `upgrade` and its release lookups never
// select one.
package main

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	iofs "io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/Masterminds/semver/v3"
)

const notesDir = "internal/wtc/release-notes"

// ciWorkflow is the workflow whose successful push run on main is the check
// evidence for an edge build of the same commit.
const ciWorkflow = ".github/workflows/ci.yml"

// Platforms and executables match the stable GoReleaser configuration;
// TestEdgeConfigMatchesStableBuildsWithoutPublishing keeps them in step.
var (
	platforms   = []string{"darwin_amd64", "darwin_arm64", "linux_amd64", "linux_arm64"}
	executables = []string{"taba", "wtc"}
	fullSHA     = regexp.MustCompile(`^[0-9a-f]{40}$`)
	edgeVersion = regexp.MustCompile(`^\d+\.\d+\.\d+-edge\.\d{14}\.g[0-9a-f]{12}$`)
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "edge-release:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: edge-release version|evidence|verify [flags]")
	}
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	sha := fs.String("sha", "", "full source commit SHA")
	switch args[0] {
	case "version":
		when := fs.Int64("time", 0, "source commit time, unix seconds")
		notes := fs.String("notes", notesDir, "numbered release notes directory")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		v, err := Version(os.DirFS(*notes), *sha, time.Unix(*when, 0))
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(stdout, v)
		return err
	case "evidence":
		repo := fs.String("repo", "", "owner/name of the trusted repository")
		runs := fs.String("runs", "", "workflow runs JSON from the GitHub API")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		data, err := os.ReadFile(*runs)
		if err != nil {
			return err
		}
		url, err := Evidence(data, *sha, *repo)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(stdout, url)
		return err
	case "verify":
		dist := fs.String("dist", "dist", "GoReleaser output directory")
		version := fs.String("version", "", "edge version")
		manifest := fs.String("manifest", "", "write the edge manifest here")
		notes := fs.String("notes", "", "write the release body here")
		evidence := fs.String("evidence", "", "URL of the CI run that is the check evidence")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		m, err := Verify(*dist, *version, *sha, *evidence, runtime.GOOS+"_"+runtime.GOARCH)
		if err != nil {
			return err
		}
		return writeOutputs(m, *manifest, *notes)
	}
	return fmt.Errorf("unknown command %q", args[0])
}

// Version names the edge build of sha committed at when:
// <next>-edge.<UTC commit time>.g<12 hex>. <next> follows the newest
// numbered release notes in the source: the patch of a release candidate, or
// the next patch after a stable release. Edge builds of one base order by
// commit time and below that base's release candidates and release.
func Version(notes iofs.FS, sha string, when time.Time) (string, error) {
	if !fullSHA.MatchString(sha) {
		return "", fmt.Errorf("source commit %q is not a full lowercase SHA", sha)
	}
	if when.Unix() <= 0 {
		return "", errors.New("source commit time is required")
	}
	entries, err := iofs.ReadDir(notes, ".")
	if err != nil {
		return "", fmt.Errorf("read numbered release notes: %w", err)
	}
	var newest *semver.Version
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".md") {
			continue
		}
		v, err := semver.StrictNewVersion(strings.TrimSuffix(name, ".md"))
		if err == nil && (newest == nil || v.GreaterThan(newest)) {
			newest = v
		}
	}
	if newest == nil {
		return "", errors.New("no numbered release notes to derive the edge base from")
	}
	base := newest.IncPatch()
	if newest.Prerelease() != "" {
		base = *semver.New(newest.Major(), newest.Minor(), newest.Patch(), "", "")
	}
	return fmt.Sprintf("%s-edge.%s.g%s", base.String(), when.UTC().Format("20060102150405"), sha[:12]), nil
}

type workflowRun struct {
	ID         int64  `json:"id"`
	HeadSHA    string `json:"head_sha"`
	HeadBranch string `json:"head_branch"`
	Event      string `json:"event"`
	Path       string `json:"path"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	HTMLURL    string `json:"html_url"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
	HeadRepository struct {
		FullName string `json:"full_name"`
	} `json:"head_repository"`
}

// Evidence accepts the source commit only when the trusted repository's CI
// workflow ran for exactly that commit as a push to main, and every such run
// completed successfully. The API filters are not trusted: each run is checked
// again. It returns the URL of the newest qualifying run.
func Evidence(data []byte, sha, repo string) (string, error) {
	if !fullSHA.MatchString(sha) {
		return "", fmt.Errorf("source commit %q is not a full lowercase SHA", sha)
	}
	if repo == "" {
		return "", errors.New("trusted repository is required")
	}
	var payload struct {
		WorkflowRuns []workflowRun `json:"workflow_runs"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return "", fmt.Errorf("read workflow runs: %w", err)
	}
	var matched []workflowRun
	for _, r := range payload.WorkflowRuns {
		if r.HeadSHA != sha || r.Path != ciWorkflow || r.Event != "push" || r.HeadBranch != "main" ||
			!strings.EqualFold(r.Repository.FullName, repo) || !strings.EqualFold(r.HeadRepository.FullName, repo) {
			continue
		}
		matched = append(matched, r)
	}
	if len(matched) == 0 {
		return "", fmt.Errorf("no %s push run on main of %s for %s; edge builds need check evidence for the exact commit", ciWorkflow, repo, sha)
	}
	sort.Slice(matched, func(i, j int) bool { return matched[i].ID > matched[j].ID })
	for _, r := range matched {
		if r.Status != "completed" {
			return "", fmt.Errorf("CI run %d for %s is %s; wait for it to complete", r.ID, sha, r.Status)
		}
		if r.Conclusion != "success" {
			return "", fmt.Errorf("CI run %d for %s concluded %s", r.ID, sha, r.Conclusion)
		}
	}
	return matched[0].HTMLURL, nil
}

// Manifest describes one verified edge build. It is published beside the
// archives as edge.json.
type Manifest struct {
	Schema      string     `json:"schema"`
	Channel     string     `json:"channel"`
	Version     string     `json:"version"`
	Tag         string     `json:"tag"`
	SourceSHA   string     `json:"source_sha"`
	Evidence    string     `json:"check_evidence,omitempty"`
	Executables []string   `json:"executables"`
	Checksums   string     `json:"checksums"`
	Artifacts   []Artifact `json:"artifacts"`
}

type Artifact struct {
	Platform string `json:"platform"`
	Archive  string `json:"archive"`
	SHA256   string `json:"sha256"`
}

// Verify checks GoReleaser's unpublished output for an edge build: exactly
// one archive per supported platform, matching checksums.txt, each holding
// both executable names; on the host platform both report the edge version.
func Verify(dist, version, sha, evidence, host string) (Manifest, error) {
	m := Manifest{Schema: "taba-edge/v1", Channel: "edge", Version: version, Tag: version, SourceSHA: sha,
		Evidence: evidence, Executables: executables, Checksums: "checksums.txt"}
	if !edgeVersion.MatchString(version) {
		return m, fmt.Errorf("%q is not an edge version", version)
	}
	if !fullSHA.MatchString(sha) || !strings.HasSuffix(version, ".g"+sha[:12]) {
		return m, fmt.Errorf("edge version %s does not name source commit %q", version, sha)
	}
	sums, err := readChecksums(filepath.Join(dist, "checksums.txt"))
	if err != nil {
		return m, err
	}
	want := map[string]string{}
	for _, p := range platforms {
		want[fmt.Sprintf("taba_%s_%s.tar.gz", version, p)] = p
	}
	for name := range sums {
		if _, ok := want[name]; !ok {
			return m, fmt.Errorf("checksums.txt lists unexpected file %s", name)
		}
	}
	archives, _ := filepath.Glob(filepath.Join(dist, "*.tar.gz"))
	for _, path := range archives {
		if _, ok := want[filepath.Base(path)]; !ok {
			return m, fmt.Errorf("unexpected archive %s", filepath.Base(path))
		}
	}
	for _, p := range platforms {
		name := fmt.Sprintf("taba_%s_%s.tar.gz", version, p)
		path := filepath.Join(dist, name)
		sum, err := fileSHA256(path)
		if err != nil {
			return m, fmt.Errorf("missing %s archive: %w", p, err)
		}
		if sums[name] != sum {
			return m, fmt.Errorf("%s does not match checksums.txt", name)
		}
		if err := checkArchive(path, version, p == host); err != nil {
			return m, fmt.Errorf("%s: %w", name, err)
		}
		m.Artifacts = append(m.Artifacts, Artifact{Platform: p, Archive: name, SHA256: sum})
	}
	return m, nil
}

func readChecksums(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sums := map[string]string{}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 || len(fields[0]) != 64 {
			return nil, fmt.Errorf("malformed checksums.txt line %q", scanner.Text())
		}
		if _, dup := sums[fields[1]]; dup {
			return nil, fmt.Errorf("checksums.txt lists %s twice", fields[1])
		}
		sums[fields[1]] = fields[0]
	}
	return sums, scanner.Err()
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// checkArchive requires both executables at the archive root, regular and
// executable. Other entries must be regular, non-executable files, such as
// the license and readme.
// On the host platform it extracts and runs them.
func checkArchive(path, version string, execute bool) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	found := map[string]bool{}
	dir := ""
	if execute {
		if dir, err = os.MkdirTemp("", "taba-edge-"); err != nil {
			return err
		}
		defer os.RemoveAll(dir)
	}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		name := filepath.Clean(h.Name)
		isExec := h.Typeflag == tar.TypeReg && h.Mode&0o111 != 0
		wanted := false
		for _, e := range executables {
			wanted = wanted || name == e
		}
		if !wanted {
			if isExec || h.Typeflag != tar.TypeReg {
				return fmt.Errorf("unexpected entry %s", h.Name)
			}
			continue
		}
		if !isExec || found[name] {
			return fmt.Errorf("%s must be one executable regular file", name)
		}
		found[name] = true
		if execute {
			out, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o755)
			if err != nil {
				return err
			}
			_, err = io.Copy(out, tr)
			if cerr := out.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return err
			}
		}
	}
	for _, e := range executables {
		if !found[e] {
			return fmt.Errorf("missing executable %s", e)
		}
		if execute {
			out, err := exec.Command(filepath.Join(dir, e), "--version").Output()
			if err != nil {
				return fmt.Errorf("%s --version: %w", e, err)
			}
			if !strings.Contains(" "+strings.TrimSpace(string(out))+" ", " "+version+" ") {
				return fmt.Errorf("%s --version reports %q, not %s", e, strings.TrimSpace(string(out)), version)
			}
		}
	}
	return nil
}

func writeOutputs(m Manifest, manifest, notes string) error {
	if manifest != "" {
		data, err := json.MarshalIndent(m, "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(manifest, append(data, '\n'), 0o644); err != nil {
			return err
		}
	}
	if notes != "" {
		return os.WriteFile(notes, []byte(Notes(m)), 0o644)
	}
	return nil
}

// Notes is the body of an edge GitHub prerelease. Edge builds carry no
// release notes of their own: the embedded notes and `release-notes
// unreleased` describe the source.
func Notes(m Manifest) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Edge build of `%s`. Not a release: no release notes, no Homebrew formula, and `upgrade` never selects it.\n\n", m.SourceSHA)
	if m.Evidence != "" {
		fmt.Fprintf(&b, "Check evidence: %s\n\n", m.Evidence)
	}
	b.WriteString("Select it for one collection, verify it and return to the pinned release as described in [Edge builds](https://github.com/lcorneliussen/taba/blob/main/docs/edge-builds.md). Upcoming changes: `taba release-notes unreleased`.\n\n")
	b.WriteString("| Platform | Archive | SHA-256 |\n| --- | --- | --- |\n")
	for _, a := range m.Artifacts {
		fmt.Fprintf(&b, "| %s | `%s` | `%s` |\n", a.Platform, a.Archive, a.SHA256)
	}
	return b.String()
}
