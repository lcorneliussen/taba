package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"taba.sh/taba/internal/branding"
	"taba.sh/taba/internal/openclaw"
	"taba.sh/taba/internal/wtc"
)

func addUpgradeCommand(root *cobra.Command, asJSON *bool) {
	var opt wtc.UpgradeOptions
	var rollout bool
	cmd := &cobra.Command{
		Use:   "upgrade",
		Short: "Move the harness to a newer wtc release, or roll a merged upgrade out",
		Long: "Without --rollout: bump the harness's version pin (.taba-version or .wtc-cli-version) and taba.toml or wtc.toml compatibility range to the latest\n" +
			"(or --version) release, refresh this collection's mise config, install the release, and print the\n" +
			"release notes in between. Nothing is committed; review the notes and open the harness PR.\n\n" +
			"With --rollout, after that PR merged: catch every collection's harness up, clean worktrees and idle\n" +
			"agents only, install the pinned tools, and report the collections left for their owners.",
		Args: cobra.NoArgs,
	}
	cmd.Flags().StringVar(&opt.Version, "version", "", "Release to pin (default: the latest published release)")
	cmd.Flags().BoolVar(&opt.DryRun, "dry-run", false, "Plan without writing; notes come from the published releases")
	cmd.Flags().BoolVar(&rollout, "rollout", false, "Catch every collection's harness up to the merged pin")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		if rollout && opt.Version != "" {
			return fmt.Errorf("--rollout takes the pin from each harness's development tip; it cannot be combined with --version")
		}
		if currentHost() == openclaw.HostOpenClaw {
			return errors.New(branding.Text("under OpenClaw, upgrade by installing the new wtc and running `{{command}} openclaw install`"))
		}
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		c, err := wtc.Discover(cwd)
		if err != nil {
			return err
		}
		if rollout {
			sweep := wtc.RolloutOptions(opt.DryRun)
			if sweep.Panes, err = catchUpPanes(c); err != nil {
				return err
			}
			report, runErr := c.CatchUp(sweep)
			if *asJSON {
				if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
					return err
				}
			} else {
				fmt.Print(report.Markdown())
			}
			return runErr
		}
		result, err := c.Upgrade(opt)
		if err != nil {
			return err
		}
		if *asJSON {
			return emit(envelope{OK: true, Data: result}, true)
		}
		verb := "pinned"
		if result.DryRun {
			verb = "would pin"
		}
		fmt.Printf("%s wtc %s (was %s); %s requires %q\n", verb, result.To, result.From, result.Files[1], result.Requires)
		if result.NotInstalled {
			fmt.Printf("mise not found: wtc %s is pinned but not installed\n", result.To)
		}
		if result.RootSelectionError != "" {
			fmt.Printf(branding.Text("project root selection not updated: %s (see `{{command}} env root --check`)\n"), result.RootSelectionError)
		} else if result.RootSelection != nil && result.RootSelection.Source != nil {
			fmt.Printf("project root selects %s from %s; it follows the harness default ref after the upgrade merges\n", result.RootSelection.Source.Version, result.RootSelection.Source.Ref)
		}
		if result.NotesError != "" {
			fmt.Printf(branding.Text("release notes unavailable (%s); read them with the new binary: {{command}} release-notes --since %s\n"), result.NotesError, result.From)
		} else {
			fmt.Printf("release notes %s → %s, from %s:\n\n", result.From, result.To, result.NotesSource)
		}
		for i, note := range result.Notes {
			if i > 0 {
				fmt.Print("\n---\n\n")
			}
			fmt.Print(strings.TrimRight(note.Content, "\n") + "\n")
		}
		printUpgradeDoctor(result.Doctor)
		if !result.DryRun {
			fmt.Println(branding.Text("\nNext: decide what to adapt or adopt from these notes, commit the harness change on a branch, open its PR,\nand after it merges run `{{command}} upgrade --rollout`."))
		}
		return nil
	}
	root.AddCommand(cmd)
}

// printUpgradeDoctor reports the target release's diagnostics as advice,
// after the notes. After a real upgrade the transition has completed and the
// findings cannot change it; on a dry run it only says what would run.
func printUpgradeDoctor(d *wtc.UpgradeDoctor) {
	if d == nil {
		return
	}
	fmt.Printf("\ndiagnostics (advisory) with wtc %s doctor: %s", d.Version, d.Status)
	if d.Reason != "" {
		fmt.Printf(" — %s", d.Reason)
	}
	fmt.Println()
	for _, finding := range d.Findings {
		fmt.Printf("  %-15s %s\n", finding.Name, finding.Detail)
	}
	if d.Truncated > 0 {
		fmt.Printf("  … %d more; run the doctor in this collection for all of them\n", d.Truncated)
	}
}
