package wtc

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const devPolicyFile = ".wtc-dev-mode.json"

// Check the exact collection boundary again after waiting for a lock. A retired
// or removed root must not be recreated by a publisher that started earlier.
func devCollectionRoot(collection string, previous os.FileInfo) (os.FileInfo, error) {
	info, err := os.Lstat(collection)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || !ownedByCurrentUser(info) || info.Mode()&0022 != 0 || previous != nil && !os.SameFile(previous, info) {
		return nil, fmt.Errorf("unsafe or replaced dev collection root")
	}
	if RetiredFolder(collection) {
		return nil, retiredFolderError(collection)
	}
	present, err := collectionMarker(collection)
	if err != nil {
		return nil, fmt.Errorf("dev collection boundary is unavailable: %w", err)
	}
	if !present {
		return nil, fmt.Errorf("dev collection boundary is unavailable")
	}
	return info, nil
}

// Retirement's existing eligibility is unchanged for collections that never
// used dev state. Introduced private state and lock prerequisites are checked
// without taking locks or executing hooks before destructive teardown.
func preflightDevRetirement(collection string) (bool, error) {
	used := false
	for _, namespace := range []string{".wtc", ".taba"} {
		for _, suffix := range []string{"-dev-mode.json", "-dev-mode.lock", "-dev-build.lock"} {
			info, err := os.Lstat(filepath.Join(collection, namespace+suffix))
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return false, err
			}
			used = used || suffix == "-dev-mode.json"
			if !info.Mode().IsRegular() || !ownedByCurrentUser(info) || info.Mode()&0022 != 0 {
				return false, fmt.Errorf("unsafe dev retirement slot: %s", namespace+suffix)
			}
		}
		root := filepath.Join(collection, namespace)
		info, err := os.Lstat(root)
		if err != nil && !os.IsNotExist(err) {
			return false, err
		}
		if err == nil && info.IsDir() {
			if _, err := os.Lstat(filepath.Join(root, "dev-generations")); err == nil {
				used = true
			} else if !os.IsNotExist(err) {
				return false, err
			}
			if info, err := os.Lstat(filepath.Join(root, "dev")); err == nil && info.IsDir() {
				if _, err := os.Lstat(filepath.Join(root, "dev", "current.json")); err == nil {
					used = true
				} else if !os.IsNotExist(err) {
					return false, err
				}
			} else if err != nil && !os.IsNotExist(err) {
				return false, err
			}
		}
	}
	if used {
		info, err := os.Lstat(collection)
		if err != nil {
			return false, err
		}
		if !info.IsDir() || !ownedByCurrentUser(info) || info.Mode()&0022 != 0 {
			return false, fmt.Errorf("unsafe dev collection root")
		}
	}
	return used, nil
}

// DevMode is the only selection input. Generations and versions are build
// observations, not additional release pins. An absent policy inherits AUTO.
type DevMode struct {
	Mode     string `json:"mode"`
	Source   string `json:"source,omitempty"`
	Revision string `json:"revision"`
}

type DevFollowState struct {
	Mode   string    `json:"mode"`
	Source string    `json:"source,omitempty"`
	Active bool      `json:"active"`
	Reason string    `json:"reason,omitempty"`
	Build  *DevBuild `json:"build,omitempty"`
}

type devFailedBuild struct {
	Generation string    `json:"generation"`
	Revision   string    `json:"revision"`
	Error      string    `json:"error"`
	RetryAfter time.Time `json:"retry_after"`
}

func readPrivateDevFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || !ownedByCurrentUser(info) || info.Mode()&0022 != 0 || info.Size() > 64*1024 {
		return nil, fmt.Errorf("unsafe dev state %s", path)
	}
	return os.ReadFile(path)
}

func readDevMode(collection string) (DevMode, error) {
	var found *DevMode
	for _, name := range []string{devPolicyFile, ".taba-dev-mode.json"} {
		raw, err := readPrivateDevFile(filepath.Join(collection, name))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return DevMode{}, err
		}
		var policy DevMode
		if err := json.Unmarshal(raw, &policy); err != nil {
			return DevMode{}, fmt.Errorf("invalid dev mode: %w", err)
		}
		if policy.Mode != "auto" && policy.Mode != "on" && policy.Mode != "off" || policy.Revision == "" {
			return DevMode{}, fmt.Errorf("invalid dev mode")
		}
		if found != nil && *found != policy {
			return DevMode{}, fmt.Errorf("conflicting dev-mode aliases")
		}
		found = &policy
	}
	if found == nil {
		return DevMode{Mode: "auto"}, nil
	}
	return *found, nil
}

func devReviewContext(collection string) bool {
	for _, name := range reviewModeNames {
		if _, err := os.Lstat(filepath.Join(collection, name)); !os.IsNotExist(err) {
			return true
		}
	}
	return false
}

// DevFollowConfiguration only reads selection; it does not run Go or install.
func DevFollowConfiguration(collection string) (DevFollowState, error) {
	if RetiredFolder(collection) {
		return DevFollowState{}, retiredFolderError(collection)
	}
	present, err := collectionMarker(collection)
	if err != nil {
		return DevFollowState{}, err
	}
	if !present {
		return DevFollowState{}, fmt.Errorf("dev collection boundary is unavailable")
	}
	policy, err := readDevMode(collection)
	state := DevFollowState{Mode: policy.Mode}
	if err != nil {
		return state, err
	}
	if policy.Mode == "off" {
		state.Reason = "disabled explicitly"
		return state, nil
	}
	if devReviewContext(collection) {
		state.Reason = "review-only context; no dev redirection"
		return state, nil
	}
	if policy.Mode == "on" {
		if _, err := devCollectionRoot(collection, nil); err != nil {
			return state, err
		}
		state.Source, err = devSourcePath(policy.Source)
		state.Active = err == nil
		state.Reason = "explicit source"
		return state, err
	}
	name, err := devAutoSource(collection)
	if err != nil {
		return state, err
	}
	if name == "" {
		state.Reason = "no project dev convention"
		return state, nil
	}
	if _, err := devCollectionRoot(collection, nil); err != nil {
		return state, err
	}
	c, err := OpenCollection(collection)
	if err != nil {
		return state, err
	}
	if _, err := c.Repository(name); err != nil {
		return state, fmt.Errorf("dev auto_source must name a registered repository: %w", err)
	}
	source, err := DevBuildSource(collection)
	if err != nil {
		return state, err
	}
	identity, err := c.prRepositories().resolve(name)
	if err != nil || identity == nil || identity.dir() == "" || filepath.Join(collection, identity.dir()) != source {
		return state, fmt.Errorf("dev auto_source does not identify the unique CLI checkout")
	}
	state.Source, err = devSourcePath(source)
	state.Active = err == nil
	state.Reason = "project development convention"
	return state, err
}

func devSourcePath(source string) (string, error) {
	if source == "" {
		return "", fmt.Errorf("dev source is missing")
	}
	abs, err := filepath.Abs(source)
	if err != nil {
		return "", err
	}
	entry, err := os.Lstat(abs)
	if err != nil || entry.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("dev source must be an existing, unlinked checkout")
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(real)
	if err != nil || !info.IsDir() || !ownedByCurrentUser(info) || info.Mode()&0022 != 0 {
		return "", fmt.Errorf("unsafe dev source")
	}
	top, err := gitOutput("-C", real, "rev-parse", "--show-toplevel")
	if canonical, e := filepath.EvalSymlinks(top); e == nil {
		top = canonical
	}
	if err != nil || top != real {
		return "", fmt.Errorf("dev source must be the checkout root")
	}
	return real, nil
}

// SetDevMode publishes OFF without waiting for compilation. A separate short
// lock serializes the policy change with generation publication.
func SetDevMode(collection, mode, source string) error {
	root, err := devCollectionRoot(collection, nil)
	if err != nil {
		return err
	}
	if mode != "auto" && mode != "on" && mode != "off" {
		return fmt.Errorf("mode must be auto, on or off")
	}
	if mode != "on" && source != "" {
		return fmt.Errorf("--source requires mode on")
	}
	if mode == "on" {
		var err error
		if source == "" {
			source, err = DevBuildSource(collection)
		}
		if err == nil {
			source, err = devSourcePath(source)
		}
		if err != nil {
			return err
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	unlock, err := lockDevFile(ctx, filepath.Join(collection, ".wtc-dev-mode.lock"))
	if err != nil {
		return err
	}
	defer unlock()
	if _, err := devCollectionRoot(collection, root); err != nil {
		return err
	}
	// Even recovery must not replace a link or special file. Malformed regular
	// JSON can be repaired by explicitly selecting a new mode.
	for _, name := range []string{devPolicyFile, ".taba-dev-mode.json"} {
		if _, err := readPrivateDevFile(filepath.Join(collection, name)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	var revision [16]byte
	if _, err := rand.Read(revision[:]); err != nil {
		return err
	}
	data, _ := json.Marshal(DevMode{Mode: mode, Source: source, Revision: hex.EncodeToString(revision[:])})
	if err := devAtomicJSON(filepath.Join(collection, devPolicyFile), data); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(collection, ".taba-dev-mode.json")); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func devAtomicJSON(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".dev-state-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(append(data, '\n')); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func readDevPublishedBuild(collection, dir string) (*DevBuild, bool, error) {
	raw, err := readPrivateDevFile(filepath.Join(dir, "current.json"))
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, true, err
	}
	var build DevBuild
	if err := json.Unmarshal(raw, &build); err != nil {
		return nil, true, err
	}
	if err := validateDevPublishedBuild(collection, dir, &build); err != nil {
		return nil, true, err
	}
	return &build, true, nil
}

func validateDevPublishedBuild(collection, dir string, build *DevBuild) error {
	if !validDevBuildVersion(build.Version) {
		return fmt.Errorf("invalid dev build version")
	}
	relative := filepath.Join("dev", "wtc")
	if build.Generation != "" {
		if build.FollowProtocol != 1 {
			return fmt.Errorf("dev generation lacks follow capability; use dev install for one-shot builds")
		}
		if len(build.Generation) != 64 {
			return fmt.Errorf("invalid dev generation")
		}
		if _, err := hex.DecodeString(build.Generation); err != nil {
			return err
		}
		relative = filepath.Join("dev-generations", build.Generation, "wtc")
	} else if build.FollowProtocol != 0 {
		return fmt.Errorf("invalid one-shot artifact protocol")
	} else if name := filepath.Base(filepath.Dir(build.Binary)); strings.HasPrefix(name, "oneshot-") {
		identity := strings.TrimPrefix(name, "oneshot-")
		if _, err := hex.DecodeString(identity); err != nil || len(identity) != 64 {
			return fmt.Errorf("invalid one-shot artifact identity")
		}
		relative = filepath.Join("dev-generations", name, "wtc")
	}
	expected := filepath.Join(filepath.Dir(dir), relative)
	// A supported namespace move changes only the selected generated root.
	// Never remap an arbitrary manifest path into an executable slot.
	if build.Binary != filepath.Join(collection, ".wtc", relative) && build.Binary != filepath.Join(collection, ".taba", relative) || build.Collection != collection || safeGeneratedParent(collection, filepath.Dir(expected)) != nil {
		return fmt.Errorf("unsafe dev generation path")
	}
	info, err := os.Lstat(expected)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&0111 == 0 || info.Mode()&0022 != 0 || !ownedByCurrentUser(info) {
		return fmt.Errorf("unsafe dev generation executable")
	}
	build.Binary = expected
	return nil
}

// Callers hold the policy lock. Both artifact kinds use this selection point;
// a manual install must not replace a linked/special current-state slot either.
func publishDevBuild(collection, dir string, build *DevBuild) error {
	if _, err := readPrivateDevFile(filepath.Join(dir, "current.json")); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := validateDevPublishedBuild(collection, dir, build); err != nil {
		return err
	}
	data, err := json.Marshal(build)
	if err != nil {
		return err
	}
	return devAtomicJSON(filepath.Join(dir, "current.json"), data)
}

func devArtifactHash(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Mode()&0111 == 0 || info.Mode()&0022 != 0 || !ownedByCurrentUser(info) {
		return "", fmt.Errorf("unsafe immutable dev executable")
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// A completed immutable slot is either retained unchanged or rejected. Never
// replace an executable a running process may still use for a child command.
func retainDevArtifact(collection, destination, staged string) error {
	if err := safeGeneratedParent(collection, destination); err != nil {
		return err
	}
	if _, err := os.Lstat(destination); os.IsNotExist(err) {
		return os.Rename(staged, destination)
	} else if err != nil {
		return err
	}
	want, err := devArtifactHash(filepath.Join(staged, "wtc"))
	if err != nil {
		return err
	}
	have, err := devArtifactHash(filepath.Join(destination, "wtc"))
	if err != nil || have != want {
		return fmt.Errorf("existing immutable dev executable does not match completed build")
	}
	wantVersion, err := readPrivateDevFile(filepath.Join(staged, "version"))
	if err != nil {
		return err
	}
	haveVersion, err := readPrivateDevFile(filepath.Join(destination, "version"))
	if err != nil || !bytes.Equal(wantVersion, haveVersion) {
		return fmt.Errorf("existing immutable dev metadata does not match completed build")
	}
	return nil
}

// EnsureDevFollow is the shared strict freshness path for startup and passive
// TUI checks. Errors never present an expired artifact as a fresh executable.
func EnsureDevFollow(ctx context.Context, collection string) (DevFollowState, error) {
	state, err := DevFollowConfiguration(collection)
	if err != nil || !state.Active {
		return state, err
	}
	root, err := devCollectionRoot(collection, nil)
	if err != nil {
		return state, err
	}
	unlock, err := lockDevFile(ctx, filepath.Join(collection, ".wtc-dev-build.lock"))
	if err != nil {
		return state, err
	}
	defer unlock()
	if _, err := devCollectionRoot(collection, root); err != nil {
		return state, err
	}
	state, err = DevFollowConfiguration(collection)
	if err != nil || !state.Active {
		return state, err
	}
	policy, err := readDevMode(collection)
	if err != nil {
		return state, err
	}
	for attempt := 0; attempt < 2; attempt++ {
		fingerprint, err := devFingerprint(ctx, state.Source)
		if err != nil {
			return state, err
		}
		dir, err := collectionDirectoryInput(collection, "dev")
		if err != nil {
			return state, err
		}
		if err := safeGeneratedParent(collection, dir); err != nil {
			return state, err
		}
		if build, _, err := readDevPublishedBuild(collection, dir); err != nil {
			return state, err
		} else if build != nil && build.Source == state.Source && build.Generation == fingerprint {
			var selected DevFollowState
			_, err := WithDevGeneration(ctx, collection, fingerprint, func(current *DevBuild) error {
				selected, selected.Build = state, current
				return nil
			})
			if err != nil {
				return state, err
			}
			if selected.Build == nil {
				current, err := DevFollowConfiguration(collection)
				if err == nil && current.Active {
					err = fmt.Errorf("dev selection changed during freshness check; retry")
				}
				return current, err
			}
			return selected, nil
		}
		if raw, err := readPrivateDevFile(filepath.Join(dir, "failure.json")); err == nil {
			var failed devFailedBuild
			if json.Unmarshal(raw, &failed) != nil {
				return state, fmt.Errorf("invalid dev failure state")
			}
			if failed.Generation == fingerprint && failed.Revision == policy.Revision && time.Now().Before(failed.RetryAfter) {
				return state, fmt.Errorf("last build failed; retry after %s or select mode on to retry now: %s", failed.RetryAfter.Format(time.RFC3339), failed.Error)
			}
		} else if !os.IsNotExist(err) {
			return state, err
		}
		select {
		case <-ctx.Done():
			return state, ctx.Err()
		case <-time.After(150 * time.Millisecond):
		}
		quiet, err := devFingerprint(ctx, state.Source)
		if err != nil {
			return state, err
		}
		if quiet != fingerprint {
			continue
		}
		if err := os.MkdirAll(dir, 0700); err != nil {
			return state, err
		}
		store := filepath.Join(filepath.Dir(dir), "dev-generations")
		if err := safeGeneratedParent(collection, store); err != nil {
			return state, err
		}
		if err := os.MkdirAll(store, 0700); err != nil {
			return state, err
		}
		staging, err := os.MkdirTemp(store, ".building-")
		if err != nil {
			return state, err
		}
		var output bytes.Buffer
		build, err := installDevBuild(ctx, staging, state.Source, &output)
		if err == nil {
			err = probeDevFollow(ctx, build.Binary)
		}
		if err != nil {
			os.RemoveAll(staging)
			text := output.String()
			if len(text) > 4096 {
				text = text[len(text)-4096:]
			}
			failure := fmt.Errorf("%w: %s", err, strings.TrimSpace(text))
			data, _ := json.Marshal(devFailedBuild{Generation: fingerprint, Revision: policy.Revision, Error: failure.Error(), RetryAfter: time.Now().Add(30 * time.Second)})
			// Failure metadata is diagnostic only and can never activate code.
			_ = devAtomicJSON(filepath.Join(dir, "failure.json"), data)
			return state, failure
		}
		after, err := devFingerprint(ctx, state.Source)
		if err != nil || after != fingerprint {
			os.RemoveAll(staging)
			if err != nil {
				return state, err
			}
			continue
		}
		publishUnlock, err := lockDevFile(ctx, filepath.Join(collection, ".wtc-dev-mode.lock"))
		if err != nil {
			os.RemoveAll(staging)
			return state, err
		}
		current, modeErr := readDevMode(collection)
		selected, configErr := DevFollowConfiguration(collection)
		if modeErr != nil || configErr != nil || current != policy || !selected.Active || selected.Source != state.Source {
			publishUnlock()
			os.RemoveAll(staging)
			return state, fmt.Errorf("dev mode changed during build; nothing published")
		}
		generationDir := filepath.Join(store, fingerprint)
		build.Collection, build.Generation, build.FollowProtocol = collection, fingerprint, 1
		build.Binary = filepath.Join(generationDir, "wtc")
		// Never replace a published generation: a running action may still
		// launch a child from its original immutable executable path.
		if err := retainDevArtifact(collection, generationDir, filepath.Join(staging, ".wtc", "dev")); err != nil {
			publishUnlock()
			os.RemoveAll(staging)
			return state, err
		}
		err = publishDevBuild(collection, dir, build)
		publishUnlock()
		os.RemoveAll(staging)
		if err != nil {
			return state, err
		}
		state.Build = ReadDevBuild(collection)
		if state.Build == nil {
			return state, fmt.Errorf("published dev generation is invalid")
		}
		return state, nil
	}
	return state, fmt.Errorf("dev source is still changing; retry after edits settle")
}

// WithDevGeneration serializes final delegation with OFF. Exec closes the
// policy lock in the kernel, so OFF cannot complete before a stale delegation.
func WithDevGeneration(ctx context.Context, collection, generation string, run func(*DevBuild) error) (bool, error) {
	if len(generation) != 64 {
		return false, nil
	}
	build := ReadDevBuild(collection)
	if build == nil || build.Generation != generation {
		return false, nil
	}
	return WithDevBuild(ctx, collection, build, run)
}

// WithDevBuild guards final one-shot and follow delegation with the same
// current selector and OFF policy. Callers compile before taking this lock.
func WithDevBuild(ctx context.Context, collection string, expected *DevBuild, run func(*DevBuild) error) (bool, error) {
	if expected == nil {
		return false, nil
	}
	root, err := devCollectionRoot(collection, nil)
	if err != nil {
		return false, err
	}
	unlock, err := lockDevFile(ctx, filepath.Join(collection, ".wtc-dev-mode.lock"))
	if err != nil {
		return false, err
	}
	defer unlock()
	if _, err := devCollectionRoot(collection, root); err != nil {
		return false, err
	}
	state, err := DevFollowConfiguration(collection)
	if err != nil || state.Mode == "off" || devReviewContext(collection) {
		return false, err
	}
	build := ReadDevBuild(collection)
	if build == nil || *build != *expected {
		return false, nil
	}
	if build.Generation != "" {
		if !state.Active || build.Source != state.Source {
			return false, nil
		}
	} else if state.Active {
		return false, nil
	}
	return true, run(build)
}

func runDevCompiler(ctx context.Context, command *exec.Cmd) error {
	command.WaitDelay = time.Second
	if err := command.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		_ = command.Process.Kill()
		<-done
		return ctx.Err()
	}
}

func devCommandOutput(command *exec.Cmd) ([]byte, error) {
	command.WaitDelay = time.Second
	return command.Output()
}

func probeDevFollow(ctx context.Context, binary string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binary, "--json", "dev", "capabilities")
	command.Env = append(os.Environ(), DevBuildDisableEnv+"=off", DevBuildDelegatedEnv+"=")
	raw, err := devCommandOutput(command)
	if err != nil {
		return fmt.Errorf("local CLI cannot report follow capability; use dev install for one-shot builds: %w", err)
	}
	var capability struct {
		OK   bool `json:"ok"`
		Data struct {
			DevFollow      int `json:"dev_follow"`
			StatusHandover int `json:"status_handover"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &capability) != nil || !capability.OK || capability.Data.DevFollow != 1 || capability.Data.StatusHandover != 1 {
		return fmt.Errorf("local CLI lacks compatible follow/status handover; use dev install for a one-shot build")
	}
	return nil
}

// Go reports the actual selected source and embed inputs, including files
// ignored by Git. No hand-maintained extension list can establish freshness.
func devFingerprint(ctx context.Context, source string) (string, error) {
	version, err := devBuildVersion(source)
	if err != nil {
		return "", err
	}
	compiler, _ := devCompilerCommand(source, "", version, io.Discard)
	list := exec.CommandContext(ctx, "go", "list", "-deps", "-json", compiler.Args[len(compiler.Args)-1])
	list.Dir = source
	raw, err := devCommandOutput(list)
	if err != nil {
		return "", fmt.Errorf("cannot list dev build inputs: %w", err)
	}
	// Map paths to the package/module root whose descendants may be read.
	files := map[string]string{filepath.Join(source, "go.mod"): source}
	addModule := func(path string) error {
		if path == "" {
			return nil
		}
		canonical, err := filepath.EvalSymlinks(path)
		if err != nil {
			return err
		}
		files[canonical] = filepath.Dir(canonical)
		sum := filepath.Join(filepath.Dir(canonical), "go.sum")
		if _, err := os.Stat(sum); err == nil {
			files[sum] = filepath.Dir(sum)
		}
		return nil
	}
	if err := addModule(filepath.Join(source, "go.mod")); err != nil {
		return "", err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	for decoder.More() {
		var pkg struct {
			Dir                                                                                                                 string
			GoFiles, CgoFiles, CFiles, CXXFiles, MFiles, HFiles, FFiles, SFiles, SwigFiles, SwigCXXFiles, SysoFiles, EmbedFiles []string
			Module                                                                                                              *struct {
				Dir, GoMod string
				Main       bool
				Replace    *struct{ Dir, Version, GoMod string }
			}
		}
		if err := decoder.Decode(&pkg); err != nil {
			return "", err
		}
		rel, err := filepath.Rel(source, pkg.Dir)
		outside := err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator))
		mutable := pkg.Module != nil && (pkg.Module.Main || pkg.Module.Replace != nil && pkg.Module.Replace.Version == "")
		if outside && !mutable {
			continue
		}
		root, err := filepath.EvalSymlinks(pkg.Dir)
		if err != nil {
			return "", err
		}
		for _, group := range [][]string{pkg.GoFiles, pkg.CgoFiles, pkg.CFiles, pkg.CXXFiles, pkg.MFiles, pkg.HFiles, pkg.FFiles, pkg.SFiles, pkg.SwigFiles, pkg.SwigCXXFiles, pkg.SysoFiles, pkg.EmbedFiles} {
			for _, name := range group {
				files[filepath.Join(root, name)] = root
			}
		}
		if pkg.Module != nil {
			mod := pkg.Module.GoMod
			if pkg.Module.Replace != nil && pkg.Module.Replace.GoMod != "" {
				mod = pkg.Module.Replace.GoMod
			}
			if err := addModule(mod); err != nil {
				return "", err
			}
		}
	}
	hash := sha256.New()
	fmt.Fprintln(hash, compiler.Path)
	for i, arg := range compiler.Args {
		if i > 0 && compiler.Args[i-1] == "-o" {
			continue // The staging output path is not a semantic compiler input.
		}
		fmt.Fprintln(hash, arg)
	}
	head, err := gitOutput("-C", source, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	fmt.Fprintln(hash, head)
	env := exec.CommandContext(ctx, "go", "env", "-json")
	env.Dir = source
	settings, err := devCommandOutput(env)
	if err != nil {
		return "", err
	}
	var config map[string]string
	if err := json.Unmarshal(settings, &config); err != nil {
		return "", err
	}
	// GOGCCFLAGS is derived output containing a fresh temporary build path.
	// Its semantic inputs (CC, CGO_*FLAGS, GOOS/GOARCH and tuning) remain here.
	delete(config, "GOGCCFLAGS")
	settings, err = json.Marshal(config)
	if err != nil {
		return "", err
	}
	hash.Write(settings)
	if work := config["GOWORK"]; work != "" && work != "off" {
		work, err = filepath.EvalSymlinks(work)
		if err != nil {
			return "", err
		}
		files[work] = filepath.Dir(work)
		if _, err := os.Stat(work + ".sum"); err == nil {
			files[work+".sum"] = filepath.Dir(work)
		}
	}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := safeGeneratedParent(files[name], filepath.Dir(name)); err != nil {
			return "", err
		}
		info, err := os.Lstat(name)
		if err != nil || !info.Mode().IsRegular() {
			return "", fmt.Errorf("unsafe dev build input %s", name)
		}
		fmt.Fprintf(hash, "\x00%s\x00%d\x00", name, info.Size())
		f, err := os.Open(name)
		if err != nil {
			return "", err
		}
		_, err = io.Copy(hash, f)
		f.Close()
		if err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
