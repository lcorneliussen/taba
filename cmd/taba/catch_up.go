package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"taba.sh/taba/internal/openclaw"
	"taba.sh/taba/internal/wtc"
)

func addCatchUpCommand(root *cobra.Command, asJSON *bool) {
	var opt wtc.CatchUpOptions
	var repos, dest, from string
	var harnessOnly, cont bool
	cmd := &cobra.Command{Use: "catch-up [collection ...]", Short: "Catch up selected worktree collections"}
	cmd.Flags().BoolVar(&opt.All, "all", false, "Select every collection in the workspace (clean only)")
	cmd.Flags().StringVar(&repos, "repos", "", "Comma-separated sibling or registry names")
	cmd.Flags().BoolVar(&harnessOnly, "harness-only", false, "Select only each collection's harness")
	cmd.Flags().BoolVar(&opt.CleanOnly, "clean-only", false, "Leave dirty worktrees with their owner")
	cmd.Flags().BoolVar(&opt.DryRun, "dry-run", false, "Plan using local refs without fetching or writing")
	cmd.Flags().BoolVar(&opt.ReloadStatus, "reload-status", false, "Restart eligible status panes after the harness updates")
	cmd.Flags().BoolVar(&opt.IdleOnly, "idle-only", false, "Leave collections whose herdr agent is working or blocked with that agent")
	cmd.Flags().BoolVar(&opt.InstallTools, "install-tools", false, "Run mise install in each harness target (skipped when its environment refresh fails)")
	cmd.Flags().StringVar(&opt.Report, "report", "", "Also write a JSON report and a readable .md companion")
	cmd.Flags().BoolVar(&opt.NoSkills, "no-skills", false, "Skip skill refresh")
	cmd.Flags().BoolVar(&opt.NoMCP, "no-mcp", false, "Skip MCP refresh")
	cmd.Flags().BoolVar(&opt.NoEnv, "no-env", false, "Skip environment refresh")
	cmd.Flags().BoolVar(&opt.NoSecrets, "no-secrets", false, "Skip secret links")
	cmd.Flags().StringVar(&dest, "dest", "", "Under OpenClaw: branch to merge in (default: origin's default branch)")
	cmd.Flags().StringVar(&from, "from", "", "Under OpenClaw: merge this branch of the same repository instead (a sub-session's, by its name), with no fetch")
	cmd.Flags().BoolVar(&cont, "continue", false, "In an OpenClaw sandbox: finish a catch-up that stopped on conflicts, once they are resolved")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		if gw := openclaw.GatewayFromEnv(os.Getenv); gw != nil {
			// In a sandbox, catch-up merges the session's base, and only that.
			var other []string
			cmd.Flags().Visit(func(f *pflag.Flag) {
				if f.Name != "continue" && f.Name != "json" {
					other = append(other, "--"+f.Name)
				}
			})
			if len(other) > 0 || len(args) > 0 {
				return fmt.Errorf("in an OpenClaw sandbox, catch-up merges the session's base and takes only --continue (got %s): merge another branch of the repository, or another base, from the repository's session outside the sandbox", strings.Join(append(other, args...), " "))
			}
			return sandboxCatchUp(gw, cont, *asJSON)
		}
		if cont {
			return fmt.Errorf("--continue is for a catch-up in an OpenClaw sandbox (%s set)", openclaw.GatewayGitEnv)
		}
		if currentHost() == openclaw.HostOpenClaw {
			return catchUpCheckout(dest, from, *asJSON)
		}
		if harnessOnly && repos != "" {
			return fmt.Errorf("--harness-only and --repos cannot be combined")
		}
		if harnessOnly {
			repos = "harness"
		}
		if repos != "" {
			if strings.HasPrefix(repos, ",") || strings.HasSuffix(repos, ",") || strings.Contains(repos, ",,") {
				return fmt.Errorf("invalid repository selector")
			}
			opt.Repos = strings.Split(repos, ",")
		}
		opt.Collections = args
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		c, err := wtc.Discover(cwd)
		if err != nil {
			return err
		}
		report, runErr := c.CatchUp(opt)
		if *asJSON {
			if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
				return err
			}
		} else {
			fmt.Print(report.Markdown())
		}
		return runErr
	}
	root.AddCommand(cmd)
}
