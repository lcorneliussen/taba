package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/x/term"

	"github.com/spf13/cobra"
	"taba.sh/taba/internal/branding"
	"taba.sh/taba/internal/openclaw"
	"taba.sh/taba/internal/wtc"
)

func addAddRepoCommand(root *cobra.Command, asJSON *bool) {
	var collection, branch, name, target, worktree, base, harness, worker, work string
	var tip, separate, adHoc bool
	cmd := &cobra.Command{
		Use:   "add-repo <repo|clone-url|path> [...]",
		Short: "Add repository worktrees to a collection",
		Args:  cobra.MinimumNArgs(1),
	}
	cmd.Flags().StringVar(&collection, "collection", "", "Target collection name (default: current)")
	cmd.Flags().StringVarP(&branch, "branch", "b", "", "Explicitly check out a branch in each new worktree")
	cmd.Flags().BoolVar(&tip, "tip", false, "Compatibility flag; detached tip is the default")
	cmd.Flags().StringVar(&name, "name", "", "Ad-hoc: registry name for a new repository (default: from its remote)")
	cmd.Flags().StringVar(&target, "target", "", "Ad-hoc: development branch and PR target for a new repository (default: ask, or the remote's default)")
	cmd.Flags().StringVar(&worktree, "worktree", "", "Under OpenClaw: the managed worktree name (default: this session's, or $WTC_COLLECTION)")
	cmd.Flags().StringVar(&base, "base", "", "Under OpenClaw: base ref for the new worktree (default: the gateway's)")
	cmd.Flags().StringVar(&harness, "project", "", branding.Text("Under OpenClaw: the wtc project (a configured harness) this collection works in, or none for an ad-hoc one (default: the collection's, or the one whose repositories these are; {{command}} projects lists them)"))
	// The earlier name of --project.
	cmd.Flags().StringVar(&harness, "harness", "", "Same as --project")
	_ = cmd.Flags().MarkHidden("harness")
	cmd.Flags().StringVar(&worker, "worker", "", "Under OpenClaw: spawn a worker session for part of the task, in collection <worktree>--<worker>, starting from the task's branch where it has one")
	cmd.Flags().StringVar(&work, "work", "", "Under OpenClaw, with --worker: the part of the task the worker takes")
	// Parallel work is a collection of its own now; workers still work, for
	// what started them.
	for _, f := range []string{"worker", "work"} {
		_ = cmd.Flags().MarkDeprecated(f, branding.Text("parallel work is a collection of its own, with its own conversation ({{command}} docs instructions/openclaw.md)"))
	}
	cmd.Flags().BoolVar(&adHoc, "ad-hoc", false, "Under OpenClaw: add repositories the collection's wtc project does not list, as ad hoc ones beside it, once the person chose that")
	cmd.Flags().BoolVar(&separate, "separate", false, "Under OpenClaw: a GitHub repository the gateway has not cloned yet becomes a conversation of its own")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		if currentHost() == openclaw.HostOpenClaw {
			return addRepoIntents(args, worktree, base, harness, separate, adHoc, worker, work)
		}
		if tip {
			_, _ = os.Stderr.WriteString("note: --tip is already the default\n")
		}
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		c, err := wtc.Discover(cwd)
		if err != nil {
			return err
		}
		repos := args
		if c.AdHoc && collection != "" {
			// Refuse before anything is registered or prompted for.
			if err := c.RequireWorkspace("add-repo --collection"); err != nil {
				return err
			}
		}
		if c.AdHoc {
			if (name != "" || target != "") && len(args) != 1 {
				return fmt.Errorf("--name and --target describe one repository")
			}
			var ask func(string, string) (string, error)
			if !*asJSON && term.IsTerminal(os.Stdin.Fd()) && term.IsTerminal(os.Stderr.Fd()) {
				in := bufio.NewReader(os.Stdin)
				ask = func(repo, suggested string) (string, error) {
					fmt.Fprintf(os.Stderr, "%s: development branch and PR target [%s]: ", repo, suggested)
					answer, err := in.ReadString('\n')
					return strings.TrimSpace(answer), err
				}
			}
			repos = make([]string, 0, len(args))
			for _, spec := range args {
				repo, err := c.RegisterAdHocRepo(wtc.AdHocRepoOptions{Spec: spec, Name: name, Target: target, Ask: ask})
				if err != nil {
					return err
				}
				repos = append(repos, repo.Name)
			}
		} else if name != "" || target != "" {
			return fmt.Errorf("--name and --target apply to ad-hoc collections; edit the harness registry instead")
		}
		result, err := c.AddRepositories(wtc.AddRepoOptions{Collection: collection, Repos: repos, Branch: branch})
		if err != nil {
			return err
		}
		return emit(envelope{OK: true, Data: result, Summary: "done: added repositories to " + result.Collection}, *asJSON)
	}
	root.AddCommand(cmd)
}
