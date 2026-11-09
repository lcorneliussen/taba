package wtc

import (
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"golang.org/x/sys/unix"
	"gopkg.in/yaml.v3"
	"taba.sh/taba/internal/branding"
	"taba.sh/taba/internal/envcompat"
)

// AdHocConfigDir holds an ad-hoc collection's registry, wtc.toml, hooks and
// overrides — the same shape as a harness, so the collection can later be
// promoted to one. It sits inside .wtc but is never generated.
const AdHocConfigDir = ".wtc/config"

const adHocRegistryFile = "repos.yml"

const adHocRegistryHeader = `# Repo registry of this ad-hoc collection — local, tracked nowhere.
# ` + "`wtc add-repo <clone-url|path-to-checkout>`" + ` appends to it. Fields match a
# harness registry; ` + "`owner`" + ` names an existing checkout that owns the worktrees.
schema_version: 1
repos:
`

var scpRemote = regexp.MustCompile(`^(?:[^@/]+@)?([^:/]+):(.+)$`)

// UserConfigRoot is the per-user control root for secrets of ad-hoc
// collections, used when WTC_CONFIG_ROOT is unset.
func UserConfigRoot() (string, error) {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return legacyInput(dir, "wtc", "taba")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return legacyInput(filepath.Join(home, ".config"), "wtc", "taba")
}

// UserDataRoot holds the per-user owner cache and repo registry shared by
// every ad-hoc collection.
func UserDataRoot() (string, error) {
	if dir := envcompat.Getenv("WTC_DATA_HOME"); dir != "" {
		return dir, nil
	}
	if dir := os.Getenv("XDG_DATA_HOME"); dir != "" {
		return legacyInput(dir, "wtc", "taba")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return legacyInput(filepath.Join(home, ".local", "share"), "wtc", "taba")
}

// userOwnerPath keys the shared owner cache by remote location rather than by
// name, so two repositories that share a basename never share an owner.
func userOwnerPath(remote string) (string, error) {
	root, err := UserDataRoot()
	if err != nil {
		return "", err
	}
	key, err := remoteKey(remote)
	if err != nil {
		return "", err
	}
	return filepath.Join(append([]string{root, "bare"}, key...)...) + ".git", nil
}

var defaultPorts = map[string]string{"ssh": "22", "git+ssh": "22", "https": "443", "http": "80", "git": "9418"}

func remoteKey(remote string) ([]string, error) {
	var host, path string
	switch {
	case strings.Contains(remote, "://"):
		u, err := url.Parse(remote)
		if err != nil {
			return nil, fmt.Errorf("invalid remote %q: %w", remote, err)
		}
		host, path = u.Hostname(), u.Path
		// Hostnames cannot contain "_", so a non-default port appended with it
		// keeps two servers on one host apart without colliding with a name.
		if port := u.Port(); port != "" && port != defaultPorts[u.Scheme] {
			host += "_" + port
		}
		if u.Scheme == "file" {
			host = "local"
		}
	case filepath.IsAbs(remote):
		host, path = "local", remote
	default:
		m := scpRemote.FindStringSubmatch(remote)
		if m == nil {
			return nil, fmt.Errorf("cannot derive an owner location from remote %q", remote)
		}
		host, path = m[1], m[2]
	}
	parts := []string{host}
	for _, part := range strings.Split(strings.TrimSuffix(strings.Trim(path, "/"), ".git"), "/") {
		parts = append(parts, part)
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || strings.ContainsAny(part, "\\\r\n\x00") {
			return nil, fmt.Errorf("cannot derive an owner location from remote %q", remote)
		}
	}
	return parts, nil
}

type InitResult struct {
	Collection string `json:"collection"`
	Registry   string `json:"registry"`
}

// InitAdHoc turns an empty folder into a collection without a harness
// repository. It never contacts a remote; repositories join with add-repo.
func InitAdHoc(dir string) (InitResult, error) {
	result := InitResult{}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return result, err
	}
	if existing, err := Discover(abs); err == nil {
		return result, fmt.Errorf("already inside a collection: %s", existing.Collection)
	} else if !errors.Is(err, errNotInCollection) {
		return result, err
	}
	if info, err := os.Lstat(abs); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return result, fmt.Errorf("collection target must be a real directory: %s", abs)
		}
		entries, err := os.ReadDir(abs)
		if err != nil {
			return result, err
		}
		for _, entry := range entries {
			if entry.Name() != ".DS_Store" {
				// Retire deletes the collection folder, so it must hold
				// nothing that wtc did not put there.
				return result, fmt.Errorf("folder is not empty: %s", abs)
			}
		}
	} else if !os.IsNotExist(err) {
		return result, err
	}
	config := filepath.Join(abs, AdHocConfigDir)
	if err := os.MkdirAll(config, 0755); err != nil {
		return result, err
	}
	result.Collection, result.Registry = abs, filepath.Join(config, adHocRegistryFile)
	if err := os.WriteFile(result.Registry, []byte(adHocRegistryHeader), 0644); err != nil {
		return result, err
	}
	c, err := OpenCollection(abs)
	if err != nil {
		return result, err
	}
	if err := c.ValidateEnvSupport(); err != nil {
		return result, err
	}
	env, err := c.RenderEnv()
	if err != nil {
		return result, err
	}
	if err := c.WriteEnv(env); err != nil {
		return result, err
	}
	if err := c.EnsureEnvSupport(); err != nil {
		return result, err
	}
	if err := c.TrustMise(); err != nil {
		return result, err
	}
	if _, err := c.RenderSkills(SkillRenderOptions{SeedScope: true}); err != nil {
		return result, err
	}
	return result, nil
}

type AdHocRepoOptions struct {
	// Spec is a registry name, a clone URL, or a path to an existing checkout.
	Spec string
	// Name overrides the name derived from the remote.
	Name string
	// Target is the branch worktrees rest on and pull requests target.
	Target string
	// Ask, when set, chooses the target for a repository seen for the first
	// time. It receives the remote's default branch as the suggestion.
	Ask func(name, suggested string) (string, error)
}

// RegisterAdHocRepo resolves a spec to a repository in this collection's
// registry, recording it there and in the per-user registry when new.
func (c *Context) RegisterAdHocRepo(opt AdHocRepoOptions) (Repo, error) {
	if !c.AdHoc {
		return Repo{}, errors.New("only an ad-hoc collection registers repositories on demand; edit the harness registry instead")
	}
	if opt.Name == "" {
		if repo, err := c.Repository(opt.Spec); err == nil {
			if opt.Target != "" && repo.DefaultRef != "origin/"+opt.Target {
				return Repo{}, fmt.Errorf("%s is already registered with development tip %s", repo.Name, repo.DefaultRef)
			}
			return repo, nil
		}
	}
	user, err := loadUserRegistry()
	if err != nil {
		return Repo{}, err
	}
	var repo Repo
	known := false
	if validRepoName.MatchString(opt.Spec) && opt.Name == "" {
		if _, statErr := os.Stat(opt.Spec); statErr != nil {
			for _, r := range user.Repos {
				if r.Name == opt.Spec {
					repo, known = r, true
					repo.Owner = liveOwner(r.Owner)
				}
			}
			if !known {
				return Repo{}, fmt.Errorf("unknown repository %q: pass a clone URL or a path to a checkout the first time", opt.Spec)
			}
		}
	}
	if !known {
		repo, err = describeRemoteSpec(opt.Spec, opt.Name)
		if err != nil {
			return Repo{}, err
		}
		for _, r := range user.Repos {
			if r.Name == repo.Name && r.Remote != repo.Remote {
				return Repo{}, fmt.Errorf("the name %s already stands for %s; choose another with --name", r.Name, r.Remote)
			}
			if r.Name == repo.Name && repo.Owner == "" {
				repo.Owner, repo.DefaultRef = liveOwner(r.Owner), r.DefaultRef
				known = true
			}
		}
	}
	if repo.Name == "harness" {
		return Repo{}, errors.New("the name harness is reserved; choose another with --name")
	}
	target := opt.Target
	if target == "" && !known {
		suggested, err := remoteDefaultBranch(repo.Remote)
		if err != nil {
			return Repo{}, err
		}
		target = suggested
		if opt.Ask != nil {
			if target, err = opt.Ask(repo.Name, suggested); err != nil {
				return Repo{}, err
			}
			if target == "" {
				target = suggested
			}
		}
	}
	if target != "" {
		if _, err := gitOutput("check-ref-format", "--branch", target); err != nil {
			return Repo{}, fmt.Errorf("invalid target branch %q", target)
		}
		heads, err := gitOutput("ls-remote", "--heads", repo.Remote, "refs/heads/"+target)
		if err != nil {
			return Repo{}, err
		}
		if heads == "" {
			return Repo{}, fmt.Errorf("%s has no branch %s", repo.Remote, target)
		}
		repo.DefaultRef = "origin/" + target
	}
	if repo.Owner != "" {
		if err := trackOwnerTip(repo.Owner, repo.DefaultRef); err != nil {
			return Repo{}, err
		}
	}
	if existing, err := c.Repository(repo.Name); err == nil {
		if existing.Remote != repo.Remote {
			return Repo{}, fmt.Errorf("the name %s already stands for %s in this collection; choose another with --name", existing.Name, existing.Remote)
		}
		if opt.Target != "" && existing.DefaultRef != repo.DefaultRef {
			return Repo{}, fmt.Errorf("%s is already registered with development tip %s", existing.Name, existing.DefaultRef)
		}
		return existing, nil
	}
	if err := appendRegistryEntry(c.registryPath(), repo); err != nil {
		return Repo{}, err
	}
	if err := saveUserRegistry(repo); err != nil {
		return Repo{}, err
	}
	if err := c.load(); err != nil {
		return Repo{}, err
	}
	return c.Repository(repo.Name)
}

// trackOwnerTip makes a checkout owner fetch the development tip on every
// later plain fetch, then fetches it now. A single-branch clone would
// otherwise keep a frozen tip. It runs before anything is recorded, so a
// failed add leaves no registry entry.
func trackOwnerTip(owner, ref string) error {
	branch := strings.TrimPrefix(ref, "origin/")
	refspec := "+refs/heads/" + branch + ":refs/remotes/" + ref
	configured, _ := gitOutput("--git-dir="+owner, "config", "--get-all", "remote.origin.fetch")
	covered := false
	for _, line := range strings.Split(configured, "\n") {
		line = strings.TrimSpace(line)
		covered = covered || line == refspec || line == "+refs/heads/*:refs/remotes/origin/*" || line == "refs/heads/*:refs/remotes/origin/*"
	}
	if !covered {
		if _, err := gitOutput("--git-dir="+owner, "config", "--add", "remote.origin.fetch", refspec); err != nil {
			return err
		}
	}
	if _, err := gitOutput("--git-dir="+owner, "fetch", "origin", refspec); err != nil {
		return fmt.Errorf("fetch %s into %s: %w", ref, owner, err)
	}
	return nil
}

// liveOwner drops a remembered checkout that no longer exists, so the
// repository falls back to the shared owner cache.
func liveOwner(owner string) string {
	if owner == "" {
		return ""
	}
	if info, err := os.Stat(owner); err != nil || !info.IsDir() {
		return ""
	}
	return owner
}

// describeRemoteSpec reads a clone URL, or an existing checkout that will
// own the worktrees. A checkout's own branch never decides the tip.
func describeRemoteSpec(spec, name string) (Repo, error) {
	repo := Repo{Remote: spec}
	if info, err := os.Stat(spec); err == nil && info.IsDir() {
		owner, err := gitOutput("-C", spec, "rev-parse", "--path-format=absolute", "--git-common-dir")
		if err != nil {
			return Repo{}, fmt.Errorf("%s is not a Git checkout: %w", spec, err)
		}
		remote, err := gitOutput("-C", spec, "remote", "get-url", "origin")
		if err != nil {
			return Repo{}, fmt.Errorf("%s has no origin remote to take branches from", spec)
		}
		repo.Owner, repo.Remote = owner, remote
	} else if _, err := remoteKey(spec); err != nil {
		return Repo{}, fmt.Errorf("unknown repository %q: pass a clone URL or a path to a checkout", spec)
	}
	repo.Name = name
	if repo.Name == "" {
		key, err := remoteKey(repo.Remote)
		if err != nil {
			return Repo{}, err
		}
		repo.Name = key[len(key)-1]
	}
	if !validRepoName.MatchString(repo.Name) {
		return Repo{}, fmt.Errorf("invalid repository name %q; choose one with --name", repo.Name)
	}
	return repo, nil
}

func remoteDefaultBranch(remote string) (string, error) {
	out, err := gitOutput("ls-remote", "--symref", remote, "HEAD")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(out, "\n") {
		if fields := strings.Fields(line); len(fields) == 3 && fields[0] == "ref:" && fields[2] == "HEAD" {
			return strings.TrimPrefix(fields[1], "refs/heads/"), nil
		}
	}
	return "", fmt.Errorf("cannot tell the default branch of %s; pass --target", remote)
}

// registryEntry writes a Repo, leaving out empty optional fields and keeping
// hand-added ones such as port_offset when an entry is copied elsewhere.
type registryEntry struct {
	Name          string         `yaml:"name"`
	Remote        string         `yaml:"remote"`
	DefaultRef    string         `yaml:"default_ref"`
	Owner         string         `yaml:"owner,omitempty"`
	PortOffset    *int           `yaml:"port_offset,omitempty"`
	Role          string         `yaml:"role,omitempty"`
	IssuesPrefix  string         `yaml:"issues_prefix,omitempty"`
	ProductionRef string         `yaml:"production_ref,omitempty"`
	ReleaseRef    string         `yaml:"release_ref,omitempty"`
	ProdPaths     []string       `yaml:"prod_paths,omitempty"`
	IDE           string         `yaml:"ide,omitempty"`
	MergeStrategy string         `yaml:"merge_strategy,omitempty"`
	Extra         map[string]any `yaml:",inline"`
}

func entryOf(r Repo) registryEntry {
	return registryEntry{Name: r.Name, Remote: r.Remote, DefaultRef: r.DefaultRef, Owner: r.Owner,
		PortOffset: r.PortOffset, Role: r.Role, IssuesPrefix: r.IssuesPrefix, ProductionRef: r.ProductionRef, ReleaseRef: r.ReleaseRef,
		ProdPaths: r.ProdPaths, IDE: r.IDE, MergeStrategy: r.MergeStrategy, Extra: r.Extra}
}

// appendRegistryEntry adds text instead of re-encoding the file, so comments
// and hand-added fields survive. The result must still parse to the entry.
func appendRegistryEntry(path string, repo Repo) error {
	old, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	item, err := yaml.Marshal([]registryEntry{entryOf(repo)})
	if err != nil {
		return err
	}
	var b bytes.Buffer
	b.Write(old)
	if len(old) > 0 && old[len(old)-1] != '\n' {
		b.WriteByte('\n')
	}
	for _, line := range strings.SplitAfter(string(item), "\n") {
		if line != "" {
			b.WriteString("  " + line)
		}
	}
	var check Registry
	if err := yaml.Unmarshal(b.Bytes(), &check); err != nil || len(check.Repos) == 0 || check.Repos[len(check.Repos)-1].Name != repo.Name {
		return fmt.Errorf("cannot append to %s; add %s under repos: by hand", path, repo.Name)
	}
	return os.WriteFile(path, b.Bytes(), 0644)
}

type userRegistry struct {
	SchemaVersion int             `yaml:"schema_version"`
	Repos         []registryEntry `yaml:"repos"`
}

func userRegistryPath() (string, error) {
	root, err := UserDataRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, adHocRegistryFile), nil
}

func loadUserRegistry() (Registry, error) {
	path, err := userRegistryPath()
	if err != nil {
		return Registry{}, err
	}
	var r Registry
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return r, nil
	}
	if err != nil {
		return r, err
	}
	if err := yaml.Unmarshal(data, &r); err != nil {
		return r, fmt.Errorf("%s: %w", path, err)
	}
	return r, nil
}

// saveUserRegistry remembers the latest choice for a repository so the next
// ad-hoc collection can add it by name. It reloads under a lock, so concurrent
// registrations from different collections do not drop each other's entries.
func saveUserRegistry(repo Repo) error {
	path, err := userRegistryPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	fd, err := unix.Open(path+".lock", unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return fmt.Errorf("open user registry lock: %w", err)
	}
	lock := os.NewFile(uintptr(fd), path+".lock")
	defer lock.Close()
	if err := unix.Flock(fd, unix.LOCK_EX); err != nil {
		return fmt.Errorf("lock user registry: %w", err)
	}
	defer unix.Flock(fd, unix.LOCK_UN)
	current, err := loadUserRegistry()
	if err != nil {
		return err
	}
	out := userRegistry{SchemaVersion: 1}
	replaced := false
	for _, r := range current.Repos {
		if r.Name == repo.Name {
			r, replaced = repo, true
		}
		out.Repos = append(out.Repos, entryOf(r))
	}
	if !replaced {
		out.Repos = append(out.Repos, entryOf(repo))
	}
	data, err := yaml.Marshal(out)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".repos.tmp-")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append([]byte("# Repositories added to ad-hoc collections, by name. Written by wtc.\n"), data...)); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// RequireWorkspace refuses an operation on other collections. The parent of
// an ad-hoc collection is not a workspace; its other folders are unrelated.
func (c *Context) RequireWorkspace(operation string) error {
	if c.AdHoc {
		return fmt.Errorf("%s: an ad-hoc collection has no workspace of collections", operation)
	}
	return nil
}

// harnessRel is the harness directory relative to the collection, for links
// rendered at the collection root.
func (c *Context) harnessRel() string {
	if rel, err := filepath.Rel(c.Collection, c.Harness); err == nil {
		return filepath.ToSlash(rel)
	}
	return filepath.Base(c.Harness)
}

// checkAdHocSpec rejects a bare repository name that neither this collection
// nor the per-user registry knows, before anything is created for it.
func (c *Context) checkAdHocSpec(spec string) error {
	if _, err := c.Repository(spec); err == nil || !validRepoName.MatchString(spec) {
		return nil
	}
	if _, err := os.Stat(spec); err == nil {
		return nil
	}
	user, err := loadUserRegistry()
	if err != nil {
		return err
	}
	for _, r := range user.Repos {
		if r.Name == spec {
			return nil
		}
	}
	return fmt.Errorf("unknown repository %q: pass a clone URL or a path to a checkout the first time", spec)
}

// newAdHoc creates a sibling ad-hoc collection next to this one. Repositories
// keep this collection's registration (owner, tip, ports); other names and
// URLs resolve through the per-user registry as in add-repo.
func (c *Context) newAdHoc(opt NewOptions) (NewResult, error) {
	result := NewResult{}
	if opt.PR != "" || opt.Issue != "" {
		return result, errors.New("--pr and --issue need a harness registry; use a slug, optionally with --tracker")
	}
	if !collectionNamePattern.MatchString(opt.Slug) {
		return result, fmt.Errorf("invalid collection slug %q", opt.Slug)
	}
	name := opt.Slug
	if opt.Tracker != "" {
		if !collectionNamePattern.MatchString(opt.Tracker) {
			return result, fmt.Errorf("invalid tracker key %q", opt.Tracker)
		}
		name = strings.ToLower(opt.Tracker) + "-" + opt.Slug
		result.Source = fmt.Sprintf("Tracker wtc for %s — create the linking issue (frontmatter `tracker: %s`) when consuming this note.", opt.Tracker, opt.Tracker)
	}
	result.IntendedBranch = name
	if opt.Branch != "" {
		if _, err := gitOutput("check-ref-format", "--branch", opt.Branch); err != nil {
			return result, fmt.Errorf("invalid explicit branch: %w", err)
		}
		result.IntendedBranch = opt.Branch
	}
	result.Collection = filepath.Join(c.Workspace, name)
	if _, err := os.Lstat(result.Collection); err == nil {
		return result, fmt.Errorf("collection already exists: %s", result.Collection)
	} else if !os.IsNotExist(err) {
		return result, err
	}
	seen := map[string]bool{}
	for _, spec := range opt.Repos {
		if seen[spec] {
			return result, fmt.Errorf("repository %q was requested more than once", spec)
		}
		seen[spec] = true
		if err := c.checkAdHocSpec(spec); err != nil {
			return result, err
		}
	}
	values := map[string]string{"collection": result.Collection, "source": result.Source, "branch": result.IntendedBranch}
	if err := c.RunHook("new.pre", values); err != nil {
		return result, err
	}
	if _, err := InitAdHoc(result.Collection); err != nil {
		return result, err
	}
	// Until worktrees exist, a failure removes the folder InitAdHoc created,
	// so a corrected rerun is not blocked by "collection already exists".
	registered := false
	defer func() {
		if !registered {
			os.RemoveAll(result.Collection)
		}
	}()
	// Settings such as [runtime] carry over, as siblings of a harness
	// collection share its configuration. Hooks stay with their collection.
	config, err := configInput(c.Harness)
	if err != nil {
		return result, err
	}
	if data, err := os.ReadFile(config); err == nil {
		if err := os.WriteFile(filepath.Join(result.Collection, AdHocConfigDir, "wtc.toml"), data, 0644); err != nil {
			return result, err
		}
	} else if !os.IsNotExist(err) {
		return result, err
	}
	target, err := OpenCollection(result.Collection)
	if err != nil {
		return result, err
	}
	// Inherit the creating collection's control root, as a harness `new`
	// does, before any secret is linked from it.
	target.ConfigRoot = c.ConfigRoot
	env, err := target.RenderEnv()
	if err != nil {
		return result, err
	}
	if err := target.WriteEnv(env); err != nil {
		return result, err
	}
	result.Repositories = []string{}
	for _, spec := range opt.Repos {
		if repo, err := c.Repository(spec); err == nil {
			if _, err := target.Repository(repo.Name); err == nil {
				continue // An earlier URL or path spec already named it.
			}
			if err := appendRegistryEntry(target.registryPath(), repo); err != nil {
				return result, err
			}
			if err := target.load(); err != nil {
				return result, err
			}
			result.Repositories = append(result.Repositories, repo.Name)
			continue
		}
		repo, err := target.RegisterAdHocRepo(AdHocRepoOptions{Spec: spec})
		if err != nil {
			return result, err
		}
		if !slices.Contains(result.Repositories, repo.Name) {
			result.Repositories = append(result.Repositories, repo.Name)
		}
	}
	registered = true
	if len(result.Repositories) > 0 {
		if _, err := target.AddRepositories(AddRepoOptions{Repos: result.Repositories, Branch: opt.Branch}); err != nil {
			return result, fmt.Errorf("%w (partial collection at %s)", err, result.Collection)
		}
	}
	branchNote := fmt.Sprintf("Worktrees start **detached at the development tip** — no branch exists yet.\nCreate one at your first commit:\n\n    git switch -c %s\n", result.IntendedBranch)
	if opt.Branch != "" {
		branchNote = "All siblings are already on the explicitly requested branch. Commit on that branch; do not create it again.\n"
	}
	handoff := fmt.Sprintf(branding.Text("# wtc: %s — launch note (EPHEMERAL)\n\n**Goal:** %s\n\nAn ad-hoc collection: no harness, registry in `.wtc/config/repos.yml`.\nFirst agent on this wtc: read this, turn anything durable into issues /\ncommits / PRs, then **delete this file as your very first action**.\n\n%s\nAdd repositories with `{{command}} add-repo <name|clone-url|path>`. Retire with `{{command}} retire .` from inside it.\n"), name, defaultSource(result.Source), branchNote)
	if err := os.WriteFile(filepath.Join(result.Collection, "HANDOFF.md"), []byte(handoff), 0644); err != nil {
		return result, err
	}
	if err := c.RunHook("new.post", values); err != nil {
		return result, err
	}
	return result, nil
}
