package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"taba.sh/taba/internal/branding"
	"taba.sh/taba/internal/envcompat"
	"taba.sh/taba/internal/openclaw"
	"taba.sh/taba/internal/wtc"
)

func currentHost() openclaw.HostKind { return openclaw.DetectHost(os.Getenv) }

// readBodyFile reads a pull request description from path, or from stdin
// for -.
func readBodyFile(path string) ([]byte, error) {
	if path == "-" {
		return io.ReadAll(os.Stdin)
	}
	return os.ReadFile(path)
}

// printPlan writes the plan to stdout as the command's whole output, and what
// to do with it to stderr, where it reaches the agent without getting in the
// way of whatever parses the JSON.
func printPlan(plan openclaw.Plan) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(plan); err != nil {
		return err
	}
	for _, in := range plan.Intents {
		who := map[openclaw.Actor]string{
			openclaw.ActorParent:      "in the collection's session",
			openclaw.ActorRepoSession: "in this repository's session",
			openclaw.ActorCurrent:     "in this session",
		}[in.Actor]
		if in.Action == openclaw.ActionPublish {
			how := " with the skill for " + string(in.Forge)
			if in.Tool != "" {
				how = " with the " + in.Tool + " tool and its args, unchanged"
			}
			fmt.Fprintf(os.Stderr, branding.Text("{{command}}: nothing was done. Publishing is the person's: tell them it is ready to push with ↑ in the collection card. Only if they asked you to publish, carry out this intent %s%s.\n"), who, how)
		} else if in.Tool != "" {
			fmt.Fprintf(os.Stderr, branding.Text("{{command}}: nothing was done. Call the %s tool %s with this intent's args, unchanged.\n"), in.Tool, who)
		} else {
			fmt.Fprintf(os.Stderr, branding.Text("{{command}}: nothing was done. Carry out this %s intent %s with the skill for %s.\n"), in.Action, who, in.Forge)
		}
	}
	for _, w := range plan.Warnings {
		fmt.Fprintln(os.Stderr, branding.Text("{{command}}: warning:"), w)
	}
	return nil
}

func addHostCommands(root *cobra.Command, asJSON *bool) {
	host := &cobra.Command{Use: "host", Short: "Print what wtc is running under: openclaw, herdr or terminal", Args: cobra.NoArgs}
	host.RunE = func(cmd *cobra.Command, args []string) error {
		h := string(currentHost())
		return emit(envelope{OK: true, Data: map[string]string{"host": h}, Summary: h}, *asJSON)
	}
	root.AddCommand(host)

	var noPullRequests bool
	checkouts := &cobra.Command{
		Use:   "checkouts [path ...]",
		Short: "Print the status of checkouts as JSON: branch, ahead, pending changes, pull request",
		Long: `Describes each checkout (default: the current directory) the way a gateway
plugin shows a collection whose repositories live in separate sessions:
branch, commits ahead of and behind the base, pending changes, whether the
branch was published, and its pull request. It reads and changes nothing.`,
	}
	checkouts.Flags().BoolVar(&noPullRequests, "no-pull-requests", false, "Do not ask the forge for pull requests")
	checkouts.RunE = func(cmd *cobra.Command, args []string) error {
		if len(args) == 0 {
			args = []string{"."}
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(openclaw.CheckoutsStatus(args, !noPullRequests))
	}
	root.AddCommand(checkouts)

	var opt openclaw.PublishOptions
	var dest string
	publish := &cobra.Command{
		Use:   "publish",
		Short: "Under OpenClaw: check this session's worktree and print the intent that publishes it",
		Long: `Under OpenClaw a command in the agent's shell holds no git credential and
cannot reach the gateway. publish therefore pushes nothing. It checks that the
session's managed worktree is safe to publish and prints the intent for the
agent to carry out: the github_publish tool on GitHub, a push and a pull
request through a forge skill elsewhere.

It refuses when history that was already published has been rewritten, when a
merge or rebase is unfinished, when files that look like credentials would be
committed, and outside a gateway-managed worktree.

--title and --body (or --body-file) are recorded for the branch, in the
worktree's git directory: the person's push from the collection card opens the
pull request with them. One left out keeps what was recorded before.`,
		Args: cobra.NoArgs,
	}
	var bodyFile string
	publish.Flags().StringVar(&opt.Title, "title", "", "Pull request title, one line")
	publish.Flags().StringVar(&opt.Body, "body", "", "Pull request description (Markdown)")
	publish.Flags().StringVar(&bodyFile, "body-file", "", "Read the pull request description from a file, - for stdin")
	publish.Flags().StringVar(&dest, "dest", "", "Branch the pull request goes into (default: origin's default branch)")
	publish.Flags().BoolVar(&opt.AllowSensitive, "allow-sensitive", false, "Publish uncommitted files even when their names look like credentials")
	publish.RunE = func(cmd *cobra.Command, args []string) error {
		if h := currentHost(); h != openclaw.HostOpenClaw {
			return fmt.Errorf(branding.Text("{{command}} publish hands a publication to an OpenClaw gateway, and this is a %s session. Push and open the pull request with the wtc-pr or wtc-draft-pr skill"), h)
		}
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		if bodyFile != "" {
			if opt.Body != "" {
				return fmt.Errorf("pass --body or --body-file, not both")
			}
			data, err := readBodyFile(bodyFile)
			if err != nil {
				return err
			}
			opt.Body = string(data)
		}
		if err := openclaw.CheckProposal(strings.TrimSpace(opt.Title), strings.TrimSpace(opt.Body)); err != nil {
			return err
		}
		checkout, err := openclaw.OpenCheckout(cwd, dest)
		if err != nil {
			return err
		}
		plan, err := checkout.PlanPublish(opt)
		if err != nil {
			return err
		}
		if checkout.Private {
			plan.Warnings = append(plan.Warnings, "A sandbox's copy cannot record the title and description for the collection card: give them to github_publish when the person asks you to publish.")
		} else if proposal, err := checkout.Propose(opt.Title, opt.Body); err != nil {
			return fmt.Errorf("cannot record the title and description: %w", err)
		} else if proposal == nil || proposal.Title == "" || proposal.Body == "" {
			what := "No title and description are"
			switch {
			case proposal != nil && proposal.Title != "":
				what = "No description is"
			case proposal != nil && proposal.Body != "":
				what = "No title is"
			}
			plan.Warnings = append(plan.Warnings, what+" recorded for the pull request yet: pass --title and --body-file (what changed and why, how it was checked, what is left; the repository's pull request template if it has one). Without a title the pull request opens as \"Publish "+checkout.Branch()+"\".")
		}
		return printPlan(plan)
	}
	root.AddCommand(publish)
	addProjectsCommand(root, asJSON)
}

// addRepoIntents is add-repo under OpenClaw: one sessions_spawn intent per
// repository, for the collection's session to carry out. The gateway creates
// the worktrees, so that it owns them and can publish them. A collection with
// a harness resolves names through the harness's registry, and gets a
// worktree of the harness itself first; a worker never does.
func addRepoIntents(repos []string, worktree, baseRef, harnessFlag string, separate, adHoc bool, worker, work string) error {
	sandbox := openclaw.GatewayFromEnv(os.Getenv) != nil
	// A sandbox emits intents only. Gateway base and project records must
	// never be written into a sandbox's private filesystem.
	recordsGatewayState := !sandbox
	if sandbox && worker != "" {
		return errors.New("prepare worker plans in the gateway collection session: a read-only sandbox cannot record or inherit their publication base")
	}
	if worktree == "" {
		if cwd, err := os.Getwd(); err == nil {
			worktree = openclaw.CurrentWorktree(cwd)
		}
	}
	if worktree == "" {
		worktree = envcompat.Getenv("WTC_COLLECTION")
	}
	if worktree == "" {
		return errors.New("which worktree name? Pass --worktree <issue-id>-<slug>: every repository of the collection gets a worktree of that name, on branch " + openclaw.BranchPrefix + "<name>")
	}
	if !openclaw.ValidWorktreeName(worktree) {
		return fmt.Errorf("%q is not a valid OpenClaw worktree name (lowercase letters, digits and hyphens, at most 64)", worktree)
	}
	// A base names a branch: of origin, or for a worker also one that exists
	// only in the gateway's clone, such as another worker's.
	baseFlag := strings.TrimPrefix(baseRef, "origin/")
	// A worker works in a collection of its own, task--worker, through the
	// task's harness but without a worktree of it.
	task, target := worktree, worktree
	if worker != "" {
		if separate {
			return errors.New("a worker is a session the task's session spawns; --separate and --worker do not go together")
		}
		if strings.TrimSpace(work) == "" {
			return errors.New("what should the worker do? Pass --work \"<the part of the task it takes>\"")
		}
		var err error
		if target, err = openclaw.WorkerWorktree(task, worker); err != nil {
			return err
		}
	}
	adds, harness, err := harnessRepos(repos, task, baseFlag, harnessFlag, adHoc, worker != "")
	if err != nil {
		return err
	}
	var intents []openclaw.Intent
	var projects []string
	for _, add := range adds {
		arg, branch := add.arg, add.branch
		base := ""
		if branch != "" {
			base = "origin/" + branch
		}
		repo, project, gitURL, err := openclaw.ProjectSource(arg)
		if err != nil {
			return err
		}
		instructions := "You own the " + repo + " worktree of collection " + target + branding.Text(". The collection's session works in it; do nothing until it asks you to. When asked to publish, run {{command}} publish in your worktree and carry out its plan.")
		if worker != "" {
			instructions = workerTask(target, repo, work)
		}
		if sandbox && branch != "" && (add.project == "" || add.destination == "" || branch != add.destination) {
			return fmt.Errorf("%s needs a gateway base record for %s: prepare this add-repo plan in the gateway collection session; the read-only sandbox cannot change its publication destination", repo, branch)
		}
		if sandbox && gitURL != "" {
			// Discovery can identify an existing gateway project, but never
			// creates a clone or changes a base. These are intents only;
			// sessions_spawn still applies the gateway's own permissions.
			if add.project != "" {
				project, gitURL = add.project, ""
			} else if openclaw.ParseOrigin(gitURL).Forge != openclaw.ForgeGitHub || worker != "" || !separate {
				return fmt.Errorf("%s cannot be resolved from this sandbox's allowed project metadata. Use the registered project name or prepare this add-repo plan in the gateway collection session; listing grants no clone permission", repo)
			} else {
				instructions = "Work on " + repo + " for " + target + ". Wait for instructions from the collection's session."
			}
		} else if !sandbox && gitURL != "" {
			origin := openclaw.ParseOrigin(gitURL)
			if dir := openclaw.GatewayProjectDir(gatewayStateDir(), origin); dir != "" {
				project, gitURL = filepath.Base(dir), ""
				if branch != "" {
					// What the worktree is compared with, caught up from and
					// published to, not only where it starts.
					if err := openclaw.RecordBase(dir, target, branch); err != nil {
						return fmt.Errorf("recording %s as the base of %s: %w", branch, repo, err)
					}
				}
			} else if origin.Forge != openclaw.ForgeGitHub {
				// The gateway clones GitHub repositories only.
				return fmt.Errorf("%s is not on the gateway yet. Ask the person to add %s with Add repository in the Collection panel, then run this again", repo, arg)
			} else if worker != "" {
				return fmt.Errorf("%s is not on the gateway yet, and a worker needs it there. Ask the person to add %s with Add repository in the Collection panel, then run this again", repo, arg)
			} else if separate {
				instructions = "Work on " + repo + " for " + target + ". Wait for instructions from the collection's session."
			} else {
				return fmt.Errorf("%s is not on the gateway yet. Ask the person to add %s with Add repository in the Collection panel, then run this again. Only if they want %s as a conversation of its own: --separate", repo, arg, repo)
			}
		}
		if recordsGatewayState && gitURL == "" {
			if dir := openclaw.GatewayProjectDirByName(gatewayStateDir(), project); dir != "" {
				project = filepath.Base(dir)
			}
		}
		if recordsGatewayState && gitURL == "" && branch != "" {
			if dir := openclaw.GatewayProjectDirByName(gatewayStateDir(), project); dir != "" {
				if err := openclaw.RecordBase(dir, target, branch); err != nil {
					return fmt.Errorf("recording %s as the base of %s: %w", branch, repo, err)
				}
			}
		}
		if recordsGatewayState && worker != "" && baseFlag != "" {
			// An explicit base that is a branch only here, another worker's or
			// the task's before it is published, is where the worker starts.
			// It is still measured and caught up against the task's base.
			if dir := openclaw.GatewayProjectDirByName(gatewayStateDir(), project); dir != "" && openclaw.HasBranch(dir, baseFlag) && !openclaw.HasRemoteBranch(dir, baseFlag) {
				base = baseFlag
				if err := openclaw.RecordBase(dir, target, openclaw.RecordedBase(dir, task)); err != nil {
					return fmt.Errorf("recording the base of %s: %w", repo, err)
				}
			}
		}
		if recordsGatewayState && worker != "" && baseFlag == "" {
			// A worker builds on the task's work so far, when the task has a
			// branch in this repository, and is measured and caught up
			// against the task's base.
			if dir := openclaw.GatewayProjectDirByName(gatewayStateDir(), project); dir != "" {
				if taskBase := openclaw.RecordedBase(dir, task); taskBase != "" {
					if err := openclaw.RecordBase(dir, target, taskBase); err != nil {
						return fmt.Errorf("recording %s as the base of %s: %w", taskBase, repo, err)
					}
					base = "origin/" + taskBase
				}
				if openclaw.HasBranch(dir, openclaw.Branch(task)) {
					base = openclaw.Branch(task)
				}
			}
		}
		in, err := openclaw.AddRepo(repo, project, gitURL, target, base, instructions)
		if err != nil {
			return err
		}
		if worker != "" {
			in = openclaw.Worker(in)
		}
		intents = append(intents, in)
		if project != "" && !add.resolved {
			projects = append(projects, project)
		}
	}
	if worker == "" && recordsGatewayState {
		// Only a plan that can be carried out binds the collection: to its
		// harness, or, when the person chose none, to none, so that a later
		// add-repo does not bind it to a harness after all.
		if harness != nil {
			if err := wtc.RecordGatewayHarness(openclaw.NewCollectionFolder(gatewayStateDir(), worktree, harness.Name), harness.Name, harness.Remote); err != nil {
				return err
			}
		} else if harnessFlag == openclaw.NoHarness {
			if err := wtc.RecordGatewayHarness(openclaw.NewCollectionFolder(gatewayStateDir(), worktree, ""), openclaw.NoHarness, ""); err != nil {
				return err
			}
		}
	}
	plan := openclaw.NewPlan(intents...)
	if len(projects) > 0 {
		plan.Warnings = append(plan.Warnings, "Each of "+strings.Join(projects, ", ")+" must be a project on the gateway under that name. For a GitHub repository that is not, pass owner/name instead: the gateway clones it.")
	}
	return printPlan(plan)
}

// workerTask is what a worker's session is told: the part of the task it
// takes, and how it hands its work back.
func workerTask(worktree, repo, work string) string {
	return "You are a worker on " + repo + ", in collection " + worktree + ", on branch " + openclaw.Branch(worktree) + ". Your part of the task: " + strings.TrimSpace(work) +
		branding.Text("\n\nWork, build, test and commit only in your own worktree. Never publish, push or rebase, and do not edit other checkouts. When you are done, or stuck, end with what you did, the commits, and what is left: the task's session merges your branch with {{command}} catch-up --from, and publishes.")
}

// repoToAdd is one repository add-repo spawns a worktree of: arg as
// ProjectSource reads it, and the branch it is based on, or "" for the
// gateway's default. resolved marks a remote from a harness.
type repoToAdd struct {
	arg, branch string
	destination string // existing gateway destination; no base record is written by a sandbox
	project     string // existing gateway project ID, supplied by permitted sandbox metadata
	resolved    bool
}

// harnessRepos chooses the collection's harness and resolves the
// repositories through it: a name in its registry becomes that entry's
// remote, based on its default_ref unless --base says otherwise. When the
// collection has no worktree of the harness yet, it comes first, except for
// a worker.
func harnessRepos(repos []string, worktree, branch, flag string, adHoc, worker bool) ([]repoToAdd, *openclaw.Harness, error) {
	plain := func() []repoToAdd {
		var adds []repoToAdd
		for _, r := range repos {
			adds = append(adds, repoToAdd{arg: r, branch: branch})
		}
		return adds
	}
	cfg, registries, recorded, err := harnessProjectContext(worktree)
	if err != nil {
		return nil, nil, err
	}
	if openclaw.GatewayFromEnv(os.Getenv) == nil {
		if folder := openclaw.CollectionFolder(gatewayStateDir(), worktree); folder != "" {
			if recorded, _, err = wtc.GatewayHarness(folder); err != nil {
				return nil, nil, err
			}
		}
	}
	registryOf := func(h openclaw.Harness) harnessRegistry {
		if r, ok := registries[h.Name]; ok {
			return r
		}
		r := readHarnessRegistry(h, worktree)
		registries[h.Name] = r
		return r
	}
	// A repository counts by its registry name, or by a URL or owner/name
	// that is a registered remote.
	lists := func(h openclaw.Harness, r string) bool {
		reg := registryOf(h)
		if _, ok := reg.find(r); ok {
			return true
		}
		_, _, gitURL, err := openclaw.ProjectSource(r)
		if err != nil || gitURL == "" {
			return false
		}
		_, ok := reg.findRemote(gitURL)
		return ok
	}
	h, err := openclaw.ChooseHarness(cfg, flag, recorded, repos, lists)
	if err != nil || h == nil {
		return plain(), nil, err
	}
	reg := registryOf(*h)
	if reg.err != nil {
		return nil, nil, reg.err
	}
	var adds []repoToAdd
	if !reg.hasHarnessWorktree() && !worker {
		adds = append(adds, repoToAdd{arg: h.Remote, branch: h.Ref, resolved: true, project: reg.projectID, destination: reg.destination})
	}
	var unlisted []string
	for _, r := range repos {
		add := repoToAdd{arg: r, branch: branch}
		entry, ok := reg.find(r)
		if !ok {
			if _, _, gitURL, err := openclaw.ProjectSource(r); err == nil && gitURL != "" {
				entry, ok = reg.findRemote(gitURL)
			}
		}
		if ok {
			add.arg, add.resolved = entry.Remote, true
			add.project = reg.projectIDs[entry.Name]
			add.destination = reg.destinations[entry.Name]
			if add.branch == "" {
				add.branch = strings.TrimPrefix(entry.DefaultRef, "origin/")
			}
		}
		url := add.arg
		if _, _, gitURL, err := openclaw.ProjectSource(add.arg); err == nil && gitURL != "" {
			url = gitURL
		}
		if openclaw.SameRepository(url, h.Remote) {
			if !worker {
				// The harness is added once, above, or is there already.
				continue
			}
			if add.branch == "" {
				// A worker on the harness starts where the harness is configured
				// to, unless the task has a branch or base there.
				add.branch = h.Ref
			}
		} else if !ok {
			unlisted = append(unlisted, r)
		}
		adds = append(adds, add)
	}
	// A worker takes part of the task, whose session already decided about
	// its repositories.
	if len(unlisted) > 0 && !adHoc && !worker {
		// Another wtc project may list them: work on it belongs in a
		// conversation of its own there, never in this collection.
		elsewhere := map[string][]string{}
		for _, other := range cfg.Harnesses {
			if other.Name == h.Name {
				continue
			}
			for _, r := range unlisted {
				if lists(other, r) {
					elsewhere[other.Name] = append(elsewhere[other.Name], r)
				}
			}
		}
		return nil, nil, unlistedError(*h, worktree, repos, unlisted, recorded == "", reg.hasHarnessWorktree(), elsewhere)
	}
	return adds, h, nil
}

// unlistedError asks the agent to ask the person what to do with
// repositories the collection's harness does not list: add them beside the
// harness ad hoc, register them in it first, or give them a collection of
// their own without a harness. Its add-repo commands for this collection
// run as printed: they name it and, while it has not recorded its harness
// yet, the harness this add-repo chose. 1 is one such command, for every
// repository asked for. 2 adds the harness first when the collection has no
// worktree of it, then, after the registry edit, every repository. 3 adds
// the unlisted ones to another collection, whose name the person picks for
// <another-name>, and the listed ones here.
//
// elsewhere names the other wtc projects that list some of them, by project:
// 4 suggests working on those there, in a conversation of its own.
func unlistedError(h openclaw.Harness, worktree string, repos, unlisted []string, unrecorded, hasWorktree bool, elsewhere map[string][]string) error {
	here := " --worktree " + worktree
	if unrecorded {
		here += " --project " + h.Name
	}
	all := strings.Join(repos, " ")
	register := fmt.Sprintf(branding.Text("run `{{command}} add-repo %s%s` first, then "), h.Remote, here)
	if hasWorktree {
		register = ""
	}
	var listed []string
	for _, r := range repos {
		if !slices.Contains(unlisted, r) {
			listed = append(listed, r)
		}
	}
	separate := fmt.Sprintf(branding.Text("{{command}} add-repo %s --worktree <another-name> --project %s"), strings.Join(unlisted, " "), openclaw.NoHarness)
	if len(listed) > 0 {
		separate += fmt.Sprintf(branding.Text(", and here {{command}} add-repo %s%s"), strings.Join(listed, " "), here)
	}
	err := fmt.Errorf("wtc project %s does not list %s. Ask the person which they want:\n"+branding.Text("  1. ad hoc beside the project's repositories, in this collection: {{command}} add-repo %s%s --ad-hoc\n")+branding.Text("  2. registered in the project: %sadd it to .harness-repos.yml in the collection's worktree of its harness %s and commit that (it is published like any repository), then {{command}} add-repo %s%s\n")+
		"  3. a collection of its own, in an ad-hoc wtc project: %s",
		h.Name, strings.Join(unlisted, ", "), all, here, register, h.Name, all, here, separate)
	if len(elsewhere) > 0 {
		var projects []string
		for p := range elsewhere {
			projects = append(projects, p)
		}
		sort.Strings(projects)
		var in []string
		for _, p := range projects {
			in = append(in, fmt.Sprintf(branding.Text("in a new conversation for wtc project %s, which lists %s: the person starts it, and there {{command}} add-repo %s --worktree <name> --project %s"), p, strings.Join(elsewhere[p], ", "), strings.Join(elsewhere[p], " "), p))
		}
		err = fmt.Errorf("%w\n  4. usually the right one for work on that project: %s", err, strings.Join(in, "; or "))
	}
	return err
}

// harnessRegistry is a harness's registry as a collection sees it: from its
// worktree of the harness when it has one, else from the gateway's clone of
// the harness at its configured ref. Local resolution retains the path;
// sandbox snapshots carry only presence and safe metadata.
type harnessRegistry struct {
	originAliases     map[string][]string
	registeredRemotes map[string]string
	destination       string
	destinations      map[string]string
	hasWorktree       bool // current collection presence without exposing the gateway path
	projectID         string
	projectIDs        map[string]string
	repos             []wtc.Repo
	worktree          string
	err               error
}

func (r harnessRegistry) hasHarnessWorktree() bool { return r.worktree != "" || r.hasWorktree }

func (r harnessRegistry) find(name string) (wtc.Repo, bool) {
	if !metadataRepoPart.MatchString(name) || name == "." || name == ".." {
		return wtc.Repo{}, false
	}
	for _, repo := range r.repos {
		if repo.Name == name {
			return repo, true
		}
	}
	var named *wtc.Repo
	for _, repo := range r.repos {
		if repo.Answers(name) {
			if named != nil {
				return wtc.Repo{}, false
			}
			copy := repo
			named = &copy
		}
	}
	if named != nil {
		return *named, true
	}
	var found *wtc.Repo
	for _, repo := range r.repos {
		origin := openclaw.ParseOrigin(repo.Remote)
		matches := origin.Forge != openclaw.ForgeUnknown && strings.EqualFold(origin.Repo, name)
		for _, remote := range r.originAliases[repo.Name] {
			matches = matches || strings.EqualFold(openclaw.ParseOrigin(remote).Repo, name)
		}
		if openclaw.GatewayFromEnv(os.Getenv) == nil {
			if dir := openclaw.GatewayProjectDir(gatewayStateDir(), origin); dir != "" {
				for _, o := range openclaw.GatewayProjectOrigins(dir) {
					matches = matches || strings.EqualFold(o.Repo, name)
				}
			}
		}
		if matches {
			if found != nil {
				return wtc.Repo{}, false
			}
			copy := repo
			found = &copy
		}
	}
	if found != nil {
		return *found, true
	}
	return wtc.Repo{}, false
}

func (r harnessRegistry) findRemote(url string) (wtc.Repo, bool) {
	// A registered remote takes precedence over another clone's old URL.
	var exact *wtc.Repo
	for _, repo := range r.repos {
		registered := r.registeredRemotes[repo.Name]
		if registered == "" {
			registered = repo.Remote
		}
		if openclaw.SameRepository(registered, url) {
			if exact != nil {
				return wtc.Repo{}, false
			}
			copy := repo
			exact = &copy
		}
	}
	if exact != nil {
		return *exact, true
	}
	var found *wtc.Repo
	for _, repo := range r.repos {
		matches := false
		// Logical registry names never grant a forge URL. These aliases come
		// only from the operator-verified clone record in permitted metadata.
		for _, alias := range r.originAliases[repo.Name] {
			matches = matches || openclaw.SameRepository(alias, url)
		}
		if !matches && openclaw.GatewayFromEnv(os.Getenv) == nil {
			dir := openclaw.GatewayProjectDir(gatewayStateDir(), openclaw.ParseOrigin(repo.Remote))
			matches = dir != "" && openclaw.CheckoutOf(dir, url)
		}
		if matches {
			if found != nil {
				return wtc.Repo{}, false
			}
			copy := repo
			found = &copy
		}
	}
	if found != nil {
		return *found, true
	}
	return wtc.Repo{}, false
}

func readHarnessRegistry(h openclaw.Harness, worktree string) harnessRegistry {
	var r harnessRegistry
	var data []byte
	all, _ := openclaw.ManagedCollections(gatewayStateDir())
	for _, dir := range all[worktree] {
		if openclaw.CheckoutOf(dir, h.Remote) {
			r.worktree = dir
			data, r.err = os.ReadFile(filepath.Join(dir, ".harness-repos.yml"))
			break
		}
	}
	if r.worktree == "" {
		dir := openclaw.GatewayProjectDir(gatewayStateDir(), openclaw.ParseOrigin(h.Remote))
		if dir == "" {
			r.err = fmt.Errorf("the harness of wtc project %s is not on the gateway yet. Ask the person to add %s with Add repository in the Collection panel, then run this again", h.Name, h.Remote)
			return r
		}
		if data, r.err = openclaw.FileAt(dir, "origin/"+h.Ref, ".harness-repos.yml"); r.err != nil {
			data, r.err = openclaw.FileAt(dir, h.Ref, ".harness-repos.yml")
		}
	}
	if r.err != nil {
		r.err = fmt.Errorf("the harness of wtc project %s has no .harness-repos.yml to read at %s, so wtc cannot use it; check %s.harnesses: %w", h.Name, h.Ref, openclaw.PluginConfigKey, r.err)
		return r
	}
	reg, err := wtc.ParseRegistry(data)
	if err != nil {
		r.err = fmt.Errorf("harness %s: %w", h.Name, err)
		return r
	}
	r.repos = reg.Repos
	return r
}

// runOpenClaw runs the openclaw CLI; tests replace it.
var runOpenClaw = func(args ...string) (string, error) {
	if _, err := exec.LookPath("openclaw"); err != nil {
		return "", errNoOpenClaw
	}
	out, err := exec.Command("openclaw", args...).CombinedOutput()
	return string(out), err
}

var errNoOpenClaw = errors.New("openclaw not found")

// gatewayConfig is the wtc plugin's config on this gateway. Without the
// openclaw CLI, or with nothing set, there are no harnesses.
func gatewayConfig() (openclaw.GatewayConfig, error) {
	out, err := runOpenClaw("config", "get", openclaw.PluginConfigKey, "--json")
	if errors.Is(err, errNoOpenClaw) || (err != nil && strings.Contains(out, "unset")) {
		return openclaw.GatewayConfig{}, nil
	}
	if err != nil {
		return openclaw.GatewayConfig{}, fmt.Errorf("reading %s: %v: %s", openclaw.PluginConfigKey, err, strings.TrimSpace(out))
	}
	return openclaw.ParseGatewayConfig(out)
}

// catchUpCheckout is catch-up under OpenClaw. With from it merges another
// branch of the repository by its name: a sub-session's, a worker's, or the
// collection's into a sub-session. In a worker's checkout without --dest it
// merges the collection's branch. Otherwise it merges the destination branch,
// or moves a detached worktree to its tip. No rebase and no push.
func catchUpCheckout(dest, from string, asJSON bool) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	checkout, err := openclaw.OpenCatchUpCheckout(cwd, dest)
	if err != nil {
		return err
	}
	var result openclaw.CatchUp
	switch {
	case from != "":
		result, err = checkout.MergeFrom(from)
	case dest == "" && checkout.IsWorker():
		// A worker catches up with its task, the branch it merges into.
		result, err = checkout.CatchUpWithTask()
	default:
		result, err = checkout.CatchUp()
	}
	result.Worker = checkout.IsWorker()
	if err != nil {
		return err
	}
	return emit(envelope{OK: true, Data: result, Summary: catchUpSummary(result)}, asJSON)
}

// catchUpSummary says what a catch-up under OpenClaw did. A fast-forward of
// the destination leaves the branch at its base with nothing to publish; a
// worker's commits, even fast-forwarded, are the task's to publish.
func catchUpSummary(result openclaw.CatchUp) string {
	// A worker's catch-up merges its task's branch, which is its own to merge.
	if task := openclaw.TaskOf(strings.TrimPrefix(result.Branch, openclaw.BranchPrefix)); result.Worker && result.From == openclaw.Branch(task) {
		if result.Merged {
			return fmt.Sprintf("merged the task's %s into the worker's %s", result.From, result.Branch)
		}
		return fmt.Sprintf("%s %s", result.Branch, result.Note)
	}
	switch {
	case result.ReturnedToTip:
		return fmt.Sprintf("returned to origin/%s's tip, detached; %s", result.Dest, result.Note)
	case result.Branch == "" && result.Merged:
		return fmt.Sprintf("detached at origin/%s's tip again: no work of its own, nothing to publish", result.Dest)
	case result.Branch == "":
		return fmt.Sprintf("detached, %s", result.Note)
	case result.Merged && result.FastForward && result.From == "":
		return fmt.Sprintf("%s is at origin/%s again: nothing of its own on it, nothing to publish", result.Branch, result.Dest)
	case result.Merged && result.From != "":
		return fmt.Sprintf("merged %s into %s; the person publishes it with ↑ when the task is ready", result.From, result.Branch)
	case result.Merged && result.Worker:
		return fmt.Sprintf("merged origin/%s into %s; it reaches the forge through the task", result.Dest, result.Branch)
	case result.Merged:
		return fmt.Sprintf(branding.Text("merged origin/%s into %s; publish to send the merge ({{command}} publish)"), result.Dest, result.Branch)
	}
	return fmt.Sprintf("%s %s", result.Branch, result.Note)
}

// gatewayStatus is status under OpenClaw. A collection there is no folder:
// it is the managed worktrees of one name, one per repository, each owned by
// a session of its own. Without a name it shows every collection.
func gatewayStatus(names, subs []string, asJSON bool) error {
	all, err := openclaw.ManagedCollections(gatewayStateDir())
	if err != nil {
		return err
	}
	if len(names) == 0 {
		for name := range all {
			names = append(names, name)
		}
		sort.Strings(names)
	}
	result := map[string]gatewayCollection{}
	for _, name := range names {
		openclaw.FetchBases(all[name], openclaw.FetchBaseInterval)
		col := gatewayCollection{Collection: openclaw.CheckoutsStatus(all[name], true), Services: []wtc.RuntimeItem{}}
		col.Folder, col.Services, col.Error = linkGatewayFolder(name, col.Repos)
		col.Harness = gatewayHarness(col.Folder, all[name])
		col.Workers = gatewayWorkers(name, all)
		if len(subs) > 0 {
			col.SubSessions = openclaw.WorkerRepos(name, all[name], subs)
		}
		result[name] = col
	}
	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(result)
	}
	if len(names) == 0 {
		fmt.Println(branding.Text("No collections: the gateway has no managed worktrees yet. {{command}} add-repo <repo> --worktree <name> starts one."))
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	for i, name := range names {
		if i > 0 {
			fmt.Fprintln(w)
		}
		fmt.Fprintf(w, "%s\n", name)
		repos := result[name].Repos
		if folder := result[name].Folder; folder != "" {
			fmt.Fprintf(w, "  folder %s\n", folder)
		}
		if h := result[name].Harness; h != nil {
			fmt.Fprintf(w, "  %s\n", h)
		}
		for _, wk := range result[name].Workers {
			fmt.Fprintf(w, "  %s\n", wk)
		}
		if len(repos) == 0 {
			fmt.Fprintln(w, "  (no repositories)")
			continue
		}
		fmt.Fprintln(w, "  REPO\tBRANCH\tAGAINST BASE\tTO PUBLISH\tPR")
		for _, r := range repos {
			if r.Error != "" {
				fmt.Fprintf(w, "  %s\t%s\t\t%s\t\n", filepath.Base(filepath.Dir(r.Path)), r.Branch, r.Error)
				continue
			}
			base := fmt.Sprintf("%d ahead, %d behind", r.Ahead, r.Behind)
			pending := "nothing"
			if n := r.Dirty + r.Untracked; n > 0 || r.Unpublished > 0 {
				pending = fmt.Sprintf("%d files, %d commits", n, r.Unpublished)
			}
			pr := "—"
			if p := r.PullRequest; p != nil {
				pr = fmt.Sprintf("#%d %s", p.Number, p.State)
				if p.Draft && p.State == "open" {
					pr = fmt.Sprintf("#%d draft", p.Number)
				}
			}
			fmt.Fprintf(w, "  %s\t%s\t%s\t%s\t%s\n", r.Repo, r.Branch, base, pending, pr)
		}
		for _, svc := range result[name].Services {
			fmt.Fprintf(w, "  service %s\t%s\t%s\t%s\t\n", svc.Path, svc.Kind, svc.State, svc.URL)
		}
		if e := result[name].Error; e != "" {
			fmt.Fprintf(w, "  folder: %s\n", e)
		}
	}
	return w.Flush()
}

// gatewayCollection is one collection as wtc status shows it under OpenClaw.
type gatewayCollection struct {
	openclaw.Collection
	// Folder holds the collection as one folder: a link per repository to its
	// managed worktree. Services run there.
	Folder string `json:"folder,omitempty"`
	// Harness is the collection's harness, whose AGENTS.md and
	// instructions apply to the work in it.
	Harness *gatewayHarnessStatus `json:"harness,omitempty"`
	// Workers are the collections task--worker of this one's workers.
	Workers []gatewayWorker `json:"workers,omitempty"`
	// SubSessions compares the worktrees given with --sub, a sub-session's
	// whatever their names, with the collection's checkouts.
	SubSessions []openclaw.WorkerRepo `json:"subSessions,omitempty"`
	Services    []wtc.RuntimeItem     `json:"services"`
	Error       string                `json:"error,omitempty"`
}

type gatewayHarnessStatus struct {
	Name   string `json:"name"`
	Remote string `json:"remote"`
	// Path is the collection's worktree of the harness, once it has one.
	Path string `json:"path,omitempty"`
}

// gatewayWorker is one worker of a task: its collection and, per
// repository, what the task's branch has not merged from it yet.
type gatewayWorker struct {
	Name       string                `json:"name"`
	Collection string                `json:"collection"`
	Repos      []openclaw.WorkerRepo `json:"repos"`
}

func (w gatewayWorker) String() string {
	var parts []string
	for _, r := range w.Repos {
		switch {
		case r.Error != "":
			parts = append(parts, r.Repo+": "+r.Error)
		case r.ToMerge == 0:
			parts = append(parts, r.Repo+" merged")
		default:
			parts = append(parts, fmt.Sprintf(branding.Text("%s %d to merge ({{command}} catch-up --from %s)"), r.Repo, r.ToMerge, w.Name))
		}
	}
	return "worker " + w.Name + ": " + strings.Join(parts, ", ")
}

// gatewayWorkers are the direct workers of task among the collections.
func gatewayWorkers(task string, all map[string][]string) []gatewayWorker {
	var workers []gatewayWorker
	for name, dirs := range all {
		short, ok := strings.CutPrefix(name, task+openclaw.WorkerSeparator)
		if !ok || short == "" || strings.Contains(short, openclaw.WorkerSeparator) {
			continue
		}
		workers = append(workers, gatewayWorker{Name: short, Collection: name, Repos: openclaw.WorkerRepos(task, all[task], dirs)})
	}
	sort.Slice(workers, func(i, j int) bool { return workers[i].Name < workers[j].Name })
	return workers
}

func (h *gatewayHarnessStatus) String() string {
	if h.Path == "" {
		return "wtc project " + h.Name + ", no worktree of its harness yet"
	}
	return "wtc project " + h.Name + ", harness at " + h.Path + ": read its AGENTS.md"
}

// gatewayHarness is the harness recorded for the collection folder, with its
// worktree among the collection's, or nil.
func gatewayHarness(folder string, worktrees []string) *gatewayHarnessStatus {
	name, remote, err := wtc.GatewayHarness(folder)
	if err != nil || name == "" || name == openclaw.NoHarness {
		return nil
	}
	h := &gatewayHarnessStatus{Name: name, Remote: remote}
	for _, dir := range worktrees {
		if openclaw.CheckoutOf(dir, remote) {
			h.Path = dir
			break
		}
	}
	return h
}

// linkGatewayFolder keeps the collection's folder in step with its worktrees
// and reads the services running for it.
func linkGatewayFolder(name string, repos []openclaw.CheckoutStatus) (string, []wtc.RuntimeItem, string) {
	var linked []wtc.LinkedRepo
	for _, r := range repos {
		if r.Error != "" || r.Repo == "" {
			continue
		}
		remote, err := exec.Command("git", "-C", r.Path, "remote", "get-url", "origin").Output()
		if err != nil {
			continue
		}
		base := r.Base
		if base == "" {
			base = "origin/main"
		}
		linked = append(linked, wtc.LinkedRepo{Name: r.Repo, Remote: strings.TrimSpace(string(remote)), DefaultRef: base, Path: r.Path})
	}
	// A collection whose first add-repo chose a harness has its folder by
	// now; one without a harness gets it here.
	folder := openclaw.NewCollectionFolder(gatewayStateDir(), name, "")
	c, err := wtc.LinkGatewayCollection(folder, linked)
	if err != nil {
		return folder, []wtc.RuntimeItem{}, err.Error()
	}
	services := c.RuntimeStatus()
	if services == nil {
		services = []wtc.RuntimeItem{}
	}
	return folder, services, ""
}

// refuseUnderGateway stops commands that make folder collections: under
// OpenClaw the gateway owns the worktrees, and a folder collection beside it
// is one it can neither see nor publish.
func refuseUnderGateway(command string) error {
	if currentHost() != openclaw.HostOpenClaw {
		return nil
	}
	return fmt.Errorf(branding.Text("{{command}} %s makes a folder collection, which the OpenClaw gateway cannot see or publish. Here a collection is managed worktrees of one name: {{command}} add-repo <owner/name> --worktree <name> starts one, and {{command}} status lists them ({{command}} docs instructions/openclaw.md)"), command)
}

// gatewayStateDir is where the gateway keeps its projects and worktrees.
func gatewayStateDir() string {
	if dir := os.Getenv("OPENCLAW_STATE_DIR"); dir != "" {
		return dir
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".openclaw")
}
