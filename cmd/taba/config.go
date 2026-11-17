package main

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"taba.sh/taba/internal/wtc"
)

type requirementsFlags struct {
	examples []string
	process  bool
}

func addRequirementsFlags(cmd *cobra.Command, flags *requirementsFlags) {
	cmd.Flags().StringArrayVar(&flags.examples, "example", nil, "Select a canonical example as repo:path (repeatable; default: tracked opted-in root dotenv examples)")
	cmd.Flags().BoolVar(&flags.process, "process-environment", false, "Inspect only explicitly declared owned process inputs (values are never printed)")
}
func addConfigCommands(root *cobra.Command, asJSON *bool) {
	config := &cobra.Command{Use: "config", Short: "Inspect example-backed configuration requirements"}
	for _, lint := range []bool{false, true} {
		name, short := "list", "List declared requirements and presence without values"
		if lint {
			name, short = "lint", "Validate schemas and provided owned keys without requiring credentials"
		}
		cmd := &cobra.Command{Use: name, Short: short, Args: cobra.NoArgs}
		var flags requirementsFlags
		var collection, repo string
		addRequirementsFlags(cmd, &flags)
		cmd.Flags().StringVar(&collection, "collection", "", "Collection directory (default: current)")
		cmd.Flags().StringVar(&repo, "repo", "", "Only inspect this present repository")
		cmd.RunE = func(cmd *cobra.Command, args []string) error {
			dir, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("cannot identify configuration directory")
			}
			var c *wtc.Context
			if collection != "" {
				c, err = wtc.OpenCollection(collection)
			} else {
				c, err = wtc.Discover(dir)
			}
			var result wtc.RequirementsInventory
			standalone := lint && collection == "" && repo == "" && err != nil
			// A plain example path explicitly selects standalone static lint; it must
			// be relative to the current repository root, never a receiving project.
			if lint && collection == "" && repo == "" && len(flags.examples) > 0 && !strings.Contains(flags.examples[0], ":") {
				standalone = true
			}
			if standalone {
				if flags.process {
					return fmt.Errorf("standalone config lint does not inspect process inputs")
				}
				result, err = wtc.LintRepositoryRequirements(dir, flags.examples)
			} else if err == nil {
				result, err = c.ListConfigRequirements(wtc.RequirementsOptions{Examples: flags.examples, Repo: repo, ProcessEnvironment: flags.process, Lint: lint})
			}
			if err != nil {
				return err
			}
			return printRequirementsInventory(result, *asJSON, lint)
		}
		config.AddCommand(cmd)
	}
	root.AddCommand(config)
}
func printRequirementsInventory(result wtc.RequirementsInventory, asJSON, lint bool) error {
	okay := result.LintOK
	summary := fmt.Sprintf("%d requirement(s), %d diagnostic(s); local readiness %s", len(result.Items), len(result.Diagnostics), result.LocalReadiness)
	if lint {
		summary = "schema and provided-owned-input validation; credentials are not required"
	}
	if asJSON {
		if err := emit(envelope{OK: okay, Data: result, Summary: summary}, true); err != nil {
			return err
		}
	} else {
		fmt.Println(summary)
		printRequirementsRows(result)
	}
	if !okay {
		return fmt.Errorf("configuration requirement validation failed")
	}
	return nil
}
func printRequirementsRows(result wtc.RequirementsInventory) {
	if len(result.Items) > 0 {
		out := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(out, "REPOSITORY/NAME\tTARGET/KIND\tREQUIREMENT/CONDITION\tSTATE/APPLICABILITY\tDECLARATION\tSOURCE/SCOPE\tPURPOSE; AFFECTS; SETUP; DOC")
		for _, item := range result.Items {
			requirement := item.Required
			if item.Condition != nil {
				requirement += " when " + item.Condition.Key + "=" + item.Condition.Equals
			}
			fmt.Fprintf(out, "%s/%s\t%s/%s\t%s\t%s/%s\t%s\t%s/%s (declared %s; %s)\t%s; %s; %s; %s\n", item.Repository, item.Name, item.Target, item.Kind, requirement, item.State, item.Applicability, item.Declaration, item.Source, item.Scope, item.DeclaredScope, item.Provenance, item.Purpose, item.Affects, item.Hint, item.Doc)
		}
		_ = out.Flush()
	}
	for _, d := range result.Diagnostics {
		fmt.Printf("%s/%s: %s (line %d)\n", d.Repository, d.Declaration, d.Code, d.Line)
	}
}
