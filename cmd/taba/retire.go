package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"taba.sh/taba/internal/retry"
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
		if name == filepath.Base(c.Collection) && c.AdHoc {
			host, err := retireHost(c)
			if err != nil {
				return err
			}
			if _, inPane := host.Here(); !inPane {
				result, err := c.RetireCollection(wtc.RetireOptions{Name: name, Force: force, Self: true, InPlace: true})
				if err != nil {
					return err
				}
				return emitRetireResult(name, result, *asJSON)
			}
		}
		if name == filepath.Base(c.Collection) {
			return handoffSelfRetire(c, name, force, *asJSON)
		}
		result, err := c.RetireCollection(wtc.RetireOptions{Name: name, Force: force, CloseWorkspace: retireEverywhere(c)})
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
	located, err := retireHost(c)
	if err != nil {
		return err
	}
	here, inPane := located.Here()
	session, sourceID, sourcePane := here.Session, here.Workspace, here.Pane
	host := newTerminalHost(located.Kind(), session)
	if !inPane || session == "" || sourceID == "" || sourcePane == "" {
		return errors.New("retiring the current collection requires its " + retireHostTitle(host) + " pane; use another collection otherwise")
	}
	if _, err := c.RetirePreflight(wtc.RetireOptions{Name: name, Force: force, Self: true}); err != nil {
		return err
	}
	workspaces, err := host.Workspaces()
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
		return errors.New("current " + retireHostTitle(host) + " workspace does not match the collection being retired")
	}
	panes, err := host.Panes(sourceID)
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
		return errors.New("current " + retireHostTitle(host) + " pane is not in the target workspace")
	}
	for _, pane := range panes {
		if pane.ID != sourcePane && pane.Agent != "" {
			return fmt.Errorf("another agent is active in pane %s; close it before retiring", pane.ID)
		}
	}
	settings := []string{
		"WTC_RETIRE_SESSION=" + session,
		"WTC_RETIRE_SOURCE_WORKSPACE=" + sourceID,
		"WTC_RETIRE_SOURCE_PANE=" + sourcePane,
		"WTC_RETIRE_TARGET=" + name,
	}
	if host.Kind() == "rex" {
		settings = append(settings, "WTC_RETIRE_MULTIPLEXER=rex")
	} else {
		settings = append(settings, "HARNESS_HERDR_SESSION="+session)
	}
	var paneID string
	if cleanupID == "" {
		cleanupID, paneID, err = host.CreateWorkspace(cleanupWorkspaceLabel, c.Workspace, settings)
	} else {
		paneID, err = host.CreateTab(cleanupID, c.Workspace, name, settings)
	}
	if err != nil {
		return err
	}
	if cleanupID == "" || paneID == "" {
		return errors.New(retireHostTitle(host) + " created cleanup space without a workspace and pane ID")
	}
	binary, err := os.Executable()
	if err != nil {
		return err
	}
	command := shellQuote(binary) + " retire-worker " + shellQuote(name)
	if force {
		command += " --force"
	}
	if err := host.Run(paneID, command); err != nil {
		return err
	}
	return emit(envelope{OK: true, Data: map[string]string{"workspace_id": cleanupID, "pane_id": paneID, "collection": name}, Summary: "retirement of " + name + " handed to " + cleanupWorkspaceLabel}, asJSON)
}

func runRetireWorker(name string, force, asJSON bool) error {
	located := newTerminalHost(os.Getenv("WTC_RETIRE_MULTIPLEXER"), "")
	here, inPane := located.Here()
	host := newTerminalHost(located.Kind(), here.Session)
	if !inPane || here.Session != os.Getenv("WTC_RETIRE_SESSION") || os.Getenv("WTC_RETIRE_TARGET") != name {
		return errors.New("retire worker is not in its scheduled " + retireHostTitle(host) + " session")
	}
	cleanupID := here.Workspace
	sourceID := os.Getenv("WTC_RETIRE_SOURCE_WORKSPACE")
	sourcePane := os.Getenv("WTC_RETIRE_SOURCE_PANE")
	if cleanupID == "" || sourceID == "" || sourcePane == "" || cleanupID == sourceID {
		return errors.New("retire worker is missing its source or cleanup pane identity")
	}
	workspaces, err := host.Workspaces()
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
	if err := waitRetireSource(host, sourceID, sourcePane); err != nil {
		return err
	}
	panes, err := host.Panes(sourceID)
	if err != nil {
		return fmt.Errorf("cannot verify remaining source panes: %w", err)
	}
	for _, pane := range panes {
		if pane.ID != sourcePane && pane.Agent != "" {
			return fmt.Errorf("another agent appeared in pane %s; retirement remains pending", pane.ID)
		}
	}
	result, err := c.RetireCollection(wtc.RetireOptions{Name: name, Force: force, Self: true, WorkspaceID: sourceID, CloseWorkspace: retireCloser(host)})
	if err != nil {
		return err
	}
	return emitRetireResult(name, result, asJSON)
}

func waitRetireSource(host terminalHost, workspaceID, paneID string) error {
	time.Sleep(time.Second) //nolint:forbidigo // A single delay lets the initiating CLI return before its pane closes.
	watch := newRetireSourceWatch(host)
	err := retry.Poll(context.Background(), retry.Policy{Every: 2 * time.Second, Within: 2 * time.Hour}, func(context.Context) (bool, error) {
		panes, err := host.Panes(workspaceID)
		if err != nil {
			return false, fmt.Errorf("cannot verify source pane state: %w", err)
		}
		for _, pane := range panes {
			if pane.ID == paneID {
				return watch.finished(pane, time.Now()), nil
			}
		}
		return false, errors.New("source pane disappeared before cleanup could verify it")
	})
	if errors.Is(err, retry.ErrExhausted) {
		return errors.New("source agent did not finish; retirement remains pending in cleanup space")
	}
	return err
}

var retireQuietScreen = time.Minute

type retireSourceWatch struct {
	screens     quietScreenHost
	screen      string
	unchangedAt time.Time
}

func newRetireSourceWatch(host terminalHost) *retireSourceWatch {
	screens, _ := host.(quietScreenHost)
	return &retireSourceWatch{screens: screens}
}

func (w *retireSourceWatch) finished(pane openPaneInfo, now time.Time) bool {
	switch {
	case pane.Agent == "" || pane.AgentStatus == "idle" || pane.AgentStatus == "done":
		return true
	case pane.AgentStatus != "unknown" || w.screens == nil:
		return false
	}
	text, err := w.screens.VisibleText(pane.ID)
	if err != nil || text != w.screen || w.unchangedAt.IsZero() {
		w.screen, w.unchangedAt = text, now
		return false
	}
	return now.Sub(w.unchangedAt) >= retireQuietScreen
}

func retireHost(c *wtc.Context) (terminalHost, error) {
	kind, err := openMultiplexer(c)
	return newTerminalHost(kind, ""), err
}

func retireEverywhere(c *wtc.Context) func(name, expectedID string) (bool, string) {
	rex := retireCloser(newTerminalHost("rex", openSession(c, "")))
	return func(name, expectedID string) (bool, string) {
		herdrClosed, herdrWarning := c.CloseHerdrWorkspace(name, expectedID)
		rexClosed, rexWarning := rex(name, expectedID)
		warnings := slices.DeleteFunc([]string{herdrWarning, rexWarning}, func(w string) bool { return w == "" })
		return herdrClosed || rexClosed, strings.Join(warnings, "; ")
	}
}

// retireCloser is nil for herdr, whose workspace the retirement closes itself.
func retireCloser(host terminalHost) func(name, expectedID string) (bool, string) {
	if host.Kind() == "herdr" {
		return nil
	}
	return func(name, expectedID string) (bool, string) {
		workspaces, err := host.Workspaces()
		if err != nil {
			return false, "" // No running multiplexer is normal.
		}
		for _, workspace := range workspaces {
			if workspace.Label != name || expectedID != "" && workspace.ID != expectedID {
				continue
			}
			if err := host.CloseWorkspace(workspace.ID); err != nil {
				return false, fmt.Sprintf("could not close %s workspace %s: %v", host.Kind(), workspace.ID, err)
			}
			return true, ""
		}
		if expectedID != "" {
			return false, "target " + retireHostTitle(host) + " workspace identity changed; no workspace closed"
		}
		return false, ""
	}
}

func retireHostTitle(host terminalHost) string {
	kind := host.Kind()
	return strings.ToUpper(kind[:1]) + kind[1:]
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
