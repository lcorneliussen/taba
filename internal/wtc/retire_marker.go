package wtc

import (
	"encoding/json"
	"fmt"
	"github.com/lcorneliussen/wtc-cli/internal/branding"
	"os"
	"path/filepath"
	"slices"
	"time"
)

// RetiredMarkerFile marks a folder that retire emptied of worktrees but could
// not delete. Other commands treat it as leftovers, never as a collection.
const RetiredMarkerFile = ".wtc-retired"

// Either spelling retires a folder. New markers use the legacy name.
var retiredMarkerNames = []string{RetiredMarkerFile, ".taba-retired"}

// CLIVersion is the running CLI's version, recorded in retire markers.
var CLIVersion = "dev"

type RetiredMarker struct {
	SchemaVersion int      `json:"schema_version"`
	RetiredAt     string   `json:"retired_at"`
	CLIVersion    string   `json:"cli_version"`
	BlockingPaths []string `json:"blocking_paths"`
}

// RetiredFolder reports whether dir carries a retire marker.
func RetiredFolder(dir string) bool {
	return retiredMarkerPath(dir) != ""
}

func retiredMarkerPath(dir string) string {
	for _, name := range retiredMarkerNames {
		path := filepath.Join(dir, name)
		if info, err := os.Lstat(path); err == nil && info.Mode().IsRegular() {
			return path
		}
	}
	return ""
}

// ReadRetiredMarker reads the marker of a retired folder. A marker that
// cannot be parsed still marks the folder; its fields are then empty.
func ReadRetiredMarker(dir string) (RetiredMarker, error) {
	var marker RetiredMarker
	path := retiredMarkerPath(dir)
	if path == "" {
		path = filepath.Join(dir, RetiredMarkerFile)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return marker, err
	}
	if err := json.Unmarshal(data, &marker); err != nil {
		return RetiredMarker{}, fmt.Errorf("unreadable %s: %w", filepath.Base(path), err)
	}
	return marker, nil
}

func writeRetiredMarker(dir string, blocking []string) error {
	if blocking == nil {
		blocking = []string{}
	}
	data, err := json.MarshalIndent(RetiredMarker{SchemaVersion: 1, RetiredAt: time.Now().UTC().Format(time.RFC3339),
		CLIVersion: CLIVersion, BlockingPaths: blocking}, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(dir, RetiredMarkerFile)
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		// Preserve an unexpected legacy entry; refresh only an existing guard.
		path = retiredMarkerPath(dir)
		if path == "" {
			return fmt.Errorf("cannot write %s over a nonregular entry", RetiredMarkerFile)
		}
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	return writeCatchUpFile(path, append(data, '\n'))
}

// retiredAncestor returns the nearest of dir and its ancestors that carries a
// retire marker, or "" when none does.
func retiredAncestor(dir string) string {
	for {
		if RetiredFolder(dir) {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

func retiredFolderError(dir string) error {
	return fmt.Errorf(branding.Text("%s holds the leftovers of a retired collection (see %s); run `{{command}} retire %s` once they are gone"), dir, retiredMarkerName(dir), filepath.Base(dir))
}

// retiredMarkerName names the marker a diagnostic should point at.
func retiredMarkerName(dir string) string {
	if path := retiredMarkerPath(dir); path != "" {
		return filepath.Base(path)
	}
	return RetiredMarkerFile
}

// retireLeftovers lists what keeps a folder alive, except the marker itself.
func retireLeftovers(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	leftovers := []string{}
	for _, entry := range entries {
		if slices.Contains(retiredMarkerNames, entry.Name()) {
			if info, err := entry.Info(); err == nil && info.Mode().IsRegular() {
				continue
			}
		}
		leftovers = append(leftovers, entry.Name())
	}
	return leftovers, nil
}

// finishRetiredFolder completes a retire that stopped on unexpected files.
// It refuses a folder that holds a harness or worktree again, since only a
// full retire may remove those. Otherwise no hooks run: it removes the
// generated files a lingering process may have written since, and the folder
// once nothing else is left. If anything remains, the marker is refreshed.
func (c *Context) finishRetiredFolder(target string) (RetireResult, error) {
	var result RetireResult
	targetContext := &Context{Collection: target}
	err := targetContext.withStatusWatcherLock(func() error {
		unlockDev, err := lockDevRetirement(target)
		if err != nil {
			return err
		}
		defer unlockDev()
		var finishErr error
		result, finishErr = c.finishRetiredFolderLocked(target)
		return finishErr
	})
	return result, err
}

func (c *Context) finishRetiredFolderLocked(target string) (RetireResult, error) {
	result := RetireResult{Collection: target, Removed: []string{}}
	entries, err := os.ReadDir(target)
	if err != nil {
		return result, err
	}
	if _, err := os.Lstat(filepath.Join(target, ".git")); err == nil {
		return result, fmt.Errorf("%s carries %s but is a Git checkout itself; inspect it before retiring", target, retiredMarkerName(target))
	}
	for _, entry := range entries {
		if _, err := os.Lstat(filepath.Join(target, entry.Name(), ".git")); entry.Name() == "harness" || err == nil {
			return result, fmt.Errorf("%s carries %s but holds %s again; inspect it before retiring", target, retiredMarkerName(target), entry.Name())
		}
	}
	if err := stopRetiredStatusWatchers(target); err != nil {
		return result, err
	}
	if err := removeRetiredGeneratedFiles(target, false); err != nil {
		return result, err
	}
	return removeRetiredFolder(target)
}

// Both initial and repeated retirement retain their dev locks until the guard
// and folder are finalized. Unknown files keep the existing retired guard.
func removeRetiredFolder(target string) (RetireResult, error) {
	result := RetireResult{Collection: target, Removed: []string{}}
	leftovers, err := retireLeftovers(target)
	if err != nil {
		return result, err
	}
	if len(leftovers) == 0 {
		preserveGuard := func(removeErr error) error {
			current, listErr := retireLeftovers(target)
			if listErr == nil {
				listErr = writeRetiredMarker(target, current)
			}
			if listErr != nil {
				return fmt.Errorf("%w; restoring %s failed: %v", removeErr, RetiredMarkerFile, listErr)
			}
			return removeErr
		}
		for _, name := range retiredMarkerNames {
			if err := os.Remove(filepath.Join(target, name)); err != nil && !os.IsNotExist(err) {
				return result, preserveGuard(err)
			}
		}
		if err := os.Remove(target); err != nil {
			return result, preserveGuard(fmt.Errorf("remove collection folder: %w", err))
		}
		result.FolderRemoved = true
		return result, nil
	}
	result.Leftovers = leftovers
	if err := writeRetiredMarker(target, leftovers); err != nil {
		result.Warnings = append(result.Warnings, "could not refresh "+RetiredMarkerFile+": "+err.Error())
	}
	return result, nil
}
