package main

import (
	"fmt"
	"github.com/spf13/cobra"
	"os"
	"taba.sh/taba/internal/wtc"
)

func addRepoBaseCommands(root *cobra.Command, asJSON *bool) {
	var collection, reason string
	var unmanaged bool
	repo := &cobra.Command{Use: "repo", Short: "Inspect collection repository settings"}
	base := &cobra.Command{Use: "base", Short: "Set, show or clear a receiving-local development base"}
	base.PersistentFlags().StringVar(&collection, "collection", "", "Receiving collection directory (default: current)")
	context := func() (*wtc.Context, error) {
		if collection != "" {
			return wtc.OpenCollection(collection)
		}
		cwd, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		return wtc.Discover(cwd)
	}
	for _, action := range []string{"set", "show", "clear"} {
		action := action
		args, use := 1, action+" <repo>"
		if action == "set" {
			args, use = 2, action+" <repo> <ref>"
		}
		cmd := &cobra.Command{Use: use, Short: action + " the local development base", Args: cobra.ExactArgs(args)}
		if action == "show" {
			cmd.Flags().BoolVar(&unmanaged, "unmanaged", false, "Allow an existing unregistered Git worktree (browse compatibility)")
			_ = cmd.Flags().MarkHidden("unmanaged")
		}
		if action == "set" {
			cmd.Flags().StringVar(&reason, "reason", "", "Human reason for this collection's base (required)")
			_ = cmd.MarkFlagRequired("reason")
		}
		cmd.RunE = func(cmd *cobra.Command, args []string) error {
			if action == "set" && args[1] == "" {
				return fmt.Errorf("base ref must not be empty; use repo base clear to clear an override")
			}
			c, err := context()
			if err != nil {
				return err
			}
			var result wtc.RepositoryBase
			switch action {
			case "show":
				result, err = c.RepositoryBase(args[0], unmanaged)
			case "set":
				result, err = c.SetRepositoryBase(args[0], args[1], reason)
			case "clear":
				outcome, clearErr := c.ClearRepositoryBase(args[0])
				if clearErr != nil {
					return clearErr
				}
				if outcome.Orphaned {
					summary := fmt.Sprintf("Cleared orphan repository base %q; checkout unchanged.", outcome.ExactKey)
					return emit(envelope{OK: true, Data: outcome, Summary: summary}, *asJSON)
				}
				result = *outcome.Resolution
			}
			if err != nil {
				return err
			}
			summary := fmt.Sprintf("%s: %q (%s)", result.Repository, result.EffectiveRef, result.Source)
			if *asJSON {
				return emit(envelope{OK: true, Data: result, Summary: summary}, true)
			}
			fmt.Println(summary)
			if result.RegistryRef != "" {
				fmt.Printf("registry: %q\n", result.RegistryRef)
			}
			if result.Reason != "" {
				fmt.Printf("reason: %q\n", result.Reason)
			}
			if action != "show" {
				fmt.Println("Local base setting updated; checkout unchanged.")
			}
			return nil
		}
		base.AddCommand(cmd)
	}
	repo.AddCommand(base)
	root.AddCommand(repo)
}
