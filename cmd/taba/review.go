package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"taba.sh/taba/internal/branding"
	"taba.sh/taba/internal/wtc"
)

func addReviewCommands(root *cobra.Command, asJSON *bool) {
	var collection string
	review := &cobra.Command{Use: "review", Short: "Manage independent local PR reviews"}
	review.PersistentFlags().StringVar(&collection, "collection", "", "Collection directory (default: current)")
	context := func() (*wtc.Context, error) {
		if collection != "" {
			return wtc.OpenCollection(collection)
		}
		cwd, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		return wtc.Discover(cwd)
	}
	var base, head, dir string
	var round int
	var public, private, noCatchUp bool
	var startOpen, startNoOpen bool
	start := &cobra.Command{Use: "start <repo>#<number> [repo ...]", Short: branding.Text("Create a review-only collection for someone else's PR ({{command}} new --pr … --review)"), Args: cobra.MinimumNArgs(1)}
	start.Flags().BoolVar(&startOpen, "open", false, "Open the new collection in herdr")
	start.Flags().BoolVar(&startNoOpen, "no-open", false, "Leave the collection unopened")
	start.RunE = func(cmd *cobra.Command, args []string) error {
		if err := refuseUnderGateway("review start"); err != nil {
			return err
		}
		if startOpen && startNoOpen {
			return fmt.Errorf("--open and --no-open cannot be combined")
		}
		c, err := context()
		if err != nil {
			return err
		}
		return runNew(c, wtc.NewOptions{PR: args[0], Repos: args[1:], Review: true}, startOpen, startNoOpen, *asJSON)
	}
	review.AddCommand(start)
	bundle := &cobra.Command{Use: "bundle <repo> [pr-number]", Short: "Build a local review bundle", Args: cobra.RangeArgs(1, 2)}
	bundle.Flags().StringVar(&base, "base", "", "Base ref (default: PR destination or repository default)")
	bundle.Flags().StringVar(&head, "head", "HEAD", "Head commit ref")
	bundle.Flags().StringVar(&dir, "dir", "", "Bundle directory")
	bundle.Flags().IntVar(&round, "round", 0, "Review round (default: next)")
	bundle.Flags().BoolVar(&public, "public", false, "Use target-only rules and exclude other repository context")
	bundle.Flags().BoolVar(&private, "private", false, "Include harness overlays and repository context (cannot post to public/unknown targets)")
	bundle.MarkFlagsMutuallyExclusive("public", "private")
	bundle.Flags().BoolVar(&noCatchUp, "no-catch-up", false, "Skip PR collection catch-up (PR branch checkout still runs)")
	bundle.RunE = func(cmd *cobra.Command, args []string) error {
		c, err := context()
		if err != nil {
			return err
		}
		pr := ""
		if len(args) == 2 {
			pr = args[1]
		}
		result, err := c.BuildReviewBundle(wtc.ReviewBundleOptions{Repo: args[0], PR: pr, Base: base, Head: head, Dir: dir, Round: round, Public: public, Private: private, NoCatchUp: noCatchUp})
		if err != nil {
			return err
		}
		if *asJSON {
			return emit(envelope{OK: true, Data: result, Summary: result.Dir}, true)
		}
		fmt.Fprintf(os.Stderr, "Review bundle mode: %s (audience: %s; %s)\n", result.Manifest.Mode(), result.Manifest.Audience, result.Manifest.ModeReason)
		fmt.Println(result.Dir)
		return nil
	}
	review.AddCommand(bundle)
	activity := &cobra.Command{Use: "activity <repo> [pr-number]", Short: "Inspect Copilot/Cursor reports, active reviews and open threads", Args: cobra.RangeArgs(1, 2)}
	activity.RunE = func(cmd *cobra.Command, args []string) error {
		c, err := context()
		if err != nil {
			return err
		}
		number := ""
		if len(args) == 2 {
			number = args[1]
		}
		result, err := c.GetReviewActivity(args[0], number)
		if err != nil {
			return err
		}
		if *asJSON {
			return emit(envelope{OK: true, Data: result, Summary: "hosted review activity"}, true)
		}
		fmt.Printf("Head %s; independent local review still required\n", result.Head[:7])
		for _, report := range result.Reports {
			fmt.Printf("%s: %s %s\n", report.Provider, report.State, report.URL)
			if report.Body != "" {
				fmt.Println(report.Body)
			}
		}
		for _, finding := range result.Findings {
			fmt.Printf("Open %s thread: %s (outdated: %t)\n%s\n%s\n", finding.Provider, finding.File, finding.Outdated, finding.URL, finding.Body)
		}
		return nil
	}
	review.AddCommand(activity)
	var strong, standard, fast, lead, only string
	var postRun, hostedFirst bool
	var parallel, timeout int
	run := &cobra.Command{Use: "run <bundle-dir>", Short: "Run separate headless reviewers and aggregate their findings", Args: cobra.ExactArgs(1)}
	run.Flags().StringVar(&strong, "strong", "", "Agent:model chain for strong concerns")
	run.Flags().StringVar(&standard, "standard", "", "Agent:model chain for standard concerns")
	run.Flags().StringVar(&fast, "fast", "", "Agent:model chain for fast concerns")
	run.Flags().StringVar(&lead, "lead", "", "Agent:model chain for the lead")
	run.Flags().StringVar(&only, "only", "", "Comma-separated concern IDs")
	run.Flags().IntVar(&parallel, "parallel", 0, "Maximum concurrent concern runs")
	run.Flags().IntVar(&timeout, "timeout", 0, "Seconds allowed per agent run")
	run.Flags().BoolVar(&postRun, "post", false, "Post one progress comment, then update it with the review result")
	run.Flags().BoolVar(&hostedFirst, "hosted-first", true, "Defer GitHub PR runs while Copilot/Cursor are active or have open threads")
	run.RunE = func(cmd *cobra.Command, args []string) error {
		c, err := context()
		if err != nil {
			return err
		}
		if timeout < 0 {
			return fmt.Errorf("timeout must be nonnegative")
		}
		var ids []string
		if only != "" {
			for _, id := range strings.Split(only, ",") {
				ids = append(ids, strings.TrimSpace(id))
			}
		}
		if hostedFirst {
			if err := c.PrepareHostedReviewBundle(args[0]); err != nil {
				return err
			}
		}
		var progressErr error
		if postRun {
			if _, err := c.PostReviewBundle(args[0], wtc.ReviewPostOptions{Mode: "progress"}); err != nil {
				progressErr = fmt.Errorf("post review progress: %w", err)
				fmt.Fprintln(os.Stderr, "Review progress could not be confirmed; continuing locally without further posting:", err)
			}
		}
		result, runErr := c.RunReviewBundle(args[0], wtc.ReviewRunOptions{Strong: strong, Standard: standard, Fast: fast, Lead: lead, Parallel: parallel, Timeout: time.Duration(timeout) * time.Second, Only: ids})
		var finalPostErr error
		// A failed progress write may have succeeded remotely without returning an
		// ID. Do not risk creating another comment or publishing a passing receipt.
		if postRun && progressErr == nil {
			mode, reason := "summary", ""
			if runErr != nil {
				mode, reason = "failed", runErr.Error()
			}
			if _, err := c.PostReviewBundle(args[0], wtc.ReviewPostOptions{Mode: mode, Reason: reason}); err != nil {
				finalPostErr = fmt.Errorf("post review %s: %w", mode, err)
			}
		}
		finalErr := errors.Join(progressErr, runErr, finalPostErr)
		if runErr == nil {
			if *asJSON {
				if err := emit(envelope{OK: finalErr == nil, Data: result, Summary: result.Verdict}, true); err != nil {
					finalErr = errors.Join(finalErr, err)
				}
			} else {
				fmt.Printf("%s: %s (%d blockers)\n", result.Bundle, result.Verdict, result.Blockers)
			}
		}
		return finalErr
	}
	review.AddCommand(run)
	var force, progress, failed bool
	var postBody, reason string
	post := &cobra.Command{Use: "post <bundle-dir>", Short: "Post or update a review summary and inline findings", Args: cobra.ExactArgs(1)}
	post.Flags().BoolVar(&force, "force", false, "Post even when the reviewed head is stale")
	post.Flags().BoolVar(&progress, "progress", false, "Post an in-progress status")
	post.Flags().BoolVar(&failed, "failed", false, "Mark the run as failed")
	post.Flags().StringVar(&postBody, "body", "", "Custom in-progress body file")
	post.Flags().StringVar(&reason, "reason", "", "Reason for failed status")
	post.RunE = func(cmd *cobra.Command, args []string) error {
		if progress && failed {
			return fmt.Errorf("choose either --progress or --failed")
		}
		c, err := context()
		if err != nil {
			return err
		}
		mode := "summary"
		if progress {
			mode = "progress"
		} else if failed {
			mode = "failed"
		}
		result, err := c.PostReviewBundle(args[0], wtc.ReviewPostOptions{Mode: mode, Body: postBody, Reason: reason, Force: force})
		if err != nil {
			return err
		}
		if *asJSON {
			return emit(envelope{OK: true, Data: result, Summary: result.URL}, true)
		}
		fmt.Println(result.URL)
		if result.InlineFailed > 0 {
			fmt.Fprintf(os.Stderr, branding.Text("{{command}}: warning: %d inline comments failed to post\n"), result.InlineFailed)
		}
		return nil
	}
	review.AddCommand(post)
	var reply, file, concern, key string
	var line int
	resolve := &cobra.Command{Use: "resolve <bundle-dir>", Short: "Reply to and resolve posted inline review threads", Args: cobra.ExactArgs(1)}
	resolve.Flags().StringVar(&reply, "reply", "", "Reply before resolving each selected thread")
	resolve.Flags().StringVar(&file, "file", "", "Select findings in this repo-relative file")
	resolve.Flags().IntVar(&line, "line", 0, "Select a new-file line")
	resolve.Flags().StringVar(&concern, "concern", "", "Select a concern ID")
	resolve.Flags().StringVar(&key, "key", "", "Select one inline finding key")
	resolve.RunE = func(cmd *cobra.Command, args []string) error {
		if line < 0 {
			return fmt.Errorf("line must be nonnegative")
		}
		c, err := context()
		if err != nil {
			return err
		}
		result, err := c.ResolveReviewBundle(args[0], wtc.ReviewResolveOptions{Reply: reply, File: file, Line: line, Concern: concern, Key: key})
		if err != nil {
			return err
		}
		if *asJSON {
			return emit(envelope{OK: true, Data: result, Summary: fmt.Sprintf("resolved %d threads", result.Resolved)}, true)
		}
		fmt.Printf("resolved %d threads\n", result.Resolved)
		return nil
	}
	review.AddCommand(resolve)
	var trusted bool
	status := &cobra.Command{Use: "status <repo> [pr-number]", Short: "Read the latest review comment and compare it with the PR head", Args: cobra.RangeArgs(1, 2)}
	status.Flags().BoolVar(&trusted, "trusted-local", false, "Require a local receipt from the review posting tool")
	status.RunE = func(cmd *cobra.Command, args []string) error {
		c, err := context()
		if err != nil {
			return err
		}
		number := ""
		if len(args) == 2 {
			number = args[1]
		}
		state, err := c.GetReviewStatus(args[0], number, trusted)
		if err != nil {
			return err
		}
		if *asJSON {
			return emit(envelope{OK: true, Data: state, Summary: state.State}, true)
		}
		if state.State == "none" {
			fmt.Println("none")
		} else {
			fmt.Printf("%s %s %d %d\n", state.State, state.Verdict, *state.Blockers, *state.Round)
		}
		return nil
	}
	review.AddCommand(status)
	var repo, authorization string
	ready := &cobra.Command{Use: "ready <pr-number>", Short: "Promote a draft PR after a current trusted passing review", Args: cobra.ExactArgs(1)}
	ready.Flags().StringVar(&repo, "repo", "", "Repository worktree name (default: current worktree)")
	ready.Flags().StringVar(&authorization, "user-authorized", "", "Verbatim user instruction overriding the review gate")
	ready.RunE = func(cmd *cobra.Command, args []string) error {
		c, err := context()
		if err != nil {
			return err
		}
		// Not even --user-authorized: undrafting someone else's PR is theirs.
		if err := c.RefuseInReviewMode("marking the PR ready"); err != nil {
			return err
		}
		name := repo
		if name == "" {
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			top, err := exec.Command("git", "-C", cwd, "rev-parse", "--show-toplevel").Output()
			if err != nil {
				return fmt.Errorf("run from a repository worktree or pass --repo")
			}
			name = filepath.Base(strings.TrimSpace(string(top)))
		}
		if err := wtc.ValidatePRIdentity(name, args[0]); err != nil {
			return err
		}
		worktree, slug, forge, err := c.ReviewRepo(name)
		if err != nil {
			return err
		}
		state, statusErr := c.GetReviewStatus(name, args[0], true)
		if authorization == "" {
			if statusErr != nil {
				return fmt.Errorf("cannot verify review gate: %w", statusErr)
			}
			if !reviewReadyAllowed(state) {
				return fmt.Errorf("review gate closed for %s #%s: %s %s", name, args[0], state.State, state.Verdict)
			}
		}
		var action *exec.Cmd
		if forge == "github" {
			action = exec.Command("gh", "pr", "ready", args[0], "--repo", slug)
		} else {
			action = exec.Command("bb", "pr", "ready", args[0])
			action.Dir = worktree
		}
		if !*asJSON {
			action.Stdout = os.Stdout
		}
		action.Stderr = os.Stderr
		if err := action.Run(); err != nil {
			return err
		}
		if *asJSON {
			return emit(envelope{OK: true, Data: map[string]any{"repo": name, "pr": args[0], "override": authorization != ""}, Summary: "PR marked ready"}, true)
		}
		return nil
	}
	review.AddCommand(ready)
	root.AddCommand(review)
}

func reviewReadyAllowed(state wtc.ReviewStatus) bool {
	return state.State == "current" && state.Blockers != nil && *state.Blockers == 0 &&
		(state.Verdict == "pass" || state.Verdict == "pass-with-notes")
}
