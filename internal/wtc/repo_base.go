package wtc

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/BurntSushi/toml"
	"golang.org/x/sys/unix"
)

// RepositoryBase separates receiving-local development intent from registry
// deployment policy and from a pull request's observed destination.
type RepositoryBase struct {
	Repository     string `json:"repository"`
	RegistryRef    string `json:"registry_ref,omitempty"`
	EffectiveRef   string `json:"effective_ref"`
	Source         string `json:"source"`
	Reason         string `json:"reason,omitempty"`
	ProductionRef  string `json:"production_ref,omitempty"`
	ReleaseRef     string `json:"release_ref,omitempty"`
	ReasonRedacted bool   `json:"reason_redacted,omitempty"`
}
type repoBaseEntry struct {
	Base   string `toml:"base"`
	Reason string `toml:"reason"`
}
type repoBaseStore struct {
	SchemaVersion int                      `toml:"schema_version"`
	Repos         map[string]repoBaseEntry `toml:"repos"`
}

var errUnregisteredBaseRepository = errors.New("repository is not registered")

func baseRepository(registry Registry, selector string) (Repo, error) {
	if (!prRepoName.MatchString(selector) && !validRepoName.MatchString(selector)) || strings.Contains(selector, "..") {
		return Repo{}, fmt.Errorf("invalid repository selector")
	}
	var found []Repo
	for _, r := range registry.Repos {
		if r.Answers(selector) {
			found = append(found, r)
		}
	}
	if len(found) == 0 {
		return Repo{}, fmt.Errorf("%w: %q", errUnregisteredBaseRepository, selector)
	}
	if len(found) != 1 {
		return Repo{}, fmt.Errorf("repository %q must identify exactly one registered repository", selector)
	}
	return found[0], nil
}
func normalizeRepoBase(ref string) (string, error) {
	branch := strings.TrimPrefix(ref, "origin/")
	if len(branch) == 40 || len(branch) == 64 {
		if _, err := hex.DecodeString(branch); err == nil {
			return "", fmt.Errorf("base must name a branch, not a commit ID")
		}
	}
	if branch == "" || strings.HasPrefix(branch, "-") || strings.HasPrefix(branch, "refs/") || strings.HasPrefix(branch, "origin/") || strings.ContainsAny(branch, " \t\r\n@^~:?\\*[\x00") {
		return "", fmt.Errorf("base must be a branch name or origin/<branch>")
	}
	if _, err := gitOutput("check-ref-format", "--branch", branch); err != nil {
		return "", fmt.Errorf("invalid development branch")
	}
	return "origin/" + branch, nil
}
func validateBaseReason(reason string) error {
	if !utf8.ValidString(reason) || strings.TrimSpace(reason) == "" || len(reason) > 1024 || strings.ContainsFunc(reason, unicode.IsControl) {
		return fmt.Errorf("base reason must be nonempty UTF-8, at most 1024 bytes, without control characters")
	}
	return nil
}

// baseDirectory anchors all state operations to directory descriptors. No
// namespace, state or lock symlink is followed, including during replacement.
func baseDirectory(collection string, create bool) (*os.File, error) {
	root, err := unix.Open(collection, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		if err == unix.ENOENT {
			return nil, fmt.Errorf("receiving collection directory is unavailable")
		}
		return nil, fmt.Errorf("receiving collection: %w", err)
	}
	defer unix.Close(root)
	if create {
		if err := unix.Mkdirat(root, ".wtc", 0700); err != nil && err != unix.EEXIST {
			return nil, fmt.Errorf("create repository base namespace: %w", err)
		}
	}
	fd, err := unix.Openat(root, ".wtc", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("open repository base namespace: %w", err)
	}
	return os.NewFile(uintptr(fd), ".wtc"), nil
}
func readRepoBases(dir *os.File, registry Registry) (repoBaseStore, error) {
	store := repoBaseStore{SchemaVersion: 1, Repos: map[string]repoBaseEntry{}}
	if dir == nil {
		return store, nil
	}
	fd, err := unix.Openat(int(dir.Fd()), "repo-bases.toml", unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err == unix.ENOENT {
		return store, nil
	}
	if err != nil {
		return store, err
	}
	f := os.NewFile(uintptr(fd), "repo-bases.toml")
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return store, fmt.Errorf("repository bases must be a regular file")
	}
	data, err := io.ReadAll(io.LimitReader(f, 1048577))
	if err != nil {
		return store, err
	}
	if len(data) > 1048576 {
		return store, fmt.Errorf("repository bases file too large")
	}
	var parsed repoBaseStore
	meta, err := toml.Decode(string(data), &parsed)
	if err != nil || len(meta.Undecoded()) != 0 || parsed.SchemaVersion != 1 {
		return store, fmt.Errorf("invalid repository bases schema (expected schema_version = 1)")
	}
	for name, entry := range parsed.Repos {
		repo, err := baseRepository(registry, name)
		key := name
		if err == nil {
			key = repo.Name
		} else if !errors.Is(err, errUnregisteredBaseRepository) {
			return store, err
		}
		if _, exists := store.Repos[key]; exists {
			return store, fmt.Errorf("duplicate base identity %q", key)
		}
		ref, err := normalizeRepoBase(entry.Base)
		if err != nil {
			return store, err
		}
		if err := validateBaseReason(entry.Reason); err != nil {
			return store, err
		}
		store.Repos[key] = repoBaseEntry{Base: ref, Reason: entry.Reason}
	}
	return store, nil
}

func readReceivingRepoBases(collection string, registry Registry) (repoBaseStore, error) {
	dir, err := baseDirectory(collection, false)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return repoBaseStore{}, fmt.Errorf("repository bases: %w", err)
	}
	if dir != nil {
		defer dir.Close()
	}
	store, err := readRepoBases(dir, registry)
	if err != nil {
		return store, fmt.Errorf("repository bases: %w", err)
	}
	return store, nil
}

// ResolveRepositoryBase reads only receivingCollection, never the caller's
// environment, PR destination, shared owner or registry output files.
func ResolveRepositoryBase(receivingCollection string, registry Registry, selector string) (RepositoryBase, error) {
	return resolveRepositoryBase(receivingCollection, registry, selector, false)
}

func resolveRepositoryBase(receivingCollection string, registry Registry, selector string, unmanaged bool) (RepositoryBase, error) {
	repo, err := baseRepository(registry, selector)
	store, storeErr := readReceivingRepoBases(receivingCollection, registry)
	if storeErr != nil {
		return RepositoryBase{}, storeErr
	}
	if errors.Is(err, errUnregisteredBaseRepository) {
		if _, exists := store.Repos[selector]; exists {
			return RepositoryBase{}, fmt.Errorf("%w; stored intent is orphaned; run repo base clear %s", err, selector)
		}
	}
	external := unmanaged && errors.Is(err, errUnregisteredBaseRepository)
	if external {
		path := filepath.Join(receivingCollection, selector)
		info, pathErr := os.Lstat(path)
		if pathErr != nil || !info.IsDir() {
			return RepositoryBase{}, fmt.Errorf("unmanaged base needs an existing Git worktree")
		}
		top, pathErr := gitOutput("-C", path, "rev-parse", "--show-toplevel")
		if pathErr != nil || !sameCanonicalPath(top, path) {
			return RepositoryBase{}, fmt.Errorf("unmanaged base needs an existing Git worktree")
		}
		repo = Repo{Name: selector}
	} else if err != nil {
		return RepositoryBase{}, err
	}
	result := RepositoryBase{Repository: repo.Name, RegistryRef: repo.DefaultRef, EffectiveRef: repo.DefaultRef, Source: "registry", ProductionRef: repo.ProductionRef, ReleaseRef: repo.ReleaseRef}
	if result.EffectiveRef == "" {
		result.EffectiveRef = "origin/main"
		result.Source = "fallback"
		if external {
			result.EffectiveRef = "origin/HEAD"
		}
	}
	if entry, ok := store.Repos[repo.Name]; ok {
		result.EffectiveRef, result.Reason, result.Source = entry.Base, entry.Reason, "collection"
	}
	return result, nil
}
func (c *Context) RepositoryBase(selector string, unmanaged ...bool) (RepositoryBase, error) {
	if selector == "harness" {
		name, err := c.catchUpHarnessRepo()
		if err == nil && name != "" {
			selector = name
		}
	}
	return resolveRepositoryBase(c.Collection, c.Registry, selector, len(unmanaged) > 0 && unmanaged[0])
}

// SetRepositoryBase changes local intent only; it never moves a checkout or
// contacts a remote. An empty ref clears the entry.
func (c *Context) SetRepositoryBase(selector, ref, reason string) (RepositoryBase, error) {
	resolved, err := c.RepositoryBase(selector)
	if err != nil {
		return resolved, err
	}
	if ref != "" {
		ref, err = normalizeRepoBase(ref)
		if err != nil {
			return resolved, err
		}
		if err = validateBaseReason(reason); err != nil {
			return resolved, err
		}
	}
	if err := c.updateRepositoryBases(func(store *repoBaseStore) error {
		if ref == "" {
			delete(store.Repos, resolved.Repository)
		} else {
			store.Repos[resolved.Repository] = repoBaseEntry{Base: ref, Reason: reason}
		}
		return nil
	}); err != nil {
		return resolved, err
	}
	return c.RepositoryBase(selector)
}

// RepositoryBaseClear is a command outcome. Orphan cleanup has no resolution.
type RepositoryBaseClear struct {
	Cleared    bool            `json:"cleared"`
	ExactKey   string          `json:"exact_key"`
	Orphaned   bool            `json:"orphaned"`
	Resolution *RepositoryBase `json:"resolution,omitempty"`
}

func (c *Context) ClearRepositoryBase(selector string) (RepositoryBaseClear, error) {
	result := RepositoryBaseClear{}
	resolved, err := c.RepositoryBase(selector)
	if err == nil {
		resolved, err = c.SetRepositoryBase(selector, "", "")
		if err != nil {
			return result, err
		}
		return RepositoryBaseClear{Cleared: true, ExactKey: resolved.Repository, Resolution: &resolved}, nil
	}
	if !errors.Is(err, errUnregisteredBaseRepository) {
		return result, err
	}
	store, readErr := readReceivingRepoBases(c.Collection, c.Registry)
	if readErr != nil {
		return result, readErr
	}
	if _, exists := store.Repos[selector]; !exists {
		return result, fmt.Errorf("no stored orphan base for %q", selector)
	}
	if err := c.updateRepositoryBases(func(store *repoBaseStore) error {
		if _, identityErr := baseRepository(c.Registry, selector); !errors.Is(identityErr, errUnregisteredBaseRepository) {
			return fmt.Errorf("orphan cleanup requires an unregistered exact key")
		}
		if _, exists := store.Repos[selector]; !exists {
			return fmt.Errorf("no stored orphan base for %q", selector)
		}
		delete(store.Repos, selector)
		return nil
	}); err != nil {
		return result, err
	}
	return RepositoryBaseClear{Cleared: true, ExactKey: selector, Orphaned: true}, nil
}

func (c *Context) updateRepositoryBases(change func(*repoBaseStore) error) error {
	dir, err := baseDirectory(c.Collection, true)
	if err != nil {
		return err
	}
	defer dir.Close()
	// Avoid Darwin's concurrent openat(O_CREAT) create-or-open race by
	// separating exclusive creation from opening an existing lock (go.dev/issue/81246).
	flags := unix.O_RDWR | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK
	fd, err := unix.Openat(int(dir.Fd()), "repo-bases.lock", flags, 0)
	if err == unix.ENOENT {
		fd, err = unix.Openat(int(dir.Fd()), "repo-bases.lock", flags|unix.O_CREAT|unix.O_EXCL, 0600)
		if err == unix.EEXIST {
			fd, err = unix.Openat(int(dir.Fd()), "repo-bases.lock", flags, 0)
		}
	}
	if err != nil {
		return fmt.Errorf("open repository base lock: %w", err)
	}
	lock := os.NewFile(uintptr(fd), "repo-bases.lock")
	defer lock.Close()
	info, err := lock.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("repository bases lock must be regular")
	}
	if err = unix.Flock(fd, unix.LOCK_EX); err != nil {
		return fmt.Errorf("lock repository bases: %w", err)
	}
	defer unix.Flock(fd, unix.LOCK_UN)
	store, err := readRepoBases(dir, c.Registry)
	if err != nil {
		return err
	}
	if err := change(&store); err != nil {
		return err
	}

	var body bytes.Buffer
	if err = toml.NewEncoder(&body).Encode(store); err != nil {
		return err
	}
	var nonce [12]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return err
	}
	temp := ".repo-bases-" + hex.EncodeToString(nonce[:])
	outfd, err := unix.Openat(int(dir.Fd()), temp, unix.O_CREAT|unix.O_EXCL|unix.O_WRONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return fmt.Errorf("create repository base replacement: %w", err)
	}
	out := os.NewFile(uintptr(outfd), temp)
	defer unix.Unlinkat(int(dir.Fd()), temp, 0)
	_, err = out.Write(body.Bytes())
	if err == nil {
		err = out.Sync()
	}
	closeErr := out.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	// Refuse a swapped namespace instead of claiming success in an orphaned dir.
	current, err := baseDirectory(c.Collection, false)
	if err != nil {
		return err
	}
	defer current.Close()
	a, _ := dir.Stat()
	b, _ := current.Stat()
	if !os.SameFile(a, b) {
		return fmt.Errorf("repository bases directory changed; retry")
	}
	if err = unix.Renameat(int(dir.Fd()), temp, int(dir.Fd()), "repo-bases.toml"); err != nil {
		return fmt.Errorf("replace repository bases: %w", err)
	}
	return nil
}

func validateNewBaseInputs(opt NewOptions) error {
	if len(opt.RepoBases) == 0 {
		if opt.BaseReason != "" {
			return fmt.Errorf("--base-reason needs --repo-base")
		}
		return nil
	}
	if err := validateBaseReason(opt.BaseReason); err != nil {
		return err
	}
	for selector, ref := range opt.RepoBases {
		if (!prRepoName.MatchString(selector) && !validRepoName.MatchString(selector)) || strings.Contains(selector, "..") {
			return fmt.Errorf("invalid base repository selector")
		}
		if _, err := normalizeRepoBase(ref); err != nil {
			return err
		}
	}
	return nil
}

func (c *Context) validateNewBases(opt NewOptions, selected []string) error {
	if err := validateNewBaseInputs(opt); err != nil {
		return err
	}
	seen := map[string]bool{}
	for selector, ref := range opt.RepoBases {
		if selector == "harness" {
			name, err := c.catchUpHarnessRepo()
			if err != nil {
				return err
			}
			selector = name
		}
		repo, err := baseRepository(c.Registry, selector)
		if err != nil {
			return err
		}
		if seen[repo.Name] {
			return fmt.Errorf("duplicate base identity %q", repo.Name)
		}
		seen[repo.Name] = true
		present := false
		for _, name := range selected {
			present = present || repo.Answers(name)
		}
		if !present {
			return fmt.Errorf("base repository %q is not selected for this collection", selector)
		}
		if _, err := normalizeRepoBase(ref); err != nil {
			return err
		}
	}
	return nil
}
func (c *Context) writeNewBases(opt NewOptions, destination string) error {
	receiver := *c
	receiver.Collection = destination
	for selector, ref := range opt.RepoBases {
		if _, err := receiver.SetRepositoryBase(selector, ref, opt.BaseReason); err != nil {
			return err
		}
	}
	return nil
}

// worktreeReceiver uses the receiving registry and owner mapping once its
// harness exists. Bootstrap reuses the invoking registry; overrides are always
// read from the receiving collection, never inherited from the caller.
func (c *Context) worktreeReceiver(destination string) (*Context, error) {
	if filepath.Clean(destination) != filepath.Clean(c.Collection) {
		marked, err := collectionMarker(destination)
		if err != nil {
			return nil, err
		}
		if marked {
			return OpenCollection(destination)
		}
	}
	receiver := *c
	receiver.Collection = destination
	return &receiver, nil
}
