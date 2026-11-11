package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"taba.sh/taba/internal/wtc"
)

func addMCPCommands(root *cobra.Command, asJSON *bool) {
	var collection string
	var dryRun bool
	var all bool
	var skipHooks bool
	var runHooks bool
	mcp := &cobra.Command{Use: "mcp", Short: "Render agent MCP configuration"}
	mcp.PersistentFlags().StringVar(&collection, "collection", "", "Collection directory (default: current)")
	render := &cobra.Command{Use: "render", Short: "Render the harness MCP registry into agent configs", Args: cobra.NoArgs}
	render.Flags().BoolVar(&dryRun, "dry-run", false, "Report changes without writing")
	render.Flags().BoolVar(&all, "all", false, "Render every collection in the workspace (omit per-collection credential diagnostics)")
	render.Flags().BoolVar(&skipHooks, "skip-hooks", false, "Do not run MCP lifecycle hooks")
	render.Flags().BoolVar(&runHooks, "run-hooks", false, "Run each target's hooks during --all (requires trusted collections)")
	render.RunE = func(cmd *cobra.Command, args []string) error {
		if runHooks && (!all || skipHooks) {
			return fmt.Errorf("--run-hooks requires --all and cannot be combined with --skip-hooks")
		}
		invokeHooks := !skipHooks && (!all || runHooks)
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
		if !all {
			result, err := renderMCPCollection(c, dryRun, invokeHooks)
			if err != nil {
				return err
			}
			if result.Absent {
				return emit(envelope{OK: true, Summary: "MCP registry absent; nothing to render"}, *asJSON)
			}
			summary := mcpSummary(result, dryRun)
			if *asJSON {
				return emit(envelope{OK: true, Data: map[string]any{"changed": result.Changed, "missing_env": result.Missing, "dry_run": dryRun}, Summary: summary}, true)
			}
			printMCPResult(result, dryRun)
			return nil
		}
		if err := c.RequireWorkspace("mcp render --all"); err != nil {
			return err
		}
		collections, err := wtc.WorkspaceCollections(c.Workspace)
		if err != nil {
			return err
		}
		results := make([]mcpSweepResult, 0, len(collections))
		failures := 0
		for _, dir := range collections {
			item := mcpSweepResult{Collection: dir}
			// Older worktrees without an MCP registry have nothing to render.
			if _, err := os.Stat(filepath.Join(dir, "harness", ".mcp-servers.yml")); os.IsNotExist(err) {
				item.Absent = true
			} else if err != nil {
				item.Error = err.Error()
			} else {
				target, openErr := wtc.OpenCollection(dir)
				if openErr != nil {
					item.Error = openErr.Error()
				} else {
					item.mcpResult, openErr = renderMCPCollection(target, dryRun, invokeHooks)
					if openErr != nil {
						item.Error = openErr.Error()
					} else {
						// The invoking shell is not each target's agent environment.
						// Its unset variables cannot diagnose target credentials.
						item.Missing = nil
					}
				}
			}
			if item.Error != "" {
				failures++
			}
			results = append(results, item)
			if !*asJSON {
				fmt.Printf("=== %s\n", filepath.Base(dir))
				if item.Error != "" {
					fmt.Fprintln(os.Stderr, "error:", item.Error)
				} else {
					printMCPResult(item.mcpResult, dryRun)
				}
				fmt.Println()
			}
		}
		summary := fmt.Sprintf("swept %d collection(s), %d failed", len(results), failures)
		if *asJSON {
			if err := emit(envelope{OK: failures == 0, Data: map[string]any{"results": results, "dry_run": dryRun, "failed": failures, "hooks_run": invokeHooks && !dryRun, "missing_env_diagnostics": false}, Summary: summary}, true); err != nil {
				return err
			}
		} else {
			fmt.Println(summary)
			if !invokeHooks {
				fmt.Println("note: target lifecycle hooks were not run")
			}
			fmt.Println("note: per-collection credential diagnostics are omitted from workspace sweeps")
		}
		if failures > 0 {
			return fmt.Errorf("%d collection(s) failed MCP rendering", failures)
		}
		return nil
	}
	mcp.AddCommand(render)
	root.AddCommand(mcp)
}

type mcpResult struct {
	Changed []string `json:"changed,omitempty"`
	Missing []string `json:"missing_env,omitempty"`
	Absent  bool     `json:"absent,omitempty"`
}

type mcpSweepResult struct {
	mcpResult
	Collection string `json:"collection"`
	Error      string `json:"error,omitempty"`
}

func renderMCPCollection(c *wtc.Context, dryRun, invokeHooks bool) (mcpResult, error) {
	if _, err := os.Stat(filepath.Join(c.Harness, ".mcp-servers.yml")); os.IsNotExist(err) {
		return mcpResult{Absent: true}, nil
	} else if err != nil {
		return mcpResult{}, err
	}
	outputs, err := c.RenderMCP()
	if err != nil {
		return mcpResult{}, err
	}
	if !dryRun && invokeHooks {
		if err := c.RunHook("mcp.render.pre", nil); err != nil {
			return mcpResult{}, err
		}
	}
	changed, err := c.WriteMCP(outputs, dryRun)
	if err != nil {
		return mcpResult{}, err
	}
	if !dryRun && invokeHooks {
		if err := c.RunHook("mcp.render.post", nil); err != nil {
			return mcpResult{}, err
		}
	}
	return mcpResult{Changed: changed, Missing: outputs.Missing}, nil
}

func mcpSummary(result mcpResult, dryRun bool) string {
	if dryRun {
		return fmt.Sprintf("%d MCP configs would change", len(result.Changed))
	}
	return fmt.Sprintf("%d MCP configs changed", len(result.Changed))
}

func printMCPResult(result mcpResult, dryRun bool) {
	if result.Absent {
		fmt.Println("MCP registry absent; nothing to render")
		return
	}
	fmt.Println(mcpSummary(result, dryRun))
	for _, rel := range result.Changed {
		fmt.Println("  " + rel)
	}
	if len(result.Missing) > 0 {
		fmt.Fprintln(os.Stderr, "note: unset in this shell: "+strings.Join(result.Missing, " "))
	}
}
