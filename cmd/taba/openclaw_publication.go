package main

import (
	"github.com/spf13/cobra"
	"taba.sh/taba/internal/wtc"
)

func addOpenClawPublicationCommand(oc *cobra.Command) {
	var collection, worktree, prURL, branch string
	command := &cobra.Command{Use: "enlist-publication", Short: "Record a verified native publication in its collection without hooks", Args: cobra.NoArgs}
	command.Flags().StringVar(&collection, "collection", "", "Linked collection folder")
	command.Flags().StringVar(&worktree, "worktree", "", "Native publisher's managed worktree")
	command.Flags().StringVar(&prURL, "url", "", "Native publisher's pull request URL")
	command.Flags().StringVar(&branch, "branch", "", "Native publisher's captured branch")
	for _, name := range []string{"collection", "worktree", "url", "branch"} {
		_ = command.MarkFlagRequired(name)
	}
	command.RunE = func(cmd *cobra.Command, args []string) error {
		c, err := wtc.OpenCollection(collection)
		if err != nil {
			return err
		}
		record, err := c.EnlistGatewayPublication(worktree, wtc.PRRecord{URL: prURL, Branch: branch})
		if err != nil {
			return err
		}
		return emit(envelope{OK: true, Data: record, Summary: "Pull request enlisted"}, true)
	}
	oc.AddCommand(command)
}
