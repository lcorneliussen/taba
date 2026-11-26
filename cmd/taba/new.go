package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"taba.sh/taba/internal/wtc"
)

func addNewCommand(root *cobra.Command, asJSON *bool) {
	var issue, tracker, pr, branch, baseReason string
	var repoBases map[string]string
	var open, noOpen, tip, review bool
	cmd := &cobra.Command{Use: "new [slug] [repo ...]", Short: "Create a worktree collection", Args: cobra.ArbitraryArgs}
	cmd.Flags().StringVar(&issue, "issue", "", "Issue ID whose repository becomes the primary sibling")
	cmd.Flags().StringVar(&tracker, "tracker", "", "Tracker key for the launch note")
	cmd.Flags().StringVar(&pr, "pr", "", "Pull request in <repo>#<number> form")
	cmd.Flags().StringVarP(&branch, "branch", "b", "", "Explicitly check out this branch in each sibling")
	cmd.Flags().StringToStringVar(&repoBases, "repo-base", nil, "Receiving repository base: repo=origin/branch (repeatable)")
	cmd.Flags().StringVar(&baseReason, "base-reason", "", "Human reason for explicitly selected repository bases")
	cmd.Flags().BoolVar(&review, "review", false, "With --pr: review someone else's PR without writing to its branch")
	cmd.Flags().BoolVar(&tip, "tip", false, "Compatibility flag; detached tip is the default")
	cmd.Flags().BoolVar(&open, "open", false, "Open the new collection in herdr or Rex")
	cmd.Flags().BoolVar(&noOpen, "no-open", false, "Leave the collection unopened")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		if err := refuseUnderGateway("new"); err != nil {
			return err
		}
		if open && noOpen {
			return fmt.Errorf("--open and --no-open cannot be combined")
		}
		if pr == "" && len(args) == 0 {
			return fmt.Errorf("a collection slug is required")
		}
		if tip {
			fmt.Fprintln(os.Stderr, "note: --tip is already the default")
		}
		opt := wtc.NewOptions{Issue: issue, Tracker: tracker, PR: pr, Branch: branch, Review: review, RepoBases: repoBases, BaseReason: baseReason}
		if pr == "" {
			opt.Slug, opt.Repos = args[0], args[1:]
		} else {
			opt.Repos = args
		}
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		return newFromDirectory(cwd, opt, open, noOpen, *asJSON)
	}
	root.AddCommand(cmd)
}

// runNew creates a collection from the one c describes, then opens it.
func runNew(c *wtc.Context, opt wtc.NewOptions, open, noOpen, asJSON bool) error {
	result, err := c.NewCollection(opt)
	if err != nil {
		return err
	}
	if err := openAfterNew(c, result.Collection, open, noOpen); err != nil {
		return err
	}
	return emit(envelope{OK: true, Data: result, Summary: "done: " + result.Collection}, asJSON)
}

// newFromDirectory runs the same creation for options parsed once: from a
// collection it creates from that collection; from a project root that has a
// generated root selection it creates from the recorded project source.
func newFromDirectory(dir string, opt wtc.NewOptions, open, noOpen, asJSON bool) error {
	c, err := wtc.Discover(dir)
	if err == nil {
		return runNew(c, opt, open, noOpen, asJSON)
	}
	if location, locErr := wtc.DiscoverLocation(dir); locErr == nil && location.Collection == "" {
		if root, ok := wtc.FindProjectRoot(dir); ok {
			return runNewAtRoot(root, opt, open, noOpen, asJSON)
		}
	}
	return err
}

// runNewAtRoot creates a collection from the project source a root records.
func runNewAtRoot(root string, opt wtc.NewOptions, open, noOpen, asJSON bool) error {
	result, creating, err := wtc.NewCollectionAtRoot(root, opt)
	if err != nil {
		return err
	}
	if err := openAfterNew(creating, result.Collection, open, noOpen); err != nil {
		return err
	}
	return emit(envelope{OK: true, Data: result, Summary: "done: " + result.Collection}, asJSON)
}

func openAfterNew(source *wtc.Context, collection string, explicitOpen, noOpen bool) error {
	if noOpen {
		return nil
	}
	kind, err := openMultiplexer(source)
	if err != nil {
		return fmt.Errorf("collection created at %s, but %w", collection, err)
	}
	shouldOpen := explicitOpen
	if !explicitOpen {
		if host := newTerminalHost(kind, openSession(source, "")); host.Available() == nil {
			shouldOpen = openProjectRunning(host)
		}
	}
	if !shouldOpen {
		return nil
	}
	if host := newTerminalHost(kind, ""); host.Available() != nil {
		return fmt.Errorf("collection created at %s, but %s is unavailable: %w", collection, host.Kind(), host.Available())
	}
	target, err := wtc.OpenCollection(collection)
	if err != nil {
		return fmt.Errorf("collection created at %s, but cannot read its configuration: %w", collection, err)
	}
	opt := openOptions{Session: openSession(target, ""), Multiplexer: kind, Tools: openWantedTools(statusSetting(target, "WTC_PANES") == "full", nil)}
	host := openHostFor(opt)
	running := host.Running()
	if !running {
		if err := host.EnsureRunning(); err != nil {
			return fmt.Errorf("collection created at %s, but opening failed: %w", collection, err)
		}
		running = true
	}
	item := openCollection(source, filepath.Base(collection), opt, openDesiredLayout(target, opt, running), running, true)
	if item.Error != "" {
		if strings.Contains(item.Error, "openpty") {
			item.Error += "; " + openPtyHint(host)
		}
		return fmt.Errorf("collection created at %s, but opening failed: %s", collection, item.Error)
	}
	return nil
}

// openProjectRunning: herdr runs a server per project session, Rex one for
// everything, so there a project runs once one of its collections has a session.
func openProjectRunning(host terminalHost) bool {
	if host.Kind() != "rex" {
		return host.Running()
	}
	workspaces, err := host.Workspaces()
	return err == nil && len(workspaces) > 0
}
