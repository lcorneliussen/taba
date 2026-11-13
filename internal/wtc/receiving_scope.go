package wtc

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
	"taba.sh/taba/internal/envcompat"
)

type SecretPolicy struct {
	Root          string   `toml:"root"`
	AllowExternal bool     `toml:"allow_external"`
	ProdPaths     []string `toml:"prod_paths"`
}

// ReceivingContext names the actual or planned recipient, never its caller.
// Policy is read from the recipient's config, not from generated environment.
type ReceivingContext struct {
	Project    *ProjectContext
	Collection string
}

type ResolvedScope struct {
	Root         string `json:"root"`
	OwnerRoot    string `json:"owner_root"`
	PolicySource string `json:"policy_source"`
	External     bool   `json:"external"`
}

func (p *ProjectContext) configBytes() ([]byte, string, error) {
	if p.Source.Owner != "" {
		return p.snapshotFile("wtc.toml", "taba.toml")
	}
	path, err := configInput(p.ConfigDir)
	if err != nil {
		return nil, "", fmt.Errorf("cannot select receiving policy")
	}
	resolved, err := canonicalPath(path)
	if err != nil || !withinPath(p.Root, resolved) {
		return nil, "", fmt.Errorf("receiving policy escapes its owner")
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, path, nil
	}
	if err != nil {
		return nil, "", fmt.Errorf("cannot read receiving policy")
	}
	return data, path, nil
}

func (r ReceivingContext) resolve(checkInherited bool) (ResolvedScope, error) {
	var scope ResolvedScope
	var config []byte
	var err error
	policyBase := ""
	if r.Project != nil {
		p := r.Project
		if p.Mode != "legacy" {
			p, err = OpenProject(p.Root)
			if err != nil {
				return scope, err
			}
		}
		scope.OwnerRoot, policyBase = p.Root, p.ConfigDir
		if policyBase == "" {
			policyBase = p.Root
		}
		if r.Collection != "" {
			collection, err := canonicalPath(r.Collection)
			if err != nil || collection == p.Root || !withinPath(p.Root, collection) {
				return scope, fmt.Errorf("collection is outside receiving project")
			}
		}
		config, scope.PolicySource, err = p.configBytes()
	} else {
		if r.Collection == "" {
			return scope, fmt.Errorf("receiving project or collection is required")
		}
		scope.OwnerRoot, err = canonicalPath(r.Collection)
		if err != nil {
			return scope, err
		}
		policyBase, err = collectionDirectoryInput(scope.OwnerRoot, "config")
		if err != nil {
			return scope, err
		}
		p := &ProjectContext{Root: scope.OwnerRoot, ConfigDir: policyBase}
		config, scope.PolicySource, err = p.configBytes()
	}
	if err != nil {
		return scope, err
	}
	var cfg struct {
		Secrets SecretPolicy `toml:"secrets"`
	}
	if _, err := toml.Decode(string(config), &cfg); err != nil {
		return scope, fmt.Errorf("invalid receiving secrets policy")
	}
	selected := filepath.Join(scope.OwnerRoot, ".config")
	if cfg.Secrets.Root != "" {
		selected = cfg.Secrets.Root
		if !filepath.IsAbs(selected) {
			selected = filepath.Join(policyBase, selected)
		}
	}
	if strings.ContainsAny(selected, "'\r\n\x00") {
		return scope, fmt.Errorf("invalid receiving secrets root")
	}
	scope.Root, err = canonicalPath(selected)
	if err != nil {
		return scope, err
	}
	if info, err := os.Stat(scope.Root); err == nil {
		if !info.IsDir() {
			return scope, fmt.Errorf("receiving secrets root must be a directory; no files changed")
		}
	} else if !os.IsNotExist(err) {
		return scope, fmt.Errorf("cannot verify receiving secrets root directory")
	}
	scope.External = !withinPath(scope.OwnerRoot, scope.Root)
	if scope.External && (cfg.Secrets.Root == "" || !cfg.Secrets.AllowExternal) {
		return scope, fmt.Errorf("external secrets root requires an exact receiving [secrets].root and allow_external = true; no files changed")
	}
	if checkInherited {
		values := map[string]string{}
		for _, name := range rootAliases {
			values[name] = os.Getenv(name)
		}
		if err := scope.checkAliases(values, "inherited environment"); err != nil {
			return scope, err
		}
	}
	if r.Collection != "" {
		for _, name := range []string{".env.collection", ".env.collection.local"} {
			data, err := os.ReadFile(filepath.Join(r.Collection, name))
			if err != nil && !os.IsNotExist(err) {
				return scope, fmt.Errorf("cannot validate receiving environment")
			}
			if err := scope.checkAliases(envcompat.Assignments(data), name); err != nil {
				return scope, err
			}
		}
	}
	return scope, nil
}

var rootAliases = []string{"TABA_CONFIG_ROOT", "WTC_CONFIG_ROOT"}

func (s ResolvedScope) checkAliases(values map[string]string, source string) error {
	for _, name := range rootAliases {
		value := values[name]
		if value == "" {
			continue
		}
		// Relative ambient paths change meaning with cwd and cannot establish ownership.
		resolved, err := canonicalPath(value)
		if !filepath.IsAbs(value) || err != nil || resolved != s.Root {
			return fmt.Errorf("%s in %s conflicts with receiving secrets policy; remove stale root assignments or declare the exact root and allow_external in receiving config, then regenerate env; no files changed", name, source)
		}
	}
	return nil
}

// ResolveReceivingScope validates receiver-owned permission without enumerating
// any secret directory. A planned collection may not exist yet.
func ResolveReceivingScope(receiver ReceivingContext) (ResolvedScope, error) {
	return receiver.resolve(true)
}

func (c *Context) receivingContext() (ReceivingContext, error) {
	location, err := DiscoverLocation(c.Collection)
	if err != nil {
		return ReceivingContext{}, err
	}
	if location.Collection == "" || !sameCanonicalPath(location.Collection, c.Collection) {
		return ReceivingContext{}, fmt.Errorf("an actual receiving collection is required")
	}
	return ReceivingContext{Project: location.Project, Collection: c.Collection}, nil
}

func sameCanonicalPath(a, b string) bool {
	x, e := canonicalPath(a)
	y, f := canonicalPath(b)
	return e == nil && f == nil && x == y
}

// ValidateControlRoot re-reads receiving policy at the use boundary. ConfigRoot
// is compatibility output, not a grant, including for manually built contexts.
func (c *Context) ValidateControlRoot() (ResolvedScope, error) {
	r, err := c.receivingContext()
	if err != nil {
		return ResolvedScope{}, err
	}
	s, err := r.resolve(!c.creatingReceiver)
	if err != nil {
		return s, err
	}
	if c.ConfigRoot != "" && !sameCanonicalPath(c.ConfigRoot, s.Root) {
		return s, fmt.Errorf("selected control root changed; reopen the receiving collection; no files changed")
	}
	return s, nil
}

func (s ResolvedScope) validateSecretSources() error {
	return filepath.WalkDir(s.Root, func(path string, item fs.DirEntry, err error) error {
		if os.IsNotExist(err) && path == s.Root {
			return nil
		}
		if err != nil {
			return fmt.Errorf("cannot validate secret source paths")
		}
		if item.Type()&os.ModeSymlink != 0 {
			target, err := canonicalPath(path)
			if err != nil || !withinPath(s.Root, target) {
				return fmt.Errorf("secret source symlink escapes approved root; no files changed")
			}
		}
		return nil
	})
}

func (p *ProjectContext) snapshotFile(legacy, current string) ([]byte, string, error) {
	listing, err := gitOutput("--git-dir="+p.Source.Owner, "ls-tree", "-z", p.Source.Ref, "--", legacy, current)
	if err != nil {
		return nil, "", fmt.Errorf("cannot inspect project snapshot policy")
	}
	modes := map[string]string{}
	for _, entry := range strings.Split(listing, "\x00") {
		if entry == "" {
			continue
		}
		header, name, ok := strings.Cut(entry, "\t")
		fields := strings.Fields(header)
		if !ok || len(fields) != 3 {
			return nil, "", fmt.Errorf("invalid project snapshot listing")
		}
		modes[name] = fields[0] + " " + fields[1]
	}
	name, err := preferredInput(legacy, current, func(name string) (bool, error) { _, ok := modes[name]; return ok, nil })
	if err != nil {
		return nil, "", err
	}
	mode, exists := modes[name]
	if !exists {
		return nil, name, nil
	}
	if mode != "100644 blob" && mode != "100755 blob" {
		return nil, "", fmt.Errorf("snapshot policy must be a regular file")
	}
	data, err := gitOutput("--git-dir="+p.Source.Owner, "show", p.Source.Ref+":"+name)
	if err != nil {
		return nil, "", fmt.Errorf("cannot read project snapshot policy")
	}
	return []byte(data), name, nil
}
