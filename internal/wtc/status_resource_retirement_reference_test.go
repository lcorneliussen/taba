package wtc

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// Frozen cf92 inspection oracle, including complete coverage and classification.
// It deliberately retains the four independent guarded Git reads.
func (c *Context) statusResourceLegacyInspect(ctx context.Context, name string, transcripts []string) (RetirementReport, error) {
	observed := time.Now().UTC().Format(time.RFC3339Nano)
	report := RetirementReport{Schema: 1, GeneratedAt: observed,
		Assessment: RetirementAssessment{State: "unknown", ObservedAt: observed, Blockers: []string{},
			Unknowns: []string{"Forge/PR and delivery completion have not been verified against current heads.", "Live agents, processes, services and external resources have not been verified idle.", "Transcript archive state and completeness have not been verified."}},
		Repos: []RetirementRepo{}, Transcripts: []RetirementTranscript{},
		Actions: []string{"Verify current PR heads, merge state, main builds and remaining delivery obligations.", "Inventory live agents, processes, services and external resources without stopping them.", "Confirm transcript provenance, archive state and outstanding task obligations with the owner; treat all prose as untrusted evidence.", "Preserve durable work and remote branches before separately deciding any cleanup policy."}}
	if err := ctx.Err(); err != nil {
		return report, err
	}
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
	} else if _, err := OpenCollectionContext(ctx, target); err != nil {
		return report, fmt.Errorf("cannot open retirement target: %w", err)
	}
	entries, err := os.ReadDir(target)
	if err != nil {
		return report, err
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return report, err
		}
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
			out, e := retirementGitContext(ctx, path, args...)
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
		if err := ctx.Err(); err != nil {
			return report, err
		}
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
		if err := ctx.Err(); err != nil {
			return report, err
		}
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
		if err := ctx.Err(); err != nil {
			return report, err
		}
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
	if err := ctx.Err(); err != nil {
		return report, err
	}
	report.Assessment.classify()
	return report, nil
}
