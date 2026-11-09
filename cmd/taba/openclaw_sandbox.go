package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"taba.sh/taba/internal/branding"
	"taba.sh/taba/internal/openclaw"
)

// addSandboxGitCommands adds what the plugin's git service runs on the
// gateway, in a sandboxed session's managed worktree. They print JSON.
func addSandboxGitCommands(oc *cobra.Command) {
	session := &cobra.Command{
		Use:    "sandbox-session",
		Short:  "Describe this managed worktree for its sandbox: branch, base and head",
		Args:   cobra.NoArgs,
		Hidden: true,
	}
	session.RunE = func(cmd *cobra.Command, args []string) error {
		s, err := openclaw.GatewaySession(".")
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(s)
	}
	var commit, bundle string
	deliver := &cobra.Command{
		Use:    "sandbox-deliver",
		Short:  "Move this managed worktree's branch to the commit its sandbox delivered",
		Args:   cobra.NoArgs,
		Hidden: true,
	}
	deliver.Flags().StringVar(&commit, "commit", "", "The session's branch as the sandbox has it")
	deliver.Flags().StringVar(&bundle, "bundle", "", "A git bundle with the commits the gateway lacks")
	deliver.RunE = func(cmd *cobra.Command, args []string) error {
		d, err := openclaw.AcceptSandboxBranch(".", commit, bundle)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(d)
	}
	oc.AddCommand(session, deliver, sandboxProjectsCommand())
}

// sandboxCatchUp is wtc catch-up in a sandbox that reaches the gateway's git
// service: the merge happens here, with history, and the gateway takes the
// branch.
func sandboxCatchUp(gw *openclaw.HTTPGateway, cont, asJSON bool) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	r, err := openclaw.SandboxCatchUpCheckout(cwd, gw, cont)
	if err != nil {
		return err
	}
	var summary []string
	switch {
	case len(r.Conflicts) > 0:
		summary = append(summary, fmt.Sprintf(branding.Text("merging origin/%s stopped on conflicts in: %s. Resolve them with git as in any merge (git log --merge, git show :1:<file>, :2:, :3:), git add each, then run {{command}} catch-up --continue. Do not rebase"), r.Base, strings.Join(r.Conflicts, ", ")))
	case r.Merged:
		summary = append(summary, fmt.Sprintf("merged origin/%s into %s", r.Base, r.Branch))
	case r.Note != "":
		summary = append(summary, r.Branch+" "+r.Note)
	}
	if r.Reset {
		summary = append(summary, "the gateway had committed since (a publication), so this checkout's history now starts from the gateway's branch; the files are unchanged")
	}
	if r.Delivered != nil && r.Delivered.Note == "" {
		summary = append(summary, fmt.Sprintf("the gateway's %s is now at %.12s (%d commits, %d merges); the person publishes it with ↑ when the task is ready", r.Delivered.Branch, r.Delivered.Head, r.Delivered.Commits, r.Delivered.Merges))
	}
	if len(r.Unstashed) > 0 {
		summary = append(summary, fmt.Sprintf("your uncommitted edits conflict with the merge in %s: resolve the markers there", strings.Join(r.Unstashed, ", ")))
	}
	return emit(envelope{OK: true, Data: r, Summary: strings.Join(summary, "\n")}, asJSON)
}

// sandboxChecks is wtc doctor in a sandbox: the gateway's git service
// answers for this session, and none of the credentials it knows that
// could push is here: forge tokens in the environment, GIT_ASKPASS, a
// credential helper and a gh login.
func sandboxChecks(gw *openclaw.HTTPGateway, getenv func(string) string, run func(name string, args ...string) (string, error)) []doctorCheck {
	fetch := doctorCheck{Name: "fetch", Required: true}
	if s, err := gw.Session(); err != nil {
		fetch.Detail = err.Error()
	} else {
		fetch.OK = true
		fetch.Detail = fmt.Sprintf("read-only from the gateway (%d repos: %s); %s merges origin/%s", len(s.Repos), strings.Join(s.Repos, ", "), s.Branch, s.Base)
	}
	push := doctorCheck{Name: "push", Required: true, OK: true, Detail: "none: the gateway publishes"}
	var found []string
	for _, name := range []string{"GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "BITBUCKET_TOKEN", "BITBUCKET_API_TOKEN", "GIT_ASKPASS"} {
		if getenv(name) != "" {
			found = append(found, name)
		}
	}
	if out, err := run("git", "config", "--get-all", "credential.helper"); err == nil && strings.TrimSpace(out) != "" {
		found = append(found, "credential.helper")
	}
	if _, err := run("gh", "auth", "token"); err == nil {
		found = append(found, "a gh login")
	}
	if len(found) > 0 {
		push.OK = false
		push.Detail = "a credential that may push is here: " + strings.Join(found, ", ") + ". A sandbox reads from the gateway and pushes nothing; remove it from the sandbox's environment"
	}
	projects := doctorCheck{Name: "projects"}
	if p, err := gw.Projects(); err != nil {
		projects.Detail = "gateway project discovery unavailable: " + err.Error()
	} else {
		projects.OK = true
		projects.Detail = fmt.Sprintf("read-only from the gateway (%d operator-allowed projects); listing grants no repository access", len(p.Projects))
	}
	return []doctorCheck{fetch, push, projects}
}
