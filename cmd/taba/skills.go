package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"taba.sh/taba/internal/branding"
	"taba.sh/taba/internal/wtc"
)

func addSkillsCommands(root *cobra.Command, asJSON *bool) {
	var collection string
	var all, dryRun, seedScope bool
	skills := &cobra.Command{Use: "skills", Short: "Expose versioned skills to agent clients"}
	render := &cobra.Command{Use: "render", Short: "Render skills, entry point, hooks, and agent shell setup", Args: cobra.NoArgs}
	render.Flags().StringVar(&collection, "collection", "", "Collection directory (default: current)")
	render.Flags().BoolVar(&all, "all", false, "Render every collection in this workspace")
	render.Flags().BoolVar(&dryRun, "dry-run", false, "Show changes without writing")
	render.Flags().BoolVar(&seedScope, "seed-scope", false, "Seed WTC-SCOPE.md when absent")
	render.RunE = func(cmd *cobra.Command, args []string) error {
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
		collections := []*wtc.Context{c}
		failures := []string{}
		if all {
			if err := c.RequireWorkspace("skills render --all"); err != nil {
				return err
			}
			collections = nil
			entries, err := os.ReadDir(c.Workspace)
			if err != nil {
				return err
			}
			for _, entry := range entries {
				if !entry.IsDir() {
					continue
				}
				candidate := filepath.Join(c.Workspace, entry.Name())
				if wtc.RetiredFolder(candidate) {
					continue
				}
				if _, err := os.Stat(filepath.Join(candidate, "harness", ".harness-repos.yml")); err != nil {
					continue
				}
				target, err := wtc.OpenCollection(candidate)
				if err != nil {
					failures = append(failures, candidate+": "+err.Error())
					continue
				}
				collections = append(collections, target)
			}
		}
		reports := []wtc.SkillRenderResult{}
		for _, target := range collections {
			if !dryRun {
				if err := target.RunHook("skills.render.pre", nil); err != nil {
					if !all {
						return err
					}
					failures = append(failures, target.Collection+": "+err.Error())
					continue
				}
			}
			report, err := target.RenderSkills(wtc.SkillRenderOptions{DryRun: dryRun, SeedScope: seedScope})
			if err != nil {
				if !all {
					return err
				}
				failures = append(failures, target.Collection+": "+err.Error())
				continue
			}
			if !dryRun {
				if err := target.RunHook("skills.render.post", nil); err != nil {
					if !all {
						return err
					}
					failures = append(failures, target.Collection+": "+err.Error())
				}
			}
			reports = append(reports, report)
		}
		if *asJSON {
			if err := emit(envelope{OK: len(failures) == 0, Data: reports, Summary: fmt.Sprintf("rendered %d collection(s); %d failed", len(reports), len(failures)), Meta: map[string]any{"failures": failures}}, true); err != nil {
				return err
			}
			if len(failures) > 0 {
				return fmt.Errorf("%d collection(s) failed", len(failures))
			}
			return nil
		}
		for _, report := range reports {
			fmt.Printf("%s: linked=%d current=%d pruned=%d skipped=%d\n", filepath.Base(report.Collection), report.Linked, report.Current, report.Pruned, report.Skipped)
			for _, action := range report.Actions {
				fmt.Println("  " + action)
			}
		}
		for _, failure := range failures {
			fmt.Fprintln(os.Stderr, branding.Text("{{command}}: ")+failure)
		}
		if len(failures) > 0 {
			return fmt.Errorf("%d collection(s) failed", len(failures))
		}
		return nil
	}
	skills.AddCommand(render)
	var diffCollection string
	var showChanges bool
	diff := &cobra.Command{Use: "diff", Short: "Review skill overrides and upstream drift", Args: cobra.NoArgs}
	diff.Flags().StringVar(&diffCollection, "collection", "", "Collection directory (default: current)")
	diff.Flags().BoolVar(&showChanges, "changes", false, "Print changed lines for each override")
	diff.RunE = func(cmd *cobra.Command, args []string) error {
		var c *wtc.Context
		var err error
		if diffCollection == "" {
			var cwd string
			cwd, err = os.Getwd()
			if err == nil {
				c, err = wtc.Discover(cwd)
			}
		} else {
			c, err = wtc.OpenCollection(diffCollection)
		}
		if err != nil {
			return err
		}
		reports, err := c.DiffSkills()
		if err != nil {
			return err
		}
		if *asJSON {
			return emit(envelope{OK: true, Data: reports, Summary: fmt.Sprintf("%d skill override(s)", len(reports))}, true)
		}
		for _, report := range reports {
			fmt.Printf("%s: %s (%s)\n", report.Name, report.Status, report.Source)
			if report.Status == "drifted" || report.Status == "untracked" || report.Status == "invalid" {
				fmt.Printf("  current base: %s\n", report.DefaultHash)
			}
			if report.Error != "" {
				fmt.Printf("  cannot apply: %s\n", report.Error)
			}
			if showChanges {
				for _, line := range report.Changes {
					fmt.Println("  " + line)
				}
			}
		}
		return nil
	}
	skills.AddCommand(diff)
	root.AddCommand(skills)
}
