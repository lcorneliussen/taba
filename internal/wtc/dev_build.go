package wtc

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/Masterminds/semver/v3"
	"taba.sh/taba/internal/branding"
)

// A collection may carry its own build of wtc, made from the CLI checkout
// beside the harness. Any wtc started inside that collection hands over to it
// when it is the same version or newer, so panes, agents and hooks all run the
// code under development without PATH changes, and fall back to the pinned
// release once that has moved past the build.

// cliModules are the CLI's module paths: taba.sh/taba, and the path before
// the rename for checkouts of older branches.
var cliModules = []string{"taba.sh/taba", "github.com/lcorneliussen/wtc-cli"}

func cliModule(gomod string) bool {
	module := goModule(gomod)
	for _, m := range cliModules {
		if module == m {
			return true
		}
	}
	return false
}

// cliPackage is the main package to build: cmd/taba, or cmd/wtc before the
// rename.
func cliPackage(source string) string {
	if info, err := os.Stat(filepath.Join(source, "cmd", "taba")); err == nil && info.IsDir() {
		return "./cmd/taba"
	}
	return "./cmd/wtc"
}

// DevBuildDelegatedEnv is set on the dev build's environment when another wtc
// handed over to it. It carries the version that stepped aside and prevents a
// second handover.
const DevBuildDelegatedEnv = "WTC_DEV_DELEGATED_FROM"

// DevBuildDisableEnv turns the handover off for one invocation.
const DevBuildDisableEnv = "WTC_DEV_BUILD"

type DevBuild struct {
	Collection string `json:"collection"`
	Binary     string `json:"binary"`
	Version    string `json:"version"`
	Source     string `json:"source,omitempty"`
}

func devBuildDir(collection string) string {
	return filepath.Join(collection, ".wtc", "dev")
}

// FindCollectionRoot walks up from start to the collection root, recognized
// by the same boundaries as Discover: a harness registry or the selected
// ad-hoc config directory, even when incomplete. It does not load the collection;
// invalid namespace paths stop the walk.
func FindCollectionRoot(start string) string {
	dir, err := filepath.Abs(start)
	if err != nil {
		return ""
	}
	for {
		if present, err := collectionMarker(dir); err != nil {
			return ""
		} else if present {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// ReadDevBuild returns the collection's dev build, or nil when it has none or
// the build is not safe to run: it must be an executable regular file owned
// by this user and writable by nobody else.
func ReadDevBuild(collection string) *DevBuild {
	if collection == "" {
		return nil
	}
	dir, err := collectionDirectoryInput(collection, "dev")
	if err != nil || safeGeneratedParent(collection, dir) != nil {
		return nil
	}
	binary := filepath.Join(dir, "wtc")
	info, err := os.Lstat(binary)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&0111 == 0 || info.Mode()&0022 != 0 || !ownedByCurrentUser(info) {
		return nil
	}
	raw, err := os.ReadFile(filepath.Join(dir, "version"))
	if err != nil {
		return nil
	}
	lines := strings.SplitN(strings.TrimSpace(string(raw)), "\n", 2)
	build := &DevBuild{Collection: collection, Binary: binary, Version: strings.TrimSpace(lines[0])}
	if len(lines) == 2 {
		build.Source = strings.TrimSpace(lines[1])
	}
	if _, err := semver.NewVersion(build.Version); err != nil {
		return nil
	}
	return build
}

// DevBuildTakesOver says whether a wtc of version running should hand over to
// the dev build. A running binary without a release version ("dev", a plain
// go build) never hands over; it is already somebody's build.
func DevBuildTakesOver(running string, build *DevBuild) bool {
	if build == nil {
		return false
	}
	have, err := semver.NewVersion(running)
	if err != nil {
		return false
	}
	want, err := semver.NewVersion(build.Version)
	if err != nil {
		return false
	}
	return !want.LessThan(have)
}

var describePattern = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)-(\d+)-g([0-9a-f]+)(-dirty)?$`)

// DevVersion turns `git describe --tags --long --dirty` into a semantic
// version that sorts where the build belongs: exactly at a tag it equals that
// release; past a tag, or with local changes, it sorts above that release and
// below the next patch release.
func DevVersion(describe string) (string, error) {
	m := describePattern.FindStringSubmatch(strings.TrimSpace(describe))
	if m == nil {
		return "", fmt.Errorf("cannot derive a version from %q; the checkout needs a release tag", describe)
	}
	meta := "dev.g" + m[5]
	if m[6] != "" {
		meta += ".dirty"
	}
	if m[4] == "0" && m[6] == "" {
		return fmt.Sprintf("%s.%s.%s+%s", m[1], m[2], m[3], meta), nil
	}
	patch, _ := strconv.Atoi(m[3])
	return fmt.Sprintf("%s.%s.%d-dev.%s+%s", m[1], m[2], patch+1, m[4], meta), nil
}

// DevBuildSource finds the CLI checkout (either module path) among the
// collection's siblings.
func DevBuildSource(collection string) (string, error) {
	entries, err := os.ReadDir(collection)
	if err != nil {
		return "", err
	}
	found := []string{}
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == "harness" {
			continue
		}
		dir := filepath.Join(collection, entry.Name())
		if cliModule(filepath.Join(dir, "go.mod")) {
			found = append(found, dir)
		}
	}
	switch len(found) {
	case 0:
		return "", fmt.Errorf(branding.Text("no wtc-cli checkout in %s; add it with {{command}} add-repo or pass --source"), collection)
	case 1:
		return found[0], nil
	}
	return "", fmt.Errorf("several wtc-cli checkouts in %s; pass --source", collection)
}

func goModule(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	for s.Scan() {
		if fields := strings.Fields(s.Text()); len(fields) == 2 && fields[0] == "module" {
			return fields[1]
		}
	}
	return ""
}

// InstallDevBuild builds source into the collection's dev build slot. The
// binary is replaced atomically, so a running pane is never left without one.
func InstallDevBuild(collection, source string) (*DevBuild, error) {
	if !cliModule(filepath.Join(source, "go.mod")) {
		return nil, fmt.Errorf("%s is not a wtc-cli checkout", source)
	}
	describe, err := gitOutput("-C", source, "describe", "--tags", "--long", "--dirty", "--match", "v[0-9]*")
	if err != nil {
		return nil, fmt.Errorf("describe %s: %w", source, err)
	}
	version, err := DevVersion(describe)
	if err != nil {
		return nil, err
	}
	dir := devBuildDir(collection)
	if err := safeGeneratedParent(collection, dir); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}
	tmp := filepath.Join(dir, ".wtc.tmp")
	build := exec.Command("go", "build", "-ldflags", "-X main.version="+version, "-o", tmp, cliPackage(source))
	build.Dir = source
	build.Stdout, build.Stderr = os.Stderr, os.Stderr
	if err := build.Run(); err != nil {
		os.Remove(tmp)
		return nil, fmt.Errorf("go build: %w", err)
	}
	if err := os.Chmod(tmp, 0755); err != nil {
		return nil, err
	}
	binary := filepath.Join(dir, "wtc")
	if err := os.Rename(tmp, binary); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, "version"), []byte(version+"\n"+source+"\n"), 0644); err != nil {
		return nil, err
	}
	return &DevBuild{Collection: collection, Binary: binary, Version: version, Source: source}, nil
}

func UninstallDevBuild(collection string) (bool, error) {
	// Removing only the selected build could reactivate the alternate one.
	// Validate both slots before deleting either; never follow a linked parent.
	dirs := []string{devBuildDir(collection), filepath.Join(collection, ".taba", "dev")}
	for _, dir := range dirs {
		if err := safeGeneratedParent(collection, dir); err != nil {
			return false, err
		}
	}
	removed := false
	for _, dir := range dirs {
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return removed, err
		}
		if err := os.RemoveAll(dir); err != nil {
			return removed, err
		}
		removed = true
	}
	return removed, nil
}
