package openclaw

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"taba.sh/taba/internal/next"
)

func TestDetectHost(t *testing.T) {
	for _, tc := range []struct {
		want HostKind
		env  map[string]string
	}{
		{HostOpenClaw, map[string]string{"OPENCLAW_SHELL": "exec", "HERDR_SESSION": "s"}},
		// Codex's bash tool and the Control UI terminal: the gateway's env only.
		{HostOpenClaw, map[string]string{"OPENCLAW_STATE_DIR": "/home/node/.openclaw", "CODEX_HOME": "/x"}},
		{HostHerdr, map[string]string{"HERDR_WORKSPACE_ID": "w"}},
		{HostTerminal, map[string]string{"OPENCLAW_SHELL": "", "OPENCLAW_HOME": "/home/node"}},
	} {
		if got := DetectHost(func(k string) string { return tc.env[k] }); got != tc.want {
			t.Errorf("%v: got %s, want %s", tc.env, got, tc.want)
		}
	}
}

func TestManagedCollections(t *testing.T) {
	state := t.TempDir()
	for _, dir := range []string{"p1/wtc-smoke", "p2/wtc-smoke", "p1/other"} {
		if err := os.MkdirAll(filepath.Join(state, "worktrees", dir), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(state, "worktrees", dir, ".git"), []byte("gitdir: x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Not a worktree: no .git.
	_ = os.MkdirAll(filepath.Join(state, "worktrees", "p3", "stray"), 0o755)
	got, err := ManagedCollections(state)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || len(got["wtc-smoke"]) != 2 || len(got["other"]) != 1 {
		t.Fatalf("collections %v", got)
	}
}

// managedWorktree is what the gateway hands a child session: a worktree of a
// clone, on openclaw/<name>, with origin on originURL and a remote main.
func managedWorktree(t *testing.T, originURL string) (*Checkout, func(args ...string) string) {
	t.Helper()
	// Every git command in the test, wtc's own included, commits as this
	// identity: a CI machine has none configured.
	for name, value := range map[string]string{"GIT_AUTHOR_NAME": "T", "GIT_AUTHOR_EMAIL": "t@example.com",
		"GIT_COMMITTER_NAME": "T", "GIT_COMMITTER_EMAIL": "t@example.com", "GIT_CONFIG_GLOBAL": "/dev/null"} {
		t.Setenv(name, value)
	}
	root := t.TempDir()
	run := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=T", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=T", "GIT_COMMITTER_EMAIL=t@example.com", "GIT_CONFIG_GLOBAL=/dev/null")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return strings.TrimSpace(string(out))
	}
	bare, repo, wt := filepath.Join(root, "origin.git"), filepath.Join(root, "docs"), filepath.Join(root, "72-add-thing")
	run(root, "init", "-q", "--bare", "-b", "main", bare)
	run(root, "clone", "-q", bare, repo)
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("docs\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(repo, "add", ".")
	run(repo, "commit", "-qm", "init")
	run(repo, "push", "-q", "origin", "HEAD:main")
	run(repo, "remote", "set-head", "origin", "main")
	run(repo, "remote", "set-url", "origin", originURL)
	run(repo, "remote", "set-url", "--push", "origin", bare)
	run(repo, "worktree", "add", "-q", "-b", "openclaw/72-add-thing", wt)
	c, err := OpenCheckout(wt, "")
	if err != nil {
		t.Fatal(err)
	}
	return c, func(args ...string) string { return run(wt, args...) }
}

func write(t *testing.T, c *Checkout, name, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filepath.Join(c.Dir, name)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(c.Dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestOpenCheckout(t *testing.T) {
	c, git := managedWorktree(t, "https://github.com/example-org/docs.git")
	if c.Worktree != "72-add-thing" || c.Dest != "main" || c.Repo != "docs" || c.Origin.Forge != ForgeGitHub {
		t.Fatalf("checkout %+v", c)
	}
	if got := CurrentWorktree(c.Dir); got != "72-add-thing" {
		t.Fatalf("current worktree %q", got)
	}
	git("switch", "-qc", "feature")
	if _, err := OpenCheckout(c.Dir, ""); err == nil || !strings.Contains(err.Error(), "managed worktree") {
		t.Fatalf("a branch the gateway does not own was accepted: %v", err)
	}
	if _, err := OpenCheckout(t.TempDir(), ""); err == nil {
		t.Fatal("a directory that is no checkout was accepted")
	}
}

func TestPlanPublishOnGitHub(t *testing.T) {
	c, git := managedWorktree(t, "https://github.com/example-org/docs.git")
	if _, err := c.PlanPublish(PublishOptions{}); err == nil || !strings.Contains(err.Error(), "nothing to publish") {
		t.Fatalf("an unchanged worktree: %v", err)
	}
	write(t, c, "notes.md", "one\n")
	write(t, c, ".github/workflows/ci.yml", "on: push\n")
	plan, err := c.PlanPublish(PublishOptions{Title: "Add notes"})
	if err != nil {
		t.Fatal(err)
	}
	in := plan.Intents[0]
	if in.Tool != ToolGitHubPublish || in.Actor != ActorRepoSession || in.Repo != "docs" || in.Args.(PublishArgs).Title != "Add notes" {
		t.Fatalf("intent %+v", in)
	}
	if strings.Join(plan.Pending, " ") != ".github/workflows/ci.yml notes.md" {
		t.Fatalf("pending %v", plan.Pending)
	}
	if len(plan.Warnings) != 2 || !strings.Contains(plan.Warnings[1], "workflow") {
		t.Fatalf("warnings %v", plan.Warnings)
	}
	if got := git("status", "--porcelain"); !strings.Contains(got, "notes.md") {
		t.Fatalf("planning changed the checkout:\n%s", got)
	}
}

func TestPlanPublishRefusesWhatIsNotSafe(t *testing.T) {
	t.Run("credentials lying around", func(t *testing.T) {
		c, _ := managedWorktree(t, "https://github.com/example-org/docs.git")
		write(t, c, "notes.md", "one\n")
		write(t, c, "config/.env.local", "TOKEN=x\n")
		write(t, c, ".env.example", "TOKEN=\n")
		_, err := c.PlanPublish(PublishOptions{})
		if err == nil || !strings.Contains(err.Error(), "config/.env.local") || strings.Contains(err.Error(), ".env.example") {
			t.Fatalf("got %v", err)
		}
		if _, err := c.PlanPublish(PublishOptions{AllowSensitive: true}); err != nil {
			t.Fatalf("--allow-sensitive: %v", err)
		}
	})
	t.Run("rewritten published history", func(t *testing.T) {
		c, git := managedWorktree(t, "https://github.com/example-org/docs.git")
		write(t, c, "notes.md", "one\n")
		git("add", ".")
		git("commit", "-qm", "work")
		git("push", "-q", "origin", "openclaw/72-add-thing")
		git("fetch", "-q", strings.TrimSpace(git("remote", "get-url", "--push", "origin")), "+refs/heads/*:refs/remotes/origin/*")
		write(t, c, "notes.md", "two\n")
		if _, err := c.PlanPublish(PublishOptions{}); err != nil {
			t.Fatalf("work on top of the published head: %v", err)
		}
		git("commit", "-qa", "--amend", "-m", "rewritten")
		if _, err := c.PlanPublish(PublishOptions{}); err == nil || !strings.Contains(err.Error(), "rewritten") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("rewritten history the gateway published", func(t *testing.T) {
		// The gateway pushes without leaving a remote-tracking ref; what it
		// leaves is a marked commit on the branch.
		c, git := managedWorktree(t, "https://github.com/example-org/docs.git")
		write(t, c, "notes.md", "one\n")
		git("add", ".")
		git("commit", "-qm", "Publish openclaw/72-add-thing\n\nOpenClaw-Publication: 53a47a99")
		write(t, c, "notes.md", "two\n")
		if _, err := c.PlanPublish(PublishOptions{}); err != nil {
			t.Fatalf("work on top of the published head: %v", err)
		}
		git("commit", "-qa", "--amend", "-m", "Tidied")
		write(t, c, "notes.md", "three\n")
		if _, err := c.PlanPublish(PublishOptions{}); err == nil || !strings.Contains(err.Error(), "rewritten") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("an unfinished merge", func(t *testing.T) {
		c, git := managedWorktree(t, "https://github.com/example-org/docs.git")
		write(t, c, "README.md", "ours\n")
		git("commit", "-qam", "ours")
		git("switch", "-qc", "other", "origin/main")
		write(t, c, "README.md", "theirs\n")
		git("commit", "-qam", "theirs")
		git("switch", "-q", "openclaw/72-add-thing")
		exec.Command("git", "-C", c.Dir, "merge", "other").Run()
		if _, err := c.PlanPublish(PublishOptions{}); err == nil || !strings.Contains(err.Error(), "merge") {
			t.Fatalf("got %v", err)
		}
	})
}

func TestPlanPublishOffGitHub(t *testing.T) {
	c, git := managedWorktree(t, "git@bitbucket.org:example-ws/docs.git")
	write(t, c, "notes.md", "one\n")
	if _, err := c.PlanPublish(PublishOptions{}); err == nil || !strings.Contains(err.Error(), "commit your work first") {
		t.Fatalf("uncommitted work on Bitbucket: %v", err)
	}
	git("add", ".")
	git("commit", "-qm", "work")
	plan, err := c.PlanPublish(PublishOptions{Title: "Add notes"})
	if err != nil {
		t.Fatal(err)
	}
	in := plan.Intents[0]
	want := PullRequestArgs{Owner: "example-ws", Repo: "docs", Source: "openclaw/72-add-thing", Dest: "main", Title: "Add notes", Draft: true}
	if in.Tool != "" || in.Forge != ForgeBitbucket || in.Args != want {
		t.Fatalf("intent %+v", in)
	}
}

func TestCatchUpMergesAndNeverPushes(t *testing.T) {
	c, git := managedWorktree(t, "/dev/null/unused")
	bare := git("remote", "get-url", "--push", "origin")
	git("remote", "set-url", "origin", bare)
	c, err := OpenCheckout(c.Dir, "")
	if err != nil {
		t.Fatal(err)
	}
	advance := func(file, content string) { t.Helper(); advanceOrigin(t, bare, file, content) }
	// Nothing of its own yet: the catch-up fast-forwards to the base.
	advance("FIRST", "moved\n")
	result, err := c.CatchUp()
	if err != nil || !result.Merged || !result.FastForward || git("rev-parse", "HEAD") != git("rev-parse", "origin/main") {
		t.Fatalf("catch-up of a branch with nothing on it: %+v %v", result, err)
	}

	write(t, c, "notes.md", "one\n")
	git("add", ".")
	git("commit", "-qm", "work")

	advance("UPSTREAM", "moved\n")
	result, err = c.CatchUp()
	if err != nil || !result.Merged || result.FastForward {
		t.Fatalf("catch-up: %+v %v", result, err)
	}
	if got := git("rev-list", "--count", "--merges", "HEAD"); got != "1" {
		t.Fatalf("%s merge commits, want 1", got)
	}
	if out, _ := exec.Command("git", "-C", bare, "branch", "--list", "openclaw/*").Output(); len(out) != 0 {
		t.Fatalf("catch-up pushed: %s", out)
	}
	if again, err := c.CatchUp(); err != nil || again.Merged {
		t.Fatalf("second catch-up: %+v %v", again, err)
	}

	advance("notes.md", "theirs\n")
	before := git("rev-parse", "HEAD")
	if _, err := c.CatchUp(); err == nil || !strings.Contains(err.Error(), "notes.md") {
		t.Fatalf("conflicting catch-up: %v", err)
	}
	if git("rev-parse", "HEAD") != before || git("status", "--porcelain") != "" {
		t.Fatal("a conflicted catch-up left the checkout changed")
	}
}

// advanceOrigin commits file to main in the bare repository, as someone
// else's push would.
func advanceOrigin(t *testing.T, bare, file, content string) {
	t.Helper()
	other := t.TempDir()
	for _, args := range [][]string{{"clone", "-q", bare, other}, nil, {"-C", other, "add", "."},
		{"-C", other, "-c", "user.name=T", "-c", "user.email=t@example.com", "commit", "-qm", "upstream " + file}, {"-C", other, "push", "-q", "origin", "main"}} {
		if args == nil {
			if err := os.WriteFile(filepath.Join(other, file), []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

func TestCatchUpMovesADetachedWorktreeForward(t *testing.T) {
	c, git := managedWorktree(t, "/dev/null/unused")
	bare := git("remote", "get-url", "--push", "origin")
	git("remote", "set-url", "origin", bare)
	git("checkout", "-q", "--detach", "origin/main")
	if _, err := OpenCheckout(c.Dir, ""); err == nil {
		t.Fatal("publishing opened a detached worktree")
	}
	c, err := OpenCatchUpCheckout(c.Dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if again, err := c.CatchUp(); err != nil || again.Merged || !strings.Contains(again.Note, "already contains") {
		t.Fatalf("at the tip: %+v %v", again, err)
	}
	advanceOrigin(t, bare, "MOVED", "moved\n")
	result, err := c.CatchUp()
	if err != nil || !result.Merged || !result.FastForward || result.Branch != "" {
		t.Fatalf("catch-up of a detached worktree: %+v %v", result, err)
	}
	if git("rev-parse", "HEAD") != git("rev-parse", "origin/main") || git("branch", "--show-current") != "" {
		t.Fatal("not detached at the base's tip")
	}
	if _, err := c.MergeFrom("a"); err == nil || !strings.Contains(err.Error(), "detached") {
		t.Fatalf("a worker merged into a detached worktree: %v", err)
	}

	// Commits of its own on no branch are not left behind.
	write(t, c, "notes.md", "mine\n")
	git("add", ".")
	git("commit", "-qm", "detached work")
	before := git("rev-parse", "HEAD")
	advanceOrigin(t, bare, "AGAIN", "moved\n")
	if _, err := c.CatchUp(); err == nil || !strings.Contains(err.Error(), "put them on a branch first") {
		t.Fatalf("detached commits: %v", err)
	}
	if git("rev-parse", "HEAD") != before {
		t.Fatal("the detached commit was left behind")
	}
}

func TestStatus(t *testing.T) {
	c, git := managedWorktree(t, "git@bitbucket.org:example-ws/docs.git")
	write(t, c, "notes.md", "one\n")
	git("add", ".")
	git("commit", "-qm", "Publish openclaw/72-add-thing\n\nOpenClaw-Publication: 53a47a99")
	write(t, c, "notes.md", "two\n")
	git("commit", "-qam", "More")
	write(t, c, "stray.txt", "x\n")
	write(t, c, "README.md", "changed\n")
	got := CheckoutsStatus([]string{c.Dir, t.TempDir()}, true)
	if got.Schema != CollectionSchema || len(got.Repos) != 2 {
		t.Fatalf("collection %+v", got)
	}
	want := CheckoutStatus{Path: c.Dir, Repo: "docs", Owner: "example-ws", Forge: ForgeBitbucket, Branch: "openclaw/72-add-thing", Base: "origin/main",
		Ahead: 2, Dirty: 2, Untracked: 1, Published: true, Unpublished: 1}
	if got.Repos[0].Next == nil || got.Repos[0].Next.Step != "commit" || got.Repos[0].Next.By != next.ByAgent || got.Repos[0].Choices == nil {
		t.Fatalf("dirty work needs an explicit commit handoff: %+v", got.Repos[0])
	}
	want.Next = got.Repos[0].Next
	want.Choices = got.Repos[0].Choices
	if !reflect.DeepEqual(got.Repos[0], want) {
		t.Errorf("got  %+v\nwant %+v", got.Repos[0], want)
	}
	if got.Repos[1].Error == "" {
		t.Errorf("a directory that is no checkout: %+v", got.Repos[1])
	}
}

func TestStatusAfterPersonalPush(t *testing.T) {
	for _, marked := range []bool{false, true} {
		t.Run(fmt.Sprint("prior-marker=", marked), func(t *testing.T) {
			c, git := managedWorktree(t, "https://bitbucket.org/example-ws/docs.git")
			message := "First change"
			if marked {
				message += "\n\nOpenClaw-Publication: first"
			}
			git("commit", "--allow-empty", "-qm", message)
			git("commit", "--allow-empty", "-qm", "Second change")
			ref := "refs/remotes/origin/" + c.Branch()
			git("update-ref", ref, "HEAD")
			s := CheckoutsStatus([]string{c.Dir}, false).Repos[0]
			if !s.Published || s.Unpublished != 0 || s.Ahead != 2 || s.Next != nil && s.Next.Step == "push" {
				t.Fatalf("pushed commits still offered for publication: %+v", s)
			}
			git("commit", "--allow-empty", "-qm", "New local change")
			s = CheckoutsStatus([]string{c.Dir}, false).Repos[0]
			if s.Unpublished != 1 || s.Next == nil || s.Next.Step != "push" {
				t.Fatalf("only the new local commit needs publication: %+v", s)
			}
		})
	}
}

func TestPublishedMarkerWithOtherRemoteEvidence(t *testing.T) {
	for _, ahead := range []bool{false, true} {
		t.Run(fmt.Sprint("remote-ahead=", ahead), func(t *testing.T) {
			c, git := managedWorktree(t, "https://bitbucket.org/example-ws/docs.git")
			git("commit", "--allow-empty", "-qm", "Published\n\nOpenClaw-Publication: first")
			marked := git("rev-parse", "HEAD")
			git("commit", "--allow-empty", "-qm", "Local work")
			remote := git("rev-parse", "origin/main")
			if ahead {
				remote = git("commit-tree", "HEAD^{tree}", "-p", "HEAD", "-m", "Remote work")
			}
			git("update-ref", "refs/remotes/origin/"+c.Branch(), remote)
			if got := c.publishedHead(); got != marked {
				t.Fatalf("remote evidence replaced the local marker: %s", got)
			}
			if s := CheckoutsStatus([]string{c.Dir}, false).Repos[0]; s.Unpublished != 1 {
				t.Fatalf("local unpublished work: %+v", s)
			}
			if _, err := c.PlanPublish(PublishOptions{}); err != nil {
				t.Fatalf("unchanged local history was rejected: %v", err)
			}
			git("reset", "--soft", marked)
			git("commit", "--amend", "--allow-empty", "-qm", "Rewritten")
			if _, err := c.PlanPublish(PublishOptions{}); err == nil || !strings.Contains(err.Error(), "rewritten") {
				t.Fatalf("rewritten published marker was not protected: %v", err)
			}
		})
	}
}

// Once the pull request merged and a catch-up fast-forwarded the branch to
// the base, the merge commit is not something to publish.
func TestStatusAfterThePullRequestMerged(t *testing.T) {
	c, git := managedWorktree(t, "https://github.com/example-org/docs.git")
	write(t, c, "notes.md", "one\n")
	git("add", ".")
	git("commit", "-qm", "Publish openclaw/72-add-thing\n\nOpenClaw-Publication: 53a47a99")
	merge := git("commit-tree", "-p", "origin/main", "-p", "HEAD", "-m", "Merge pull request #1", "HEAD^{tree}")
	git("update-ref", "refs/remotes/origin/main", merge)
	git("merge", "-q", "--ff-only", "origin/main")
	if s := CheckoutsStatus([]string{c.Dir}, false).Repos[0]; s.Ahead != 0 || s.Behind != 0 || !s.Published || s.Unpublished != 0 {
		t.Errorf("at the merged base: %+v", s)
	}
	write(t, c, "notes.md", "two\n")
	git("commit", "-qam", "Follow-up")
	if s := CheckoutsStatus([]string{c.Dir}, false).Repos[0]; s.Ahead != 1 || s.Unpublished != 1 {
		t.Errorf("a follow-up commit: %+v", s)
	}
	// A catch-up that merges the base into the branch makes a commit that
	// does need publishing.
	git("commit", "-qm", "Publish openclaw/72-add-thing\n\nOpenClaw-Publication: 7d0c2e11", "--allow-empty")
	git("update-ref", "refs/remotes/origin/main", git("commit-tree", "-p", "origin/main", "-m", "Upstream", "origin/main^{tree}"))
	git("merge", "-q", "--no-ff", "--no-edit", "origin/main")
	if s := CheckoutsStatus([]string{c.Dir}, false).Repos[0]; s.Behind != 0 || s.Unpublished != 1 {
		t.Errorf("after a catch-up's merge: %+v", s)
	}
}

func TestPlanPublishInASandboxCopy(t *testing.T) {
	// What a sandbox gets: the session's branch, no origin, the skills mirror.
	c, git := managedWorktree(t, "https://github.com/example-org/docs.git")
	git("remote", "remove", "origin")
	c, err := OpenCheckout(c.Dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if !c.Private {
		t.Fatal("a checkout without origin is not taken for a sandbox's copy")
	}
	write(t, c, "notes.md", "one\n")
	write(t, c, ".openclaw/sandbox-skills/github/SKILL.md", "mirror\n")
	plan, err := c.PlanPublish(PublishOptions{Title: "Add notes"})
	if err != nil {
		t.Fatal(err)
	}
	in := plan.Intents[0]
	if in.Tool != ToolGitHubPublish || in.Forge != ForgeUnknown || in.Args.(PublishArgs).Title != "Add notes" {
		t.Fatalf("intent %+v", in)
	}
	if strings.Join(plan.Pending, " ") != "notes.md" {
		t.Fatalf("pending %v: the skills mirror is not the agent's work", plan.Pending)
	}
	write(t, c, ".env", "TOKEN=x\n")
	if _, err := c.PlanPublish(PublishOptions{}); err == nil || !strings.Contains(err.Error(), ".env") {
		t.Fatalf("a .env in a sandbox's copy: %v", err)
	}
}

func TestRecordedBase(t *testing.T) {
	c, git := managedWorktree(t, "git@bitbucket.org:example-ws/docs.git")
	// develop is two commits past main; the worktree starts on main.
	git("branch", "develop")
	write(t, c, "a.md", "a\n")
	git("add", ".")
	git("commit", "-qm", "a")
	git("update-ref", "refs/remotes/origin/develop", "HEAD")
	git("reset", "-q", "--hard", "origin/main")
	if err := RecordBase(c.Dir, "72-add-thing", "origin/develop"); err != nil {
		t.Fatal(err)
	}
	got := CheckoutsStatus([]string{c.Dir}, false).Repos[0]
	if got.Base != "origin/develop" || got.Behind != 1 {
		t.Errorf("status against the recorded base: %+v", got)
	}
	opened, err := OpenCheckout(c.Dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if opened.Dest != "develop" {
		t.Errorf("publishes to %q, want develop", opened.Dest)
	}
	if b := baseBranch(c.Dir); b != "develop" {
		t.Errorf("fetches %q, want develop", b)
	}
}

// A worker's branch lives in the same clone: the task merges it without a
// fetch, never rebasing, and status counts what is left to merge.
func TestMergeFrom(t *testing.T) {
	c, git := managedWorktree(t, "https://github.com/example-org/docs.git")
	write(t, c, "notes.md", "task\n")
	git("add", ".")
	git("commit", "-qm", "task groundwork")
	worker := filepath.Join(t.TempDir(), "72-add-thing--a")
	git("worktree", "add", "-q", "-b", "openclaw/72-add-thing--a", worker, "openclaw/72-add-thing")
	if err := os.WriteFile(filepath.Join(worker, "a.md"), []byte("from a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, "-C", worker, "add", ".")
	gitIn(t, "-C", worker, "commit", "-qm", "worker a")

	if got := WorkerRepos("72-add-thing", []string{c.Dir}, []string{worker}); len(got) != 1 || got[0].ToMerge != 1 || got[0].Repo != "docs" || got[0].Error != "" {
		t.Fatalf("before: %+v", got)
	}
	for _, bad := range []string{"", "A", "72-add-thing--"} {
		if _, err := c.MergeFrom(bad); err == nil || !strings.Contains(err.Error(), "not a worker") {
			t.Errorf("worker %q: %v", bad, err)
		}
	}
	if _, err := c.MergeFrom("b"); err == nil || !strings.Contains(err.Error(), "has no branch openclaw/72-add-thing--b") {
		t.Errorf("a worker without a branch: %v", err)
	}
	result, err := c.MergeFrom("a")
	if err != nil || !result.Merged || result.From != "openclaw/72-add-thing--a" {
		t.Fatalf("merge: %+v %v", result, err)
	}
	if _, err := os.Stat(filepath.Join(c.Dir, "a.md")); err != nil {
		t.Errorf("the worker's file is not merged: %v", err)
	}
	if again, err := c.MergeFrom("72-add-thing--a"); err != nil || again.Merged || !strings.Contains(again.Note, "already contains") {
		t.Errorf("again, by its whole name: %+v %v", again, err)
	}
	if got := WorkerRepos("72-add-thing", []string{c.Dir}, []string{worker}); got[0].ToMerge != 0 {
		t.Errorf("after: %+v", got)
	}

	// Both change the same file: the merge is aborted and nothing changes.
	// A name with a space stays whole in the list.
	write(t, c, "notes.md", "task two\n")
	write(t, c, "my notes.md", "task\n")
	git("add", "-A")
	git("commit", "-qm", "task")
	for name, body := range map[string]string{"notes.md": "worker two\n", "my notes.md": "worker\n"} {
		if err := os.WriteFile(filepath.Join(worker, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gitIn(t, "-C", worker, "add", "-A")
	gitIn(t, "-C", worker, "commit", "-qm", "worker")
	before := git("rev-parse", "HEAD")
	// The Control UI card reads the files out of this sentence (conflictsOf
	// in openclaw-plugin/dist/control-ui/v1/index.js): keep the two in step.
	if _, err := c.MergeFrom("a"); err == nil || !regexp.MustCompile(`conflicts in: my notes\.md, notes\.md\. The merge was aborted`).MatchString(err.Error()) {
		t.Errorf("conflict: %v", err)
	}
	if git("rev-parse", "HEAD") != before || git("status", "--porcelain") != "" {
		t.Error("a conflicted merge left the checkout changed")
	}
	if got := WorkerRepos("72-add-thing", nil, []string{worker}); got[0].Error == "" {
		t.Errorf("without the task's worktree: %+v", got)
	}
}

func TestTaskOf(t *testing.T) {
	for name, want := range map[string]string{"site-work--docs": "site-work", "site-work": "", "site-work--": "", "--docs": "", "a--b--c": "a--b"} {
		if got := TaskOf(name); got != want {
			t.Errorf("TaskOf(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestAWorkerAgainstItsTask(t *testing.T) {
	c, git := managedWorktree(t, "https://github.com/example-org/docs.git")
	write(t, c, "notes.md", "task\n")
	git("add", ".")
	git("commit", "-qm", "task groundwork")
	dir := filepath.Join(t.TempDir(), "72-add-thing--a")
	git("worktree", "add", "-q", "-b", "openclaw/72-add-thing--a", dir, "openclaw/72-add-thing")
	worker, err := OpenCatchUpCheckout(dir, "main")
	if err != nil {
		t.Fatal(err)
	}

	// A worker never publishes: its work leaves through the task.
	if _, err := worker.PlanPublish(PublishOptions{}); err == nil || !strings.Contains(err.Error(), "is a worker of 72-add-thing") {
		t.Fatalf("a worker's publish: %v", err)
	}

	// The task moves on: the worker is behind it, and catches up with it.
	write(t, c, "more.md", "more\n")
	git("add", ".")
	git("commit", "-qm", "task moves on")
	if err := os.WriteFile(filepath.Join(dir, "draft.md"), []byte("draft\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.md"), []byte("worker's notes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A file the task has just like this is the same.
	if err := os.WriteFile(filepath.Join(dir, "more.md"), []byte("more\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := WorkerRepos("72-add-thing", []string{c.Dir}, []string{dir})
	if len(got) != 1 || got[0].Behind != 1 || got[0].ToMerge != 0 {
		t.Fatalf("behind: %+v", got)
	}
	files := map[string]string{}
	for _, f := range got[0].Uncommitted {
		files[f.Path] = f.State
	}
	if files["draft.md"] != "only" || files["notes.md"] != "differs" || files["more.md"] != "same" || len(files) != 3 {
		t.Errorf("uncommitted: %+v", got[0].Uncommitted)
	}
	gitIn(t, "-C", dir, "checkout", "-q", "--", "notes.md")
	for _, f := range []string{"draft.md", "more.md"} {
		if err := os.Remove(filepath.Join(dir, f)); err != nil {
			t.Fatal(err)
		}
	}
	result, err := worker.CatchUpWithTask()
	if err != nil || !result.Merged || result.From != "openclaw/72-add-thing" {
		t.Fatalf("catch-up with the task: %+v %v", result, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "more.md")); err != nil {
		t.Errorf("the task's commit did not reach the worker: %v", err)
	}
	if got := WorkerRepos("72-add-thing", []string{c.Dir}, []string{dir}); got[0].Behind != 0 {
		t.Errorf("after: %+v", got)
	}
	// A worker of a task with no branch in this repository has nothing to
	// catch up with.
	orphan := filepath.Join(t.TempDir(), "73-other--b")
	git("worktree", "add", "-q", "-b", "openclaw/73-other--b", orphan, "openclaw/72-add-thing")
	if o, err := OpenCatchUpCheckout(orphan, "main"); err != nil {
		t.Fatal(err)
	} else if _, err := o.CatchUpWithTask(); err == nil || !strings.Contains(err.Error(), "has no branch openclaw/73-other") {
		t.Errorf("a worker without its task's branch: %v", err)
	}
	// The task itself has no task to catch up with.
	if _, err := c.CatchUpWithTask(); err == nil {
		t.Error("the task caught up with a task")
	}
}

func TestATaskWithDashesIsNoWorker(t *testing.T) {
	_, git := managedWorktree(t, "https://github.com/example-org/docs.git")
	dir := filepath.Join(t.TempDir(), "fix--login")
	git("worktree", "add", "-q", "-b", "openclaw/fix--login", dir, "openclaw/72-add-thing")
	task, err := OpenCatchUpCheckout(dir, "main")
	if err != nil {
		t.Fatal(err)
	}
	// There is no branch openclaw/fix: fix--login is a task of its own.
	if task.IsWorker() {
		t.Fatal("fix--login was taken for a worker of fix")
	}
	if _, err := task.PlanPublish(PublishOptions{}); err != nil && strings.Contains(err.Error(), "is a worker of") {
		t.Errorf("a task with dashes was refused as a worker: %v", err)
	}
}

func TestMergeFromASubSessionsBranchByItsName(t *testing.T) {
	c, git := managedWorktree(t, "https://github.com/example-org/docs.git")
	write(t, c, "notes.md", "groundwork\n")
	git("add", ".")
	git("commit", "-qm", "groundwork")
	// A sub-session names its branch after its change.
	dir := filepath.Join(t.TempDir(), "nav")
	git("worktree", "add", "-q", "-b", "13-docs-nav", dir, "openclaw/72-add-thing")
	if err := os.WriteFile(filepath.Join(dir, "nav.md"), []byte("nav\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, "-C", dir, "add", ".")
	gitIn(t, "-C", dir, "commit", "-qm", "nav")

	got := WorkerRepos("72-add-thing", []string{c.Dir}, []string{dir})
	if len(got) != 1 || got[0].Path != dir || got[0].Branch != "13-docs-nav" || got[0].ToMerge != 1 {
		t.Fatalf("compared: %+v", got)
	}
	result, err := c.MergeFrom("13-docs-nav")
	if err != nil || !result.Merged || result.From != "13-docs-nav" {
		t.Fatalf("merge by name: %+v %v", result, err)
	}
	if _, err := os.Stat(filepath.Join(c.Dir, "nav.md")); err != nil {
		t.Errorf("the sub-session's file is not merged: %v", err)
	}
	// Its own branch is no branch to merge from.
	if _, err := c.MergeFrom(c.Branch()); err == nil {
		t.Error("merged the checkout's own branch into itself")
	}
}

func TestStatusNamesTheNextStep(t *testing.T) {
	c, git := managedWorktree(t, "/dev/null/unused")
	bare := git("remote", "get-url", "--push", "origin")
	git("remote", "set-url", "origin", bare)
	fetch := func() { t.Helper(); git("fetch", "-q", "origin", "+refs/heads/main:refs/remotes/origin/main") }

	// Committed work, nothing new on the base: the person pushes it.
	write(t, c, "notes.md", "mine\n")
	git("add", ".")
	git("commit", "-qm", "work")
	s := checkoutStatus(c.Dir, false)
	if s.Next == nil || s.Next.Step != "push" || s.Next.By != next.ByWTC || s.Next.Approve != next.ApprovePerson || s.Next.Gateway != "github_publish" {
		t.Fatalf("committed work: %+v", s.Next)
	}

	// The base moves on without touching it: a clean catch-up, the agent's call.
	advanceOrigin(t, bare, "OTHER", "theirs\n")
	fetch()
	s = checkoutStatus(c.Dir, false)
	if s.Next == nil || s.Next.Step != "catch-up" || s.Next.Approve != next.ApproveAgent || !strings.Contains(s.Next.Reason, "merges cleanly") {
		t.Fatalf("behind, clean: %+v", s.Next)
	}
	// ...and changing the same file: a merge for the agent, naming it.
	advanceOrigin(t, bare, "notes.md", "theirs\n")
	fetch()
	s = checkoutStatus(c.Dir, false)
	if s.Next == nil || s.Next.Step != "merge" || s.Next.By != next.ByAgent || !strings.Contains(s.Next.Reason, "conflicts in notes.md") {
		t.Fatalf("behind, conflicting: %+v", s.Next)
	}
	if git("status", "--porcelain") != "" {
		t.Fatal("predicting the merge touched the worktree")
	}

	// Uncommitted work offers an explicit commit handoff; catch-up waits.
	write(t, c, "draft.md", "wip\n")
	git("add", "draft.md")
	if s = checkoutStatus(c.Dir, false); s.Next == nil || s.Next.Step != "commit" || s.Next.By != next.ByAgent {
		t.Fatalf("work in progress must stay with the author: %+v", s.Next)
	}
	git("rm", "-q", "--cached", "draft.md")
	if err := os.Remove(filepath.Join(c.Dir, "draft.md")); err != nil {
		t.Fatal(err)
	}

	// Detached at an older base: wtc moves it, nobody needs to agree.
	git("checkout", "-q", "--detach", "origin/main~1")
	s = checkoutStatus(c.Dir, false)
	if s.Next == nil || s.Next.Step != "catch-up" || s.Next.By != next.ByWTC || s.Next.Approve != "" {
		t.Fatalf("detached, behind: %+v", s.Next)
	}
}

// A squash merge leaves the branch's commits out of the base. Following the
// step the status names, back to the tip, must not end in a step that asks
// to move wtc's own merge of the base to a new branch.
func TestAfterASquashMergeTheNextStepsEnd(t *testing.T) {
	c, git := managedWorktree(t, "/dev/null/unused")
	bare := git("remote", "get-url", "--push", "origin")
	git("remote", "set-url", "origin", bare)
	write(t, c, "feature.md", "feature\n")
	git("add", ".")
	git("commit", "-qm", "the feature")
	head := git("rev-parse", "HEAD")
	// The pull request is squash-merged: its content lands on main as one
	// new commit, and main moves on.
	advanceOrigin(t, bare, "feature.md", "feature\n")
	advanceOrigin(t, bare, "OTHER", "later\n")
	git("fetch", "-q", "origin", "+refs/heads/main:refs/remotes/origin/main")
	squash := git("rev-parse", "origin/main~1")
	merged := func() *PullRequest {
		in := gitOK(c.Dir, "merge-base", "--is-ancestor", squash, "HEAD")
		return &PullRequest{Number: 7, State: "merged", Head: head, MergedIn: &in}
	}
	step := func() *next.Step {
		s := checkoutStatus(c.Dir, false)
		s.PullRequest = merged()
		return next.For(s.facts(c.Dir))
	}
	if got := step(); got == nil || got.Step != "return-to-tip" || got.Approve != "" {
		t.Fatalf("after the squash merge: %+v", got)
	}
	// The unknown-forge fixture exercises the legacy fallback: its merge of
	// the base must not be counted as new work.
	if _, err := c.CatchUp(); err != nil {
		t.Fatal(err)
	}
	// The merge itself is no work of its own, but its completed branch must
	// still be left. The gateway path that knows the PR does this directly.
	if got := step(); got == nil || got.Step != "return-to-tip" {
		t.Fatalf("still on the completed branch: %+v", got)
	}
	git("checkout", "-q", "--detach", "origin/main")
	// ...and then nothing is left to do.
	if got := step(); got != nil {
		t.Fatalf("after catching up: %+v", got)
	}
	// Real work after the merge goes to a new branch.
	write(t, c, "followup.md", "more\n")
	git("add", ".")
	git("commit", "-qm", "follow-up")
	if got := step(); got == nil || got.Step != "new-branch" || got.By != next.ByAgent {
		t.Fatalf("a follow-up commit: %+v", got)
	}
}
