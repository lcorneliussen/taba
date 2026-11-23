package main

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"taba.sh/taba/internal/prompts"
	"taba.sh/taba/internal/wtc"
)

// Outside a collection only the embedded defaults resolve.
func promptsCommand(asJSON *bool, cwd func() (*wtc.Context, error)) *cobra.Command {
	promptsContext := func() prompts.Context {
		if c, err := cwd(); err == nil {
			return c.Prompts()
		}
		return prompts.Context{Defaults: wtc.PromptDefaults(), Audience: prompts.Private}
	}
	group := &cobra.Command{Use: "prompts", Short: "Inspect the agent prompts Taba sends and where each one resolves from"}
	group.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List every prompt ID with its effective source",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			list, err := prompts.List(promptsContext())
			if err != nil {
				return err
			}
			if *asJSON {
				return emit(envelope{OK: true, Data: list, Summary: fmt.Sprintf("%d prompts", len(list))}, true)
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tSOURCE\tVERSION\tAUDIENCE")
			for _, p := range list {
				fmt.Fprintf(w, "%s\t%s\t%d\t%s\n", p.ID, p.Source, p.EmbeddedVersion, p.Audience)
			}
			return w.Flush()
		},
	})
	group.AddCommand(&cobra.Command{
		Use:   "show <id>",
		Short: "Print the effective template for a prompt ID",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := prompts.Resolve(promptsContext(), args[0])
			if err != nil {
				return err
			}
			if *asJSON {
				return emit(envelope{OK: true, Data: res}, true)
			}
			fmt.Print(res.Body)
			if !strings.HasSuffix(res.Body, "\n") {
				fmt.Println()
			}
			return nil
		},
	})
	group.AddCommand(&cobra.Command{
		Use:   "explain <id>",
		Short: "Show which file a prompt resolves from, its inputs and recorded base",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := prompts.Resolve(promptsContext(), args[0])
			if err != nil {
				return err
			}
			p := res.Provenance
			if *asJSON {
				return emit(envelope{OK: true, Data: p}, true)
			}
			fmt.Printf("id:        %s\n", p.ID)
			fmt.Printf("source:    %s\n", p.Source)
			fmt.Printf("file:      %s\n", p.File)
			fmt.Printf("audience:  %s\n", p.Audience)
			fmt.Printf("inputs:    %s\n", strings.Join(p.Inputs, ", "))
			fmt.Printf("embedded:  version %d\n", p.EmbeddedVersion)
			switch {
			case p.BaseFile != "" && p.BaseCurrent:
				fmt.Printf("base:      version %d, %s (matches the embedded default)\n", p.BaseVersion, p.BaseFile)
			case p.BaseFile != "":
				fmt.Printf("base:      version %d, %s (embedded default has changed since eject)\n", p.BaseVersion, p.BaseFile)
			case p.Source == prompts.SourceProject:
				fmt.Printf("base:      none recorded at harness/prompts/.base/\n")
			}
			for _, skip := range p.Skipped {
				fmt.Printf("skipped:   %s (%s)\n", skip.File, skip.Reason)
			}
			return nil
		},
	})
	return group
}
