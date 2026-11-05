package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"github.com/Masterminds/semver/v3"
	"github.com/charmbracelet/x/term"
	"github.com/lcorneliussen/wtc-cli/internal/branding"
	"github.com/lcorneliussen/wtc-cli/internal/wtc"
	"github.com/spf13/cobra"
)

// delegatedFrom is the version that handed over to this binary, if any. The
// handover marker is read once and removed from the environment, so hooks,
// agents and panes started from here hand over again on their own.
var delegatedFrom string

var devAmbientExecutable string
var devRunningGeneration string
var devValidatedHandover bool

const devAmbientEnv = "WTC_DEV_AMBIENT_EXE"
const devGenerationEnv = "WTC_DEV_GENERATION"
const devStatusStateEnv = "WTC_DEV_STATUS_STATE"

func restartStatusDev(collection, binary, generation string, presentation statusTUIPresentation) error {
	state, err := json.Marshal(presentation)
	if err != nil {
		return err
	}
	execute := func(target, currentGeneration string) error {
		env := append(os.Environ(), devAmbientEnv+"="+devAmbientExecutable, devGenerationEnv+"="+currentGeneration, devStatusStateEnv+"="+string(state))
		return syscall.Exec(target, os.Args, env)
	}
	if generation != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		ran, err := wtc.WithDevGeneration(ctx, collection, generation, func(current *wtc.DevBuild) error {
			return execute(current.Binary, current.Generation)
		})
		if ran || err != nil {
			return err
		}
		state, err := wtc.DevFollowConfiguration(collection)
		if err != nil {
			return err
		}
		if state.Active {
			return fmt.Errorf("dev selection changed; retry status with the selected CLI")
		}
		// OFF or inactive policy may have arrived while the TUI waited.
		// Only that inactive state may return through the ambient CLI.
		if devAmbientExecutable == "" {
			return fmt.Errorf("dev selection changed; restart status with the selected CLI")
		}
		return execute(devAmbientExecutable, "")
	}
	return execute(binary, generation)
}

func takeStatusDevPresentation() *statusTUIPresentation {
	raw := os.Getenv(devStatusStateEnv)
	os.Unsetenv(devStatusStateEnv)
	if raw == "" || len(raw) > 16384 {
		return nil
	}
	var state statusTUIPresentation
	if json.Unmarshal([]byte(raw), &state) != nil {
		return nil
	}
	return &state
}

func takeDelegatedFrom() {
	delegatedFrom = os.Getenv(wtc.DevBuildDelegatedEnv)
	os.Unsetenv(wtc.DevBuildDelegatedEnv)
	devAmbientExecutable = os.Getenv(devAmbientEnv)
	os.Unsetenv(devAmbientEnv)
	// The immutable executable path, not an inherited claim or mutable current
	// pointer, names the code loaded in this process.
	devRunningGeneration = ""
	incomingGeneration := os.Getenv(devGenerationEnv)
	os.Unsetenv(devGenerationEnv)
	if self, err := os.Executable(); err == nil && filepath.Base(filepath.Dir(filepath.Dir(self))) == "dev-generations" {
		candidate := filepath.Base(filepath.Dir(self))
		if _, err := hex.DecodeString(candidate); err == nil && len(candidate) == 64 {
			devRunningGeneration = candidate
		}
	}
	devValidatedHandover = incomingGeneration != "" && incomingGeneration == devRunningGeneration
	if devAmbientExecutable == "" {
		if self, err := os.Executable(); err == nil && !devArtifactExecutable(self) {
			devAmbientExecutable = self
		}
	}
}

func devArtifactExecutable(path string) bool {
	parent := filepath.Dir(path)
	if filepath.Base(filepath.Dir(parent)) == "dev-generations" {
		return true
	}
	return filepath.Base(parent) == "dev" && (filepath.Base(filepath.Dir(parent)) == ".wtc" || filepath.Base(filepath.Dir(parent)) == ".taba")
}

// handOverToDevBuild replaces this process with the invocation collection's
// fresh followed source, or a same/newer legacy one-shot artifact. Nil permits
// this executable to continue; an error aborts startup without running stale code.
func handOverToDevBuild() error {
	takeDelegatedFrom()
	if os.Getenv(wtc.DevBuildDisableEnv) == "off" || devControlInvocation(os.Args[1:]) || devValidatedHandover {
		return nil
	}
	if _, err := semver.NewVersion(version); err != nil {
		// An unversioned build ("dev") is already somebody's build.
		return nil
	}
	dir, err := os.Getwd()
	if err != nil {
		return nil
	}
	collection := wtc.FindCollectionRoot(dir)
	if collection == "" {
		return nil
	}
	follow, err := wtc.DevFollowConfiguration(collection)
	if err != nil {
		return fmt.Errorf("dev mode: %w (use dev mode off or WTC_DEV_BUILD=off to recover)", err)
	}
	if follow.Mode == "off" || strings.Contains(follow.Reason, "review-only") {
		return nil
	}
	if follow.Active {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		follow, err = wtc.EnsureDevFollow(ctx, collection)
		if err != nil {
			return fmt.Errorf("dev build is not current: %w", err)
		}
		if !follow.Active {
			return nil // Mode was disabled while this invocation waited.
		}
		if follow.Build == nil {
			return fmt.Errorf("dev selection has no fresh build; retry")
		}
		if follow.Build.Generation == devRunningGeneration {
			return nil
		}
		return execDevBuild(follow.Build)
	}
	if delegatedFrom != "" {
		return nil // A legacy version-only hop guards only inactive fallback.
	}
	build := wtc.ReadDevBuild(collection)
	if build == nil || isDevBuildBinary(build) {
		return nil
	}
	if build.Generation != "" {
		return nil // An inactive follow generation is not a one-shot override.
	}
	if !wtc.DevBuildTakesOver(version, build) {
		if term.IsTerminal(os.Stderr.Fd()) {
			fmt.Fprintf(os.Stderr, branding.Text("{{command}}: dev build %s is older than %s; using %s ({{command}} dev install to rebuild)\n"), build.Version, version, version)
		}
		return nil
	}
	if err := execDevBuild(build); err != nil {
		fmt.Fprintf(os.Stderr, branding.Text("{{command}}: cannot run dev build %s: %v\n"), build.Binary, err)
	}
	return nil
}

// Selection and recovery commands must work without building broken source.
// Review commands never automatically run the candidate being verified.
func devControlInvocation(args []string) bool {
	var asJSON bool
	root := commandRoot(&asJSON)
	root.InitDefaultHelpFlag()
	addDevCommands(root, &asJSON)
	addReviewCommands(root, &asJSON)
	// Find uses the actual command/flag definitions, including values and
	// aliases, without parsing into a context or executing any callback.
	command, _, _ := root.Find(args)
	for command != nil && command != root {
		if command.Name() == "dev" || command.Name() == "review" {
			return true
		}
		command = command.Parent()
	}
	return false
}

func execDevBuild(build *wtc.DevBuild) error {
	ambient := devAmbientExecutable
	if ambient == "" {
		if self, err := os.Executable(); err == nil && !devArtifactExecutable(self) {
			ambient = self
		}
	}
	env := append(os.Environ(), wtc.DevBuildDelegatedEnv+"="+version, devAmbientEnv+"="+ambient, devGenerationEnv+"="+build.Generation)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ran, err := wtc.WithDevBuild(ctx, build.Collection, build, func(current *wtc.DevBuild) error {
		return syscall.Exec(current.Binary, os.Args, env)
	})
	if ran || err != nil {
		return err
	}
	state, err := wtc.DevFollowConfiguration(build.Collection)
	if err != nil || state.Mode == "off" {
		return err
	}
	return fmt.Errorf("dev selection changed; retry the command")
}

// isDevBuildBinary says whether this process is the dev build itself, by file
// identity, so symlinked collection paths compare correctly.
func isDevBuildBinary(build *wtc.DevBuild) bool {
	self, err := os.Executable()
	if err != nil {
		return false
	}
	a, errA := os.Stat(self)
	b, errB := os.Stat(build.Binary)
	return errA == nil && errB == nil && os.SameFile(a, b)
}

// versionLabel is what --version prints: a dev build standing in for another
// version says so.
func versionLabel() string {
	if delegatedFrom != "" {
		return version + " (dev build, in place of " + delegatedFrom + ")"
	}
	if version == "dev" {
		return describeBuild(debug.ReadBuildInfo)
	}
	return version
}

// describeBuild names a build without a release version (a plain go build),
// similar to wtc dev install: below the next patch release after the newest
// release it embeds notes for, with its commit, the commit's time and .dirty
// for uncommitted changes, as far as Go recorded them.
func describeBuild(readInfo func() (*debug.BuildInfo, bool)) string {
	base := ""
	if notes, err := wtc.EmbeddedReleaseNotes(); err == nil {
		var newest *semver.Version
		for _, n := range notes {
			if v, err := semver.NewVersion(n.Version); err == nil && (newest == nil || v.GreaterThan(newest)) {
				newest = v
			}
		}
		if newest != nil {
			base = fmt.Sprintf("%d.%d.%d-dev", newest.Major(), newest.Minor(), newest.Patch()+1)
		}
	}
	if base == "" {
		return "dev"
	}
	meta := []string{}
	if info, ok := readInfo(); ok {
		settings := map[string]string{}
		for _, s := range info.Settings {
			settings[s.Key] = s.Value
		}
		if rev := settings["vcs.revision"]; len(rev) >= 7 {
			meta = append(meta, "g"+rev[:7])
		}
		if at, err := time.Parse(time.RFC3339, settings["vcs.time"]); err == nil {
			meta = append(meta, at.UTC().Format("20060102.1504"))
		}
		if settings["vcs.modified"] == "true" {
			meta = append(meta, "dirty")
		}
	}
	if len(meta) == 0 {
		return base
	}
	return base + "+" + strings.Join(meta, ".")
}

func addDevCommands(root *cobra.Command, asJSON *bool) {
	dev := &cobra.Command{Use: "dev", Short: branding.Text("Run a collection-local build of {{command}} in place of the pinned release"),
		Long: branding.Text("{{command}} dev install builds a one-shot local artifact; a newer release can overtake it.\n") +
			branding.Text("{{command}} dev mode on follows the selected checkout, rebuilding changes before new commands.\n") +
			"Mode auto requires an explicit project convention; mode off disables local builds persistently.\n" +
			"Uninstall also selects off. WTC_DEV_BUILD=off bypasses local builds for one invocation.", Args: cobra.NoArgs}
	collectionRoot := func() (string, error) {
		dir, err := os.Getwd()
		if err != nil {
			return "", err
		}
		root := wtc.FindCollectionRoot(dir)
		if root == "" {
			return "", fmt.Errorf("not inside a worktree collection")
		}
		return root, nil
	}
	var source string
	install := &cobra.Command{Use: "install", Short: "Build the collection's wtc-cli checkout as its dev build", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := collectionRoot()
			if err != nil {
				return err
			}
			from := source
			if from == "" {
				if from, err = wtc.DevBuildSource(root); err != nil {
					return err
				}
			} else if from, err = filepath.Abs(from); err != nil {
				return err
			}
			build, err := wtc.InstallDevBuild(root, from)
			if err != nil {
				return err
			}
			return emit(envelope{OK: true, Data: build, Summary: fmt.Sprintf("installed dev build %s from %s\n%s", build.Version, build.Source, devBuildEffect(build))}, *asJSON)
		}}
	install.Flags().StringVar(&source, "source", "", "wtc-cli checkout to build (default: the collection's wtc-cli sibling)")
	status := &cobra.Command{Use: "status", Short: branding.Text("Show which {{command}} runs in this collection"), Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := collectionRoot()
			if err != nil {
				return err
			}
			build := wtc.ReadDevBuild(root)
			follow, err := wtc.DevFollowConfiguration(root)
			if err != nil {
				return err
			}
			data := map[string]any{"running": version, "delegated_from": delegatedFrom, "dev_build": build, "follow": follow, "running_generation": devRunningGeneration}
			summary := "no dev build in " + root
			if build != nil {
				summary = fmt.Sprintf("dev build %s from %s\n%s", build.Version, build.Source, devBuildEffect(build))
			}
			summary += fmt.Sprintf("\nmode: %s; %s", follow.Mode, follow.Reason)
			if follow.Active {
				summary += "following " + follow.Source
			}
			return emit(envelope{OK: true, Data: data, Summary: summary}, *asJSON)
		}}
	uninstall := &cobra.Command{Use: "uninstall", Short: "Remove the collection's dev build", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := collectionRoot()
			if err != nil {
				return err
			}
			removed, err := wtc.UninstallDevBuild(root)
			if err != nil {
				return err
			}
			summary := "no dev build to remove"
			if removed {
				summary = branding.Text("removed the dev build; {{command}} runs the pinned or ambient release here again")
			}
			return emit(envelope{OK: true, Data: map[string]bool{"removed": removed}, Summary: summary}, *asJSON)
		}}
	var modeSource string
	mode := &cobra.Command{Use: "mode <auto|on|off>", Short: "Follow local source automatically, explicitly, or disable it persistently", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := collectionRoot()
			if err != nil {
				return err
			}
			if err := wtc.SetDevMode(root, args[0], modeSource); err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 2*time.Minute)
			defer cancel()
			state, err := wtc.EnsureDevFollow(ctx, root)
			if err != nil {
				return fmt.Errorf("mode saved; build is not current: %w", err)
			}
			return emit(envelope{OK: true, Data: state, Summary: fmt.Sprintf("dev mode %s: %s", state.Mode, state.Reason)}, *asJSON)
		}}
	mode.Flags().StringVar(&modeSource, "source", "", "Explicit checkout for mode on; never fetches or changes its branch")
	capabilities := &cobra.Command{Use: "capabilities", Short: "Report this executable's local-build lifecycle protocol", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return emit(envelope{OK: true, Data: map[string]int{"dev_follow": 1, "status_handover": 1}, Summary: "dev-follow 1; status-handover 1"}, *asJSON)
		}}
	dev.AddCommand(install, status, uninstall, mode, capabilities)
	root.AddCommand(dev)
}

func devBuildEffect(build *wtc.DevBuild) string {
	if build.Collection != "" {
		state, err := wtc.DevFollowConfiguration(build.Collection)
		if err != nil {
			return "stored artifact; dev mode needs attention: " + err.Error()
		}
		if state.Mode == "off" {
			return "stored artifact; mode off keeps it inactive"
		}
		if state.Active {
			return "mode " + state.Mode + " follows " + state.Source
		}
	}
	if build.Generation != "" {
		if build.Generation == devRunningGeneration {
			return "running this immutable local generation"
		}
		return "stored local generation; dev mode controls whether future commands use it"
	}
	if delegatedFrom != "" {
		return "in effect: running it in place of " + delegatedFrom
	}
	if version == build.Version {
		return "in effect: this is the dev build"
	}
	if wtc.DevBuildTakesOver(version, build) {
		return branding.Text("in effect: {{command}} ") + version + " hands over to it"
	}
	if _, err := os.Stat(build.Binary); err == nil && version == "dev" {
		return branding.Text("this {{command}} is an unversioned build and does not hand over")
	}
	return branding.Text("not in effect: {{command}} ") + version + " is newer"
}
