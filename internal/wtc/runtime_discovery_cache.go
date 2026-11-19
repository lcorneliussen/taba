package wtc

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// This is process-local advisory declaration data, never PR/action authority.
// Compatibility callers still discover afresh. One UI shares a cancellable
// gate between its full collector and short service observations.
type runtimeDeclarationCache struct {
	gate      chan struct{}
	signature string
	at        time.Time
	items     []RuntimeItem
}

func newRuntimeDeclarationCache() *runtimeDeclarationCache {
	gate := make(chan struct{}, 1)
	gate <- struct{}{}
	return &runtimeDeclarationCache{gate: gate}
}

func (c *Context) cachedRuntimeDeclarations() []RuntimeItem {
	cache := c.runtimeDeclarationsCache
	ctx := statusResourceOperation(c.resourceContext())
	select {
	case <-ctx.Done():
		return []RuntimeItem{c.runtimeDeclarationError("", ctx.Err())}
	case <-cache.gate:
	}
	defer func() { cache.gate <- struct{}{} }()
	if ctx.Err() != nil {
		return []RuntimeItem{c.runtimeDeclarationError("", ctx.Err())}
	}
	signature := c.statusResourceSignature(false)
	if cache.signature == signature && !cache.at.IsZero() && time.Since(cache.at) < 5*time.Minute {
		return append([]RuntimeItem(nil), cache.items...)
	}
	// Revalidate the receiver and registry when the advisory inventory changes.
	// A stale context cannot preserve a removed/renamed repository association.
	fresh, err := OpenCollectionContext(ctx, c.Collection)
	if err != nil {
		return []RuntimeItem{c.runtimeDeclarationError("", err)}
	}
	items := fresh.runtimeDeclarations()
	if ctx.Err() != nil {
		return []RuntimeItem{c.runtimeDeclarationError("", ctx.Err())}
	}
	if signature == c.statusResourceSignature(false) {
		cache.signature, cache.at, cache.items = signature, time.Now(), append([]RuntimeItem(nil), items...)
	}
	return items
}

// StatusResourceSignature reads local metadata (including HEAD, index and refs)
// with stat and file reads, without invoking Git, services, Herdr or providers.
// It is advisory; bounded reconciliation still covers unrepresented changes.
func (c *Context) StatusResourceSignature() string { return c.statusResourceSignature(true) }

func (c *Context) statusResourceSignature(repoState bool) string {
	hash := sha256.New()
	add := func(path string) {
		info, err := os.Lstat(path)
		if err != nil {
			fmt.Fprintln(hash, path, err)
			return
		}
		identity := ""
		if stat, ok := info.Sys().(*syscall.Stat_t); ok {
			identity = fmt.Sprintf("%d:%d", stat.Dev, stat.Ino)
		}
		fmt.Fprintln(hash, path, info.Mode(), info.Size(), info.ModTime().UnixNano(), identity)
		if info.Mode().IsRegular() && info.Size() <= 65536 {
			if data, err := statusResourceReadFile(path, 65536); err == nil {
				hash.Write(data)
			}
		} else if info.Mode()&os.ModeSymlink != 0 {
			target, _ := os.Readlink(path)
			fmt.Fprintln(hash, target)
		}
	}
	fmt.Fprintln(hash, c.Collection)
	fmt.Fprintln(hash, statusResolveAuthContext("github.com"), statusResolveAuthContext("bitbucket.org"))
	if info, err := os.Lstat(c.Collection); err == nil {
		if stat, ok := info.Sys().(*syscall.Stat_t); ok {
			fmt.Fprintln(hash, stat.Dev, stat.Ino)
		}
	}
	add(c.Harness)
	add(c.registryPath())
	add(filepath.Join(c.Collection, ".env.collection"))
	add(filepath.Join(c.Collection, ".env.collection.local"))
	for _, name := range []string{"wtc.toml", "taba.toml"} {
		add(filepath.Join(c.Harness, name))
	}
	if c.Project != nil {
		add(filepath.Join(c.Project.Root, ProjectDescriptorFile))
		if c.Project.Source.Owner != "" {
			add(filepath.Join(c.Project.Source.Owner, "packed-refs"))
			add(filepath.Join(c.Project.Source.Owner, "refs", "heads"))
		}
	}
	add(filepath.Join(c.Collection, CollectionDescriptorFile))
	for _, name := range []string{"repo-bases.toml"} {
		add(filepath.Join(c.Collection, ".wtc", name))
	}
	entries, _ := os.ReadDir(c.Collection)
	for _, entry := range entries {
		path := filepath.Join(c.Collection, entry.Name())
		gitDir := filepath.Join(path, ".git")
		info, err := os.Lstat(gitDir)
		if err != nil {
			continue
		}
		add(path)
		if repoState || info.Mode().IsRegular() {
			add(gitDir)
		} else if stat, ok := info.Sys().(*syscall.Stat_t); ok {
			fmt.Fprintln(hash, gitDir, stat.Dev, stat.Ino)
		}
		add(runtimeSharedFragment(path))
		add(c.runtimeLocalFragment(entry.Name()))
		if info.Mode().IsRegular() {
			data, err := statusResourceReadFile(gitDir, 4096)
			if err != nil {
				continue
			}
			target, ok := strings.CutPrefix(strings.TrimSpace(string(data)), "gitdir: ")
			if !ok {
				continue
			}
			if !filepath.IsAbs(target) {
				target = filepath.Join(path, target)
			}
			gitDir = filepath.Clean(target)
		}
		if repoState {
			add(filepath.Join(gitDir, "HEAD"))
			add(filepath.Join(gitDir, "index"))
		}
		add(filepath.Join(gitDir, "config"))
		common := gitDir
		add(filepath.Join(gitDir, "commondir"))
		if data, err := statusResourceReadFile(filepath.Join(gitDir, "commondir"), 4096); err == nil {
			common = strings.TrimSpace(string(data))
			if !filepath.IsAbs(common) {
				common = filepath.Join(gitDir, common)
			}
		}
		add(filepath.Join(common, "config"))
		if !repoState {
			continue
		}
		add(filepath.Join(common, "packed-refs"))
		add(filepath.Join(common, "refs", "heads"))
		if data, err := statusResourceReadFile(filepath.Join(gitDir, "HEAD"), 4096); err == nil {
			ref, ok := strings.CutPrefix(strings.TrimSpace(string(data)), "ref: ")
			if ok && strings.HasPrefix(ref, "refs/heads/") && !strings.Contains(ref, "..") && !strings.ContainsAny(ref, "\r\n\x00") {
				add(filepath.Join(common, ref))
			}
		}
	}
	return fmt.Sprintf("%x", hash.Sum(nil))
}

func statusResourceReadFile(path string, limit int64) ([]byte, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("not a regular observation input")
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("observation input too large")
	}
	return data, err
}
