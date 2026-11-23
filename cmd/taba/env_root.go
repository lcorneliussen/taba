package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"taba.sh/taba/internal/branding"
	"taba.sh/taba/internal/wtc"
)

// addEnvRootCommand registers `env root`: create, refresh or check the
// project root's generated CLI selection.
func addEnvRootCommand(envCmd *cobra.Command, asJSON *bool) {
	var check, dryRun bool
	var harness string
	cmd := &cobra.Command{
		Use:   "root",
		Short: "Select the project's CLI release at the project root",
		Long: "Generate " + wtc.RootSelectionFile + " at the project root from the harness pin at the default ref\n" +
			"the harness owner's own registry declares, so the project's CLI runs there without a global default.\n" +
			"Run in a collection, or at a root that already has the generated file. At a root\n" +
			"without one, name the harness owner with --harness. --check reports the project\n" +
			"source, the root selection and this collection's selection without writing.",
		Args: cobra.NoArgs,
	}
	cmd.Flags().BoolVar(&check, "check", false, "Report without writing")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show the generated file without writing")
	cmd.Flags().StringVar(&harness, "harness", "", "Harness owner name under .bare/ (at a root without a generated file)")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		if check && dryRun {
			return errors.New("--check and --dry-run cannot be combined")
		}
		dir, err := os.Getwd()
		if err != nil {
			return err
		}
		opt := wtc.RootSyncOptions{Harness: harness, Create: true, Check: check, DryRun: dryRun}
		var collection *wtc.Context
		if location, err := wtc.DiscoverLocation(dir); err == nil && location.Collection != "" {
			if collection, err = wtc.Discover(dir); err != nil {
				return err
			}
			opt.Root = collection.Workspace
			if opt.Harness == "" {
				if opt.Harness, err = collection.RootSelectionHarness(); err != nil {
					return err
				}
			}
		} else if root, ok := wtc.FindProjectRoot(dir); ok {
			opt.Root = root
		} else if harness != "" {
			if _, err := os.Stat(filepath.Join(dir, ".bare", harness+".git")); err != nil {
				return fmt.Errorf("no harness owner .bare/%s.git here; run at the project root", harness)
			}
			opt.Root = dir
		} else {
			return errors.New(branding.Text("not in a collection or at a project root with a generated selection; run `{{command}} env root` in a collection, or at the root with --harness <owner>"))
		}
		report, err := wtc.SyncRootSelection(opt)
		if collection != nil {
			report.Collection = collection.CollectionSelection(report.Source)
		}
		if *asJSON {
			if emitErr := emit(envelope{OK: err == nil, Data: report, Summary: rootSummary(report)}, true); emitErr != nil {
				return emitErr
			}
			return err
		}
		if dryRun && report.Content != "" {
			fmt.Print(report.Content)
		}
		printRootReport(report)
		return err
	}
	envCmd.AddCommand(cmd)
}

func rootSummary(report wtc.RootSelectionReport) string {
	switch {
	case report.Changed && report.DryRun:
		return "root selection: would update"
	case report.Changed:
		return "root selection: updated"
	default:
		return "root selection: " + report.State
	}
}

func printRootReport(report wtc.RootSelectionReport) {
	if report.Source != nil {
		s := report.Source
		fmt.Printf("project source:  %s %s @ %s → %s %s\n", s.Owner, s.Ref, shortSHA(s.Commit), s.Pin, s.Version)
	} else {
		fmt.Printf("project source:  unavailable: %s\n", report.SourceError)
	}
	line := report.State
	if report.Recorded != nil && report.Recorded.Version != "" {
		line += " (selects " + report.Recorded.Version + ")"
	}
	if report.Changed && report.DryRun {
		line += "; would update"
	} else if report.Changed {
		line += "; updated"
	}
	fmt.Printf("root selection:  %s %s\n", report.File, line)
	if c := report.Collection; c != nil {
		note := ""
		if c.Differs {
			note = " — differs from the project source; this collection keeps its own selection"
		}
		fmt.Printf("collection:      %s selects %s (harness %s)%s\n", c.Collection, c.Version, shortSHA(c.HarnessCommit), note)
	}
	for _, name := range report.Overrides {
		fmt.Printf("root override:   %s also selects the CLI and takes precedence at the root\n", name)
	}
	if report.MiseMissing {
		fmt.Println("note: mise not found; the file records the project source, but put the selected release on PATH yourself")
	}
}

func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
