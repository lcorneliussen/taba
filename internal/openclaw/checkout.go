package openclaw

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"taba.sh/taba/internal/branding"
)

// Checkout is a session's gateway-managed worktree, seen through plain git.
type Checkout struct {
	Dir      string
	Worktree string // the managed worktree's name; the branch is openclaw/<name>. Empty when Detached
	Origin   Origin
	Repo     string
	Dest     string // the branch a pull request goes into
	// Private is a checkout without an origin: the copy of the worktree a
	// sandbox gets. Only the gateway knows its repository and base.
	Private bool
	// Detached is a worktree on no branch, as one resting at its base with no
	// work yet is. Only catching up opens one: it moves to the base's tip when
	// the base contains it, and is refused with commits of its own.
	Detached bool
}

func git(dir string, args ...string) (string, error) {
	cmd, err := gitCommand(nil, dir, args...)
	if err != nil {
		return "", err
	}
	out, err := cmd.Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && len(exit.Stderr) > 0 {
			return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(string(exit.Stderr)))
		}
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return strings.TrimRight(string(out), "\n"), nil
}

func gitOK(dir string, args ...string) bool {
	_, err := git(dir, args...)
	return err == nil
}

// OpenCheckout reads the managed worktree dir is in. dest overrides the
// branch pull requests go into; empty means the base recorded for the
// worktree, else origin's default branch.
func OpenCheckout(dir, dest string) (*Checkout, error) {
	return openCheckout(dir, dest, false)
}

// OpenCatchUpCheckout is OpenCheckout for catching up, which also takes a
// detached worktree: catching one up moves it to the base's tip, or refuses
// when it has commits the base lacks.
func OpenCatchUpCheckout(dir, dest string) (*Checkout, error) {
	return openCheckout(dir, dest, true)
}

func openCheckout(dir, dest string, detachedOK bool) (*Checkout, error) {
	top, err := git(dir, "rev-parse", "--show-toplevel")
	if untrusted := (*UntrustedConfigError)(nil); errors.As(err, &untrusted) {
		return nil, err
	}
	if err != nil {
		return nil, errors.New("not in a git checkout: publishing and catching up work in the session that owns a repository")
	}
	branch, _ := git(top, "branch", "--show-current")
	name, managed := strings.CutPrefix(branch, BranchPrefix)
	detached := detachedOK && branch == ""
	if !detached && (!managed || !ValidWorktreeName(name)) {
		at := "a detached HEAD"
		if branch != "" {
			at = "branch " + branch
		}
		return nil, fmt.Errorf("%s is on %s, not on an %s<name> branch: the gateway only publishes a session's managed worktree. Ask the collection's session to add this repository", top, at, BranchPrefix)
	}
	c := &Checkout{Dir: top, Worktree: name, Dest: dest, Detached: detached}
	if url, err := git(top, "remote", "get-url", "origin"); err == nil {
		c.Origin = ParseOrigin(url)
	} else {
		c.Origin = Origin{Forge: ForgeUnknown}
		c.Private = true
	}
	c.Repo = c.Origin.Repo
	if c.Repo == "" {
		// A managed worktree's directory is named after the worktree; the
		// repository's name is its main checkout's.
		if common, err := git(top, "rev-parse", "--path-format=absolute", "--git-common-dir"); err == nil {
			c.Repo = filepath.Base(strings.TrimSuffix(filepath.Clean(common), string(filepath.Separator)+".git"))
		}
	}
	if c.Dest == "" {
		c.Dest = checkedOutBase(top)
	}
	if c.Dest == "" {
		head, _ := git(top, "symbolic-ref", "--short", "refs/remotes/origin/HEAD")
		c.Dest = strings.TrimPrefix(head, "origin/")
	}
	if c.Dest == "" {
		for _, candidate := range []string{"main", "master", "develop"} {
			if gitOK(top, "rev-parse", "--verify", "--quiet", "refs/remotes/origin/"+candidate) {
				c.Dest = candidate
				break
			}
		}
	}
	if c.Dest == "" && !c.Private {
		return nil, errors.New("cannot tell which branch pull requests go into; pass --dest")
	}
	return c, nil
}

// CurrentWorktree is the managed worktree name of the checkout dir is in, or
// empty when dir is not in one.
func CurrentWorktree(dir string) string {
	branch, _ := git(dir, "branch", "--show-current")
	if name, ok := strings.CutPrefix(branch, BranchPrefix); ok && ValidWorktreeName(name) {
		return name
	}
	return ""
}

// Branch is the checkout's branch.
func (c *Checkout) Branch() string { return Branch(c.Worktree) }

func (c *Checkout) inProgress() string {
	for marker, what := range map[string]string{
		"MERGE_HEAD": "a merge", "rebase-merge": "a rebase", "rebase-apply": "a rebase",
		"CHERRY_PICK_HEAD": "a cherry-pick", "REVERT_HEAD": "a revert", "BISECT_LOG": "a bisect",
	} {
		p, err := git(c.Dir, "rev-parse", "--path-format=absolute", "--git-path", marker)
		if err != nil {
			continue
		}
		if _, err := os.Stat(p); err == nil {
			return what
		}
	}
	return ""
}

// publicationMarker is the trailer the gateway puts on the commit it creates
// for a publication.
const publicationMarker = "OpenClaw-Publication:"

// publishedHead is the newest commit known to have been published from this
// branch, or empty. The gateway pushes without updating a remote-tracking
// ref, so the evidence is local: the branch's reflog remembers the commits
// the gateway marked, also after an amend or a rebase moved the branch off
// them. A remote-tracking ref recorded by a fetch or a personal push takes
// precedence when it contains the marked commit and is in HEAD. With a marker,
// an ahead remote is ignored; without one, the remote is the only evidence.
func (c *Checkout) publishedHead() string {
	branch := c.Branch()
	remote, _ := git(c.Dir, "rev-parse", "--verify", "--quiet", "refs/remotes/origin/"+branch)
	if log, err := git(c.Dir, "log", "-g", "-n", "500", "--format=%H%x00%B%x01", "refs/heads/"+branch); err == nil {
		for _, entry := range strings.Split(log, "\x01") {
			hash, body, _ := strings.Cut(strings.TrimSpace(entry), "\x00")
			if hash != "" && strings.Contains(body, publicationMarker) {
				if remote != "" && gitOK(c.Dir, "merge-base", "--is-ancestor", hash, remote) && gitOK(c.Dir, "merge-base", "--is-ancestor", remote, "HEAD") {
					return remote
				}
				return hash
			}
		}
	}
	return remote
}

type change struct{ status, path string }

func (c *Checkout) pending() ([]change, error) {
	out, err := git(c.Dir, "status", "--porcelain=v1", "-z", "--untracked-files=all", "--ignore-submodules=dirty")
	if err != nil {
		return nil, err
	}
	var changes []change
	fields := strings.Split(out, "\x00")
	for i := 0; i < len(fields); i++ {
		f := fields[i]
		if len(f) < 4 {
			continue
		}
		// OpenClaw mirrors its skills into a sandbox's checkout and leaves
		// them out of a publication.
		if strings.HasPrefix(f[3:], sandboxSkills) {
			continue
		}
		changes = append(changes, change{f[:2], f[3:]})
		if f[0] == 'R' || f[0] == 'C' {
			i++ // the path it was renamed or copied from
		}
	}
	return changes, nil
}

const sandboxSkills = ".openclaw/sandbox-skills/"

// Names that hold credentials more often than source. A publication commits
// every uncommitted file in the worktree, so one of these lying around would
// be pushed with the work.
var sensitive = regexp.MustCompile(`(?i)^(\.env(\..*)?|.*\.(pem|key|p12|pfx|keystore|jks)|id_(rsa|dsa|ecdsa|ed25519)|\.netrc|\.npmrc|\.pypirc|credentials(\.json)?|secrets?\.(json|ya?ml|env))$`)
var harmless = regexp.MustCompile(`(?i)\.(example|sample|template|dist)$`)

func sensitiveName(p string) bool {
	base := path.Base(p)
	return sensitive.MatchString(base) && !harmless.MatchString(base)
}

func workflowPath(p string) bool {
	ok, _ := path.Match(".github/workflows/*.y*ml", p)
	return ok
}

// PublishOptions are what the caller chooses about a publication.
type PublishOptions struct {
	Title, Body    string
	AllowSensitive bool
}

// PlanPublish checks that publishing this checkout is safe and returns the
// intent for it. It reads and never writes: no commit, no fetch, no push.
func (c *Checkout) PlanPublish(opt PublishOptions) (Plan, error) {
	if task := c.task(); task != "" {
		return Plan{}, fmt.Errorf(branding.Text("%s is a worker of %s: a worker's work leaves through the task. Merge it into the task ({{command}} catch-up --from %s in the task's checkout), and the task publishes it"), c.Branch(), task, strings.TrimPrefix(c.Worktree, task+WorkerSeparator))
	}
	if what := c.inProgress(); what != "" {
		return Plan{}, fmt.Errorf("%s is in progress in %s: finish or abort it before publishing", what, c.Dir)
	}
	changes, err := c.pending()
	if err != nil {
		return Plan{}, err
	}
	var pending, secrets []string
	workflows := false
	for _, ch := range changes {
		if strings.Contains(ch.status, "U") || ch.status == "AA" || ch.status == "DD" {
			return Plan{}, fmt.Errorf("%s has unresolved conflicts (%s): resolve them before publishing", c.Dir, ch.path)
		}
		pending = append(pending, ch.path)
		if ch.status[0] != 'D' && ch.status[1] != 'D' && sensitiveName(ch.path) {
			secrets = append(secrets, ch.path)
		}
		workflows = workflows || workflowPath(ch.path)
	}
	sort.Strings(pending)
	if len(secrets) > 0 && !opt.AllowSensitive {
		return Plan{}, fmt.Errorf("uncommitted files that look like credentials would be published: %s. Remove or gitignore them, or pass --allow-sensitive if they are meant to be committed", strings.Join(secrets, ", "))
	}

	branch := c.Branch()
	if published := c.publishedHead(); published != "" && !gitOK(c.Dir, "merge-base", "--is-ancestor", published, "HEAD") {
		return Plan{}, fmt.Errorf("%s no longer contains %.12s, which was published: its history was rewritten (rebase, amend or reset), and published history is never rewritten. Put the changes on top of %.12s again (`git reset --soft %.12s` keeps them), or continue in a new session for a replacement pull request", branch, published, published, published)
	}
	if c.Private {
		// A sandbox's copy names neither repository nor base: the gateway
		// takes both from the session, and decides whether it can publish.
		plan := NewPlan(intent(ActionPublish, ActorRepoSession, c.Repo, ToolGitHubPublish, PublishArgs{Title: opt.Title, Body: opt.Body}))
		plan.Intents[0].Forge = ForgeUnknown
		plan.Pending = pending
		plan.Warnings = append(plan.Warnings,
			"This checkout has no origin (a sandbox's private copy), so wtc cannot check the forge, the base branch or what was published before. The gateway publishes GitHub repositories only: for any other forge, publish from the repository's session outside the sandbox.")
		if len(pending) > 0 {
			plan.Warnings = append(plan.Warnings, fmt.Sprintf("The gateway will commit all %d pending paths. Check the list: anything that does not belong to the work has to go first.", len(pending)))
		}
		if workflows {
			plan.Warnings = append(plan.Warnings, "GitHub Actions workflow definitions changed. The gateway publishes those only for a requester with full operator write access.")
		}
		return plan, nil
	}
	dest := "refs/remotes/origin/" + c.Dest
	hasDest := gitOK(c.Dir, "rev-parse", "--verify", "--quiet", dest)
	if hasDest && !gitOK(c.Dir, "merge-base", "HEAD", dest) {
		return Plan{}, fmt.Errorf("%s shares no history with origin/%s", branch, c.Dest)
	}
	if hasDest {
		ahead, _ := git(c.Dir, "rev-list", "--count", dest+"..HEAD")
		if ahead == "0" && len(pending) == 0 {
			return Plan{}, fmt.Errorf("nothing to publish: %s has no commits and no changes beyond origin/%s", branch, c.Dest)
		}
		if names, err := git(c.Dir, "diff", "--name-only", dest+"...HEAD"); err == nil {
			for _, p := range strings.Split(names, "\n") {
				workflows = workflows || workflowPath(p)
			}
		}
	}

	plan := NewPlan(Publish(c.Repo, c.Origin, c.Worktree, c.Dest, opt.Title, opt.Body))
	plan.Pending = pending
	if c.Origin.Forge != ForgeGitHub {
		if len(pending) > 0 {
			return Plan{}, fmt.Errorf("commit your work first: the gateway only commits for GitHub, and %d uncommitted paths would stay behind (%s)", len(pending), strings.Join(pending, ", "))
		}
		plan.Warnings = append(plan.Warnings, "The gateway publishes to GitHub only. This intent names no tool: push the branch and open the pull request with the skill for "+string(c.Origin.Forge)+", and never force-push.")
	} else if len(pending) > 0 {
		plan.Warnings = append(plan.Warnings, fmt.Sprintf("The gateway will commit all %d pending paths. Check the list: anything that does not belong to the work has to go first.", len(pending)))
	}
	if workflows {
		plan.Warnings = append(plan.Warnings, "GitHub Actions workflow definitions changed. The gateway publishes those only for a requester with full operator write access.")
	}
	return plan, nil
}

// CatchUp is the result of merging the destination branch, a worker's branch
// (into its task) or the task's branch (into a worker) into the checkout.
type CatchUp struct {
	Branch string `json:"branch"`
	Dest   string `json:"dest"`
	// From is the branch merged in instead of Dest: a sub-session's or a
	// worker's, by its name, merged into the collection's, or the
	// collection's, merged into a sub-session's or a worker's.
	From   string `json:"from,omitempty"`
	Merged bool   `json:"merged"`
	// FastForward says the checkout had nothing the ref lacked: it now is the
	// ref, and no merge commit was made.
	FastForward   bool `json:"fastForward,omitempty"`
	ReturnedToTip bool `json:"returnedToTip,omitempty"`
	// Worker says the checkout is a worker's: its work reaches the forge
	// through its task.
	Worker bool   `json:"worker,omitempty"`
	Head   string `json:"head"`
	Note   string `json:"note,omitempty"`
}

// CatchUp fetches the destination branch. A merged GitHub PR returns its
// checkout to the detached base tip; a live branch merges the base. It never
// rebases and never pushes: published history stays as it is, and the gateway publishes
// the merge with the next publication (a worker's, with its task's). A
// conflicted merge is aborted. A detached worktree is not merged into but
// moved to the tip (forward).
func (c *Checkout) CatchUp() (CatchUp, error) {
	result := CatchUp{Branch: c.Branch(), Dest: c.Dest}
	if c.Detached {
		result.Branch = ""
	}
	if err := c.readyToMerge(); err != nil {
		return result, err
	}
	fetch := append(fetchCredentials(c.Origin), "fetch", "--quiet", "--no-recurse-submodules", "origin", "+refs/heads/"+c.Dest+":refs/remotes/origin/"+c.Dest)
	if _, err := git(c.Dir, fetch...); err != nil {
		return result, fmt.Errorf("cannot fetch %s from origin: %w", c.Dest, err)
	}
	if c.Detached {
		return c.forward(result, "refs/remotes/origin/"+c.Dest, "origin/"+c.Dest)
	}
	if c.Origin.Forge == ForgeGitHub {
		if pr := gitHubPullRequest(c.Dir, c.Origin, c.Branch()); pr != nil && pr.State == "merged" {
			return c.returnToTip(result, pr)
		}
	}
	return c.merge(result, "refs/remotes/origin/"+c.Dest, "origin/"+c.Dest)
}

// CatchUpWithTask merges the task's branch into a worker's checkout: a
// worker's parent is its task, not origin. Nothing is fetched; the task's
// branch is in the same clone.
func (c *Checkout) CatchUpWithTask() (CatchUp, error) {
	task := TaskOf(c.Worktree)
	result := CatchUp{Branch: c.Branch(), Dest: c.Dest, From: Branch(task)}
	if task == "" {
		return result, fmt.Errorf("%s is no worker's branch", c.Branch())
	}
	if !HasBranch(c.Dir, Branch(task)) {
		return result, fmt.Errorf("%s has no branch %s: no task of that name works in this repository", c.Repo, Branch(task))
	}
	if err := c.readyToMerge(); err != nil {
		return result, err
	}
	return c.merge(result, "refs/heads/"+Branch(task), Branch(task))
}

// forward moves a detached worktree to ref, which must contain it: with no
// branch there is nothing to merge into, and commits of its own would be
// left behind.
func (c *Checkout) forward(result CatchUp, ref, label string) (CatchUp, error) {
	if gitOK(c.Dir, "merge-base", "--is-ancestor", ref, "HEAD") {
		result.Head, _ = git(c.Dir, "rev-parse", "HEAD")
		result.Note = "already contains " + label
		return result, nil
	}
	if !gitOK(c.Dir, "merge-base", "--is-ancestor", "HEAD", ref) {
		return result, fmt.Errorf("%s is detached with commits %s does not have: put them on a branch first (git switch -c %s<name>), then catch up", c.Dir, label, BranchPrefix)
	}
	if _, err := git(c.Dir, "checkout", "--quiet", "--detach", ref); err != nil {
		return result, fmt.Errorf("moving %s to %s, the checkout is as before: %w", c.Dir, label, err)
	}
	result.Merged, result.FastForward = true, true
	result.Head, _ = git(c.Dir, "rev-parse", "HEAD")
	return result, nil
}

// HasBranch reports whether the repository at dir has the local branch.
func HasBranch(dir, branch string) bool {
	return gitOK(dir, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch+"^{commit}")
}

// HasRemoteBranch reports whether the repository at dir has fetched branch
// from origin.
func HasRemoteBranch(dir, branch string) bool {
	return gitOK(dir, "rev-parse", "--verify", "--quiet", "refs/remotes/origin/"+branch+"^{commit}")
}

// WorkerSeparator joins a task's worktree name and a worker's: worker a of
// task 12-paging works in collection 12-paging--a, on branch
// openclaw/12-paging--a.
const WorkerSeparator = "--"

// TaskOf is the task a worker's worktree name would belong to: site-work for
// site-work--docs, or empty for a name that cannot be a worker's. A task may
// have "--" in its own name, so Checkout.task also checks for the task's
// branch.
func TaskOf(worktree string) string {
	at := strings.LastIndex(worktree, WorkerSeparator)
	if at <= 0 || at+len(WorkerSeparator) == len(worktree) || !ValidWorktreeName(worktree) {
		return ""
	}
	return worktree[:at]
}

// task is the task this checkout is a worker of: the name says so, and the
// task's branch is in the same clone. Empty for a checkout that is no worker.
func (c *Checkout) task() string {
	if task := TaskOf(c.Worktree); task != "" && HasBranch(c.Dir, Branch(task)) {
		return task
	}
	return ""
}

// IsWorker reports whether the checkout is a worker's: see task.
func (c *Checkout) IsWorker() bool { return c.task() != "" }

// WorkerWorktree is the worktree name of a worker of task: worker is its
// short name or the whole one.
func WorkerWorktree(task, worker string) (string, error) {
	name := worker
	if !strings.HasPrefix(worker, task+WorkerSeparator) {
		name = task + WorkerSeparator + worker
	}
	short := strings.TrimPrefix(name, task+WorkerSeparator)
	if short == "" || strings.Contains(short, WorkerSeparator) || !ValidWorktreeName(name) {
		return "", fmt.Errorf("%q is not a worker of %s: a worker's collection is named %s%s<worker>, in lowercase letters, digits and hyphens, at most 64", worker, task, task, WorkerSeparator)
	}
	return name, nil
}

// MergeFrom merges another branch of the same clone into this checkout's:
// a sub-session's branch by its name, or a wtc worker's by its short name.
// Worktrees of one repository share the gateway's clone, so the branch is a
// local ref and nothing is fetched. Like CatchUp it never rebases and never
// pushes, and a conflicted merge is aborted.
func (c *Checkout) MergeFrom(from string) (CatchUp, error) {
	if c.Detached {
		return CatchUp{Dest: c.Dest}, fmt.Errorf("%s is detached: a branch merges into this repository's branch, and it has none yet", c.Dir)
	}
	result := CatchUp{Branch: c.Branch(), Dest: c.Dest}
	// A branch by its own name: a sub-session names its branch after its
	// change, not after this collection.
	if from != "" && from != result.Branch && HasBranch(c.Dir, from) {
		result.From = from
		if err := c.readyToMerge(); err != nil {
			return result, err
		}
		return c.merge(result, "refs/heads/"+from, from)
	}
	// Otherwise a wtc worker's short name: openclaw/<collection>--<worker>.
	name, err := WorkerWorktree(c.Worktree, from)
	if err != nil {
		return result, err
	}
	result.From = Branch(name)
	if !gitOK(c.Dir, "rev-parse", "--verify", "--quiet", "refs/heads/"+result.From+"^{commit}") {
		return result, fmt.Errorf("%s has no branch %s: the worker has no worktree of this repository, or has not committed yet", c.Repo, result.From)
	}
	if err := c.readyToMerge(); err != nil {
		return result, err
	}
	return c.merge(result, "refs/heads/"+result.From, result.From)
}

func (c *Checkout) readyToMerge() error {
	if what := c.inProgress(); what != "" {
		return fmt.Errorf("%s is in progress in %s: finish or abort it first", what, c.Dir)
	}
	changes, err := c.pending()
	if err != nil {
		return err
	}
	for _, ch := range changes {
		if ch.status != "??" {
			return fmt.Errorf("%s has uncommitted changes (%s): commit them before catching up, so a conflict cannot take them along", c.Dir, ch.path)
		}
	}
	return nil
}

// merge merges ref, shown as label, into the checkout.
func (c *Checkout) merge(result CatchUp, ref, label string) (CatchUp, error) {
	if gitOK(c.Dir, "merge-base", "--is-ancestor", ref, "HEAD") {
		result.Head, _ = git(c.Dir, "rev-parse", "HEAD")
		result.Note = "already contains " + label
		return result, nil
	}
	result.FastForward = gitOK(c.Dir, "merge-base", "--is-ancestor", "HEAD", ref)
	if _, mergeErr := git(c.Dir, "merge", "--no-edit", "--quiet", ref); mergeErr != nil {
		result.FastForward = false
		conflicts, _ := git(c.Dir, "diff", "--name-only", "--diff-filter=U", "--ignore-submodules=dirty")
		if c.inProgress() != "" {
			_, _ = git(c.Dir, "merge", "--abort")
		}
		if strings.TrimSpace(conflicts) == "" {
			return result, fmt.Errorf("merging %s into %s failed, and the checkout is as before: %w", label, c.Branch(), mergeErr)
		}
		// The Control UI card reads the files from "conflicts in: …. The merge
		// was aborted" (conflictsOf in the plugin's control-ui bundle).
		return result, fmt.Errorf("merging %s into %s conflicts in: %s. The merge was aborted and the checkout is as before. Merge by hand with `git merge %s`, resolve, commit — do not rebase", label, c.Branch(), strings.Join(strings.Split(strings.TrimSpace(conflicts), "\n"), ", "), label)
	}
	result.Merged = true
	result.Head, _ = git(c.Dir, "rev-parse", "HEAD")
	return result, nil
}

// WorkerRepo is a worker's branch in one repository, measured against the
// task's branch there.
type WorkerRepo struct {
	Repo string `json:"repo"`
	// Path is the worktree compared.
	Path   string `json:"path"`
	Branch string `json:"branch"`
	// ToMerge counts the worker's commits the task's branch does not have
	// yet: 0 once wtc catch-up --from merged them.
	ToMerge int `json:"toMerge"`
	// Behind counts the collection's commits the worktree does not have:
	// what a catch-up from the collection's branch brings in.
	Behind int `json:"behind"`
	// Uncommitted are the worker's uncommitted files, each compared with the
	// task's checkout of the repository: whether the task has it already.
	Uncommitted []WorkerFile `json:"uncommitted,omitempty"`
	Error       string       `json:"error,omitempty"`
}

// WorkerFile is an uncommitted file of a sub-session's checkout. State is "same" when the
// task's checkout has the file as the worker has it (or neither has it),
// "only" when only the worker has it, and "differs" otherwise: both have it
// differently, or only the task does (the worker deleted it).
type WorkerFile struct {
	Path  string `json:"path"`
	State string `json:"state"`
}

// workerFiles compares the uncommitted files of a sub-session's checkout
// with the collection's checkout of the same repository.
func workerFiles(workerDir, taskDir string) []WorkerFile {
	changes, err := (&Checkout{Dir: workerDir}).pending()
	if err != nil {
		return nil
	}
	var files []WorkerFile
	for _, ch := range changes {
		mine, errMine := os.ReadFile(filepath.Join(workerDir, ch.path))
		theirs, errTheirs := os.ReadFile(filepath.Join(taskDir, ch.path))
		state := "differs"
		switch {
		case errMine != nil && errTheirs != nil, errMine == nil && errTheirs == nil && bytes.Equal(mine, theirs):
			state = "same"
		case errMine == nil && errTheirs != nil:
			state = "only"
		}
		files = append(files, WorkerFile{Path: ch.path, State: state})
	}
	return files
}

// WorkerRepos compares worktrees of sub-sessions (or wtc workers) with the
// checkouts of the collection named task: each in the collection's worktree
// of the same clone.
func WorkerRepos(task string, taskDirs, workerDirs []string) []WorkerRepo {
	clones := map[string]string{}
	for _, dir := range taskDirs {
		if common, err := git(dir, "rev-parse", "--path-format=absolute", "--git-common-dir"); err == nil {
			clones[common] = dir
		}
	}
	var repos []WorkerRepo
	for _, dir := range workerDirs {
		w := WorkerRepo{Repo: filepath.Base(filepath.Dir(dir)), Path: dir}
		if url, err := git(dir, "remote", "get-url", "origin"); err == nil && ParseOrigin(url).Repo != "" {
			w.Repo = ParseOrigin(url).Repo
		}
		w.Branch, _ = git(dir, "branch", "--show-current")
		common, err := git(dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
		taskDir, ok := clones[common]
		switch {
		case err != nil:
			w.Error = err.Error()
		case !ok:
			w.Error = "the task has no worktree of this repository"
		case !HasBranch(taskDir, Branch(task)) || w.Branch == "":
			w.Error = "no branch to compare"
		default:
			n, err := git(taskDir, "rev-list", "--count", "refs/heads/"+Branch(task)+"..refs/heads/"+w.Branch)
			if err != nil {
				w.Error = err.Error()
			} else {
				w.ToMerge, _ = strconv.Atoi(n)
			}
			if n, err := git(taskDir, "rev-list", "--count", "refs/heads/"+w.Branch+"..refs/heads/"+Branch(task)); err == nil {
				w.Behind, _ = strconv.Atoi(n)
			}
		}
		if ok && err == nil {
			w.Uncommitted = workerFiles(dir, taskDir)
		}
		repos = append(repos, w)
	}
	return repos
}
