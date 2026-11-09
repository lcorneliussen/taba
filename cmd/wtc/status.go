package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/x/term"
	"github.com/lcorneliussen/wtc-cli/internal/branding"
	"github.com/lcorneliussen/wtc-cli/internal/envcompat"
	"github.com/lcorneliussen/wtc-cli/internal/openclaw"
	"github.com/lcorneliussen/wtc-cli/internal/wtc"
	"github.com/spf13/cobra"
)

// Explicit previews do not write snapshots that a status pane could mistake
// for a completed one-shot collection.
func addStatusCommand(root *cobra.Command, asJSON *bool) {
	var local, forge, all, md, ansi, reposOnly, cached, noFetch, tui, procs, noClick, click, noWatch, silent bool
	var watchSeconds, fetchAgeSeconds int
	cmd := &cobra.Command{Use: "status [collection]", Short: "Inspect worktree collection status",
		Long: branding.Text("Show repository and PR status for this collection, a named collection, or --all. Bare {{command}} status opens the live view when stdin and stdout are terminals; --no-watch prints one pass. Explicit JSON, Markdown, ANSI, preview, and cached modes remain one-shot. Interactive one-shot runs log collector progress on stderr; --silent suppresses it. --cached reads snapshots without Git or forge calls. In the live view, click refreshing or press l for its log. Captured output prints one pass. Watch defaults can be set in WTC_CONFIG_ROOT/wtc.env with WTC_STATUS_WATCH, WTC_STATUS_WATCH_BG, and WTC_STATUS_NO_CLICK."),
		Args: cobra.MaximumNArgs(1)}
	cmd.Flags().BoolVar(&local, "local", false, "Preview local Git facts (no forge facts or cache writes)")
	cmd.Flags().BoolVar(&forge, "forge", false, "Preview local Git and PR facts (no snapshot writes)")
	cmd.Flags().BoolVar(&all, "all", false, "Include every collection in the workspace")
	cmd.Flags().BoolVar(&md, "md", false, "Render agent Markdown")
	cmd.Flags().BoolVar(&ansi, "ansi", false, "Render a terminal table")
	cmd.Flags().BoolVar(&reposOnly, "repos", false, "Show repository rows without the enlisted PR section")
	cmd.Flags().BoolVar(&cached, "cached", false, "Read the last completed snapshot without Git or forge calls")
	cmd.Flags().BoolVar(&noFetch, "no-fetch", false, "Use current local refs without fetching")
	cmd.Flags().BoolVar(&tui, "tui", false, "Run the interactive status view")
	cmd.Flags().BoolVar(&procs, "procs", false, "Show processes under the herdr session")
	cmd.Flags().BoolVar(&noClick, "no-click", false, "Disable PR and build URL mouse targets in the TUI")
	cmd.Flags().BoolVar(&click, "click", false, "Enable PR and build URL mouse targets in the TUI")
	cmd.Flags().BoolVar(&noWatch, "no-watch", false, "Print one status pass and exit")
	cmd.Flags().BoolVar(&silent, "silent", false, "Suppress progress messages during one-shot terminal output")
	cmd.Flags().IntVar(&fetchAgeSeconds, "fetch-age", 300, "Refresh remote refs older than this many seconds")
	cmd.Flags().IntVar(&watchSeconds, "watch", 0, "Run the interactive view with this refresh interval in seconds")
	var subs []string
	cmd.Flags().StringArrayVar(&subs, "sub", nil, "Under OpenClaw: also compare this worktree of a sub-session with the collection's checkout of its repository (repeatable)")
	cmd.Flags().Lookup("watch").NoOptDefVal = "30"
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		if currentHost() == openclaw.HostOpenClaw {
			return gatewayStatus(args, subs, *asJSON)
		}
		terminal := term.IsTerminal(os.Stdin.Fd()) && term.IsTerminal(os.Stdout.Fd())
		implicitTUI := terminal && !cmd.Flags().Changed("tui") && !cmd.Flags().Changed("watch") &&
			!noWatch && !local && !forge && !cached && !md && !ansi && !*asJSON && !silent
		interactive := !noWatch && (tui || watchSeconds > 0 || implicitTUI) && terminal
		if watchSeconds < 0 {
			return fmt.Errorf("--watch must be nonnegative")
		}
		if fetchAgeSeconds < 0 {
			return fmt.Errorf("--fetch-age must be nonnegative")
		}
		if click && noClick {
			return fmt.Errorf("--click and --no-click cannot be combined")
		}
		if all && len(args) != 0 {
			return fmt.Errorf("--all and a named collection cannot be combined")
		}
		if procs && (local || forge || cached || all || reposOnly) {
			return fmt.Errorf("--procs cannot be combined with preview, cached, --all, or --repos")
		}
		if cached {
			if local || forge || all || noFetch {
				return fmt.Errorf("--cached cannot be combined with --local, --forge, --all, or --no-fetch")
			}
		} else if local && forge {
			return fmt.Errorf("--local and --forge cannot be combined")
		}
		if forge && all {
			return fmt.Errorf("--forge is currently scoped to this collection")
		}
		if (md && *asJSON) || (ansi && (md || *asJSON)) {
			return fmt.Errorf("--md, --ansi, and --json are mutually exclusive")
		}
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		c, err := wtc.Discover(cwd)
		if err != nil {
			return err
		}
		if len(args) != 0 {
			if args[0] == "." || args[0] == ".." || filepath.Base(args[0]) != args[0] {
				return fmt.Errorf("expected a collection name")
			}
			if err := c.RequireWorkspace("status <collection>"); err != nil {
				return err
			}
			c, err = wtc.OpenCollection(filepath.Join(c.Workspace, args[0]))
			if err != nil {
				return err
			}
		}
		if (!interactive || (watchSeconds == 0 && statusIntervalSetting(c, "WTC_STATUS_WATCH", 30) == 0)) &&
			!silent && !cached && !procs && !*asJSON && !md &&
			term.IsTerminal(os.Stdout.Fd()) && term.IsTerminal(os.Stderr.Fd()) {
			started := time.Now()
			c.StatusProgress = func(message string) {
				fmt.Fprintf(os.Stderr, branding.Text("{{command}} status [%s] %s\n"), time.Since(started).Truncate(time.Second), message)
			}
		}
		if !procs && !cmd.Flags().Changed("repos") && statusTrue(statusSetting(c, "WTC_STATUS_REPOS")) {
			reposOnly = true
		}
		if interactive {
			seconds := statusIntervalSetting(c, "WTC_STATUS_WATCH", 30)
			if watchSeconds > 0 {
				seconds = watchSeconds
			}
			background := statusIntervalSetting(c, "WTC_STATUS_WATCH_BG", 300)
			if background == 0 {
				background = seconds
			}
			clickSetting := statusSetting(c, "WTC_STATUS_NO_CLICK")
			if clickSetting == "" {
				clickSetting = statusSetting(c, "HARNESS_STATUS_NO_CLICK")
			}
			if !cmd.Flags().Changed("no-click") && statusTrue(clickSetting) {
				noClick = true
			}
			if click {
				noClick = false
			}
			if seconds > 0 {
				if local || forge || cached || md || ansi || *asJSON {
					return fmt.Errorf("--tui/--watch cannot be combined with preview, cached, Markdown, or JSON output")
				}
				return runStatusTUI(c, all, procs, reposOnly, noFetch, noClick, time.Duration(seconds)*time.Second, time.Duration(background)*time.Second, time.Duration(fetchAgeSeconds)*time.Second)
			}
		}
		if procs {
			processes, err := c.StatusProcesses()
			if err != nil {
				return err
			}
			if *asJSON {
				return json.NewEncoder(os.Stdout).Encode(processes)
			}
			fmt.Print(wtc.StatusProcessesText(processes))
			return nil
		}
		if cached {
			snapshot, err := c.ReadStatusSnapshot()
			if err == nil {
				if *asJSON {
					return json.NewEncoder(os.Stdout).Encode(snapshot)
				}
				age, err := c.CachedStatusAge()
				if err != nil {
					return err
				}
				if ansi || (!md && term.IsTerminal(os.Stdout.Fd())) {
					fmt.Printf("snapshot %ds old\n%s", age, statusTable(snapshot, reposOnly, statusTerminalWidth()))
					return nil
				}
				body := snapshot.Markdown()
				if reposOnly {
					body = snapshot.ReposMarkdown()
				}
				fmt.Printf("# %s (snapshot, %ds old)\n%s", snapshot.Collection, age, strings.TrimPrefix(body, "# "+snapshot.Collection+"\n"))
				return nil
			}
			if !os.IsNotExist(err) || *asJSON {
				return err
			}
			text, err := c.LegacyStatusText()
			if err != nil {
				return err
			}
			fmt.Print(text)
			return nil
		}
		var snapshot wtc.StatusSnapshot
		var fetched wtc.StatusFetchReport
		if forge {
			snapshot, fetched, err = c.StatusLivePreviewWithFetchAge(noFetch, time.Duration(fetchAgeSeconds)*time.Second)
		} else if local {
			snapshot, err = c.StatusLocalSnapshot(all)
		} else {
			snapshot, fetched, err = c.StatusLiveSnapshotWithFetchAge(all, noFetch, time.Duration(fetchAgeSeconds)*time.Second)
		}
		if fetched.Failed > 0 {
			fmt.Fprintf(os.Stderr, branding.Text("{{command}}: warning: %d status ref refresh(es) failed; showing local refs\n"), fetched.Failed)
		}
		if err != nil {
			return err
		}
		if *asJSON {
			return json.NewEncoder(os.Stdout).Encode(snapshot)
		}
		if ansi || (!md && term.IsTerminal(os.Stdout.Fd())) {
			if f, hoursErr := localHoursFacts(c, time.Now()); hoursErr == nil {
				fmt.Println(f.Summary())
			}
			fmt.Print(statusTable(snapshot, reposOnly, statusTerminalWidth()))
			return nil
		}
		if reposOnly {
			fmt.Print(snapshot.ReposMarkdown())
		} else {
			fmt.Print(snapshot.Markdown())
		}
		return nil
	}
	root.AddCommand(cmd)
}

func statusTerminalWidth() int {
	width, _, err := term.GetSize(os.Stdout.Fd())
	if err != nil || width <= 0 {
		return 80
	}
	return width
}

// Cobra's optional-value flags do not consume a following token. Preserve the
// established `--watch 30` spelling while also accepting bare `--watch`.
func normalizeStatusWatchArgs(args []string) ([]string, error) {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		if args[i] == "--watch" && i+1 < len(args) && len(args[i+1]) > 0 && args[i+1][0] >= '0' && args[i+1][0] <= '9' {
			if !statusDigits(args[i+1]) {
				return nil, fmt.Errorf("--watch wants a whole number of seconds")
			}
			out = append(out, "--watch="+args[i+1])
			i++
		} else if strings.HasPrefix(args[i], "--watch=") && !statusDigits(strings.TrimPrefix(args[i], "--watch=")) {
			return nil, fmt.Errorf("--watch wants a whole number of seconds")
		} else {
			out = append(out, args[i])
		}
	}
	return out, nil
}

func statusDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func statusSetting(c *wtc.Context, name string) string {
	if value, ok := envcompat.LookupEnv(name); ok && value != "" {
		return value
	}
	path, err := wtc.MachineEnvFile(c.ConfigRoot)
	if err != nil {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	value, _ := envcompat.LookupMap(name, envcompat.Assignments(data))
	return value
}

func statusTrue(value string) bool {
	switch strings.ToLower(value) {
	case "yes", "true", "1":
		return true
	}
	return false
}

func statusIntervalSetting(c *wtc.Context, name string, fallback int) int {
	raw := statusSetting(c, name)
	if raw == "" && name == "WTC_STATUS_WATCH" {
		raw = statusSetting(c, "HARNESS_STATUS_WATCH")
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		return fallback
	}
	return value
}
