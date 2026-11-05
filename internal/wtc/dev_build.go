package wtc

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Masterminds/semver/v3"
	"github.com/lcorneliussen/wtc-cli/internal/branding"
)

// A collection may carry its own build of wtc, made from the wtc-cli checkout
// beside the harness. Any wtc started inside that collection hands over to it
// when policy selects a fresh followed generation or a same/newer one-shot
// artifact. Only one-shot artifacts fall back to the pinned
// release once that has moved past the build.

const wtcModule = "github.com/lcorneliussen/wtc-cli"

// DevBuildDelegatedEnv is set on the dev build's environment when another wtc
// handed over to it. It carries the version that stepped aside and prevents a
// second handover.
const DevBuildDelegatedEnv = "WTC_DEV_DELEGATED_FROM"

// DevBuildDisableEnv turns the handover off for one invocation.
const DevBuildDisableEnv = "WTC_DEV_BUILD"

type DevBuild struct {
	Collection     string `json:"collection"`
	Binary         string `json:"binary"`
	Version        string `json:"version"`
	Source         string `json:"source,omitempty"`
	Generation     string `json:"generation,omitempty"`
	FollowProtocol int    `json:"follow_protocol,omitempty"`
}

func devBuildDir(collection string) string {
	return filepath.Join(collection, ".wtc", "dev")
}

func validDevBuildVersion(version string) bool {
	_, err := semver.NewVersion(version)
	return err == nil
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
	if build, present, err := readDevPublishedBuild(collection, dir); present || err != nil {
		return build
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

// DevBuildSource finds the wtc-cli checkout among the collection's siblings.
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
		if goModule(filepath.Join(dir, "go.mod")) == wtcModule {
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
	if goModule(filepath.Join(source, "go.mod")) != wtcModule {
		return nil, fmt.Errorf("%s is not a wtc-cli checkout", source)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	unlock, err := lockDevFile(ctx, filepath.Join(collection, ".wtc-dev-build.lock"))
	if err != nil {
		return nil, err
	}
	defer unlock()
	build, err := installDevBuild(context.Background(), collection, source, os.Stderr)
	if err != nil {
		return nil, err
	}
	// One-shot and follow artifacts share the publication point. Policy still
	// decides whether this artifact may run; installing never undoes OFF/ON.
	publishCtx, publishCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer publishCancel()
	publishUnlock, err := lockDevFile(publishCtx, filepath.Join(collection, ".wtc-dev-mode.lock"))
	if err != nil {
		return nil, err
	}
	defer publishUnlock()
	dir := devBuildDir(collection)
	if err := publishDevBuild(collection, dir, build); err != nil {
		return nil, err
	}
	return build, nil
}

// The one-shot entrypoint validates its historical module contract above.
// Follow mode validates AUTO through discovery; an explicit source additionally
// has to demonstrate the structured lifecycle capability before publication.
func installDevBuild(ctx context.Context, collection, source string, output io.Writer) (*DevBuild, error) {
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
	build := exec.Command("go", "build", "-ldflags", "-X main.version="+version, "-o", tmp, "./cmd/wtc")
	build.Dir = source
	build.Stdout, build.Stderr = output, output
	if err := runDevCompiler(ctx, build); err != nil {
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
	// OFF lives outside artifacts. An old-policy follow build cannot activate
	// after OFF; generations stay for running actions. A concurrent one-shot
	// install may still create an inert artifact, but cannot undo the policy.
	if err := SetDevMode(collection, "off", ""); err != nil {
		return false, err
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
