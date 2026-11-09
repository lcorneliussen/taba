package wtc

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"taba.sh/taba/internal/envcompat"
)

// CatchUpOptions describes one selected collection sweep. Other collections
// are never implied; All must be chosen explicitly by the caller.
type CatchUpOptions struct {
	All          bool
	Collections  []string
	Repos        []string
	CleanOnly    bool
	DryRun       bool
	ReloadStatus bool
	Report       string
	NoSkills     bool
	NoMCP        bool
	NoEnv        bool
	NoSecrets    bool
	// IdleOnly leaves a collection whose herdr agent is working or blocked
	// to that agent. InstallTools runs mise install in each harness target,
	// so a new pin is usable at once; it is skipped when that target's env
	// refresh failed, and with NoEnv it installs what mise.toml already pins.
	IdleOnly     bool
	InstallTools bool
}

// RolloutOptions is the sweep `wtc upgrade --rollout` runs: every
// collection's harness, clean worktrees and idle agents only, with the pinned
// tools installed.
func RolloutOptions(dryRun bool) CatchUpOptions {
	return CatchUpOptions{All: true, Repos: []string{"harness"}, CleanOnly: true, IdleOnly: true, InstallTools: true, DryRun: dryRun}
}

type CatchUpRow struct {
	Kind           string  `json:"kind"`
	Collection     string  `json:"collection"`
	Repo           string  `json:"repo"`
	Outcome        string  `json:"outcome"`
	Reason         string  `json:"reason"`
	SourceSHA      *string `json:"source_sha"`
	TargetSHA      *string `json:"target_sha"`
	TargetRef      string  `json:"target_ref,omitempty"`
	ResultSHA      *string `json:"result_sha"`
	NextActor      string  `json:"next_actor,omitempty"`
	Handoff        string  `json:"handoff,omitempty"`
	CollectionPath string  `json:"collection_path"`
	NextAction     string  `json:"next_action,omitempty"`
}

type CatchUpReport struct {
	SchemaVersion int          `json:"schema_version"`
	GeneratedAt   string       `json:"generated_at"`
	Initiator     string       `json:"initiator"`
	DryRun        bool         `json:"dry_run"`
	ExitStatus    int          `json:"exit_status"`
	Outcomes      []CatchUpRow `json:"outcomes"`
	ReportError   string       `json:"report_error,omitempty"`
}

type catchUpTarget struct {
	collection string
	path       string
	repo       string
	owner      string
	ref        string
	harness    bool
	// managed records registry membership at inventory time. An unmanaged
	// ext. sibling has no entry, so the control root holds nothing for it and
	// the secret linker would reject its directory name. Hooks re-resolve it
	// from the current registry, since a harness self-update may change it.
	managed bool
	// renamed is the repository's current name when this sibling's folder
	// still uses an alias from before a rename.
	renamed string
	// noRemote records an owner without any remote. Its own branches
	// are the only truth it has, so development refs resolve locally.
	noRemote bool
}

// catchUpLocalRef maps a remote-tracking development ref onto the owner's own
// branch when the owner has no remote at all. It is fully qualified, so a tag
// of the same name cannot stand in for the branch.
func catchUpLocalRef(t catchUpTarget, ref string) string {
	if t.noRemote {
		return "refs/heads/" + catchUpBranchName(ref)
	}
	return ref
}

// catchUpBranchName is the branch a development ref names, for display and
// for comparison with a worktree's current branch. It accepts short and
// qualified, local and origin forms.
func catchUpBranchName(ref string) string {
	for _, prefix := range []string{"refs/heads/", "refs/remotes/origin/", "origin/"} {
		if strings.HasPrefix(ref, prefix) {
			return strings.TrimPrefix(ref, prefix)
		}
	}
	return ref
}

var catchUpSelector = regexp.MustCompile(`^[A-Za-z0-9_./-]+$`)

func (r *CatchUpReport) add(kind, collection, repo, outcome, reason, source, target, result string) {
	row := CatchUpRow{Kind: kind, Collection: collection, Repo: repo, Outcome: outcome,
		Reason: reason, SourceSHA: catchUpMaybeSHA(source), TargetSHA: catchUpMaybeSHA(target), ResultSHA: catchUpMaybeSHA(result),
		CollectionPath: filepath.Join(filepath.Dir(r.Initiator), collection)}
	if outcome == "needs-owner" {
		row.NextActor = "owning agent for collection " + collection
		row.Handoff = "needed; no notification sent"
		row.NextAction = "Resume in target collection " + row.CollectionPath + "; read this report and inspect current state before fixing or pushing."
		if strings.HasPrefix(reason, "merge conflict;") {
			row.NextAction = "Resume in target collection " + row.CollectionPath + "; inspect the listed paths and current branch, merge the target ref, resolve conflicts, run relevant checks, commit the merge, and push only if its PR is open or draft."
		} else if strings.HasPrefix(reason, "in-progress MERGE_HEAD;") {
			row.NextAction = "Resume in target collection " + row.CollectionPath + "; inspect git status and unmerged paths, resolve or abort the existing merge, then run relevant checks and push only if its PR is open or draft."
		} else if strings.HasPrefix(reason, "merge abort failed;") {
			row.NextAction = "Resume in target collection " + row.CollectionPath + "; preserve the in-progress merge, inspect git status and unmerged paths, then resolve and commit or safely abort it before any retry."
		}
	}
	if outcome == "failed" || outcome == "needs-owner" {
		r.ExitStatus = 1
	}
	r.Outcomes = append(r.Outcomes, row)
}

func catchUpMaybeSHA(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func (r CatchUpReport) Markdown() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Catch-up report\n\nInitiated from: %s\n\n", r.Initiator)
	b.WriteString("| Collection | Repo / step | Target ref | Outcome | Reason |\n| --- | --- | --- | --- | --- |\n")
	for _, row := range r.Outcomes {
		fields := []string{row.Collection, row.Repo, row.TargetRef, row.Outcome, row.Reason}
		for i, field := range fields {
			fields[i] = strings.ReplaceAll(strings.ReplaceAll(field, "|", "\\|"), "\n", " ")
		}
		fmt.Fprintf(&b, "| %s |\n", strings.Join(fields, " | "))
	}
	for _, row := range r.Outcomes {
		if row.NextAction == "" {
			continue
		}
		fmt.Fprintf(&b, "\n### %s / %s\n\n%s\n", row.Collection, row.Repo, row.NextAction)
	}
	b.WriteString("\nneeds-owner: hand the named collection its row and source/target SHAs. This report does not wake an idle agent or authorize edits in that collection.\n")
	return b.String()
}

func writeCatchUpFile(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".catch-up-")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// SaveCatchUpReport persists the complete report even after partial failures.
// A requested but unavailable report path is reflected in the returned data.
func SaveCatchUpReport(report *CatchUpReport, path string) error {
	if path == "" {
		return nil
	}
	// Check the path as given and as resolved, so a symlinked directory
	// cannot lead into a retired folder.
	parent, err := filepath.Abs(filepath.Dir(path))
	if err != nil {
		report.ExitStatus = 1
		report.ReportError = err.Error()
		return err
	}
	resolved, err := filepath.EvalSymlinks(parent)
	if err != nil {
		report.ExitStatus = 1
		report.ReportError = err.Error()
		return err
	}
	for _, dir := range []string{parent, resolved} {
		if retired := retiredAncestor(dir); retired != "" {
			report.ExitStatus = 1
			report.ReportError = retiredFolderError(retired).Error()
			return errors.New(report.ReportError)
		}
	}
	markdownErr := writeCatchUpFile(path+".md", []byte(report.Markdown()))
	if markdownErr != nil {
		report.ExitStatus = 1
		report.ReportError = markdownErr.Error()
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	if err := writeCatchUpFile(path, append(data, '\n')); err != nil {
		report.ExitStatus = 1
		report.ReportError = err.Error()
		return err
	}
	return markdownErr
}

// CatchUpInventory resolves every target before a fetch or worktree update.
// Missing collections are reported independently, while selector typos stop
// the entire sweep before any writes.
func (c *Context) CatchUpInventory(opt CatchUpOptions) (CatchUpReport, []catchUpTarget, error) {
	report := CatchUpReport{SchemaVersion: 1, GeneratedAt: time.Now().UTC().Format(time.RFC3339Nano),
		Initiator: c.Collection, DryRun: opt.DryRun, Outcomes: []CatchUpRow{}}
	if opt.All && len(opt.Collections) != 0 {
		return report, nil, fmt.Errorf("--all cannot be combined with collection names")
	}
	selectors := map[string]bool{}
	for _, name := range opt.Repos {
		if !catchUpSelector.MatchString(name) || name == "." || name == ".." || strings.Contains(name, "..") || strings.Contains(name, "/") {
			return report, nil, fmt.Errorf("invalid repository selector %q", name)
		}
		selectors[name] = false
	}
	collections := append([]string(nil), opt.Collections...)
	if opt.All || len(collections) > 1 || len(collections) == 1 && collections[0] != filepath.Base(c.Collection) {
		if err := c.RequireWorkspace("catch-up of other collections"); err != nil {
			return report, nil, err
		}
	}
	if opt.All {
		dirs, err := WorkspaceCollections(c.Workspace)
		if err != nil {
			return report, nil, err
		}
		for _, dir := range dirs {
			collections = append(collections, filepath.Base(dir))
		}
	} else if len(collections) == 0 {
		collections = []string{filepath.Base(c.Collection)}
	}
	if len(collections) == 0 {
		return report, nil, fmt.Errorf("no collections")
	}
	var targets []catchUpTarget
	for _, name := range collections {
		if !collectionNamePattern.MatchString(name) {
			report.add("collection", name, "", "failed", "invalid collection name", "", "", "")
			continue
		}
		target, err := OpenCollection(filepath.Join(c.Workspace, name))
		if err != nil {
			report.add("collection", name, "", "failed", "missing or invalid harness", "", "", "")
			continue
		}
		harnessRepo, err := target.catchUpHarnessRepo()
		if err != nil {
			report.add("collection", name, "", "failed", err.Error(), "", "", "")
			continue
		}
		entries, err := os.ReadDir(target.Collection)
		if err != nil {
			report.add("collection", name, "", "failed", err.Error(), "", "", "")
			continue
		}
		// The harness is first, as its hooks and registry govern this target.
		paths := []string{target.Harness}
		for _, entry := range entries {
			if entry.Name() == "harness" {
				continue
			}
			path := filepath.Join(target.Collection, entry.Name())
			// The shell's */ glob follows symlinked directories. Keep them
			// eligible when the destination is still a Git worktree.
			if info, err := os.Stat(path); err == nil && info.IsDir() {
				paths = append(paths, path)
			}
		}
		for _, path := range paths {
			if _, err := os.Lstat(filepath.Join(path, ".git")); err != nil {
				continue
			}
			label := filepath.Base(path)
			repoName := label
			if label == "harness" {
				repoName = harnessRepo
			}
			if len(selectors) != 0 {
				// A selector may name the repository or any of its aliases.
				names := []string{label, repoName}
				if entry, err := target.Repository(repoName); err == nil {
					names = append(names, entry.Names()...)
				}
				selected := false
				for _, candidate := range names {
					if _, ok := selectors[candidate]; ok {
						selectors[candidate] = true
						selected = true
					}
				}
				if !selected {
					continue
				}
			}
			owner, err := gitOutput("-C", path, "rev-parse", "--path-format=absolute", "--git-common-dir")
			if err != nil {
				report.add("repo", name, repoName, "failed", "cannot find Git owner", "", "", "")
				continue
			}
			// Unmanaged ext. siblings are valid worktrees too. Their default
			// development ref follows the shell contract's origin/main fallback,
			// which catchUpLocalRef maps to local main for an owner without any
			// remote. An owner with only other remotes still fails its fetch.
			repo, repoErr := target.Repository(repoName)
			ref := repo.DefaultRef
			if ref == "" {
				ref = "origin/main"
			}
			remotes, remoteErr := gitOutput("--git-dir="+owner, "remote")
			t := catchUpTarget{collection: name, path: path, repo: repoName, owner: owner,
				harness: label == "harness", managed: repoErr == nil || label == "harness", noRemote: remoteErr == nil && remotes == ""}
			if repoErr == nil && label != "harness" && repo.Name != repoName {
				t.renamed = repo.Name
			}
			t.ref = catchUpLocalRef(t, ref)
			targets = append(targets, t)
		}
	}
	var unmatched []string
	for name, found := range selectors {
		if !found {
			unmatched = append(unmatched, name)
		}
	}
	sort.Strings(unmatched)
	for _, name := range unmatched {
		report.add("selection", filepath.Base(c.Collection), name, "failed", "selector matches no checked-out repository", "", "", "")
	}
	if len(unmatched) != 0 {
		return report, nil, fmt.Errorf("repository selection failed")
	}
	return report, targets, nil
}

// The initiating shell's WTC_HARNESS_REPO may name a different collection.
// Resolve identity from each target's own Git owner or origin first.
func (c *Context) catchUpHarnessRepo() (string, error) {
	if c.AdHoc {
		return "", nil
	}
	owner, err := gitOutput("-C", c.Harness, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err == nil {
		name := strings.TrimSuffix(filepath.Base(owner), ".git")
		if _, err := c.Repository(name); err == nil {
			return name, nil
		}
	}
	remote, err := gitOutput("-C", c.Harness, "remote", "get-url", "origin")
	if err == nil {
		for _, repo := range c.Registry.Repos {
			if normalizeCatchUpRemote(remote) == normalizeCatchUpRemote(repo.Remote) {
				return repo.Name, nil
			}
		}
	}
	if name := envcompat.Getenv("WTC_HARNESS_REPO"); name != "" {
		if _, err := c.Repository(name); err == nil {
			return name, nil
		}
	}
	return "", fmt.Errorf("cannot infer target harness repository")
}

func normalizeCatchUpRemote(remote string) string {
	remote = strings.TrimSpace(strings.TrimSuffix(remote, ".git"))
	remote = strings.TrimPrefix(strings.TrimPrefix(remote, "https://"), "http://")
	remote = strings.TrimPrefix(remote, "ssh://")
	remote = strings.TrimPrefix(remote, "git@")
	return strings.Replace(remote, ":", "/", 1)
}
