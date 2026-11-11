package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"taba.sh/taba/internal/wtc"
)

func addReleaseNotesCommand(root *cobra.Command, asJSON *bool) {
	var since string
	var list bool
	cmd := &cobra.Command{
		Use: "release-notes [version|unreleased]", Short: "Read embedded release and harness upgrade notes offline",
		Long: "Read the latest embedded release notes, a specific version, or releases newer than --since.\nUnreleased changes are shown only when requested explicitly. No collection or network is needed.",
		Args: cobra.MaximumNArgs(1),
	}
	cmd.Flags().StringVar(&since, "since", "", "Show released versions newer than this version, oldest first")
	cmd.Flags().BoolVar(&list, "list", false, "List embedded versions, including unreleased changes")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		if cmd.Flags().Changed("since") && since == "" {
			return fmt.Errorf("--since requires a version")
		}
		if list {
			if len(args) != 0 || cmd.Flags().Changed("since") {
				return fmt.Errorf("--list cannot be combined with a version or --since")
			}
			notes, err := wtc.EmbeddedReleaseNotes()
			if err != nil {
				return err
			}
			versions := make([]string, 0, len(notes))
			for _, note := range notes {
				versions = append(versions, note.Version)
			}
			return emit(envelope{OK: true, Data: versions, Summary: strings.Join(versions, "\n")}, *asJSON)
		}
		version := ""
		if len(args) != 0 {
			version = args[0]
			if version == "" {
				return fmt.Errorf("version must not be empty")
			}
		}
		notes, err := wtc.SelectReleaseNotes(version, since)
		if err != nil {
			return err
		}
		if *asJSON {
			return emit(envelope{OK: true, Data: notes}, true)
		}
		if len(notes) == 0 {
			fmt.Println("No newer release notes are embedded in this binary.")
			return nil
		}
		for i, note := range notes {
			if i > 0 {
				fmt.Print("\n---\n\n")
			}
			fmt.Print(note.Content)
		}
		return nil
	}
	root.AddCommand(cmd)
}
