package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"github.com/Masterminds/semver/v3"
	"github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"
	"taba.sh/taba/internal/branding"
	"taba.sh/taba/internal/wtc"
)

// delegatedFrom is the version that handed over to this binary, if any. The
// handover marker is read once and removed from the environment, so hooks,
// agents and panes started from here hand over again on their own.
var delegatedFrom string

func takeDelegatedFrom() {
	delegatedFrom = os.Getenv(wtc.DevBuildDelegatedEnv)
	os.Unsetenv(wtc.DevBuildDelegatedEnv)
}

// handOverToDevBuild replaces this process with the collection's dev build
// when that build is the same version as this one or newer. It returns only
// when this binary should keep running.
func handOverToDevBuild() {
	takeDelegatedFrom()
	if delegatedFrom != "" || os.Getenv(wtc.DevBuildDisableEnv) == "off" {
		return
	}
	if _, err := semver.NewVersion(version); err != nil {
		// An unversioned build ("dev") is already somebody's build.
		return
	}
	dir, err := os.Getwd()
	if err != nil {
		return
	}
	build := wtc.ReadDevBuild(wtc.FindCollectionRoot(dir))
	if build == nil || isDevBuildBinary(build) {
		return
	}
	if !wtc.DevBuildTakesOver(version, build) {
		if term.IsTerminal(os.Stderr.Fd()) {
			fmt.Fprintf(os.Stderr, branding.Text("{{command}}: dev build %s is older than %s; using %s ({{command}} dev install to rebuild)\n"), build.Version, version, version)
		}
		return
	}
	env := append(os.Environ(), wtc.DevBuildDelegatedEnv+"="+version)
	// On success Exec does not return; on failure keep running this binary.
	if err := syscall.Exec(build.Binary, os.Args, env); err != nil {
		fmt.Fprintf(os.Stderr, branding.Text("{{command}}: cannot run dev build %s: %v\n"), build.Binary, err)
	}
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
		Long: branding.Text("{{command}} dev install builds the wtc-cli checkout in this collection into .wtc/dev/wtc.\n") +
			branding.Text("Any {{command}} started inside the collection then hands over to that build while it is the same version\n") +
			"or newer, and runs itself again once it is newer than the build. Set WTC_DEV_BUILD=off to skip\n" +
			"the handover for one command.", Args: cobra.NoArgs}
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
			data := map[string]any{"running": version, "delegated_from": delegatedFrom, "dev_build": build}
			summary := "no dev build in " + root
			if build != nil {
				summary = fmt.Sprintf("dev build %s from %s\n%s", build.Version, build.Source, devBuildEffect(build))
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
	dev.AddCommand(install, status, uninstall)
	root.AddCommand(dev)
}

func devBuildEffect(build *wtc.DevBuild) string {
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
