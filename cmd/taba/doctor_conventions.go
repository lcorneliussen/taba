package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"taba.sh/taba/internal/wtc"
)

// conventionsCheck summarises findings as one doctor check. It never fails
// doctor: during 0.2.x old names are supported.
func conventionsCheck(findings []wtc.ConventionFinding) map[string]any {
	actionable := actionableFindings(findings)
	detail := "no wtc conventions found"
	if len(findings) > 0 {
		detail = fmt.Sprintf("%d wtc convention(s) to migrate, %d waiting for taba", actionable, len(findings)-actionable)
	}
	return map[string]any{"name": "conventions", "ok": actionable == 0, "detail": detail, "findings": findings}
}

// printConventionFindings prints each convention once with its migration
// step, followed by the places it was found. Findings arrive sorted.
func printConventionFindings(w io.Writer, findings []wtc.ConventionFinding, indent string) {
	for i, f := range findings {
		if i == 0 || findings[i-1].ID != f.ID {
			state := "migrate"
			if f.Blocked {
				state = "waiting"
			}
			fmt.Fprintf(w, "%s%-8s %s: %s\n", indent, state, f.ID, f.Migrate)
		}
		line := f.Path
		if f.Detail != "" {
			line += " (" + f.Detail + ")"
		}
		fmt.Fprintf(w, "%s         %s\n", indent, line)
	}
}

func printConventionSection(w io.Writer, findings []wtc.ConventionFinding) {
	if len(findings) == 0 {
		return
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "wtc conventions still in use (all of them: taba conventions):")
	printConventionFindings(w, findings, "  ")
	fmt.Fprintln(w, migrateHint)
}

// migrateHint tells the reader that an agent can do the migration from the
// steps above.
const migrateHint = "\nTo migrate, ask an agent: \"Run taba doctor --all and finish the migration to taba.\""

type doctorAllCollection struct {
	Collection string                  `json:"collection"`
	Error      string                  `json:"error,omitempty"`
	Findings   []wtc.ConventionFinding `json:"findings"`
}

// doctorWorkspace resolves the workspace for `doctor --all`: the current
// collection's, or the current directory when it is a workspace root.
func doctorWorkspace(c *wtc.Context, discoverErr error) (string, error) {
	if discoverErr == nil {
		if err := c.RequireWorkspace("doctor --all"); err != nil {
			return "", err
		}
		return c.Workspace, nil
	}
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	collections, err := wtc.WorkspaceCollections(dir)
	if err != nil || len(collections) == 0 {
		return "", fmt.Errorf("doctor --all needs a collection or a workspace root: %w", discoverErr)
	}
	return dir, nil
}

func runDoctorAll(c *wtc.Context, discoverErr error, asJSON bool) error {
	workspace, err := doctorWorkspace(c, discoverErr)
	if err != nil {
		return err
	}
	collections, err := wtc.WorkspaceCollections(workspace)
	if err != nil {
		return err
	}
	configRoot := ""
	if c != nil && discoverErr == nil {
		configRoot = c.ConfigRoot
	}
	var results []doctorAllCollection
	total := 0
	for _, dir := range collections {
		item := doctorAllCollection{Collection: filepath.Base(dir), Findings: []wtc.ConventionFinding{}}
		target, err := wtc.OpenCollection(dir)
		if err != nil {
			item.Error = err.Error()
			results = append(results, item)
			continue
		}
		if configRoot == "" {
			configRoot = target.ConfigRoot
		}
		found, err := target.ConventionFindings()
		if err != nil {
			item.Error = err.Error()
		}
		wtc.SortConventionFindings(found)
		if found != nil {
			item.Findings = found
		}
		total += len(found)
		results = append(results, item)
	}
	user := wtc.UserConventionFindings(configRoot)
	if user == nil {
		user = []wtc.ConventionFinding{}
	}
	total += len(user)
	if asJSON {
		return emit(envelope{OK: true, Data: map[string]any{"workspace": workspace, "user": user, "collections": results},
			Summary: fmt.Sprintf("%d wtc convention(s) in %d collection(s)", total, len(collections))}, true)
	}
	out := os.Stdout
	fmt.Fprintf(out, "wtc conventions in %s\n", workspace)
	if len(user) > 0 {
		fmt.Fprintln(out, "\nuser and control root")
		printConventionFindings(out, user, "  ")
	}
	// Collections usually share one harness: its findings print once, and
	// later collections with the same harness findings refer back to it.
	harnessSeen := map[string]string{}
	migrated, unchecked := 0, 0
	for _, r := range results {
		switch {
		case r.Error != "":
			unchecked++
			fmt.Fprintf(out, "\n%s: not checked: %s\n", r.Collection, r.Error)
		case len(r.Findings) == 0:
			migrated++
			fmt.Fprintf(out, "\n%s: migrated\n", r.Collection)
		default:
			if actionableFindings(r.Findings) == 0 {
				migrated++
			}
			fmt.Fprintf(out, "\n%s\n", r.Collection)
			var harness, local []wtc.ConventionFinding
			for _, f := range r.Findings {
				if f.Area == "harness" {
					harness = append(harness, f)
				} else {
					local = append(local, f)
				}
			}
			if len(harness) > 0 {
				key := conventionFindingsKey(harness)
				if first, ok := harnessSeen[key]; ok {
					fmt.Fprintf(out, "  harness: same findings as %s\n", first)
				} else {
					harnessSeen[key] = r.Collection
					printConventionFindings(out, harness, "  ")
				}
			}
			printConventionFindings(out, local, "  ")
		}
	}
	fmt.Fprintf(out, "\n%d collection(s): %d with nothing left to migrate by hand, %d with wtc conventions to migrate, %d not checked.\n", len(results), migrated, len(results)-migrated-unchecked, unchecked)
	if len(results)-migrated-unchecked > 0 || actionableFindings(user) > 0 {
		fmt.Fprintln(out, migrateHint)
	}
	return nil
}

func conventionFindingsKey(findings []wtc.ConventionFinding) string {
	var b strings.Builder
	for _, f := range findings {
		b.WriteString(f.ID + "\x00" + f.Path + "\x00" + f.Detail + "\n")
	}
	return b.String()
}

func conventionsCommand(asJSON *bool) *cobra.Command {
	return &cobra.Command{
		Use:   "conventions",
		Short: "List the wtc names taba still supports, with their taba names and how to migrate",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if *asJSON {
				return emit(envelope{OK: true, Data: wtc.Conventions, Summary: fmt.Sprintf("%d conventions", len(wtc.Conventions))}, true)
			}
			fmt.Print(wtc.ConventionsMarkdown())
			return nil
		},
	}
}

func actionableFindings(findings []wtc.ConventionFinding) int {
	n := 0
	for _, f := range findings {
		if !f.Blocked {
			n++
		}
	}
	return n
}
