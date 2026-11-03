package main

import (
	"os"

	"github.com/spf13/cobra"
	"taba.sh/taba/internal/branding"
	"taba.sh/taba/internal/wtc"
)

func addInitCommand(root *cobra.Command, asJSON *bool) {
	cmd := &cobra.Command{
		Use:   "init [directory]",
		Short: "Make an empty folder an ad-hoc collection, without a harness repository",
		Args:  cobra.MaximumNArgs(1),
	}
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		if err := refuseUnderGateway("init"); err != nil {
			return err
		}
		dir, err := os.Getwd()
		if err != nil {
			return err
		}
		if len(args) == 1 {
			dir = args[0]
		}
		result, err := wtc.InitAdHoc(dir)
		if err != nil {
			return err
		}
		return emit(envelope{OK: true, Data: result, Summary: "done: ad-hoc collection at " + result.Collection + branding.Text("\nnext: {{command}} add-repo <clone-url|path-to-checkout>")}, *asJSON)
	}
	root.AddCommand(cmd)
}
