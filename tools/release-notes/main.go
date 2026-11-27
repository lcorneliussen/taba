// Command release-notes maintains internal/wtc/release-notes from the
// repository root:
//
//	go run ./tools/release-notes cut <version>   # fragments -> <version>.md
//	go run ./tools/release-notes check [<version>] # stable builds must consume fragments
package main

import (
	"fmt"
	"os"

	"github.com/Masterminds/semver/v3"

	"taba.sh/taba/internal/wtc"
)

const dir = "internal/wtc/release-notes"

func main() {
	if err := run(dir, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "release-notes:", err)
		os.Exit(1)
	}
}

func run(dir string, args []string) error {
	switch {
	case len(args) == 2 && args[0] == "cut":
		path, err := wtc.CutReleaseNotes(dir, args[1])
		if err != nil {
			return err
		}
		fmt.Printf("wrote %s; edit it before tagging\n", path)
		return nil
	case (len(args) == 1 || len(args) == 2) && args[0] == "check":
		prerelease := false
		if len(args) == 2 {
			version, err := semver.StrictNewVersion(args[1])
			if err != nil {
				return fmt.Errorf("invalid release version %q: %w", args[1], err)
			}
			prerelease = version.Prerelease() != ""
		}
		fragments, err := wtc.ReadReleaseFragments(os.DirFS(dir))
		if err != nil {
			return err
		}
		if !prerelease && len(fragments) > 0 {
			return fmt.Errorf("%d unreleased fragment(s) remain in %s/%s; cut them into the release notes before tagging", len(fragments), dir, wtc.UnreleasedDir)
		}
		return nil
	}
	return fmt.Errorf("usage: release-notes cut <version> | check [<version>]")
}
