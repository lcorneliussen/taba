// Package openclaw defines what wtc hands to an OpenClaw agent instead of
// acting itself. A command in an OpenClaw agent's shell has no route to the
// gateway and holds no git credential, so wtc cannot create a worktree, push
// or open a pull request there. It prints an Intent, and the agent carries it
// out with the named tool.
//
// The shapes here are checked against a real gateway by the integration test
// in this package (build tag openclaw_integration).
package openclaw

import (
	"fmt"
	"regexp"
	"strings"
)

// ContractVersion changes when an emitted Intent changes shape.
const ContractVersion = 1

// Host is the value of Intent.Host.
const Host = "openclaw"

// BranchPrefix is fixed by OpenClaw: a managed worktree named n is always on
// branch openclaw/n. There is no setting for it.
const BranchPrefix = "openclaw/"

// Actor says which session has to carry an intent out. One OpenClaw session
// owns exactly one repository, so a collection is a parent session plus one
// child session per repository.
type Actor string

const (
	// ActorParent is the collection's session. Only it may add repositories.
	ActorParent Actor = "parent"
	// ActorRepoSession is the child session that owns the repository.
	ActorRepoSession Actor = "repo-session"
	// ActorCurrent is whichever session runs the command.
	ActorCurrent Actor = "current"
)

const (
	ActionAddRepo = "add-repo"
	ActionPublish = "publish"
	ActionStatus  = "status"

	ToolSessionsSpawn = "sessions_spawn"
	ToolGitHubPublish = "github_publish"
	ToolSessionsList  = "sessions_list"
)

// Forge is where a repository's origin lives. OpenClaw publishes to GitHub
// itself; for any other forge the gateway has no tool, and a publish intent
// only describes the push and the pull request.
type Forge string

const (
	ForgeGitHub    Forge = "github"
	ForgeBitbucket Forge = "bitbucket"
	ForgeUnknown   Forge = "unknown"
)

// Origin is a repository's remote, reduced to what a forge needs. On
// Bitbucket Owner is the workspace.
type Origin struct {
	Forge Forge
	Owner string
	Repo  string
}

// ParseOrigin reads a remote URL: https://, ssh:// or scp-like, with or
// without a user. The gateway reports scp-like origins without one.
func ParseOrigin(url string) Origin {
	rest := url
	if _, after, ok := strings.Cut(url, "://"); ok {
		rest = after
	} else if host, path, ok := strings.Cut(url, ":"); ok && !strings.Contains(host, "/") {
		rest = host + "/" + path
	}
	if i := strings.Index(rest, "@"); i >= 0 && i < strings.Index(rest+"/", "/") {
		rest = rest[i+1:]
	}
	host, path, _ := strings.Cut(rest, "/")
	host, _, _ = strings.Cut(host, ":")
	owner, repo, _ := strings.Cut(strings.TrimSuffix(strings.TrimSuffix(path, "/"), ".git"), "/")
	repo, _, _ = strings.Cut(repo, "/")
	switch strings.ToLower(host) {
	case "github.com":
		return Origin{ForgeGitHub, owner, repo}
	case "bitbucket.org":
		return Origin{ForgeBitbucket, owner, repo}
	}
	// Owner and repository are only known where the path layout is.
	return Origin{Forge: ForgeUnknown}
}

// Intent is one thing wtc asks the agent to do. With a Tool, Args is passed
// to that tool unchanged. Without one the gateway has nothing for it: Args
// describes the work, and a skill for Forge carries it out or the agent says
// that it cannot.
type Intent struct {
	Host    string `json:"host"`
	Version int    `json:"version"`
	Action  string `json:"action"`
	Actor   Actor  `json:"actor"`
	Repo    string `json:"repo,omitempty"`
	Forge   Forge  `json:"forge,omitempty"`
	Tool    string `json:"tool,omitempty"`
	Args    any    `json:"args"`
}

// SpawnArgs are the sessions_spawn arguments that create a child session
// owning a managed worktree of one project.
type SpawnArgs struct {
	Task            string `json:"task"`
	Label           string `json:"label,omitempty"`
	ProjectID       string `json:"projectId,omitempty"`
	ProjectGitURL   string `json:"projectGitUrl,omitempty"`
	Worktree        bool   `json:"worktree"`
	WorktreeName    string `json:"worktreeName"`
	WorktreeBaseRef string `json:"worktreeBaseRef,omitempty"`
	Visible         bool   `json:"visible"`
	// ExpectsCompletionMessage false spares the requester a turn when the
	// child finishes: an owner that only holds its worktree has nothing to
	// report. Unset keeps OpenClaw's default, a completion handoff.
	ExpectsCompletionMessage *bool `json:"expectsCompletionMessage,omitempty"`
}

// PublishArgs are the github_publish arguments. The gateway takes repository,
// branch and base from the session that calls the tool, never from here.
type PublishArgs struct {
	Title string `json:"title,omitempty"`
	Body  string `json:"body,omitempty"`
}

// PullRequestArgs describe a publication the gateway does not broker: push
// Source to origin, then open a pull request from it into Dest.
type PullRequestArgs struct {
	Owner  string `json:"owner"`
	Repo   string `json:"repo"`
	Source string `json:"source"`
	Dest   string `json:"dest"`
	Title  string `json:"title,omitempty"`
	Body   string `json:"body,omitempty"`
	Draft  bool   `json:"draft"`
}

// ListArgs are the sessions_list arguments. The collection's repositories are
// the listed sessions whose parentSessionKey is the collection's session.
type ListArgs struct{}

var worktreeName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// ValidWorktreeName reports whether the gateway accepts name for a managed
// worktree: lowercase letters, digits and hyphens, at most 64 characters.
func ValidWorktreeName(name string) bool { return worktreeName.MatchString(name) }

// Branch is the branch a managed worktree named worktree gets.
func Branch(worktree string) string { return BranchPrefix + worktree }

var gitHubShorthand = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]*/[A-Za-z0-9._-]+$`)

// ProjectSource reads what add-repo was given: the name of a project the
// gateway has registered, a GitHub repository (owner/name, or an HTTPS or
// git@github.com URL) that the gateway clones itself, or a Bitbucket
// repository URL, which someone else has to clone onto the gateway first.
// name is the repository's short name.
func ProjectSource(arg string) (name, project, gitURL string, err error) {
	if !strings.Contains(arg, "://") && !strings.HasPrefix(arg, "git@") && !gitHubShorthand.MatchString(arg) {
		return arg, arg, "", nil
	}
	if gitHubShorthand.MatchString(arg) {
		arg = "https://github.com/" + arg
	}
	origin := ParseOrigin(arg)
	if origin.Owner == "" || origin.Repo == "" {
		return "", "", "", fmt.Errorf("add-repo %s: not a repository URL", arg)
	}
	switch origin.Forge {
	case ForgeGitHub:
		return origin.Repo, "", "https://github.com/" + origin.Owner + "/" + origin.Repo + ".git", nil
	case ForgeBitbucket:
		return origin.Repo, "", "https://bitbucket.org/" + origin.Owner + "/" + origin.Repo + ".git", nil
	}
	return "", "", "", fmt.Errorf("add-repo %s: only GitHub and Bitbucket repositories can be added by URL; register a checkout of any other one as a gateway project and pass its name", arg)
}

// AddRepo asks the parent session for a child that owns a worktree of a
// project on the gateway, or of a GitHub repository at gitURL that the gateway
// clones. worktree is the collection's <issue-id>-<slug>; baseRef may be empty.
//
// The owner of a project's worktree is a hidden subagent: the collection's
// session works in the worktree itself, and the owner only holds it for
// publication. The gateway clones a repository only for a visible session, so
// a gitURL makes the owner a conversation of its own. Either way the owner
// does not report back when it has started (see Worker for one that does).
func AddRepo(repo, project, gitURL, worktree, baseRef, task string) (Intent, error) {
	if (project == "") == (gitURL == "") {
		return Intent{}, fmt.Errorf("add-repo %s: give an OpenClaw project or a GitHub URL", repo)
	}
	if !ValidWorktreeName(worktree) {
		return Intent{}, fmt.Errorf("add-repo %s: %q is not a valid OpenClaw worktree name (lowercase letters, digits and hyphens, at most 64)", repo, worktree)
	}
	labelName := project
	if labelName == "" {
		labelName = repo
	}
	return intent(ActionAddRepo, ActorParent, repo, ToolSessionsSpawn, SpawnArgs{
		Task: task, Label: worktree + " " + labelName, ProjectID: project, ProjectGitURL: gitURL,
		Worktree: true, WorktreeName: worktree, WorktreeBaseRef: baseRef, Visible: gitURL != "",
		ExpectsCompletionMessage: new(bool),
	}), nil
}

// Worker is a worker's add-repo intent: a worker takes part of the task, so
// its completion comes back to the session that asked for it.
func Worker(in Intent) Intent {
	if a, ok := in.Args.(SpawnArgs); ok {
		a.ExpectsCompletionMessage = nil
		in.Args = a
	}
	return in
}

// Publish asks the session that owns repo to publish its branch as a draft
// pull request into dest. On GitHub the gateway does it and takes branch and
// base from the session; elsewhere the intent carries them.
func Publish(repo string, origin Origin, worktree, dest, title, body string) Intent {
	var in Intent
	if origin.Forge == ForgeGitHub {
		in = intent(ActionPublish, ActorRepoSession, repo, ToolGitHubPublish, PublishArgs{Title: title, Body: body})
	} else {
		in = intent(ActionPublish, ActorRepoSession, repo, "", PullRequestArgs{
			Owner: origin.Owner, Repo: origin.Repo, Source: Branch(worktree), Dest: dest,
			Title: title, Body: body, Draft: true,
		})
	}
	in.Forge = origin.Forge
	return in
}

// Status asks the current session to list the sessions it can see.
func Status() Intent {
	return intent(ActionStatus, ActorCurrent, "", ToolSessionsList, ListArgs{})
}

func intent(action string, actor Actor, repo, tool string, args any) Intent {
	return Intent{Host: Host, Version: ContractVersion, Action: action, Actor: actor, Repo: repo, Tool: tool, Args: args}
}
