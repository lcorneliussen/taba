package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"taba.sh/taba/internal/wtc"
)

func addNewCommand(root *cobra.Command, asJSON *bool) {
	var issue, tracker, pr, branch string
	var open, noOpen, tip, review bool
	cmd := &cobra.Command{Use: "new [slug] [repo ...]", Short: "Create a worktree collection", Args: cobra.ArbitraryArgs}
	cmd.Flags().StringVar(&issue, "issue", "", "Issue ID whose repository becomes the primary sibling")
	cmd.Flags().StringVar(&tracker, "tracker", "", "Tracker key for the launch note")
	cmd.Flags().StringVar(&pr, "pr", "", "Pull request in <repo>#<number> form")
	cmd.Flags().StringVarP(&branch, "branch", "b", "", "Explicitly check out this branch in each sibling")
	cmd.Flags().BoolVar(&review, "review", false, "With --pr: review someone else's PR without writing to its branch")
	cmd.Flags().BoolVar(&tip, "tip", false, "Compatibility flag; detached tip is the default")
	cmd.Flags().BoolVar(&open, "open", false, "Open the new collection in herdr")
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
		opt := wtc.NewOptions{Issue: issue, Tracker: tracker, PR: pr, Branch: branch, Review: review}
		if pr == "" {
			opt.Slug, opt.Repos = args[0], args[1:]
		} else {
			opt.Repos = args
		}
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		c, err := wtc.Discover(cwd)
		if err != nil {
			return err
		}
		return runNew(c, opt, open, noOpen, *asJSON)
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

func openAfterNew(source *wtc.Context, collection string, explicitOpen, noOpen bool) error {
	shouldOpen := explicitOpen
	if !explicitOpen && !noOpen {
		if _, err := exec.LookPath("herdr"); err == nil {
			shouldOpen = openSessionRunning(openSession(source, ""))
		}
	}
	if !shouldOpen {
		return nil
	}
	if _, err := exec.LookPath("herdr"); err != nil {
		return fmt.Errorf("collection created at %s, but herdr is unavailable: %w", collection, err)
	}
	target, err := wtc.OpenCollection(collection)
	if err != nil {
		return fmt.Errorf("collection created at %s, but cannot read its configuration: %w", collection, err)
	}
	opt := openOptions{Session: openSession(target, ""), Tools: openWantedTools(statusSetting(target, "WTC_PANES") == "full", nil)}
	running := openSessionRunning(opt.Session)
	if !running {
		if err := openEnsureSession(opt.Session); err != nil {
			return fmt.Errorf("collection created at %s, but opening failed: %w", collection, err)
		}
		running = true
	}
	item := openCollection(source, filepath.Base(collection), opt, openDesiredLayout(target, opt, running), running, true)
	if item.Error != "" {
		if strings.Contains(item.Error, "openpty") {
			item.Error += "; " + openPtyHint()
		}
		return fmt.Errorf("collection created at %s, but opening failed: %s", collection, item.Error)
	}
	return nil
}
