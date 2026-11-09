package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"taba.sh/taba/internal/wtc"
)

func addRegistryCommands(root *cobra.Command, asJSON *bool) {
	var collection string
	registry := &cobra.Command{Use: "registry", Short: "Inspect and refresh repository registry data"}
	registry.PersistentFlags().StringVar(&collection, "collection", "", "Collection directory (default: current)")
	refresh := &cobra.Command{Use: "refresh", Short: "Regenerate the local bare-owner map", Args: cobra.NoArgs}
	refresh.RunE = func(cmd *cobra.Command, args []string) error {
		var c *wtc.Context
		var err error
		if collection == "" {
			var cwd string
			cwd, err = os.Getwd()
			if err == nil {
				c, err = wtc.Discover(cwd)
			}
		} else {
			c, err = wtc.OpenCollection(collection)
		}
		if err != nil {
			return err
		}
		if err := c.RunHook("registry.refresh.pre", nil); err != nil {
			return err
		}
		report, err := c.RefreshRegistry()
		if err != nil {
			return err
		}
		if err := c.RunHook("registry.refresh.post", nil); err != nil {
			return err
		}
		summary := fmt.Sprintf("wrote %s (%d bare owners)", report.Path, len(report.BareOwners))
		if *asJSON {
			if err := emit(envelope{OK: len(report.Unlisted) == 0, Data: report, Summary: summary}, true); err != nil {
				return err
			}
		} else {
			fmt.Println(summary)
			for _, name := range report.Missing {
				fmt.Fprintf(os.Stderr, "note: %s is registered but has no bare owner\n", name)
			}
			for _, name := range report.Unlisted {
				fmt.Fprintf(os.Stderr, "warning: bare %s is not in the tracked registry\n", name)
			}
		}
		if len(report.Unlisted) > 0 {
			return fmt.Errorf("bare owners missing from tracked registry")
		}
		return nil
	}
	registry.AddCommand(refresh)
	root.AddCommand(registry)
}
