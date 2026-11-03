package wtc

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"

	"github.com/BurntSushi/toml"
	"gopkg.in/yaml.v3"
	"taba.sh/taba/internal/branding"
	"taba.sh/taba/internal/envcompat"
)

// Repo is one registry entry. Aliases are earlier names of a renamed
// repository: existing folders, bare owners, enlistments and service files
// under an alias keep working.
type Repo struct {
	Name          string         `yaml:"name" json:"name"`
	Remote        string         `yaml:"remote" json:"remote"`
	DefaultRef    string         `yaml:"default_ref" json:"default_ref"`
	PortOffset    *int           `yaml:"port_offset" json:"port_offset,omitempty"`
	Role          string         `yaml:"role" json:"role,omitempty"`
	IssuesPrefix  string         `yaml:"issues_prefix" json:"issues_prefix,omitempty"`
	ProductionRef string         `yaml:"production_ref" json:"production_ref,omitempty"`
	ReleaseRef    string         `yaml:"release_ref" json:"release_ref,omitempty"`
	ProdPaths     []string       `yaml:"prod_paths" json:"prod_paths,omitempty"`
	IDE           string         `yaml:"ide" json:"ide,omitempty"`
	MergeStrategy string         `yaml:"merge_strategy" json:"merge_strategy,omitempty"`
	Owner         string         `yaml:"owner" json:"owner,omitempty"` // ad-hoc only: an existing checkout's Git dir
	Aliases       []string       `yaml:"aliases" json:"aliases,omitempty"`
	Extra         map[string]any `yaml:",inline" json:"extra,omitempty"`
}

// Answers reports whether name is this repository's name or one of its aliases.
func (r Repo) Answers(name string) bool {
	if r.Name == name {
		return true
	}
	for _, alias := range r.Aliases {
		if alias == name {
			return true
		}
	}
	return false
}

// Names lists the repository's name first, then its aliases.
func (r Repo) Names() []string {
	return append([]string{r.Name}, r.Aliases...)
}

type Registry struct {
	SchemaVersion int               `yaml:"schema_version" json:"schema_version"`
	Repos         []Repo            `yaml:"repos" json:"repos"`
	Repositories  []Repo            `yaml:"repositories" json:"-"`
	Selected      []Repo            `yaml:"selected" json:"-"`
	NonDefault    map[string][]Repo `yaml:"non_default" json:"-"`
}
type Config struct {
	Harness struct {
		Name string `toml:"name"`
	} `toml:"harness"`
	Herdr struct {
		Session string `toml:"session"`
	} `toml:"herdr"`
	Compatibility struct {
		Requires string `toml:"requires"`
	} `toml:"compatibility"`
	Mise struct {
		Tools map[string]string `toml:"tools"`
	} `toml:"mise"`
	AgentEnv struct {
		PrependPaths []string `toml:"prepend_paths"`
	} `toml:"agent_env"`
	Secrets struct {
		ProdPaths []string `toml:"prod_paths"`
	} `toml:"secrets"`
	Runtime struct {
		Backend string `toml:"backend"`
		Binary  string `toml:"binary"`
	} `toml:"runtime"`
	Status struct {
		// ReturnMerged lets a status refresh return a clean worktree whose
		// PR it sees merged to the development tip. Unset means on.
		ReturnMerged *bool `toml:"return_merged"`
	} `toml:"status"`
	// OpenClaw is set in a gateway's collection folder only.
	OpenClaw struct {
		// Harness names the gateway-configured harness of the collection,
		// HarnessRemote its repository.
		Harness       string `toml:"harness"`
		HarnessRemote string `toml:"harness_remote"`
	} `toml:"openclaw"`
}
type Context struct {
	Collection string   `json:"collection"`
	Harness    string   `json:"harness"`
	Workspace  string   `json:"workspace"`
	Registry   Registry `json:"registry"`
	Config     Config   `json:"-"`
	ConfigRoot string   `json:"config_root"`
	// AdHoc marks a collection without a harness repository. Its Harness is the
	// collection-local config directory and its registry is tracked nowhere.
	AdHoc bool `json:"ad_hoc,omitempty"`
	// StatusProgress receives human-readable collector steps. It is nil for
	// callers that only need the snapshot.
	StatusProgress  func(string) `json:"-"`
	previewPortBase *int
	// statusSweep marks a collection read by another collection's status
	// sweep, which reads its services itself with its own trusted binary.
	statusSweep bool
}

var validRepoName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)
var errNotInCollection = errors.New("not inside a worktree collection (harness/.harness-repos.yml not found)")

func Discover(start string) (*Context, error) {
	abs, err := filepath.Abs(start)
	if err != nil {
		return nil, err
	}
	dir := abs
	for {
		if RetiredFolder(dir) {
			return nil, retiredFolderError(dir)
		}
		if present, err := collectionMarker(dir); err != nil {
			return nil, err
		} else if present {
			return OpenCollection(dir)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return nil, fmt.Errorf(branding.Text("%w; run `{{command}} init` to make this folder an ad-hoc collection"), errNotInCollection)
}

func OpenCollection(dir string) (*Context, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if RetiredFolder(abs) {
		return nil, retiredFolderError(abs)
	}
	c := &Context{Collection: abs, Harness: filepath.Join(abs, "harness"), Workspace: filepath.Dir(abs)}
	// A collection's own config decides what it is, not the name of one of
	// its repositories: a gateway's folder may link a repository called
	// harness and stays ad hoc. wtc init never makes that config inside a
	// harness collection.
	config, err := collectionDirectoryInput(abs, "config")
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(config); err == nil {
		c.Harness, c.AdHoc = config, true
	} else if !os.IsNotExist(err) {
		return nil, err
	} else if info, err := os.Stat(filepath.Join(c.Harness, ".harness-repos.yml")); err != nil || info.IsDir() {
		return nil, fmt.Errorf("not a collection: %s", abs)
	}
	if err := c.load(); err != nil {
		return nil, err
	}
	return c, nil
}

// registryPath is the tracked registry of a harness or the local registry of
// an ad-hoc collection.
func (c *Context) registryPath() string {
	if c.AdHoc {
		return filepath.Join(c.Harness, adHocRegistryFile)
	}
	return filepath.Join(c.Harness, ".harness-repos.yml")
}

// ParseRegistry reads a .harness-repos.yml, in any of its layouts, into one
// list of repositories. It does not validate the entries.
func ParseRegistry(b []byte) (Registry, error) {
	var r Registry
	if err := yaml.Unmarshal(b, &r); err != nil {
		return r, fmt.Errorf("registry: %w", err)
	}
	if len(r.Repos) == 0 {
		r.Repos = r.Repositories
	}
	if len(r.Repos) == 0 {
		r.Repos = append(r.Repos, r.Selected...)
		categories := make([]string, 0, len(r.NonDefault))
		for name := range r.NonDefault {
			categories = append(categories, name)
		}
		sort.Strings(categories)
		for _, name := range categories {
			r.Repos = append(r.Repos, r.NonDefault[name]...)
		}
	}
	return r, nil
}

func (c *Context) load() error {
	registry := c.registryPath()
	if info, err := os.Stat(registry); err != nil {
		return err
	} else if !info.Mode().IsRegular() {
		return fmt.Errorf("registry must be a regular file: %s", registry)
	}
	b, err := os.ReadFile(registry)
	if err != nil {
		return err
	}
	if c.Registry, err = ParseRegistry(b); err != nil {
		return err
	}
	if !c.AdHoc {
		// Only a local registry may point at an owner; elsewhere the key stays
		// opaque metadata, as before ad-hoc collections existed.
		for i, r := range c.Registry.Repos {
			if r.Owner != "" {
				if r.Extra == nil {
					c.Registry.Repos[i].Extra = map[string]any{}
				}
				c.Registry.Repos[i].Extra["owner"], c.Registry.Repos[i].Owner = r.Owner, ""
			}
		}
	}
	if len(c.Registry.Repos) == 0 && !c.AdHoc {
		return errors.New("registry contains no repositories")
	}
	seen := map[string]bool{}
	portKeys := map[string]bool{}
	for _, r := range c.Registry.Repos {
		if !validRepoName.MatchString(r.Name) || seen[r.Name] {
			return fmt.Errorf("invalid or repeated repository name %q", r.Name)
		}
		seen[r.Name] = true
		for _, alias := range r.Aliases {
			if !validRepoName.MatchString(alias) || alias == "harness" || seen[alias] {
				return fmt.Errorf("invalid or repeated alias %q of %s", alias, r.Name)
			}
			seen[alias] = true
		}
		if r.PortOffset != nil {
			if *r.PortOffset < 0 || *r.PortOffset >= 100 {
				return fmt.Errorf("port_offset outside collection block for %s", r.Name)
			}
			for _, name := range r.Names() {
				key := portKey(name)
				if portKeys[key] {
					return fmt.Errorf("duplicate port variable %s", key)
				}
				portKeys[key] = true
			}
		}
		// Preserve harness-specific metadata for commands that understand it.
		// Unrecognized fields do not make basic env/doctor operations unusable.
	}
	cfg, err := configInput(c.Harness)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	if _, err := os.Stat(cfg); err == nil {
		if _, err := toml.DecodeFile(cfg, &c.Config); err != nil {
			return fmt.Errorf("config: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("config: %w", err)
	}
	c.ConfigRoot = envcompat.Getenv("WTC_CONFIG_ROOT")
	if c.ConfigRoot == "" {
		data, err := os.ReadFile(filepath.Join(c.Collection, ".env.collection"))
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		c.ConfigRoot, _ = envcompat.LookupMap("WTC_CONFIG_ROOT", envcompat.Assignments(data))
	}
	if c.ConfigRoot == "" && c.AdHoc {
		// An ad-hoc collection has no workspace; its secrets are per user.
		c.ConfigRoot, err = UserConfigRoot()
		if err != nil {
			return err
		}
	}
	if c.ConfigRoot == "" {
		// The control root is workspace-shared: it sits beside .bare/ and the
		// collections, so each workspace keeps its own secrets and gh identity.
		c.ConfigRoot = filepath.Join(c.Workspace, ".config")
	}
	return nil
}
