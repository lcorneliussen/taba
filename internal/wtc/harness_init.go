package wtc

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/Masterminds/semver/v3"
	"gopkg.in/yaml.v3"
)

type HarnessInitOptions struct {
	Dir, Name, Remote, CLIVersion string
	DryRun                        bool
}
type HarnessInitResult struct {
	Directory string   `json:"directory"`
	Files     []string `json:"files"`
	DryRun    bool     `json:"dry_run"`
}

// InitHarness scaffolds project-owned config. It does not initialize Git,
// contact a forge, install tools, create collections, or run hooks.
func InitHarness(opt HarnessInitOptions) (HarnessInitResult, error) {
	result := HarnessInitResult{DryRun: opt.DryRun, Files: []string{}}
	if !validRepoName.MatchString(opt.Name) {
		return result, fmt.Errorf("--name must be a repository name")
	}
	if strings.TrimSpace(opt.Remote) == "" || strings.ContainsAny(opt.Remote, "\r\n\x00") {
		return result, fmt.Errorf("--remote must name the harness's Git remote")
	}
	if _, err := semver.StrictNewVersion(opt.CLIVersion); err != nil || !cliVersionPattern.MatchString(opt.CLIVersion) {
		return result, fmt.Errorf("--cli-version must be an exact published version, without v")
	}
	// The scaffold uses the taba file names and command, which releases before
	// 0.2.0 cannot read or install. The 0.2.0 release candidates can.
	if v := semver.MustParse(opt.CLIVersion); v.Major() == 0 && v.Minor() < 2 {
		return result, fmt.Errorf("--cli-version must be 0.2.0 or later: earlier releases cannot read .taba-version and taba.toml")
	}
	dir, err := filepath.Abs(opt.Dir)
	if err != nil {
		return result, err
	}
	result.Directory = dir

	// Parent aliases (such as /tmp on macOS) are ordinary paths. The target
	// itself and every output filename must not be a symlink or existing file.
	if info, err := os.Lstat(dir); err == nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
		return result, fmt.Errorf("harness target must be a real directory: %s", dir)
	} else if err != nil && !os.IsNotExist(err) {
		return result, err
	}
	for _, name := range []string{"taba.toml", ".taba-version"} {
		path := filepath.Join(dir, name)
		if _, err := os.Lstat(path); err == nil {
			return result, fmt.Errorf("refusing to shadow existing %s", path)
		} else if !os.IsNotExist(err) {
			return result, err
		}
	}
	registry, err := yaml.Marshal(Registry{SchemaVersion: 1, Repos: []Repo{{Name: opt.Name, Remote: opt.Remote, DefaultRef: "origin/main", Role: "Project harness"}}})
	if err != nil {
		return result, err
	}
	requires, err := CompatibilityRange(opt.CLIVersion)
	if err != nil {
		return result, err
	}
	config := map[string]any{"compatibility": map[string]string{"requires": requires}, "harness": map[string]string{"name": opt.Name}}
	var configText strings.Builder
	if err := toml.NewEncoder(&configText).Encode(config); err != nil {
		return result, err
	}
	files := map[string][]byte{
		".harness-repos.yml": registry,
		".taba-version":      []byte(opt.CLIVersion + "\n"),
		"taba.toml":          []byte(configText.String()),
		".gitignore":         []byte(".harness-repos\n.wtc-local.md\n.DS_Store\n"),
		"AGENTS.md":          []byte("# Project harness\n\nKeep project policy and local integration here. Generic collection instructions\nand skills come from the pinned taba CLI. Before changing policy, read the\ninstalled guide with `taba docs instructions/customize.md`.\n\nNever commit credentials. Inspect the destination audience and outgoing content\nbefore publishing; private project identities belong in private records.\n"),
		"README.md":          []byte("# Project harness\n\nThis repository owns the repository registry, the taba version pin, and project\nconfiguration. Add project-specific hooks and policy overrides when needed.\nGeneric instructions and skills ship with the pinned CLI.\n\nStart with `taba docs bootstrap.md`. Run `taba docs instructions/customize.md`\nfor config and hooks, or `taba eject <path>` for a default you want to own.\n\nCommit these files and publish this repository to the remote in\n`.harness-repos.yml` before creating its shared bare owner and first collection.\ntaba has not initialized Git, contacted that remote, or started services.\n"),
	}
	names := []string{".harness-repos.yml", ".taba-version", "taba.toml", ".gitignore", "AGENTS.md", "README.md"}
	// An existing harness under the old file names is a harness too.
	for _, name := range []string{".wtc-cli-version", "wtc.toml"} {
		if _, err := os.Lstat(filepath.Join(dir, name)); err == nil {
			return result, fmt.Errorf("refusing to initialize over an existing harness: %s", filepath.Join(dir, name))
		} else if !os.IsNotExist(err) {
			return result, err
		}
	}
	for _, name := range names {
		path := filepath.Join(dir, name)
		if _, err := os.Lstat(path); err == nil {
			return result, fmt.Errorf("refusing to overwrite %s", path)
		} else if !os.IsNotExist(err) {
			return result, err
		}
		result.Files = append(result.Files, name)
	}
	if opt.DryRun {
		return result, nil
	}
	existed := true
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		existed = false
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return result, err
	}
	written := []string{}
	success := false
	defer func() {
		if !success {
			for _, path := range written {
				_ = os.Remove(path)
			}
			if !existed {
				_ = os.Remove(dir)
			}
		}
	}()
	for _, name := range names {
		path := filepath.Join(dir, name)
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
		if err != nil {
			return result, err
		}
		written = append(written, path)
		_, writeErr := f.Write(files[name])
		closeErr := f.Close()
		if writeErr != nil {
			return result, writeErr
		}
		if closeErr != nil {
			return result, closeErr
		}
	}
	success = true
	return result, nil
}
