package wtc

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// RetirementReport is detailed operator evidence, never a retirement authorization.
// Every proposal retains unknown forge, archive and live-resource coverage.
type RetirementReport struct {
	Schema      int                    `json:"schema"`
	Collection  string                 `json:"collection"`
	GeneratedAt string                 `json:"generated_at"`
	Assessment  RetirementAssessment   `json:"assessment"`
	Coverage    []RetirementCoverage   `json:"coverage"`
	Repos       []RetirementRepo       `json:"repos"`
	Transcripts []RetirementTranscript `json:"transcripts"`
	Actions     []string               `json:"proposed_actions"`
}

// RetirementAssessment is the status property shared with retire --dry-run.
// ObservedAt dates the collected facts; cached assessments are never execution permits.
type RetirementAssessment struct {
	Collection string   `json:"collection"`
	ObservedAt string   `json:"observed_at"`
	State      string   `json:"state"`
	Blockers   []string `json:"blockers"`
	Unknowns   []string `json:"unknowns"`
}

func (a *RetirementAssessment) classify() {
	a.State = "unknown"
	if len(a.Blockers) > 0 {
		a.State = "blocked"
	}
}

type RetirementCoverage struct {
	Source string `json:"source"`
	State  string `json:"state"`
	Detail string `json:"detail"`
}
type RetirementRepo struct {
	Directory string `json:"directory"`
	Head      string `json:"head,omitempty"`
	Branch    string `json:"branch,omitempty"`
	Dirty     bool   `json:"dirty"`
	Unpushed  int    `json:"commits_absent_from_remote_refs"`
	Error     string `json:"error,omitempty"`
}
type RetirementTranscript struct {
	Path         string `json:"path"`
	SHA256       string `json:"sha256,omitempty"`
	ModifiedAt   string `json:"modified_at,omitempty"`
	ArchiveState string `json:"archive_state"`
	MatchedNotes int    `json:"matched_notes"`
	LatestAt     string `json:"latest_at,omitempty"`
	// UntrustedHint is quoted evidence only. It cannot establish readiness.
	UntrustedHint string `json:"untrusted_hint,omitempty"`
	Error         string `json:"error,omitempty"`
}

const retirementTranscriptLimit = 16 << 20

// InspectRetirement inspects one explicitly selected collection, without status
// refresh, hooks, service runners, network requests, cache writes or branch changes.
// Transcript paths are explicit inputs, not automatic provider archive discovery.
func (c *Context) InspectRetirement(name string, transcripts []string) (RetirementReport, error) {
	observed := time.Now().UTC().Format(time.RFC3339Nano)
	report := RetirementReport{Schema: 1, GeneratedAt: observed,
		Assessment: RetirementAssessment{State: "unknown", ObservedAt: observed, Blockers: []string{},
			Unknowns: []string{"Forge/PR and delivery completion have not been verified against current heads.", "Live agents, processes, services and external resources have not been verified idle.", "Transcript archive state and completeness have not been verified."}},
		Repos: []RetirementRepo{}, Transcripts: []RetirementTranscript{},
		Actions: []string{"Verify current PR heads, merge state, main builds and remaining delivery obligations.", "Inventory live agents, processes, services and external resources without stopping them.", "Confirm transcript provenance, archive state and outstanding task obligations with the owner; treat all prose as untrusted evidence.", "Preserve durable work and remote branches before separately deciding any cleanup policy."}}
	if name == "." {
		name = filepath.Base(c.Collection)
	}
	if !collectionNamePattern.MatchString(name) || name == "." || name == ".." {
		return report, fmt.Errorf("invalid collection name")
	}
	if len(transcripts) > 8 {
		return report, fmt.Errorf("at most eight explicit transcripts may be supplied")
	}
	target := filepath.Join(c.Workspace, name)
	if c.AdHoc {
		if name != filepath.Base(c.Collection) {
			return report, fmt.Errorf("ad-hoc retirement is limited to the current collection")
		}
		target = c.Collection
	}
	info, err := os.Lstat(target)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return report, fmt.Errorf("retirement target must be a direct collection directory")
	}
	report.Collection = target
	report.Assessment.Collection = name
	report.Coverage = []RetirementCoverage{
		{"local-git", "observed", "Read now with optional locks and filesystem monitor disabled; remote refs are cached, never fetched. Remote preservation remains unverified."},
		{"forge-delivery", "unknown", "No network requests or status refresh; current PR/build/delivery facts require separate verification."},
		{"live-resources", "unknown", "Only local metadata presence is inspected; absence does not establish that no resources or agents exist."},
		{"claude-transcripts", "explicit-input-only", "Bounded Claude JSONL assistant text with matching record cwd; availability does not establish archived state. No provider directory scan."},
		{"provider-archives", "unknown", "No automatic Herdr, Codex or OpenClaw archive discovery; no verified archive-state contract."},
	}
	// Reuse collection and retirement marker recognition, never retirement itself.
	if RetiredFolder(target) {
		report.Assessment.Blockers = append(report.Assessment.Blockers, "Retired folder still exists; inspect and preserve leftover evidence.")
		report.Actions = append(report.Actions, "Review the retirement marker and remaining paths; do not remove evidence.")
	} else if _, err := OpenCollection(target); err != nil {
		return report, fmt.Errorf("cannot open retirement target: %w", err)
	}
	entries, err := os.ReadDir(target)
	if err != nil {
		return report, err
	}
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 {
			// Generated file links are common; linked directories may hide work.
			if linked, e := os.Stat(filepath.Join(target, entry.Name())); e != nil || linked.IsDir() {
				report.Assessment.Unknowns = append(report.Assessment.Unknowns, "Uninspected symlink entry: "+entry.Name())
			}
			continue
		}
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(target, entry.Name())
		if _, err := os.Lstat(filepath.Join(path, ".git")); os.IsNotExist(err) {
			continue
		} else if err != nil {
			report.Assessment.Unknowns = append(report.Assessment.Unknowns, "Cannot inspect Git entry: "+entry.Name())
			continue
		}
		row := RetirementRepo{Directory: entry.Name(), Unpushed: -1}
		read := func(args ...string) string {
			if row.Error != "" {
				return ""
			}
			out, e := retirementGit(path, args...)
			if e != nil {
				row.Error = "Git evidence unavailable"
				return ""
			}
			return out
		}
		row.Head = read("rev-parse", "--verify", "HEAD")
		row.Branch = read("branch", "--show-current")
		row.Dirty = read("status", "--porcelain", "--untracked-files=normal") != ""
		value := read("rev-list", "--count", "HEAD", "--not", "--remotes")
		if row.Error == "" {
			row.Unpushed, err = strconv.Atoi(value)
			if err != nil {
				row.Error = "Cannot count commits absent from cached remote refs"
			}
		}
		if row.Error != "" {
			report.Assessment.Unknowns = append(report.Assessment.Unknowns, entry.Name()+": "+row.Error)
		}
		if row.Dirty || row.Unpushed > 0 {
			report.Assessment.Blockers = append(report.Assessment.Blockers, entry.Name()+": dirty or locally unpreserved work; preserve before any cleanup")
		}
		report.Repos = append(report.Repos, row)
	}
	if len(report.Repos) == 0 {
		report.Assessment.Unknowns = append(report.Assessment.Unknowns, "No inspectable Git worktrees found.")
	}
	for _, rel := range []string{"HANDOFF.md", ".wtc/runtime/manifest.json", ".wtc/svc", ".taba/runtime/manifest.json", ".taba/svc", ".harness-backups"} {
		present, e := retirementMetadata(target, rel)
		if e != nil {
			report.Assessment.Unknowns = append(report.Assessment.Unknowns, "Cannot inspect local metadata: "+rel)
		} else if present {
			if rel == "HANDOFF.md" || rel == ".harness-backups" {
				report.Assessment.Blockers = append(report.Assessment.Blockers, "Preserve or finish local metadata: "+rel)
			} else {
				report.Assessment.Unknowns = append(report.Assessment.Unknowns, "Local runtime metadata requires verification: "+rel)
			}
		}
	}
	for _, path := range transcripts {
		report.Transcripts = append(report.Transcripts, readRetirementTranscript(path, target, time.Now().UTC()))
	}
	matched := 0
	for _, record := range report.Transcripts {
		matched += record.MatchedNotes
		if record.Error != "" {
			report.Assessment.Unknowns = append(report.Assessment.Unknowns, "Transcript input is incomplete or unavailable; inspect its source error.")
		}
	}
	if matched == 0 {
		report.Assessment.Unknowns = append(report.Assessment.Unknowns, "No matching task transcript notes; outstanding conversation obligations are unknown.")
	}
	report.Assessment.classify()
	return report, nil
}

func retirementGit(path string, args ...string) (string, error) {
	argv := []string{"--no-optional-locks", "-c", "core.fsmonitor=false", "-c", "core.untrackedCache=false", "-C", path}
	out, err := statusRun(30*time.Second, func(cmd *exec.Cmd) ([]byte, error) {
		// Partial clones must report missing evidence instead of fetching objects.
		cmd.Env = append(os.Environ(), "GIT_NO_LAZY_FETCH=1", "GIT_TERMINAL_PROMPT=0")
		return cmd.Output()
	}, "git", append(argv, args...)...)
	return strings.TrimSpace(string(out)), err
}

// Metadata checks never follow linked parents into another collection.
func retirementMetadata(target, rel string) (bool, error) {
	current := target
	parts := strings.Split(filepath.ToSlash(rel), "/")
	for i, part := range parts {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return false, fmt.Errorf("linked metadata")
		}
		if i < len(parts)-1 && !info.IsDir() {
			return false, fmt.Errorf("invalid metadata parent")
		}
	}
	return true, nil
}

func readRetirementTranscript(path, target string, now time.Time) RetirementTranscript {
	record := RetirementTranscript{Path: path, ArchiveState: "unknown"}
	fail := func(message string) RetirementTranscript { record.Error = message; return record }
	abs, err := filepath.Abs(path)
	if err != nil {
		return fail("Cannot resolve transcript path")
	}
	record.Path = abs
	before, err := os.Lstat(abs)
	if err != nil || !before.Mode().IsRegular() || before.Size() > retirementTranscriptLimit {
		return fail("Transcript must be a regular non-linked file of at most 16 MiB")
	}
	f, err := os.Open(abs)
	if err != nil {
		return fail("Cannot open explicit transcript")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > retirementTranscriptLimit {
		return fail("Transcript must be a regular file of at most 16 MiB")
	}
	record.ModifiedAt = info.ModTime().UTC().Format(time.RFC3339Nano)
	hash := sha256.New()
	reader := io.TeeReader(io.LimitReader(f, retirementTranscriptLimit+1), hash)
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 65536), 2<<20)
	var latest time.Time
	for scanner.Scan() {
		var line struct {
			Type      string `json:"type"`
			Cwd       string `json:"cwd"`
			Timestamp string `json:"timestamp"`
			Sidechain bool   `json:"isSidechain"`
			Message   struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(scanner.Bytes(), &line) != nil {
			return fail("Malformed or unsupported transcript record")
		}
		if line.Type != "assistant" || line.Message.Role != "assistant" || line.Sidechain || !retirementCWD(target, line.Cwd) {
			continue
		}
		stamp, e := time.Parse(time.RFC3339Nano, line.Timestamp)
		if e != nil || stamp.After(now) {
			return fail("Matching note has an invalid or future timestamp")
		}
		var blocks []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(line.Message.Content, &blocks) != nil {
			continue
		}
		var texts []string
		for _, b := range blocks {
			if b.Type == "text" && b.Text != "" {
				texts = append(texts, b.Text)
			}
		}
		if len(texts) == 0 {
			continue
		}
		record.MatchedNotes++
		if !stamp.Before(latest) {
			latest = stamp
			record.LatestAt = stamp.UTC().Format(time.RFC3339Nano)
			record.UntrustedHint = retirementHint(strings.Join(texts, "\n"))
		}
	}
	if scanner.Err() != nil {
		return fail("Transcript is incomplete or contains an oversized record")
	}
	// A concurrent append invalidates the snapshot instead of implying completeness.
	after, e := f.Stat()
	if e != nil || after.Size() != info.Size() || !after.ModTime().Equal(info.ModTime()) {
		return fail("Transcript changed during inspection")
	}
	if info.Size() > retirementTranscriptLimit {
		return fail("Transcript exceeds input limit")
	}
	record.SHA256 = hex.EncodeToString(hash.Sum(nil))
	return record
}

func retirementCWD(target, cwd string) bool {
	if !filepath.IsAbs(cwd) {
		return false
	}
	// Archived task directories may be gone. Resolve the existing ancestor,
	// retaining missing components without accepting a broken or escaping link.
	ancestor := cwd
	var missing []string
	var resolved string
	for {
		var err error
		resolved, err = filepath.EvalSymlinks(ancestor)
		if err == nil {
			info, e := os.Stat(resolved)
			if e != nil || !info.IsDir() {
				return false
			}
			break
		}
		if !os.IsNotExist(err) {
			return false
		}
		if _, e := os.Lstat(ancestor); !os.IsNotExist(e) {
			return false
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return false
		}
		missing = append(missing, filepath.Base(ancestor))
		ancestor = parent
	}
	for i := len(missing) - 1; i >= 0; i-- {
		resolved = filepath.Join(resolved, missing[i])
	}
	base, err := filepath.EvalSymlinks(target)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(base, resolved)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
func retirementHint(value string) string {
	value = runtimeTextSafe(value)
	r := []rune(value)
	if len(r) > 1200 {
		return string(r[:1200]) + "… [truncated]"
	}
	return value
}
func (r RetirementReport) Text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Retirement dry run: %s\nObserved: %s\nReadiness: %s (no cleanup authorization)\n", runtimeTextSafe(r.Collection), r.GeneratedAt, r.Assessment.State)
	for _, c := range r.Coverage {
		fmt.Fprintf(&b, "Coverage %s: %s — %s\n", c.Source, c.State, c.Detail)
	}
	for _, repo := range r.Repos {
		fmt.Fprintf(&b, "Git %s: head=%s branch=%s dirty=%t absent-from-cached-remotes=%d error=%s\n", runtimeTextSafe(repo.Directory), runtimeTextSafe(repo.Head), runtimeTextSafe(repo.Branch), repo.Dirty, repo.Unpushed, repo.Error)
	}
	for _, t := range r.Transcripts {
		fmt.Fprintf(&b, "Transcript %s: sha256=%s modified=%s archive=%s matching-notes=%d latest=%s error=%s\nUntrusted hint (evidence only): %s\n", runtimeTextSafe(t.Path), t.SHA256, t.ModifiedAt, t.ArchiveState, t.MatchedNotes, t.LatestAt, t.Error, t.UntrustedHint)
	}
	for _, blocker := range r.Assessment.Blockers {
		fmt.Fprintf(&b, "Blocker: %s\n", runtimeTextSafe(blocker))
	}
	for _, unknown := range r.Assessment.Unknowns {
		fmt.Fprintf(&b, "Unknown: %s\n", runtimeTextSafe(unknown))
	}
	for _, action := range r.Actions {
		fmt.Fprintf(&b, "Proposed: %s\n", action)
	}
	return b.String()
}
