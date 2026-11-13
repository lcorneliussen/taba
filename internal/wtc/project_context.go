package wtc

import (
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

const ProjectDescriptorFile = ".taba/project.toml"
const CollectionDescriptorFile = ".taba/collection.toml"

// ProjectContext describes a project without selecting or inventing a task.
// Discovery is read-only; setup and descriptor writing are separate operations.
type ProjectContext struct {
	Root         string        `json:"root"`
	Mode         string        `json:"mode"`
	ConfigDir    string        `json:"config_dir"`
	RegistryPath string        `json:"registry_path"`
	OwnerRoot    string        `json:"owner_root"`
	Source       ProjectSource `json:"source"`
}

// ProjectSource is either a project-relative directory or a local bare-owner
// snapshot. Ref is resolved to a commit before configuration is read.
type ProjectSource struct {
	Path  string `toml:"path" json:"path,omitempty"`
	Owner string `toml:"owner" json:"owner,omitempty"`
	Ref   string `toml:"ref" json:"ref,omitempty"`
}

// Snapshot reads never fetch missing objects or use ambient object directories.
func projectSnapshotGit(owner string, args ...string) (string, error) {
	argv := append([]string{"--no-replace-objects", "--git-dir=" + owner}, args...)
	cmd := exec.Command("git", argv...)
	for _, setting := range os.Environ() {
		name, _, _ := strings.Cut(setting, "=")
		if strings.HasPrefix(name, "GIT_") {
			continue
		}
		cmd.Env = append(cmd.Env, setting)
	}
	cmd.Env = append(cmd.Env, "GIT_NO_LAZY_FETCH=1", "GIT_TERMINAL_PROMPT=0")
	data, err := cmd.Output()
	return strings.TrimSpace(string(data)), err
}

type LocationKind string

const (
	LocationProject       LocationKind = "project"
	LocationCollection    LocationKind = "member-collection"
	LocationAdHoc         LocationKind = "standalone-ad-hoc"
	LocationUninitialized LocationKind = "uninitialized"
	LocationIncomplete    LocationKind = "incomplete"
	LocationConflict      LocationKind = "conflict"
)

type Location struct {
	Kind       LocationKind    `json:"kind"`
	Project    *ProjectContext `json:"project,omitempty"`
	Collection string          `json:"collection,omitempty"`
}

func readDescriptor(path string, value any) error {
	parent, err := os.Lstat(filepath.Dir(path))
	if err != nil {
		return err
	}
	if !parent.IsDir() || parent.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("descriptor directory must be a real directory")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("descriptor must be a regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("cannot read descriptor")
	}
	meta, err := toml.Decode(string(data), value)
	if err != nil || len(meta.Undecoded()) != 0 {
		return fmt.Errorf("invalid or unknown descriptor fields")
	}
	return nil
}

func descriptorPresent(root, name string) bool {
	_, err := os.Lstat(filepath.Join(root, name))
	return !os.IsNotExist(err)
}

func withinPath(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// canonicalPath resolves existing ancestors even when a planned leaf is absent.
// Broken symlinks are errors, not missing planned directories.
func canonicalPath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	var suffix []string
	for {
		_, err := os.Lstat(abs)
		if err == nil {
			resolved, err := filepath.EvalSymlinks(abs)
			if err != nil {
				return "", fmt.Errorf("cannot resolve path ownership")
			}
			if len(suffix) > 0 {
				info, err := os.Stat(resolved)
				if err != nil || !info.IsDir() {
					return "", fmt.Errorf("path ancestor must be a directory")
				}
			}
			for i := len(suffix) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, suffix[i])
			}
			return resolved, nil
		}
		if !os.IsNotExist(err) {
			return "", fmt.Errorf("cannot establish path ownership")
		}
		parent := filepath.Dir(abs)
		if parent == abs {
			return "", fmt.Errorf("cannot establish path ownership")
		}
		suffix = append(suffix, filepath.Base(abs))
		abs = parent
	}
}

func projectRelative(root, path string) (string, error) {
	if path == "" || filepath.IsAbs(path) || strings.ContainsAny(path, "\r\n\x00") {
		return "", fmt.Errorf("source must be project-relative")
	}
	resolved, err := canonicalPath(filepath.Join(root, path))
	if err != nil || !withinPath(root, resolved) {
		return "", fmt.Errorf("source escapes receiving project")
	}
	return resolved, nil
}

func OpenProject(dir string) (*ProjectContext, error) {
	root, err := canonicalPath(dir)
	if err != nil {
		return nil, err
	}
	if descriptorPresent(root, CollectionDescriptorFile) {
		return nil, fmt.Errorf("project and collection descriptors conflict")
	}
	var descriptor struct {
		SchemaVersion int           `toml:"schema_version"`
		Mode          string        `toml:"mode"`
		Harness       ProjectSource `toml:"harness"`
	}
	if err := readDescriptor(filepath.Join(root, ProjectDescriptorFile), &descriptor); err != nil {
		return nil, err
	}
	if descriptor.SchemaVersion != 1 || descriptor.Mode != "local" && descriptor.Mode != "harness" {
		return nil, fmt.Errorf("unsupported project schema or mode")
	}
	p := &ProjectContext{Root: root, Mode: descriptor.Mode, OwnerRoot: root, Source: descriptor.Harness}
	if descriptor.Mode == "local" {
		if p.Source != (ProjectSource{}) {
			return nil, fmt.Errorf("local project cannot declare a harness source")
		}
		p.ConfigDir = filepath.Join(root, ".taba", "config")
		p.RegistryPath = filepath.Join(p.ConfigDir, "repos.yml")
	} else if p.Source.Path != "" && p.Source.Owner == "" && p.Source.Ref == "" {
		p.ConfigDir, err = projectRelative(root, p.Source.Path)
		if err != nil {
			return nil, err
		}
		p.RegistryPath = filepath.Join(p.ConfigDir, ".harness-repos.yml")
	} else if p.Source.Path == "" && p.Source.Owner != "" && p.Source.Ref != "" {
		owner, err := projectRelative(root, p.Source.Owner)
		if err != nil {
			return nil, err
		}
		if strings.HasPrefix(p.Source.Ref, "-") || strings.ContainsAny(p.Source.Ref, ":\r\n\x00 ") {
			return nil, fmt.Errorf("invalid harness snapshot ref")
		}
		if bare, err := projectSnapshotGit(owner, "rev-parse", "--is-bare-repository"); err != nil || bare != "true" {
			return nil, fmt.Errorf("project snapshot requires an available local bare owner")
		}
		sha, err := projectSnapshotGit(owner, "rev-parse", "--verify", p.Source.Ref+"^{commit}")
		if _, decodeErr := hex.DecodeString(sha); err != nil || decodeErr != nil || len(sha) != 40 {
			return nil, fmt.Errorf("harness snapshot unavailable; initialize the selected project explicitly")
		}
		p.Source.Owner, p.Source.Ref = owner, sha
		p.ConfigDir = filepath.Join(root, ".taba", "config")
	} else {
		return nil, fmt.Errorf("harness project requires one explicit directory or owner/ref source")
	}
	if p.ConfigDir != "" {
		resolved, err := canonicalPath(p.ConfigDir)
		if err != nil || !withinPath(root, resolved) {
			return nil, fmt.Errorf("project config escapes receiving project")
		}
	}
	if p.RegistryPath != "" {
		resolved, err := canonicalPath(p.RegistryPath)
		if err != nil || !withinPath(root, resolved) {
			return nil, fmt.Errorf("project registry escapes receiving project")
		}
	}
	return p, nil
}

// DiscoverLocation stops at the nearest explicit boundary, including malformed
// or incomplete metadata. It never borrows another collection's harness.
func DiscoverLocation(start string) (Location, error) {
	dir, err := canonicalPath(start)
	if err != nil {
		return Location{Kind: LocationIncomplete}, err
	}
	for {
		if RetiredFolder(dir) {
			return Location{Kind: LocationConflict}, retiredFolderError(dir)
		}
		if descriptorPresent(dir, ProjectDescriptorFile) {
			p, err := OpenProject(dir)
			if err != nil {
				return Location{Kind: LocationConflict}, err
			}
			return Location{Kind: LocationProject, Project: p}, nil
		}
		if descriptorPresent(dir, CollectionDescriptorFile) {
			var descriptor struct {
				SchemaVersion int    `toml:"schema_version"`
				Project       string `toml:"project"`
			}
			if err := readDescriptor(filepath.Join(dir, CollectionDescriptorFile), &descriptor); err != nil {
				return Location{Kind: LocationConflict}, err
			}
			if descriptor.SchemaVersion != 1 || descriptor.Project == "" || filepath.IsAbs(descriptor.Project) {
				return Location{Kind: LocationConflict}, fmt.Errorf("invalid collection project association")
			}
			project, err := canonicalPath(filepath.Join(dir, descriptor.Project))
			if err != nil || project == dir || !withinPath(project, dir) {
				return Location{Kind: LocationConflict}, fmt.Errorf("collection must belong to an enclosing project")
			}
			// An intervening boundary must not be skipped by an explicit pointer.
			for parent := filepath.Dir(dir); parent != project; parent = filepath.Dir(parent) {
				if descriptorPresent(parent, ProjectDescriptorFile) || descriptorPresent(parent, CollectionDescriptorFile) {
					return Location{Kind: LocationConflict}, fmt.Errorf("collection association crosses another boundary")
				}
			}
			p, err := OpenProject(project)
			if err != nil {
				return Location{Kind: LocationIncomplete}, err
			}
			return Location{Kind: LocationCollection, Project: p, Collection: dir}, nil
		}
		present, err := legacyCollectionMarker(dir)
		if err != nil {
			return Location{Kind: LocationIncomplete}, err
		}
		if present {
			config, err := collectionDirectoryInput(dir, "config")
			if err != nil {
				return Location{Kind: LocationIncomplete}, err
			}
			if _, err := os.Stat(config); err == nil {
				return Location{Kind: LocationAdHoc, Collection: dir}, nil
			}
			root := filepath.Dir(dir)
			p := &ProjectContext{Root: root, Mode: "legacy", ConfigDir: filepath.Join(dir, "harness"), RegistryPath: filepath.Join(dir, "harness", ".harness-repos.yml"), OwnerRoot: root}
			return Location{Kind: LocationCollection, Project: p, Collection: dir}, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return Location{Kind: LocationUninitialized}, nil
		}
		dir = parent
	}
}
