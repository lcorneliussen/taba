package wtc

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type StatusFetchReport struct {
	Attempted int `json:"attempted"`
	Failed    int `json:"failed"`
}

func statusCommonDir(worktree string) (string, error) {
	common, err := statusGit(worktree, "rev-parse", "--git-common-dir")
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(common) {
		common = filepath.Join(worktree, common)
	}
	return filepath.Clean(common), nil
}

func (c *Context) StatusRefreshRefs(all bool) (StatusFetchReport, error) {
	return c.StatusRefreshRefsWithAge(all, 5*time.Minute)
}

func (c *Context) StatusRefreshRefsWithAge(all bool, maxAge time.Duration) (StatusFetchReport, error) {
	var report StatusFetchReport
	inventory, targets, err := c.CatchUpInventory(CatchUpOptions{All: all, DryRun: true})
	if err != nil {
		return report, err
	}
	if inventory.ExitStatus != 0 {
		return report, fmt.Errorf("cannot read status worktree inventory")
	}
	seen := map[string]bool{}
	c.statusProgress("Checking remote refs")
	for _, target := range targets {
		if target.noRemote {
			continue // No remote to fetch from; local branches are the only refs.
		}
		common, err := statusCommonDir(target.path)
		if err != nil {
			report.Failed++
			continue
		}
		if seen[common] {
			continue
		}
		seen[common] = true
		info, err := os.Stat(filepath.Join(common, "FETCH_HEAD"))
		if err == nil && time.Since(info.ModTime()) < maxAge {
			continue
		}
		report.Attempted++
		c.statusProgress(fmt.Sprintf("Fetching remote refs %d", report.Attempted))
		if _, err := statusCommandContext(c.resourceContext(), 30*time.Second, "git", "--git-dir="+common, "fetch", "--prune", "origin"); err != nil {
			report.Failed++
		}
	}
	return report, nil
}

// StatusLivePreview refreshes stale refs before gathering local and forge
// facts. A failed fetch is reported but does not erase the last known refs.
func (c *Context) StatusLivePreview(noFetch bool) (StatusSnapshot, StatusFetchReport, error) {
	return c.StatusLivePreviewWithFetchAge(noFetch, 5*time.Minute)
}

func (c *Context) StatusLivePreviewWithFetchAge(noFetch bool, maxAge time.Duration) (StatusSnapshot, StatusFetchReport, error) {
	var report StatusFetchReport
	if !noFetch {
		var err error
		report, err = c.StatusRefreshRefsWithAge(false, maxAge)
		if err != nil {
			return StatusSnapshot{}, report, err
		}
	}
	snapshot, err := c.StatusForgePreview()
	return snapshot, report, err
}

// StatusLiveSnapshot runs the complete public one-shot collector. A scoped
// result updates the three collection caches; an explicit workspace sweep
// stays read-only across collection boundaries and omits enlisted PR sections.
func (c *Context) StatusLiveSnapshot(all, noFetch bool) (StatusSnapshot, StatusFetchReport, error) {
	return c.StatusLiveSnapshotWithFetchAge(all, noFetch, 5*time.Minute)
}

func (c *Context) StatusLiveSnapshotWithFetchAge(all, noFetch bool, maxAge time.Duration) (StatusSnapshot, StatusFetchReport, error) {
	var report StatusFetchReport
	if all {
		if err := c.RequireWorkspace("status --all"); err != nil {
			return StatusSnapshot{}, report, err
		}
	}
	if !noFetch {
		var err error
		report, err = c.StatusRefreshRefsWithAge(all, maxAge)
		if err != nil {
			return StatusSnapshot{}, report, err
		}
	}
	if !all {
		snapshot, err := c.StatusForgePreview()
		if err != nil {
			return snapshot, report, err
		}
		c.statusProgress("Writing status snapshot")
		if err := c.resourceContext().Err(); err != nil {
			return snapshot, report, err
		}
		return snapshot, report, c.WriteStatusSnapshot(snapshot)
	}
	collections, err := WorkspaceCollections(c.Workspace)
	if err != nil {
		return StatusSnapshot{}, report, err
	}
	snapshot := StatusSnapshot{Schema: 1, GeneratedAt: time.Now().UTC().Format("2006-01-02T15:04:05Z"),
		CLIVersion:           RunningCLIVersion(),
		ShowCollectionColumn: true, Repos: []StatusRepo{}, PRs: []StatusPRRow{}, Orphans: []StatusOrphan{}}
	for i, dir := range collections {
		collection, err := OpenCollection(dir)
		if err != nil {
			return snapshot, report, err
		}
		collection.StatusProgress = c.StatusProgress
		// Its services are read below, with this collection's dekit, never
		// with a binary named by its own manifest.
		collection.statusSweep = true
		c.statusProgress(fmt.Sprintf("Checking collection %d/%d", i+1, len(collections)))
		// A workspace sweep must not execute hooks from other collections.
		// A workspace sweep may read collection siblings but must not rewrite
		// their local PR registries.
		part, err := collection.statusForgePreview(false, false)
		if err != nil {
			return snapshot, report, err
		}
		snapshot.Repos = append(snapshot.Repos, part.Repos...)
		snapshot.Retirement = append(snapshot.Retirement, part.Retirement...)
		snapshot.StaleCount += part.StaleCount
	}
	c.statusRetired(&snapshot)
	c.statusRuntime(&snapshot, true)
	// Read leftover assessments too, without rerunning the complete status collector.
	for _, retired := range snapshot.Retired {
		c.appendRetirement(&snapshot, retired.Folder)
	}
	statusRetirementFacts(&snapshot)
	return snapshot, report, nil
}

// StatusForegroundSnapshot explicitly observes active enlisted PR metadata.
// Periodic/one-shot collection retains branch discovery, refs and build coverage.
// This intent does not imply cancellation of legacy local/runtime/build reads.
func (c *Context) StatusForegroundSnapshot(ctx context.Context) (StatusSnapshot, error) {
	return c.statusForegroundSnapshot(ctx, statusForegroundTimeout)
}

func (c *Context) statusForegroundSnapshot(ctx context.Context, metadataBudget time.Duration) (StatusSnapshot, error) {
	c = c.WithStatusContext(ctx)
	// A metadata-phase deadline is a deferred per-record outcome. The caller's
	// cancellation still prevents subsequent snapshot publication.
	snapshot, err := c.statusForgePreviewWithIntent(ctx, StatusRefreshForeground, true, true, metadataBudget)
	if err != nil {
		return snapshot, err
	}
	if err := ctx.Err(); err != nil {
		return snapshot, err
	}
	return snapshot, c.WriteStatusSnapshot(snapshot)
}

// StatusLiveSnapshotContext retains the complete collector and its accounting;
// cancellation applies to context-aware reads and prevents queued publication.
func (c *Context) StatusLiveSnapshotContext(ctx context.Context, all, noFetch bool, maxAge time.Duration) (StatusSnapshot, StatusFetchReport, error) {
	return c.WithStatusContext(ctx).StatusLiveSnapshotWithFetchAge(all, noFetch, maxAge)
}
