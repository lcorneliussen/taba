package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"taba.sh/taba/internal/branding"
	"taba.sh/taba/internal/wtc"
)

func addHarnessCommands(root *cobra.Command, asJSON *bool) {
	parent := &cobra.Command{Use: "harness", Short: "Bootstrap project-owned harness configuration"}
	var opt wtc.HarnessInitOptions
	init := &cobra.Command{Use: "init <directory>", Short: "Scaffold a small harness without contacting a forge or running hooks", Args: cobra.ExactArgs(1)}
	init.Flags().StringVar(&opt.Name, "name", "agent-harness", "Harness repository name")
	init.Flags().StringVar(&opt.Remote, "remote", "", "Harness Git remote to record; no network access")
	init.Flags().StringVar(&opt.CLIVersion, "cli-version", "", "Exact published WTC version to pin, without v")
	init.Flags().BoolVar(&opt.DryRun, "dry-run", false, "Preview filenames without writing")
	init.MarkFlagRequired("remote")
	init.MarkFlagRequired("cli-version")
	init.RunE = func(cmd *cobra.Command, args []string) error {
		opt.Dir = args[0]
		result, err := wtc.InitHarness(opt)
		if err != nil {
			return err
		}
		action := "created"
		if opt.DryRun {
			action = "would create"
		}
		return emit(envelope{OK: true, Data: result, Summary: fmt.Sprintf("%s harness %s: %s", action, result.Directory, strings.Join(result.Files, ", "))}, *asJSON)
	}
	parent.AddCommand(init)
	root.AddCommand(parent)
	docs := &cobra.Command{Use: "docs [path]", Short: "Read versioned instructions embedded in this CLI, from any directory", Args: cobra.MaximumNArgs(1)}
	docs.RunE = func(cmd *cobra.Command, args []string) error {
		if len(args) == 0 {
			paths, err := wtc.DefaultPaths()
			if err != nil {
				return err
			}
			return emit(envelope{OK: true, Data: paths, Summary: strings.Join(paths, "\n")}, *asJSON)
		}
		data, err := wtc.ReadDefault(args[0])
		if err != nil {
			return fmt.Errorf(branding.Text("unknown embedded document %q; run {{command}} docs to list paths"), args[0])
		}
		if *asJSON {
			return emit(envelope{OK: true, Data: map[string]string{"path": args[0], "content": string(data)}}, true)
		}
		fmt.Print(string(data))
		return nil
	}
	root.AddCommand(docs)
}
