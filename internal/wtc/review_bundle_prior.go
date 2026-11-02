package wtc

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// copyReviewPrior supplies earlier rounds. Public bundles exclude private
// rounds, while private bundles can use either kind as context.
func copyReviewPrior(dir, root string, current ReviewManifest, alternates ...string) error {
	type previous struct {
		path  string
		round int
	}
	var earlier []previous
	seenDirs := map[string]bool{}
	for _, scan := range append(append([]string{root}, alternates...), filepath.Dir(dir)) {
		if seenDirs[scan] {
			continue
		}
		seenDirs[scan] = true
		entries, err := os.ReadDir(scan)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			path := filepath.Join(scan, entry.Name())
			if path == dir {
				continue
			}
			manifest, ok, legacy := readPriorReviewManifest(path)
			if !ok || current.Public && (legacy || !manifest.Public) || manifest.Round < 1 || manifest.Round >= current.Round {
				continue
			}
			if manifest.Repo != current.Repo || manifest.PR != current.PR || !legacy && (manifest.Slug != current.Slug || manifest.Forge != current.Forge) {
				continue
			}
			if current.PR == "" && manifest.HeadBranch != current.HeadBranch {
				continue
			}
			earlier = append(earlier, previous{path: path, round: manifest.Round})
		}
	}
	sort.Slice(earlier, func(i, j int) bool {
		if earlier[i].round == earlier[j].round {
			return earlier[i].path < earlier[j].path
		}
		return earlier[i].round < earlier[j].round
	})
	if len(earlier) == 0 {
		return prepareReviewConvergence(dir, current, "")
	}
	completed := ""
	for _, entry := range earlier {
		if data, err := os.ReadFile(filepath.Join(entry.path, "summary.md")); err == nil && len(strings.TrimSpace(string(data))) > 0 {
			verdict, err := os.ReadFile(filepath.Join(entry.path, "verdict"))
			if err == nil {
				switch strings.TrimSpace(string(verdict)) {
				case "pass", "pass-with-notes", "changes-requested", "error":
					completed = entry.path
				}
			}
		}
	}
	if err := prepareReviewConvergence(dir, current, completed); err != nil {
		return err
	}
	prior := filepath.Join(dir, "prior")
	if err := os.MkdirAll(prior, 0755); err != nil {
		return err
	}
	keys := map[string]bool{}
	for _, entry := range earlier {
		if data, err := os.ReadFile(filepath.Join(entry.path, "summary.md")); err == nil && len(data) > 0 {
			name := filepath.Join(prior, fmt.Sprintf("r%d.md", entry.round))
			if err := os.WriteFile(name, data, 0644); err != nil {
				return err
			}
		}
		for _, record := range readInlineRecords(entry.path) {
			if record.ID != "" && record.Error == "" && len(record.Key) == 10 && reviewInlineMarker.MatchString("wtc-review-inline v1 key="+record.Key) {
				keys[record.Key] = true
			}
		}
	}
	if len(keys) > 0 {
		var list []string
		for key := range keys {
			list = append(list, key)
		}
		sort.Strings(list)
		if err := os.WriteFile(filepath.Join(prior, "inline-keys.txt"), []byte(strings.Join(list, "\n")+"\n"), 0644); err != nil {
			return err
		}
	}
	return nil
}
