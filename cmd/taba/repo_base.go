package main

import (
	"fmt"
	"github.com/spf13/cobra"
	"os"
	"taba.sh/taba/internal/wtc"
)

func addRepoBaseCommands(root *cobra.Command, asJSON *bool) {
	var collection, reason string
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
		if action == "set" {
			cmd.Flags().StringVar(&reason, "reason", "", "Human reason for this collection's base (required)")
			_ = cmd.MarkFlagRequired("reason")
		}
		cmd.RunE = func(cmd *cobra.Command, args []string) error {
			c, err := context()
			if err != nil {
				return err
			}
			var result wtc.RepositoryBase
			switch action {
			case "show":
				result, err = c.RepositoryBase(args[0])
			case "set":
				result, err = c.SetRepositoryBase(args[0], args[1], reason)
			case "clear":
				result, err = c.SetRepositoryBase(args[0], "", "")
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
