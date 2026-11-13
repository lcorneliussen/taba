package wtc

import (
	"os"
	"path/filepath"
	"strings"
)

// Legacy inputs stay authoritative during phase 0: older clients still write
// them. Only an absent legacy path permits the new spelling; invalid paths
// must not silently select a different configuration. No path is created here.
func preferredInput(legacy, current string, present func(string) (bool, error)) (string, error) {
	for _, name := range []string{legacy, current} {
		exists, err := present(name)
		if err != nil {
			return name, err
		}
		if exists {
			return name, nil
		}
	}
	return legacy, nil
}

func legacyInput(dir, legacy, current string) (string, error) {
	name, err := preferredInput(legacy, current, func(name string) (bool, error) {
		path := filepath.Join(dir, name)
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			if _, err := os.Stat(path); err != nil {
				return false, err
			}
		}
		return true, nil
	})
	return filepath.Join(dir, name), err
}

func configInput(harness string) (string, error) {
	return legacyInput(harness, "wtc.toml", "taba.toml")
}

func pinInput(harness string) (string, error) {
	return legacyInput(harness, ".wtc-cli-version", ".taba-version")
}

func skillBaseInput(dir string) (string, error) {
	return legacyInput(dir, ".wtc-base.sha256", ".taba-base.sha256")
}

// MachineEnvFile selects the workspace defaults file without reading its
// values. It retains wtc.env as the default for fresh control roots.
func MachineEnvFile(root string) (string, error) {
	return legacyInput(root, "wtc.env", "taba.env")
}

// collectionStateInput selects a readable state file without creating it.
// Directories and special files must fail rather than mask alternate state or
// block a read. A missing pair still selects the legacy default.
func collectionStateInput(collection, legacy, current string) (string, error) {
	path, err := legacyInput(collection, legacy, current)
	if err != nil {
		return path, err
	}
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return path, nil
	}
	if err != nil {
		return path, err
	}
	if !info.Mode().IsRegular() {
		return path, &os.PathError{Op: "read", Path: path, Err: os.ErrInvalid}
	}
	return path, nil
}

// TabaNative reports whether a harness was created with, or moved to, the new
// file names: it has .taba-version and no .wtc-cli-version. It decides only
// which scope file a new collection is seeded with (SCOPE.md here, else
// WTC-SCOPE.md), because older harness tooling may read the old name.
func TabaNative(harness string) bool {
	if harness == "" {
		return false
	}
	if _, err := os.Lstat(filepath.Join(harness, ".wtc-cli-version")); err == nil {
		return false
	}
	_, err := os.Stat(filepath.Join(harness, ".taba-version"))
	return err == nil
}

// CommandName is the name to type for the CLI that serves this collection:
// releases from 0.2.0, including its release candidates, install both taba and
// wtc; older pins only wtc. An ad-hoc collection, or one whose pin cannot be
// read, uses the installed CLI, which is this release.
func (c *Context) CommandName() string {
	if c.AdHoc || c.Harness == "" {
		return "taba"
	}
	path, err := pinInput(c.Harness)
	if err != nil {
		return "taba"
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "taba"
	}
	v, err := releaseVersion(strings.TrimSpace(string(data)))
	if err != nil {
		return "taba"
	}
	// Compared without the pre-release tag: the 0.2.0 release candidates
	// already installed taba.
	if v.Major() == 0 && v.Minor() < 2 {
		return "wtc"
	}
	return "taba"
}
