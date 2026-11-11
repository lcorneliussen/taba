package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"taba.sh/taba/internal/wtc"
)

type openOptions struct {
	All, List, DryRun, Focus, Full, Trim                        bool
	NoAgent, NoBrowse, NoStatus, NoFirstPrompt, NoRemoteControl bool
	Session, Agent, AgentArgs                                   string
	AgentArgsSet                                                bool
	Layout                                                      string
	LayoutSet                                                   bool
	Add                                                         []string
	Tools                                                       []string // tool panes this open adds when missing
}

type openItem struct {
	Collection string   `json:"collection"`
	Workspace  string   `json:"workspace,omitempty"`
	Layout     string   `json:"layout,omitempty"`
	Actions    []string `json:"actions"`
	Error      string   `json:"error,omitempty"`
}

func addOpenCommand(root *cobra.Command, asJSON *bool) {
	var opt openOptions
	cmd := &cobra.Command{Use: "open [collection ...]", Short: "Open or repair collection workspaces in herdr", Args: cobra.ArbitraryArgs}
	cmd.Flags().BoolVar(&opt.All, "all", false, "Open every collection in this workspace")
	cmd.Flags().BoolVar(&opt.List, "list", false, "Report workspace and pane state without changing it")
	cmd.Flags().BoolVar(&opt.DryRun, "dry-run", false, "Show needed changes without making them")
	cmd.Flags().StringVar(&opt.Session, "session", "", "Herdr session")
	cmd.Flags().StringVar(&opt.Agent, "agent", "", "Agent kind to start (default: claude)")
	cmd.Flags().StringVar(&opt.AgentArgs, "agent-args", "", "Arguments passed to the agent, replacing defaults")
	cmd.Flags().BoolVar(&opt.NoRemoteControl, "no-remote-control", false, "Do not enable Claude Remote Control")
	cmd.Flags().BoolVar(&opt.NoAgent, "no-agent", false, "Leave the agent pane at a shell prompt")
	cmd.Flags().BoolVar(&opt.NoFirstPrompt, "no-first-prompt", false, "Do not submit the first prompt for a fresh collection")
	cmd.Flags().BoolVar(&opt.NoBrowse, "no-browse", false, "Leave the browse pane at a shell prompt")
	cmd.Flags().BoolVar(&opt.NoStatus, "no-status", false, "Leave the status pane at a shell prompt")
	cmd.Flags().BoolVar(&opt.Focus, "focus", false, "Focus the last opened workspace")
	cmd.Flags().Bool("narrow", false, "Use two stacked tabs")
	cmd.Flags().Bool("wide", false, "Use two stacked columns")
	cmd.Flags().BoolVar(&opt.Full, "full", false, "Also open the browse and shell panes (default: WTC_PANES)")
	cmd.Flags().StringSliceVar(&opt.Add, "add", nil, "Add a tool pane to the workspace: browse, shell or review (repeatable)")
	cmd.Flags().BoolVar(&opt.Trim, "trim", false, "Close idle browse, shell and review panes; never agent or status")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		if opt.All && len(args) != 0 {
			return errors.New("--all cannot be combined with collection names")
		}
		for _, kind := range opt.Add {
			switch kind {
			case "browse", "shell", "review":
			default:
				return fmt.Errorf("--add %q: expected browse, shell or review", kind)
			}
		}
		if opt.Trim {
			for _, name := range []string{"add", "full", "list", "narrow", "wide", "focus"} {
				if cmd.Flags().Changed(name) {
					return fmt.Errorf("--trim cannot be combined with --%s", name)
				}
			}
		}
		narrow, _ := cmd.Flags().GetBool("narrow")
		wide, _ := cmd.Flags().GetBool("wide")
		if narrow && wide {
			return errors.New("choose --narrow or --wide")
		}
		if narrow {
			opt.Layout, opt.LayoutSet = "narrow", true
		}
		if wide {
			opt.Layout, opt.LayoutSet = "wide", true
		}
		opt.AgentArgsSet = cmd.Flags().Changed("agent-args")
		opt.Tools = openWantedTools(opt.Full, opt.Add)
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		c, err := wtc.Discover(cwd)
		if err != nil {
			return err
		}
		if !cmd.Flags().Changed("full") && statusSetting(c, "WTC_PANES") == "full" {
			opt.Tools = openWantedTools(true, opt.Add)
		}
		if _, err := exec.LookPath("herdr"); err != nil {
			return errors.New("herdr is not installed")
		}
		names, err := openSelectedCollections(c, opt.All, args)
		if err != nil {
			return err
		}
		opt.Session = openSession(c, opt.Session)
		running := openSessionRunning(opt.Session)
		budget, low := openPtyReport()
		if low && !*asJSON {
			fmt.Fprintln(os.Stderr, "warning: "+budget)
		}
		if opt.Trim {
			return openTrimCommand(c, names, opt, running, asJSON)
		}
		if !running && !opt.List && !opt.DryRun {
			if err := openEnsureSession(opt.Session); err != nil {
				return err
			}
			running = true
		}
		layout := openDesiredLayout(c, opt, running)
		items := make([]openItem, 0, len(names))
		failures := 0
		for i, name := range names {
			item := openCollection(c, name, opt, layout, running, i == len(names)-1)
			if item.Error != "" {
				failures++
				if strings.Contains(item.Error, "openpty") {
					item.Error += "; " + openPtyHint()
				}
			}
			items = append(items, item)
		}
		if *asJSON {
			summary := fmt.Sprintf("%d collection(s), %d failed", len(items), failures)
			if low {
				summary += "; " + budget
			}
			if err := emit(envelope{OK: failures == 0, Data: items, Summary: summary}, true); err != nil {
				return err
			}
		} else {
			if opt.List && budget != "" && !low {
				fmt.Println("pty budget: " + budget)
			}
			if opt.List && !running {
				fmt.Printf("herdr session %q: not running\n", opt.Session)
			} else {
				for _, item := range items {
					fmt.Printf("==> %s: %s — %s\n", item.Collection, item.Workspace, strings.Join(item.Actions, ", "))
					if item.Error != "" {
						fmt.Fprintln(os.Stderr, "error:", item.Error)
					}
				}
			}
			fmt.Printf("attach: herdr --session %s\n", opt.Session)
		}
		if failures != 0 {
			return fmt.Errorf("%d collection(s) could not be opened", failures)
		}
		return nil
	}
	root.AddCommand(cmd)
}

// openWantedTools lists the tool panes an open adds: none by default, so a
// workspace holds only agent and status until a pane is asked for.
func openWantedTools(full bool, add []string) []string {
	var tools []string
	for _, kind := range []string{"browse", "shell", "review"} {
		wanted := full && kind != "review"
		for _, requested := range add {
			wanted = wanted || requested == kind
		}
		if wanted {
			tools = append(tools, kind)
		}
	}
	return tools
}

func openSelectedCollections(c *wtc.Context, all bool, args []string) ([]string, error) {
	if !all && len(args) == 0 {
		return []string{filepath.Base(c.Collection)}, nil
	}
	if all || len(args) != 1 || args[0] != filepath.Base(c.Collection) {
		if err := c.RequireWorkspace("open of other collections"); err != nil {
			return nil, err
		}
	}
	if !all {
		for _, name := range args {
			if name == "" || name == "." || name == ".." || filepath.Base(name) != name {
				return nil, fmt.Errorf("invalid collection name %q", name)
			}
		}
		return args, nil
	}
	entries, err := os.ReadDir(c.Workspace)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, entry := range entries {
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		if _, err := os.Stat(filepath.Join(c.Workspace, entry.Name(), "harness")); err == nil && !wtc.RetiredFolder(filepath.Join(c.Workspace, entry.Name())) {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		return nil, fmt.Errorf("no collections found under %s", c.Workspace)
	}
	return names, nil
}

func openSession(c *wtc.Context, requested string) string {
	if requested != "" {
		return requested
	}
	if configured := statusSetting(c, "HARNESS_HERDR_SESSION"); configured != "" {
		return configured
	}
	if c.Config.Herdr.Session != "" {
		return c.Config.Herdr.Session
	}
	return strings.TrimSuffix(strings.TrimSuffix(filepath.Base(c.Workspace), "-harness"), "-wtc")
}

func openDesiredLayout(c *wtc.Context, opt openOptions, running bool) string {
	if opt.LayoutSet {
		return opt.Layout
	}
	if preference := statusSetting(c, "WTC_LAYOUT"); preference == "wide" || preference == "narrow" {
		return preference
	}
	if running {
		if width := openSessionWidth(opt.Session); width > 0 {
			at, err := strconv.Atoi(statusSetting(c, "WTC_LAYOUT_NARROW_AT"))
			if err != nil || at <= 0 {
				at = 140
			}
			if width < at {
				return "narrow"
			}
		}
	}
	return "wide"
}
