package openclaw

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"taba.sh/taba/internal/branding"
)

// PluginConfigKey is where the gateway keeps the wtc plugin's config.
const PluginConfigKey = "plugins.entries.wtc.config"

// Harness is a harness repository the gateway is configured with. A
// collection that uses it gets a managed worktree of it, like any of its
// repositories, and resolves repository names through its registry.
type Harness struct {
	Name   string `json:"name"`
	Remote string `json:"remote"`
	// Ref is the branch new worktrees of the harness start from.
	Ref string `json:"ref,omitempty"`
}

// GatewayConfig is the part of the plugin's config that wtc reads.
type GatewayConfig struct {
	Harnesses      []Harness `json:"harnesses,omitempty"`
	DefaultHarness string    `json:"defaultHarness,omitempty"`
}

// NoHarness is what --project (or --harness) takes for an ad-hoc collection,
// without a wtc project.
const NoHarness = "none"

// AdHocFolder is the folder of collections without a harness, beside one
// per harness: $OPENCLAW_STATE_DIR/wtc/<harness>/<name>, so the path says
// which harness a collection works in.
const AdHocFolder = "ad-hoc"

// CollectionFolder is the folder of the gateway collection called name, or
// "" while it has none. Folders made before there was one per harness, in
// wtc-collections/<name>, are still found.
func CollectionFolder(stateDir, name string) string {
	legacy := filepath.Join(stateDir, "wtc-collections", name)
	if _, err := os.Stat(legacy); err == nil {
		return legacy
	}
	found, _ := filepath.Glob(filepath.Join(stateDir, "wtc", "*", name))
	for _, dir := range found {
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			return dir
		}
	}
	return ""
}

// NewCollectionFolder is where the collection called name gets its folder:
// the one it has; a worker's beside its task's; else under its harness, or
// under AdHocFolder without one.
func NewCollectionFolder(stateDir, name, harness string) string {
	if dir := CollectionFolder(stateDir, name); dir != "" {
		return dir
	}
	// A worker's collection is beside its task's, in the task's harness.
	if i := strings.LastIndex(name, WorkerSeparator); harness == "" && i > 0 {
		if task := CollectionFolder(stateDir, name[:i]); task != "" && filepath.Base(filepath.Dir(filepath.Dir(task))) == "wtc" {
			return filepath.Join(filepath.Dir(task), name)
		}
	}
	if harness == "" {
		harness = AdHocFolder
	}
	return filepath.Join(stateDir, "wtc", harness, name)
}

var (
	harnessName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
	branchName  = regexp.MustCompile(`^[A-Za-z0-9._][A-Za-z0-9._/-]*$`)
)

// ParseGatewayConfig reads what `openclaw config get` prints for
// PluginConfigKey. Empty output is a gateway without harnesses.
func ParseGatewayConfig(out string) (GatewayConfig, error) {
	var cfg GatewayConfig
	start := strings.Index(out, "{")
	if start < 0 {
		return cfg, nil
	}
	if err := json.Unmarshal([]byte(out[start:]), &cfg); err != nil {
		return cfg, fmt.Errorf("%s: %w", PluginConfigKey, err)
	}
	seen := map[string]bool{}
	for i, h := range cfg.Harnesses {
		if !harnessName.MatchString(h.Name) || h.Name == NoHarness || h.Name == AdHocFolder {
			return cfg, fmt.Errorf("%s: harness name %q: lowercase letters, digits and hyphens, and neither %q nor %q", PluginConfigKey, h.Name, NoHarness, AdHocFolder)
		}
		if seen[h.Name] {
			return cfg, fmt.Errorf("%s: harness %s is configured twice", PluginConfigKey, h.Name)
		}
		seen[h.Name] = true
		if o := ParseOrigin(h.Remote); o.Forge == ForgeUnknown || o.Owner == "" || o.Repo == "" {
			return cfg, fmt.Errorf("%s: harness %s: remote %q is not a GitHub or Bitbucket repository", PluginConfigKey, h.Name, h.Remote)
		}
		if h.Ref == "" {
			cfg.Harnesses[i].Ref = "main"
		} else if !branchName.MatchString(h.Ref) || strings.Contains(h.Ref, "..") {
			return cfg, fmt.Errorf("%s: harness %s: ref %q is not a branch name", PluginConfigKey, h.Name, h.Ref)
		}
	}
	if cfg.DefaultHarness != "" && !seen[cfg.DefaultHarness] {
		return cfg, fmt.Errorf("%s: defaultHarness %s is not one of the configured harnesses", PluginConfigKey, cfg.DefaultHarness)
	}
	return cfg, nil
}

// Find is the configured harness called name, or nil.
func (c GatewayConfig) Find(name string) *Harness {
	for i := range c.Harnesses {
		if c.Harnesses[i].Name == name {
			return &c.Harnesses[i]
		}
	}
	return nil
}

func (c GatewayConfig) names() string {
	var names []string
	for _, h := range c.Harnesses {
		names = append(names, h.Name)
	}
	return strings.Join(names, ", ")
}

// ChooseHarness decides the harness, the wtc project, of a collection, or nil
// for none. flag is --project, recorded the harness the collection already
// has, and names the
// repositories add-repo was given, by name or URL; has says whether a
// harness's registry lists one. A collection keeps the harness it recorded,
// or none, and refuses another --project; a new collection takes the one
// given. Otherwise the registries decide: the harness that lists the
// repositories (of several that list some, the ones that list all); of
// several still, the default, or the person when none of them is. When none
// lists them, or there are none to go by, the default or the only harness,
// and add-repo asks the person what to do with a repository it does not
// list.
func ChooseHarness(cfg GatewayConfig, flag, recorded string, names []string, has func(Harness, string) bool) (*Harness, error) {
	ask := fmt.Sprintf(branding.Text("Ask the person which wtc project this collection works in (%s; {{command}} projects lists their repositories), then pass --project <name>, or --project %s for an ad-hoc one"), cfg.names(), NoHarness)
	switch {
	case recorded == NoHarness:
		if flag != "" && flag != NoHarness {
			return nil, fmt.Errorf("this collection is ad hoc, without a wtc project; a collection is in one wtc project or none. Start another collection for work in %s", flag)
		}
		return nil, nil
	case recorded != "":
		h := cfg.Find(recorded)
		if h == nil {
			return nil, fmt.Errorf("this collection is in wtc project %s, which the gateway no longer configures. Ask the person to configure its harness again under %s.harnesses", recorded, PluginConfigKey)
		}
		if flag != "" && flag != recorded {
			return nil, fmt.Errorf("this collection is in wtc project %s; a collection is in one. Start another collection for work in %s", recorded, flag)
		}
		return h, nil
	case flag == NoHarness:
		return nil, nil
	case flag != "":
		h := cfg.Find(flag)
		if h == nil {
			if len(cfg.Harnesses) == 0 {
				return nil, fmt.Errorf(branding.Text("the gateway configures no {{command}} projects, so there is no wtc project %s"), flag)
			}
			return nil, fmt.Errorf("the gateway configures no wtc project %s. %s", flag, ask)
		}
		return h, nil
	case len(cfg.Harnesses) == 0:
		return nil, nil
	case len(names) == 0:
		if cfg.DefaultHarness != "" {
			return cfg.Find(cfg.DefaultHarness), nil
		}
		if len(cfg.Harnesses) == 1 {
			return &cfg.Harnesses[0], nil
		}
		return nil, fmt.Errorf("which wtc project? %s", ask)
	}
	var some, all []*Harness
	for i, h := range cfg.Harnesses {
		n := 0
		for _, name := range names {
			if has(h, name) {
				n++
			}
		}
		if n > 0 {
			some = append(some, &cfg.Harnesses[i])
		}
		if n == len(names) {
			all = append(all, &cfg.Harnesses[i])
		}
	}
	fits := some
	if len(some) > 1 && len(all) > 0 {
		fits = all
	}
	switch {
	case len(fits) == 0 && cfg.DefaultHarness != "":
		// add-repo asks the person about what it does not list.
		return cfg.Find(cfg.DefaultHarness), nil
	case len(fits) == 0 && len(cfg.Harnesses) == 1:
		return &cfg.Harnesses[0], nil
	case len(fits) == 0:
		return nil, fmt.Errorf("which wtc project? None lists %s. %s", strings.Join(names, ", "), ask)
	case len(fits) == 1:
		return fits[0], nil
	}
	for _, h := range fits {
		if h.Name == cfg.DefaultHarness {
			return h, nil
		}
	}
	return nil, fmt.Errorf("which wtc project? Several list these repositories. %s", ask)
}

// FileAt is the content of path at ref in the repository at dir, read with
// the trusted git.
func FileAt(dir, ref, path string) ([]byte, error) {
	out, err := git(dir, "show", ref+":"+path)
	if err != nil {
		return nil, err
	}
	return []byte(out + "\n"), nil
}

// SameRepository reports whether two remotes name the same forge repository.
func SameRepository(a, b string) bool {
	oa, ob := ParseOrigin(a), ParseOrigin(b)
	return oa.Forge != ForgeUnknown && oa.Forge == ob.Forge && strings.EqualFold(oa.Owner, ob.Owner) && strings.EqualFold(oa.Repo, ob.Repo)
}

// CheckoutOf reports whether the checkout at dir is of the repository at
// remote, by its current origin or an operator-verified alias bound to it.
func CheckoutOf(dir, remote string) bool {
	return projectMatchesOrigin(dir, ParseOrigin(remote))
}
