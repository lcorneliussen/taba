package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"taba.sh/taba/internal/wtc"
)

const cleanupWorkspaceLabel = "--cleanup--"

func addRetireCommand(root *cobra.Command, asJSON *bool) {
	var force, dryRun bool
	var transcripts []string
	cmd := &cobra.Command{Use: "retire [collection]", Short: "Retire a finished collection or inspect its readiness", Args: func(cmd *cobra.Command, args []string) error {
		if len(args) > 1 {
			return fmt.Errorf("expected one collection name")
		}
		if !dryRun && len(args) == 0 {
			return fmt.Errorf("retire requires a collection name; --dry-run defaults to the current collection")
		}
		if !dryRun && len(transcripts) > 0 {
			return fmt.Errorf("--transcript requires --dry-run")
		}
		if dryRun && force {
			return fmt.Errorf("--force cannot be combined with --dry-run; the assessment retains every blocker")
		}
		return nil
	}}
	cmd.Flags().BoolVar(&force, "force", false, "Allow dirty or unpushed work after checking that it is disposable")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Read fresh retirement evidence without hooks, cleanup or source changes")
	cmd.Flags().StringArrayVar(&transcripts, "transcript", nil, "With --dry-run: explicit Claude JSONL task transcript (up to eight); archive state remains unknown")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		c, err := wtc.Discover(cwd)
		if err != nil {
			return err
		}
		if dryRun {
			name := "."
			if len(args) > 0 {
				name = args[0]
			}
			report, err := c.InspectRetirement(name, transcripts)
			if err != nil {
				return err
			}
			return emit(envelope{OK: true, Data: report, Summary: report.Text()}, *asJSON)
		}
		name := filepath.Base(c.Collection)
		if args[0] != "." {
			name = args[0]
		}
		if name != filepath.Base(c.Collection) {
			if err := c.RequireWorkspace("retire of another collection"); err != nil {
				return err
			}
		}
		if name == filepath.Base(c.Collection) && c.AdHoc && os.Getenv("HERDR_ENV") != "1" {
			result, err := c.RetireCollection(wtc.RetireOptions{Name: name, Force: force, Self: true, InPlace: true})
			if err != nil {
				return err
			}
			return emitRetireResult(name, result, *asJSON)
		}
		if name == filepath.Base(c.Collection) {
			return handoffSelfRetire(c, name, force, *asJSON)
		}
		result, err := c.RetireCollection(wtc.RetireOptions{Name: name, Force: force})
		if err != nil {
			return err
		}
		return emitRetireResult(name, result, *asJSON)
	}
	root.AddCommand(cmd)

	worker := &cobra.Command{Use: "retire-worker <collection>", Hidden: true, Args: cobra.ExactArgs(1)}
	worker.Flags().BoolVar(&force, "force", false, "Allow dirty or unpushed work")
	worker.RunE = func(cmd *cobra.Command, args []string) error {
		return runRetireWorker(args[0], force, *asJSON)
	}
	root.AddCommand(worker)
}

func emitRetireResult(name string, result wtc.RetireResult, asJSON bool) error {
	summary := fmt.Sprintf("retired %s", name)
	if !result.FolderRemoved {
		summary = fmt.Sprintf("retired %s; files remain: %s", name, strings.Join(result.Leftovers, ", "))
	}
	return emit(envelope{OK: true, Data: result, Summary: summary}, asJSON)
}

func handoffSelfRetire(c *wtc.Context, name string, force, asJSON bool) error {
	session := os.Getenv("HERDR_SESSION")
	sourceID := os.Getenv("HERDR_WORKSPACE_ID")
	sourcePane := os.Getenv("HERDR_PANE_ID")
	if os.Getenv("HERDR_ENV") != "1" || session == "" || sourceID == "" || sourcePane == "" {
		return errors.New("retiring the current collection requires its Herdr pane; use another collection otherwise")
	}
	if _, err := c.RetirePreflight(wtc.RetireOptions{Name: name, Force: force, Self: true}); err != nil {
		return err
	}
	workspaces, err := openWorkspaces(session)
	if err != nil {
		return err
	}
	sourceFound := false
	cleanupID := ""
	for _, ws := range workspaces {
		if ws.ID == sourceID && ws.Label == name {
			sourceFound = true
		}
		if ws.Label == cleanupWorkspaceLabel {
			cleanupID = ws.ID
		}
	}
	if !sourceFound {
		return errors.New("current Herdr workspace does not match the collection being retired")
	}
	panes, err := openPanes(session, sourceID)
	if err != nil {
		return err
	}
	sourcePaneFound := false
	for _, pane := range panes {
		if pane.ID == sourcePane {
			sourcePaneFound = true
		}
	}
	if !sourcePaneFound {
		return errors.New("current Herdr pane is not in the target workspace")
	}
	for _, pane := range panes {
		if pane.ID != sourcePane && pane.Agent != "" {
			return fmt.Errorf("another agent is active in pane %s; close it before retiring", pane.ID)
		}
	}
	settings := []string{
		"--cwd", c.Workspace, "--no-focus",
		"--env", "WTC_RETIRE_SESSION=" + session,
		"--env", "WTC_RETIRE_SOURCE_WORKSPACE=" + sourceID,
		"--env", "WTC_RETIRE_SOURCE_PANE=" + sourcePane,
		"--env", "WTC_RETIRE_TARGET=" + name,
		"--env", "HARNESS_HERDR_SESSION=" + session,
	}
	var launched openHerdrResponse
	if cleanupID == "" {
		launched, err = openHerdr(session, append([]string{"workspace", "create", "--label", cleanupWorkspaceLabel}, settings...)...)
		cleanupID = launched.Result.WorkspaceID
		if cleanupID == "" {
			cleanupID = launched.Result.Workspace.ID
		}
	} else {
		launched, err = openHerdr(session, append([]string{"tab", "create", "--workspace", cleanupID, "--label", name}, settings...)...)
	}
	if err != nil {
		return err
	}
	paneID := launched.Result.RootPane.ID
	if paneID == "" {
		paneID = launched.Result.Pane.ID
	}
	if paneID == "" {
		paneID = launched.Result.PaneID
	}
	if cleanupID == "" || paneID == "" {
		return errors.New("Herdr created cleanup space without a workspace and pane ID")
	}
	binary, err := os.Executable()
	if err != nil {
		return err
	}
	command := shellQuote(binary) + " retire-worker " + shellQuote(name)
	if force {
		command += " --force"
	}
	if _, err := openHerdr(session, "pane", "run", paneID, command); err != nil {
		return err
	}
	return emit(envelope{OK: true, Data: map[string]string{"workspace_id": cleanupID, "pane_id": paneID, "collection": name}, Summary: "retirement of " + name + " handed to " + cleanupWorkspaceLabel}, asJSON)
}

func runRetireWorker(name string, force, asJSON bool) error {
	if os.Getenv("HERDR_ENV") != "1" || os.Getenv("HERDR_SESSION") != os.Getenv("WTC_RETIRE_SESSION") || os.Getenv("WTC_RETIRE_TARGET") != name {
		return errors.New("retire worker is not in its scheduled Herdr session")
	}
	session := os.Getenv("HERDR_SESSION")
	cleanupID := os.Getenv("HERDR_WORKSPACE_ID")
	sourceID := os.Getenv("WTC_RETIRE_SOURCE_WORKSPACE")
	sourcePane := os.Getenv("WTC_RETIRE_SOURCE_PANE")
	if cleanupID == "" || sourceID == "" || sourcePane == "" || cleanupID == sourceID {
		return errors.New("retire worker is missing its source or cleanup pane identity")
	}
	workspaces, err := openWorkspaces(session)
	if err != nil {
		return err
	}
	cleanupFound, sourceFound := false, false
	for _, ws := range workspaces {
		cleanupFound = cleanupFound || ws.ID == cleanupID && ws.Label == cleanupWorkspaceLabel
		sourceFound = sourceFound || ws.ID == sourceID && ws.Label == name
	}
	if !cleanupFound || !sourceFound {
		return errors.New("retire worker workspace identity changed; no deletion attempted")
	}
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	if current, err := wtc.Discover(root); err == nil && current.Collection == root {
		return errors.New("retire worker is still inside the target collection")
	}
	c, err := wtc.OpenCollection(filepath.Join(root, name))
	if err != nil {
		return fmt.Errorf("retire worker cannot open target collection: %w", err)
	}
	if c.Workspace != root {
		return errors.New("retire worker is outside the target workspace root")
	}
	if _, err := c.RetirePreflight(wtc.RetireOptions{Name: name, Force: force, Self: true}); err != nil {
		return err
	}
	fmt.Printf("Waiting for the source pane to finish before retiring %s...\n", name)
	if err := waitRetireSource(session, sourceID, sourcePane); err != nil {
		return err
	}
	panes, err := openPanes(session, sourceID)
	if err != nil {
		return fmt.Errorf("cannot verify remaining source panes: %w", err)
	}
	for _, pane := range panes {
		if pane.ID != sourcePane && pane.Agent != "" {
			return fmt.Errorf("another agent appeared in pane %s; retirement remains pending", pane.ID)
		}
	}
	result, err := c.RetireCollection(wtc.RetireOptions{Name: name, Force: force, Self: true, WorkspaceID: sourceID})
	if err != nil {
		return err
	}
	return emitRetireResult(name, result, asJSON)
}

func waitRetireSource(session, workspaceID, paneID string) error {
	time.Sleep(time.Second) // Let the initiating CLI return before closing its pane.
	deadline := time.Now().Add(2 * time.Hour)
	for {
		panes, err := openPanes(session, workspaceID)
		if err != nil {
			return fmt.Errorf("cannot verify source pane state: %w", err)
		}
		for _, pane := range panes {
			if pane.ID != paneID {
				continue
			}
			if pane.Agent == "" || pane.AgentStatus == "idle" || pane.AgentStatus == "done" {
				return nil
			}
			if time.Now().After(deadline) {
				return errors.New("source agent did not finish; retirement remains pending in cleanup space")
			}
			time.Sleep(2 * time.Second)
			goto retry
		}
		return errors.New("source pane disappeared before cleanup could verify it")
	retry:
	}
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
