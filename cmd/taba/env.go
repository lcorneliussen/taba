package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"text/tabwriter"

	"github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"
	"taba.sh/taba/internal/wtc"
)

type envResult struct {
	Collection string `json:"collection"`
	Changed    bool   `json:"changed"`
	DryRun     bool   `json:"dry_run"`
	Env        string `json:"env"`
	Mise       string `json:"mise"`
}

type envSweepResult struct {
	envResult
	Error string `json:"error,omitempty"`
}

func refreshEnvCollection(c *wtc.Context, dryRun, invokeHooks bool) (envResult, error) {
	data, err := c.RenderEnv()
	if err != nil {
		return envResult{}, err
	}
	mise, err := c.RenderMise()
	if err != nil {
		return envResult{}, err
	}
	old, _ := os.ReadFile(filepath.Join(c.Collection, ".env.collection"))
	oldMise, _ := os.ReadFile(filepath.Join(c.Collection, "mise.toml"))
	changed := !bytes.Equal(old, data) || !bytes.Equal(oldMise, mise)
	if !dryRun {
		if err := c.ValidateEnvSupport(); err != nil {
			return envResult{}, err
		}
		if invokeHooks {
			if err := c.RunHook("env.pre", nil); err != nil {
				return envResult{}, err
			}
		}
		if changed {
			if err := c.WriteEnv(data); err != nil {
				return envResult{}, err
			}
		}
		if err := c.EnsureEnvSupport(); err != nil {
			return envResult{}, err
		}
		if invokeHooks {
			if err := c.TrustMise(); err != nil {
				return envResult{}, err
			}
		}
		if invokeHooks {
			if err := c.RunHook("env.post", nil); err != nil {
				return envResult{}, err
			}
		}
	}
	return envResult{Collection: c.Collection, Changed: changed, DryRun: dryRun, Env: string(data), Mise: string(mise)}, nil
}

func envSummary(result envResult) string {
	state := "already current"
	if result.Changed {
		if result.DryRun {
			state = "would update"
		} else {
			state = "updated"
		}
	}
	return filepath.Base(result.Collection) + ": " + state
}

func printEnvPreview(result envResult) {
	fmt.Print(result.Env)
	fmt.Print("\n# mise.toml\n")
	fmt.Print(result.Mise)
}

func addEnvListCommand(envCmd *cobra.Command, asJSON *bool, cwd func() (*wtc.Context, error)) {
	var collection string
	var tui, noTUI bool
	list := &cobra.Command{Use: "list", Short: "List environment variable names, scopes, and overrides without values", Args: cobra.NoArgs}
	list.Flags().StringVar(&collection, "collection", "", "Collection directory (default: current)")
	list.Flags().BoolVar(&tui, "tui", false, "Open the interactive inventory view")
	list.Flags().BoolVar(&noTUI, "no-tui", false, "Print one table and exit")
	list.RunE = func(cmd *cobra.Command, args []string) error {
		if tui && noTUI {
			return fmt.Errorf("--tui and --no-tui cannot be combined")
		}
		if tui && *asJSON {
			return fmt.Errorf("--tui and --json cannot be combined")
		}
		var c *wtc.Context
		var err error
		if collection == "" {
			c, err = cwd()
		} else {
			c, err = wtc.OpenCollection(collection)
		}
		if err != nil {
			return err
		}
		result, err := c.ListEnv()
		if err != nil {
			return err
		}
		if *asJSON {
			return emit(envelope{OK: true, Data: result, Summary: fmt.Sprintf("%d variable name(s)", len(result.Variables))}, true)
		}
		if tui || !cmd.Flags().Changed("tui") && !noTUI &&
			term.IsTerminal(os.Stdin.Fd()) && term.IsTerminal(os.Stdout.Fd()) {
			return envInventoryTUI(c, result)
		}
		out := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(out, "SOURCE\tSCOPE\tFILE")
		for _, file := range result.Files {
			state := "present"
			if !file.Exists {
				state = "absent"
			}
			fmt.Fprintf(out, "%s\t%s\t%s\n", file.Path, shortInventoryScope(file.Scope), state)
		}
		fmt.Fprintln(out)
		fmt.Fprintln(out, "KEY\tSOURCE\tSCOPE\tOVERRIDES")
		for _, variable := range result.Variables {
			override := ""
			if variable.Overrides {
				override = "yes"
			}
			fmt.Fprintf(out, "%s\t%s\t%s\t%s\n", variable.Name, variable.Source, shortInventoryScope(variable.Scope), override)
		}
		return out.Flush()
	}
	envCmd.AddCommand(list)
}
