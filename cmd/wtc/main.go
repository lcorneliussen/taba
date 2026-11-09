package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/Masterminds/semver/v3"
	"github.com/lcorneliussen/wtc-cli/internal/branding"
	"github.com/lcorneliussen/wtc-cli/internal/openclaw"
	"github.com/lcorneliussen/wtc-cli/internal/wtc"
	"github.com/spf13/cobra"
)

var version = "dev"

type envelope struct {
	OK      bool   `json:"ok"`
	Data    any    `json:"data,omitempty"`
	Summary string `json:"summary,omitempty"`
	Meta    any    `json:"meta,omitempty"`
}

func emit(v envelope, asJSON bool) error {
	if asJSON {
		return json.NewEncoder(os.Stdout).Encode(v)
	}
	if v.Summary != "" {
		fmt.Fprintln(os.Stdout, v.Summary)
	}
	return nil
}
func main() {
	handOverToDevBuild()
	printRenameNotice(os.Stderr)
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, branding.Text("{{command}}:"), err)
		os.Exit(1)
	}
}
func run() error {
	var asJSON bool
	root := &cobra.Command{Use: branding.Name(), Short: "Worktree collection tools", SilenceUsage: true, SilenceErrors: true}
	root.CompletionOptions.DisableDefaultCmd = true
	root.PersistentFlags().BoolVar(&asJSON, "json", false, "Emit a JSON result")
	root.Version = versionLabel()
	wtc.CLIVersion = version
	cwd := func() (*wtc.Context, error) {
		dir, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		return wtc.Discover(dir)
	}
	envCmd := &cobra.Command{Use: "env", Short: "Inspect or set up collection environment", Args: cobra.NoArgs}
	var collection string
	var dryRun bool
	var envAll bool
	var envSkipHooks bool
	var envRunHooks bool
	setupFlags := func(cmd *cobra.Command) {
		cmd.Flags().StringVar(&collection, "collection", "", "Collection directory (default: current)")
		cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show generated environment without writing")
		cmd.Flags().BoolVar(&envAll, "all", false, "Refresh every collection in the workspace")
		cmd.Flags().BoolVar(&envSkipHooks, "skip-hooks", false, "Do not run environment lifecycle hooks or trust mise")
		cmd.Flags().BoolVar(&envRunHooks, "run-hooks", false, "Run each target's hooks and trust mise during --all (requires trusted collections)")
	}
	setupFlags(envCmd) // Existing flagged invocations remain compatible.
	setup := &cobra.Command{Use: "setup", Short: "Generate this collection's environment and mise configuration", Args: cobra.NoArgs}
	setupFlags(setup)
	runEnvSetup := func(cmd *cobra.Command, args []string) error {
		if envRunHooks && (!envAll || envSkipHooks) {
			return fmt.Errorf("--run-hooks requires --all and cannot be combined with --skip-hooks")
		}
		invokeHooks := !envSkipHooks && (!envAll || envRunHooks)
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
		if envAll {
			if err := c.RequireWorkspace("env setup --all"); err != nil {
				return err
			}
		}
		if !envAll {
			result, err := refreshEnvCollection(c, dryRun, invokeHooks)
			if err != nil {
				return err
			}
			if dryRun && !asJSON {
				printEnvPreview(result)
				return nil
			}
			return emit(envelope{OK: true, Data: result, Summary: envSummary(result)}, asJSON)
		}
		collections, err := wtc.WorkspaceCollections(c.Workspace)
		if err != nil {
			return err
		}
		results := make([]envSweepResult, 0, len(collections))
		failures := 0
		reservedPorts := map[int]bool{}
		for _, dir := range collections {
			item := envSweepResult{Collection: dir}
			target, openErr := wtc.OpenCollection(dir)
			if openErr != nil {
				item.Error = openErr.Error()
			} else {
				previewBase := 0
				if dryRun {
					previewBase, openErr = target.PreviewPortBase(reservedPorts)
				}
				if openErr == nil {
					item.envResult, openErr = refreshEnvCollection(target, dryRun, invokeHooks)
					if openErr == nil && dryRun {
						reservedPorts[previewBase] = true
					}
				}
				item.Collection = dir // A failed render returns a zero-value result.
				if openErr != nil {
					item.Error = openErr.Error()
				}
			}
			if item.Error != "" {
				failures++
			}
			results = append(results, item)
			if !asJSON {
				fmt.Printf("=== %s\n", filepath.Base(dir))
				if item.Error != "" {
					fmt.Fprintln(os.Stderr, "error:", item.Error)
				} else if dryRun {
					printEnvPreview(item.envResult)
				} else {
					fmt.Println(envSummary(item.envResult))
				}
			}
		}
		summary := fmt.Sprintf("swept %d collection(s), %d failed", len(results), failures)
		if asJSON {
			if err := emit(envelope{OK: failures == 0, Data: map[string]any{"results": results, "dry_run": dryRun, "failed": failures, "hooks_run": invokeHooks && !dryRun}, Summary: summary}, true); err != nil {
				return err
			}
		} else {
			fmt.Println(summary)
			if !invokeHooks {
				fmt.Println("note: target lifecycle hooks were not run and mise was not trusted")
			}
		}
		if failures > 0 {
			return fmt.Errorf("%d collection(s) failed environment refresh", failures)
		}
		return nil
	}
	envCmd.RunE = func(cmd *cobra.Command, args []string) error {
		if !cmd.Flags().Changed("collection") && !cmd.Flags().Changed("dry-run") &&
			!cmd.Flags().Changed("all") && !cmd.Flags().Changed("skip-hooks") &&
			!cmd.Flags().Changed("run-hooks") {
			return cmd.Help()
		}
		return runEnvSetup(cmd, args)
	}
	setup.RunE = runEnvSetup
	envCmd.AddCommand(setup)
	addEnvListCommand(envCmd, &asJSON, cwd)
	root.AddCommand(envCmd)
	commands := &cobra.Command{Use: "commands", Short: "List commands and machine-readable metadata", Args: cobra.NoArgs}
	commands.RunE = func(cmd *cobra.Command, args []string) error {
		items := []map[string]string{}
		for _, c := range root.Commands() {
			if c.Hidden || c.Name() == "help" {
				continue
			}
			items = append(items, map[string]string{"name": c.Name(), "description": c.Short})
		}
		if asJSON {
			return emit(envelope{OK: true, Data: items, Summary: fmt.Sprintf("%d commands", len(items))}, true)
		}
		for _, item := range items {
			fmt.Printf("%-12s %s\n", item["name"], item["description"])
		}
		return nil
	}
	root.AddCommand(commands)
	doctor := &cobra.Command{Use: "doctor", Short: "Check this collection's configuration and dependencies, or under OpenClaw the gateway's", Args: cobra.NoArgs}
	var doctorOpenClaw bool
	doctor.Flags().BoolVar(&doctorOpenClaw, "openclaw", false, "Check the OpenClaw gateway on this host, also from outside an agent's command")
	doctor.RunE = func(cmd *cobra.Command, args []string) error {
		if gw := openclaw.GatewayFromEnv(os.Getenv); gw != nil {
			env := realOpenClawEnv()
			return printDoctor(sandboxChecks(gw, os.Getenv, env.run), "OpenClaw sandbox diagnostics", asJSON)
		}
		if doctorOpenClaw || currentHost() == openclaw.HostOpenClaw {
			return printDoctor(openClawChecks(realOpenClawEnv()), "OpenClaw gateway diagnostics", asJSON)
		}
		c, err := cwd()
		if err != nil {
			return err
		}
		checks := []map[string]any{{"name": "registry", "ok": len(c.Registry.Repos) > 0, "detail": fmt.Sprintf("%d repositories", len(c.Registry.Repos))}}
		for _, name := range []string{"git", "mise", "herdr"} {
			path, err := exec.LookPath(name)
			checks = append(checks, map[string]any{"name": name, "ok": err == nil, "detail": path})
		}
		if budget, low := openPtyReport(); budget != "" {
			checks = append(checks, map[string]any{"name": "pty", "ok": !low, "detail": budget})
		}
		if c.Config.Compatibility.Requires != "" {
			checks = append(checks, map[string]any{"name": "compatibility", "ok": compatible(c.Config.Compatibility.Requires, version), "detail": fmt.Sprintf("requires %s; running %s", c.Config.Compatibility.Requires, version)})
		}
		okay := true
		for _, check := range checks {
			if check["name"] == "git" || check["name"] == "registry" || check["name"] == "compatibility" {
				if !check["ok"].(bool) {
					okay = false
				}
			}
		}
		if asJSON {
			if err := emit(envelope{OK: okay, Data: checks, Summary: "collection diagnostics"}, true); err != nil {
				return err
			}
			if !okay {
				return fmt.Errorf("required checks failed")
			}
			return nil
		}
		for _, check := range checks {
			state := "ok"
			if !check["ok"].(bool) {
				state = "missing"
				if check["name"] == "pty" {
					state = "low"
				}
			}
			fmt.Printf("%-15s %-8s %v\n", check["name"], state, check["detail"])
		}
		if !okay {
			return fmt.Errorf("required checks failed")
		}
		return nil
	}
	root.AddCommand(doctor)
	eject := &cobra.Command{Use: "eject <glob>...", Short: "Copy embedded defaults into the harness for editing", Args: cobra.MinimumNArgs(1)}
	eject.RunE = func(cmd *cobra.Command, args []string) error {
		c, err := cwd()
		if err != nil {
			return err
		}
		paths, err := wtc.Eject(c.Harness, args)
		if err != nil {
			return err
		}
		if asJSON {
			return emit(envelope{OK: true, Data: paths, Summary: fmt.Sprintf("ejected %d files", len(paths))}, true)
		}
		fmt.Println(strings.Join(paths, "\n"))
		return nil
	}
	root.AddCommand(eject)
	customize := &cobra.Command{Use: "customize", Short: "Show harness and repository customization points", Args: cobra.NoArgs}
	customize.RunE = func(cmd *cobra.Command, args []string) error {
		guide, err := wtc.ReadDefault("instructions/customize.md")
		if err != nil {
			return err
		}
		if asJSON {
			return emit(envelope{OK: true, Data: map[string]string{"guide": string(guide)}, Summary: "customization guide"}, true)
		}
		_, err = os.Stdout.Write(guide)
		return err
	}
	root.AddCommand(customize)
	addPRCommands(root, &asJSON)
	addHostCommands(root, &asJSON)
	addOpenClawCommands(root)
	addRegistryCommands(root, &asJSON)
	addMCPCommands(root, &asJSON)
	addSecretsCommands(root, &asJSON)
	addAgentEnvCommand(root, &asJSON)
	addSkillsCommands(root, &asJSON)
	addReviewCommands(root, &asJSON)
	addInitCommand(root, &asJSON)
	addNewCommand(root, &asJSON)
	addAddRepoCommand(root, &asJSON)
	addRetireCommand(root, &asJSON)
	addCatchUpCommand(root, &asJSON)
	addStatusCommand(root, &asJSON)
	addWorkingHoursCommand(root, &asJSON, cwd)
	addOpenCommand(root, &asJSON)
	addBrowseCommand(root, &asJSON)
	addRuntimeCommands(root, &asJSON)
	addHarnessCommands(root, &asJSON)
	addReleaseNotesCommand(root, &asJSON)
	addUpgradeCommand(root, &asJSON)
	addDevCommands(root, &asJSON)
	args, err := normalizeStatusWatchArgs(os.Args[1:])
	if err != nil {
		return err
	}
	root.SetArgs(args)
	return root.Execute()
}

func compatible(requirement, actual string) bool {
	constraint, err := semver.NewConstraint(requirement)
	if err != nil {
		return false
	}
	// Dev builds past a release are prereleases of the next patch
	// (0.1.40-dev.3); they sit inside the same ranges as the releases around them.
	constraint.IncludePrerelease = true
	v, err := semver.NewVersion(actual)
	if err != nil {
		return false
	}
	return constraint.Check(v)
}
