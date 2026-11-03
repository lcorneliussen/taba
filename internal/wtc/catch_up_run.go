package wtc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type catchUpPR struct {
	state, mergeCommit, head, base string
	// branch, when set, is the local branch these facts were read for.
	// Reconciliation refuses a worktree that has moved to another branch.
	branch        string
	lookupFailure string
}

func catchUpGit(path string, args ...string) (string, error) {
	return gitOutput(append([]string{"-C", path}, args...)...)
}

func catchUpGitOK(path string, args ...string) bool {
	_, err := catchUpGit(path, args...)
	return err == nil
}

func (c *Context) catchUpDefaultRef(t catchUpTarget) (string, error) {
	target, err := OpenCollection(filepath.Join(c.Workspace, t.collection))
	if err != nil {
		return "", err
	}
	repo, err := target.Repository(t.repo)
	if err != nil || repo.DefaultRef == "" {
		return catchUpLocalRef(t, "origin/main"), nil
	}
	return catchUpLocalRef(t, repo.DefaultRef), nil
}

// catchUpOriginRepo is the registry entry whose remote a shared owner should
// follow. Ad-hoc collections and unmanaged siblings are left alone.
func (c *Context) catchUpOriginRepo(t catchUpTarget) (Repo, bool) {
	if !t.managed {
		return Repo{}, false
	}
	target, err := OpenCollection(filepath.Join(c.Workspace, t.collection))
	if err != nil || target.AdHoc {
		return Repo{}, false
	}
	repo, err := target.Repository(t.repo)
	return repo, err == nil
}

// catchUpFetch fetches origin into an owner and reduces a failure to git's
// own cause line, which says why the configured remote cannot be reached.
func catchUpFetch(owner string) error {
	out, err := exec.Command("git", "--git-dir="+owner, "fetch", "--prune", "origin").CombinedOutput()
	if err == nil {
		return nil
	}
	first := ""
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "fatal:") || strings.HasPrefix(line, "error:") {
			return errors.New(line)
		}
		if first == "" {
			first = line
		}
	}
	if first != "" {
		return errors.New(first)
	}
	return err
}

// catchUpForgeTimeout bounds one forge CLI call, as status bounds its own.
var catchUpForgeTimeout = 30 * time.Second

// catchUpJSON runs a forge CLI. A stalled gh or bb fails the lookup, which
// leaves the branch untouched, instead of blocking a sweep or status refresh.
func catchUpJSON(args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), catchUpForgeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.WaitDelay = 200 * time.Millisecond
	out, err := forgeOutput(cmd)
	if ctx.Err() != nil {
		return nil, fmt.Errorf("%s timed out after %s", args[0], catchUpForgeTimeout)
	}
	return out, err
}

func catchUpForge(remote string) (string, string) {
	remote = normalizeCatchUpRemote(remote)
	for _, host := range []string{"github.com", "bitbucket.org"} {
		if strings.HasPrefix(remote, host+"/") {
			slug := strings.TrimPrefix(remote, host+"/")
			if strings.Count(slug, "/") == 1 {
				return slug, host
			}
		}
	}
	return "", ""
}

func catchUpValidPRState(state string) bool {
	switch state {
	case "OPEN", "DRAFT", "MERGED", "CLOSED", "DECLINED", "SUPERSEDED":
		return true
	}
	return false
}

func catchUpRefForPR(worktree, defaultRef string, pr catchUpPR) (string, error) {
	if pr.state != "OPEN" && pr.state != "DRAFT" {
		return defaultRef, nil
	}
	if pr.base == "" || !catchUpGitOK(worktree, "check-ref-format", "--branch", pr.base) {
		return "", fmt.Errorf("open PR merge target unavailable or invalid")
	}
	return "origin/" + pr.base, nil
}

var catchUpLookupAuthStatus = regexp.MustCompile(`(?i)\b(?:HTTP(?:/[0-9.]+)?[ \t]+|status(?: code)?[ :=\t]+)(?:401|403)\b`)

// Lookup diagnostics use fixed cause categories instead of echoing forge stderr,
// response bodies or remote URLs: each can carry credentials or terminal controls.
func catchUpLookupCause(err error) string {
	if errors.Is(err, exec.ErrNotFound) {
		return "forge CLI not installed; install it and retry"
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		detail := exit.Stderr
		if len(detail) > 8192 {
			detail = detail[:8192]
		}
		text := strings.ToLower(string(detail))
		if catchUpLookupAuthStatus.MatchString(text) {
			return "authentication or authorization failed; check CLI login and repository access, then retry"
		}
		for _, marker := range []string{"unauthorized", "forbidden", "authentication", "not logged", "auth login"} {
			if strings.Contains(text, marker) {
				return "authentication or authorization failed; check CLI login and repository access, then retry"
			}
		}
		for _, marker := range []string{"could not resolve host", "no such host", "connection refused", "connection reset", "dial tcp", "tls handshake", "tls:", "timeout", "timed out", "network is unreachable"} {
			if strings.Contains(text, marker) {
				return "transport failure; check network connectivity and retry"
			}
		}
		return fmt.Sprintf("forge CLI exited with status %d; inspect the operation locally and retry", exit.ExitCode())
	}
	if strings.Contains(err.Error(), "timed out after") {
		return "forge lookup timed out; check connectivity and retry"
	}
	return "forge command could not run; check CLI installation and local configuration, then retry"
}

func catchUpLookupLabel(value string) string {
	if len(value) > 160 {
		value = value[:160] + "..."
	}
	return strconv.QuoteToASCII(value)
}

func catchUpLookupUnavailable(operation, cause string) catchUpPR {
	return catchUpPR{state: "UNKNOWN", lookupFailure: operation + ": " + cause}
}

func (c *Context) catchUpPRState(t catchUpTarget, branch string) catchUpPR {
	target, err := OpenCollection(filepath.Join(c.Workspace, t.collection))
	if err != nil {
		return catchUpLookupUnavailable("open target collection", "collection unavailable; inspect its local configuration and retry")
	}
	records, err := target.ListPRs()
	if err != nil {
		return catchUpLookupUnavailable("read enlisted PR records", "records unavailable; inspect local PR enlistment and retry")
	}
	selected := []PRRecord{}
	repos := target.prRepositories()
	for _, record := range records {
		if record.Branch != branch {
			continue
		}
		match, err := repos.matchesDir(record.Repo, filepath.Base(t.path))
		if err != nil {
			return catchUpLookupUnavailable("resolve enlisted PR repository", "repository identity unavailable; inspect local registry and PR enlistment, then retry")
		}
		if match {
			selected = append(selected, record)
		}
	}
	remote, err := catchUpGit(t.path, "remote", "get-url", "origin")
	if err != nil {
		return catchUpLookupUnavailable("git remote get-url origin", "origin unavailable; inspect repository remote configuration and retry")
	}
	slug, forge := catchUpForge(remote)
	if forge == "" {
		return catchUpLookupUnavailable("identify origin forge", "unsupported or invalid remote; configure a supported GitHub or Bitbucket origin and retry")
	}
	// Only validated forge repository identifiers are included in diagnostics.
	identity := forge + "/" + slug
	for _, ch := range slug {
		if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || strings.ContainsRune("_./-", ch)) {
			identity = forge + "/[repository redacted]"
			break
		}
	}
	if len(identity) > 200 {
		identity = forge + "/[repository redacted]"
	}
	operation := identity + " branch " + catchUpLookupLabel(branch) + ": gh pr list"
	if len(selected) == 0 {
		if forge != "github.com" {
			return catchUpLookupUnavailable(identity+" branch "+catchUpLookupLabel(branch), "branch PR lookup unsupported; enlist the PR number with wtc pr enlist and retry")
		}
		out, err := catchUpJSON("gh", "pr", "list", "--repo", slug, "--head", branch, "--state", "all", "--json", "state,isDraft,baseRefName")
		if err != nil {
			return catchUpLookupUnavailable(operation+" failed", catchUpLookupCause(err))
		}
		var rows []struct {
			State       string `json:"state"`
			IsDraft     bool   `json:"isDraft"`
			BaseRefName string `json:"baseRefName"`
		}
		if json.Unmarshal(out, &rows) != nil {
			return catchUpLookupUnavailable(operation+" failed", "invalid JSON response; check forge CLI compatibility and retry")
		}
		if len(rows) == 0 {
			return catchUpPR{state: "NONE"}
		}
		chosen := catchUpPR{state: "NONE"}
		for _, row := range rows {
			state := row.State
			if !catchUpValidPRState(state) {
				return catchUpLookupUnavailable(operation+" failed", "unrecognized PR state; check forge CLI compatibility and retry")
			}
			if row.IsDraft && state == "OPEN" {
				state = "DRAFT"
			}
			if state == "OPEN" || state == "DRAFT" {
				if chosen.state == "OPEN" || chosen.state == "DRAFT" {
					if chosen.base != row.BaseRefName {
						return catchUpLookupUnavailable(operation+" failed", "ambiguous open PR merge targets; inspect matching PRs and enlist the intended PR")
					}
				} else {
					chosen = catchUpPR{state: state, base: row.BaseRefName}
				}
			} else if chosen.state == "NONE" {
				chosen = catchUpPR{state: state}
			}
		}
		return chosen
	}
	chosen := catchUpPR{state: "UNKNOWN"}
	liveState := ""
	liveBase := ""
	for _, record := range selected {
		number := record.Number
		if _, err := strconv.ParseUint(number, 10, 64); err != nil {
			number = "[invalid number]"
		}
		cli := "gh"
		if forge == "bitbucket.org" {
			cli = "bb"
		}
		operation = identity + " PR #" + number + ": " + cli + " pr view"
		if forge == "bitbucket.org" {
			parts := strings.SplitN(slug, "/", 2)
			out, err := catchUpJSON("bb", "pr", "view", record.Number, "--workspace", parts[0], "--repo", parts[1], "--json")
			if err != nil {
				return catchUpLookupUnavailable(operation+" failed", catchUpLookupCause(err))
			}
			var facts struct {
				State string `json:"state"`
				Draft bool   `json:"draft"`
				Merge struct {
					Hash string `json:"hash"`
				} `json:"merge_commit"`
				Source struct {
					Commit struct {
						Hash string `json:"hash"`
					} `json:"commit"`
				} `json:"source"`
				Destination struct {
					Branch struct {
						Name string `json:"name"`
					} `json:"branch"`
				} `json:"destination"`
			}
			if json.Unmarshal(out, &facts) != nil {
				return catchUpLookupUnavailable(operation+" failed", "invalid JSON response; check forge CLI compatibility and retry")
			}
			state := strings.ToUpper(facts.State)
			if facts.Draft && state == "OPEN" {
				state = "DRAFT"
			}
			if !catchUpValidPRState(state) {
				return catchUpLookupUnavailable(operation+" failed", "unrecognized PR state; check forge CLI compatibility and retry")
			}
			if state == "OPEN" || state == "DRAFT" {
				base := facts.Destination.Branch.Name
				if liveState != "" && liveBase != base {
					return catchUpLookupUnavailable(operation+" failed", "ambiguous open PR merge targets; inspect enlisted PRs and correct enlistment")
				}
				liveBase = base
				if state == "DRAFT" || liveState == "" {
					liveState = state
				}
				continue
			}
			if chosen.state == "UNKNOWN" || chosen.state == "NONE" {
				chosen = catchUpPR{state: state, head: facts.Source.Commit.Hash, mergeCommit: facts.Merge.Hash}
			}
			continue
		}
		out, err := catchUpJSON("gh", "pr", "view", record.Number, "--repo", slug, "--json", "state,isDraft,headRefOid,mergeCommit,baseRefName")
		if err != nil {
			return catchUpLookupUnavailable(operation+" failed", catchUpLookupCause(err))
		}
		var facts struct {
			State       string `json:"state"`
			IsDraft     bool   `json:"isDraft"`
			HeadRefOid  string `json:"headRefOid"`
			BaseRefName string `json:"baseRefName"`
			Merge       struct {
				OID string `json:"oid"`
			} `json:"mergeCommit"`
		}
		if json.Unmarshal(out, &facts) != nil {
			return catchUpLookupUnavailable(operation+" failed", "invalid JSON response; check forge CLI compatibility and retry")
		}
		if !catchUpValidPRState(facts.State) {
			return catchUpLookupUnavailable(operation+" failed", "unrecognized PR state; check forge CLI compatibility and retry")
		}
		state := facts.State
		if facts.IsDraft && state == "OPEN" {
			state = "DRAFT"
		}
		if state == "OPEN" || state == "DRAFT" {
			if liveState != "" && liveBase != facts.BaseRefName {
				return catchUpLookupUnavailable(operation+" failed", "ambiguous open PR merge targets; inspect enlisted PRs and correct enlistment")
			}
			liveBase = facts.BaseRefName
			if state == "DRAFT" || liveState == "" {
				liveState = state
			}
			continue
		}
		if chosen.state == "UNKNOWN" || chosen.state == "NONE" {
			chosen = catchUpPR{state: state, head: facts.HeadRefOid, mergeCommit: facts.Merge.OID}
		}
	}
	if liveState != "" {
		return catchUpPR{state: liveState, base: liveBase}
	}
	return chosen
}

func catchUpLanded(t catchUpTarget, target string, pr catchUpPR) bool {
	extra, err := catchUpGit(t.path, "rev-list", "--count", target+"..HEAD")
	if err != nil {
		return false
	}
	if extra == "0" {
		return true
	}
	if pr.mergeCommit != "" && catchUpGitOK(t.path, "merge-base", "--is-ancestor", pr.mergeCommit, target) {
		if pr.head != "" {
			prHead, err := catchUpGit(t.path, "rev-parse", "--verify", pr.head+"^{commit}")
			if head, headErr := catchUpGit(t.path, "rev-parse", "HEAD"); err == nil && headErr == nil && head == prHead {
				return true
			}
		}
		// A squash merge rewrites the branch's commits into one, so cherry
		// finds no equivalents. It still carries the branch's content.
		if catchUpSquashCarries(t.path, pr.mergeCommit) {
			return true
		}
	}
	cherry, err := catchUpGit(t.path, "cherry", target, "HEAD")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(cherry, "\n") {
		if strings.HasPrefix(line, "+") {
			return false
		}
	}
	return true
}

// catchUpSquashCarries reports whether a merge commit on the target carries
// the branch at HEAD: either both have the same tree, or the merge commit's
// own change has the same patch-id as the branch's cumulative change since it
// forked (whitespace and line numbers aside).
func catchUpSquashCarries(worktree, merge string) bool {
	head, err := catchUpGit(worktree, "rev-parse", "--verify", "HEAD^{tree}")
	if err != nil {
		return false
	}
	if tree, err := catchUpGit(worktree, "rev-parse", "--verify", merge+"^{tree}"); err == nil && tree == head {
		return true
	}
	parent, err := catchUpGit(worktree, "rev-parse", "--verify", merge+"^1")
	if err != nil {
		return false
	}
	base, err := catchUpGit(worktree, "merge-base", "HEAD", parent)
	if err != nil {
		return false
	}
	branch := catchUpPatchID(worktree, base, "HEAD")
	return branch != "" && branch == catchUpPatchID(worktree, parent, merge)
}

// catchUpPatchID identifies the change between two commits independently of
// how it was committed. An empty change has no identity.
func catchUpPatchID(worktree, from, to string) string {
	diff, err := exec.Command("git", "-C", worktree, "diff", "--no-ext-diff", "--no-textconv", "--no-color", "--binary", from, to).Output()
	if err != nil || len(diff) == 0 {
		return ""
	}
	cmd := exec.Command("git", "-C", worktree, "patch-id", "--stable")
	cmd.Stdin = bytes.NewReader(diff)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

func catchUpOperation(t catchUpTarget) string {
	for _, marker := range []string{"MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "rebase-merge", "rebase-apply", "sequencer"} {
		path, err := catchUpGit(t.path, "rev-parse", "--git-path", marker)
		if err != nil {
			return marker
		}
		if !filepath.IsAbs(path) {
			path = filepath.Join(t.path, path)
		}
		if _, err := os.Lstat(path); err == nil {
			return marker
		}
	}
	return ""
}

func catchUpStashRef(t catchUpTarget) string {
	return "refs/wtc-catch-up/" + t.collection + "/" + t.repo
}

func catchUpRestoreStash(t catchUpTarget, stash string) (string, bool) {
	if stash == "" {
		return "", true
	}
	if !catchUpGitOK(t.path, "stash", "apply", stash) {
		return "stash restore conflicted; retained " + stash + " pinned under refs/wtc-catch-up/", false
	}
	latest, _ := catchUpGit(t.path, "rev-parse", "-q", "--verify", "refs/stash")
	if latest != stash || !catchUpGitOK(t.path, "stash", "drop", "stash@{0}") {
		return "changes restored; retained stash " + stash + " because stack changed or drop failed", false
	}
	_, _ = catchUpGit(t.path, "update-ref", "-d", catchUpStashRef(t))
	return "", true
}

func (c *Context) catchUpReconcile(t catchUpTarget, ref, target string, pr catchUpPR, opt CatchUpOptions) (string, string) {
	if marker := catchUpOperation(t); marker != "" {
		reason := "in-progress " + marker + "; untouched"
		if marker == "MERGE_HEAD" {
			if paths, err := catchUpGit(t.path, "diff", "--name-only", "--diff-filter=U"); err == nil && paths != "" {
				reason += "; unmerged: " + strings.ReplaceAll(paths, "\n", ", ")
			}
		}
		return "needs-owner", reason
	}
	status, err := catchUpGit(t.path, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return "failed", "worktree status unreadable"
	}
	if (opt.All || opt.CleanOnly) && status != "" {
		return "needs-owner", "dirty worktree; clean-only leaves it untouched"
	}
	branch, _ := catchUpGit(t.path, "symbolic-ref", "-q", "--short", "HEAD")
	if pr.branch != "" && branch != pr.branch {
		return "needs-owner", "worktree left " + pr.branch + " while its PR was read; untouched"
	}
	if branch == "" && !catchUpGitOK(t.path, "merge-base", "--is-ancestor", "HEAD", target) {
		return "needs-owner", "detached commits not contained in default tip; preserve them on an owner branch"
	}
	if branch != "" && pr.state != "MERGED" && reviewOnlyWorktree(t.path) {
		return "current", "review-only PR worktree; `wtc review bundle` follows the PR head"
	}
	switch pr.state {
	case "UNKNOWN":
		reason := "PR state unavailable; branch left untouched"
		if pr.lookupFailure != "" {
			reason += "; " + pr.lookupFailure
		}
		return "needs-owner", reason
	case "CLOSED", "DECLINED", "SUPERSEDED":
		return "needs-owner", "closed PR branch; owner must decide continuation"
	case "MERGED":
		if !catchUpLanded(t, target, pr) {
			return "needs-owner", "merged PR landing not established; owner must inspect remaining commits or unavailable Git evidence"
		}
	default:
		if branch != "" && catchUpGitOK(t.path, "merge-base", "--is-ancestor", target, "HEAD") {
			return "current", "already contains " + ref
		}
	}
	if branch == "" {
		if head, err := catchUpGit(t.path, "rev-parse", "HEAD"); err == nil && head == target {
			return "current", "already contains default tip"
		}
	}
	if opt.DryRun {
		return "planned", "would update against local " + ref + " (remote not fetched)"
	}
	stash := ""
	if status != "" {
		before, _ := catchUpGit(t.path, "rev-parse", "-q", "--verify", "refs/stash")
		if !catchUpGitOK(t.path, "stash", "push", "--include-untracked", "-m", "wtc-catch-up "+t.collection+"/"+t.repo) {
			return "needs-owner", "stash failed; update skipped"
		}
		after, _ := catchUpGit(t.path, "rev-parse", "-q", "--verify", "refs/stash")
		if after != before {
			stash = after
			if !catchUpGitOK(t.path, "update-ref", catchUpStashRef(t), stash) {
				if restoreReason, ok := catchUpRestoreStash(t, stash); !ok {
					return "needs-owner", "recovery stash pin failed; " + restoreReason
				}
				return "needs-owner", "recovery stash pin failed; update skipped"
			}
		}
	}
	outcome, reason := "updated", ""
	if branch == "" || pr.state == "MERGED" {
		if !catchUpGitOK(t.path, "checkout", "--detach", target) {
			outcome, reason = "needs-owner", "checkout refused"
		} else {
			reason = "detached at default tip"
			if branch != "" && !catchUpGitOK(t.path, "branch", "-d", branch) {
				reason += "; local branch retained because safe pruning refused"
			}
		}
	} else if branch == catchUpBranchName(ref) {
		if catchUpGitOK(t.path, "merge", "--ff-only", target) {
			reason = "fast-forwarded default branch"
		} else {
			outcome, reason = "needs-owner", "default branch could not fast-forward"
		}
	} else if !catchUpGitOK(t.path, "merge", "--no-edit", target) {
		outcome, reason = "needs-owner", "merge refused before creating merge state"
		if catchUpGitOK(t.path, "rev-parse", "-q", "--verify", "MERGE_HEAD") {
			paths, _ := catchUpGit(t.path, "diff", "--name-only", "--diff-filter=U")
			if catchUpGitOK(t.path, "merge", "--abort") {
				reason = "merge conflict; aborted to original tree"
				if paths != "" {
					reason += "; paths: " + strings.ReplaceAll(paths, "\n", ", ")
				}
			} else {
				reason = "merge abort failed; owner must restore worktree"
			}
		}
	} else {
		reason = "merged " + ref + " into " + branch
		if (pr.state == "OPEN" || pr.state == "DRAFT") && !catchUpGitOK(t.path, "push") {
			outcome, reason = "needs-owner", "merge succeeded but push refused"
		}
	}
	if restoreReason, ok := catchUpRestoreStash(t, stash); !ok {
		return "needs-owner", restoreReason
	}
	return outcome, reason
}

// CatchUp performs a collected sweep. Its report is returned even when one
// target fails, so callers can emit the full result before a nonzero exit.
func (c *Context) CatchUp(opt CatchUpOptions) (CatchUpReport, error) {
	report := CatchUpReport{SchemaVersion: 1, Initiator: c.Collection, ExitStatus: 1, Outcomes: []CatchUpRow{}}
	if opt.Report == "" && !opt.DryRun {
		opt.Report = filepath.Join(c.Collection, ".wtc-catch-up.json")
	}
	var err error
	if opt.Report != "" {
		opt.Report, err = filepath.Abs(opt.Report)
		if err != nil {
			return report, err
		}
		if info, statErr := os.Stat(filepath.Dir(opt.Report)); statErr != nil || !info.IsDir() {
			return report, fmt.Errorf("report parent directory does not exist")
		}
	}
	report, targets, inventoryErr := c.CatchUpInventory(opt)
	if inventoryErr != nil {
		report.ExitStatus = 1
		if err := SaveCatchUpReport(&report, opt.Report); err != nil {
			return report, fmt.Errorf("%w; report persistence failed: %v", inventoryErr, err)
		}
		return report, inventoryErr
	}
	// Every collection sharing an owner gets a say before the fetch: one whose
	// registry predates a rename cannot repoint origin, so a later one with the
	// alias must still be asked. Once origin moved, the others are no-ops.
	if !opt.DryRun {
		for _, t := range targets {
			repo, ok := c.catchUpOriginRepo(t)
			if !ok || t.noRemote {
				continue
			}
			if changed, err := syncOwnerOrigin(t.owner, repo); err != nil {
				report.add("origin", t.collection, t.repo, "failed", err.Error(), "", "", "")
			} else if changed {
				report.add("origin", t.collection, t.repo, "updated", "origin now follows the registry remote "+repo.Remote, "", "", "")
			}
		}
	}
	fetched := map[string]string{}
	for _, t := range targets {
		if _, ok := fetched[t.owner]; ok {
			continue
		}
		state, reason := "planned", t.owner
		if t.noRemote {
			// Reported on its own outcome, which callers may treat as they
			// choose; it does not fail the sweep by itself.
			state, reason = "no-remote", "no remote; reconciling against local branches"
		}
		if !t.noRemote && !opt.DryRun {
			if err := catchUpFetch(t.owner); err != nil {
				state, reason = "failed", err.Error()
			} else {
				state = "ok"
			}
		}
		fetched[t.owner] = state
		report.add("fetch", t.collection, t.repo, state, reason, "", "", "")
	}
	// The initiating collection's own agent is the one running this sweep.
	busy := map[string]string{}
	if opt.IdleOnly {
		session := catchUpSession(c)
		for _, t := range targets {
			if _, seen := busy[t.collection]; seen || t.collection == filepath.Base(c.Collection) {
				continue
			}
			status, err := catchUpAgentBusy(session, t.collection)
			if err != nil {
				status = "unknown: " + err.Error()
			}
			busy[t.collection] = status
		}
	}
	for _, t := range targets {
		source, err := catchUpGit(t.path, "rev-parse", "--verify", "HEAD")
		if status := busy[t.collection]; status != "" {
			report.add("repo", t.collection, t.repo, "needs-owner", "agent "+status+"; idle-only leaves it untouched", source, "", source)
			continue
		}
		if fetched[t.owner] == "failed" {
			// Nothing is reconciled from stale refs, so no PR or ref check can
			// change the outcome. Repo-scoped hooks never needed the remote;
			// collection-level renders wait for a harness known to be current.
			report.add("repo", t.collection, t.repo, "failed", "owner fetch failed; stale refs not used", source, "", source)
			report.Outcomes[len(report.Outcomes)-1].TargetRef = t.ref
			c.catchUpHooks(&report, t, opt, true)
			if t.harness && opt.ReloadStatus {
				report.add("pane", t.collection, "status", "skipped", "harness update needs attention; pane left running", "", "", "")
			}
			continue
		}
		if err != nil {
			report.add("repo", t.collection, t.repo, "failed", "worktree HEAD unreadable; target may have moved or disappeared", "", "", "")
			continue
		}
		ref, refErr := c.catchUpDefaultRef(t)
		if refErr != nil {
			report.add("repo", t.collection, t.repo, "failed", "target harness registry unavailable after update", source, "", source)
			continue
		}
		pr := catchUpPR{state: "NONE"}
		branch, _ := catchUpGit(t.path, "symbolic-ref", "-q", "--short", "HEAD")
		// An owner without a remote has no forge, so no PR to consult.
		if branch != "" && branch != catchUpBranchName(ref) && !t.noRemote && catchUpOperation(t) == "" {
			pr = c.catchUpPRState(t, branch)
		}
		ref, refErr = catchUpRefForPR(t.path, ref, pr)
		if refErr != nil {
			report.add("repo", t.collection, t.repo, "needs-owner", refErr.Error()+"; branch left untouched", source, "", source)
			continue
		}
		target, targetErr := catchUpGit(t.path, "rev-parse", "--verify", ref+"^{commit}")
		outcome, reason := "failed", ref+" unavailable"
		if targetErr != nil && (pr.state == "OPEN" || pr.state == "DRAFT") {
			outcome, reason = "needs-owner", "open PR merge target "+ref+" is unavailable; branch left untouched"
		} else if targetErr == nil {
			outcome, reason = c.catchUpReconcile(t, ref, target, pr, opt)
		}
		result, resultErr := catchUpGit(t.path, "rev-parse", "--verify", "HEAD")
		if resultErr != nil {
			outcome, reason = "failed", "worktree HEAD unreadable after update"
		}
		report.add("repo", t.collection, t.repo, outcome, reason, source, target, result)
		row := &report.Outcomes[len(report.Outcomes)-1]
		row.TargetRef = ref
		if strings.HasPrefix(reason, "merge conflict;") {
			row.NextAction = strings.Replace(row.NextAction, "merge the target ref", "merge "+ref, 1)
		}
		if outcome == "updated" || outcome == "current" || outcome == "planned" {
			c.catchUpHooks(&report, t, opt, false)
		} else if t.harness && opt.ReloadStatus {
			report.add("pane", t.collection, "status", "skipped", "harness update needs attention; pane left running", "", "", "")
		}
	}
	if err := SaveCatchUpReport(&report, opt.Report); err != nil {
		return report, fmt.Errorf("report persistence failed: %w", err)
	}
	if report.ExitStatus != 0 {
		return report, fmt.Errorf("catch-up needs attention; see report")
	}
	return report, nil
}

// catchUpHooks runs the repo-scoped hooks, then, unless repoOnly, the
// collection-level renders a harness target governs.
func (c *Context) catchUpHooks(report *CatchUpReport, t catchUpTarget, opt CatchUpOptions, repoOnly bool) {
	targetDir := filepath.Join(c.Workspace, t.collection)
	// The harness updates first and may have changed registry membership, so
	// decide from the current target registry.
	managed := t.managed
	target, openErr := OpenCollection(targetDir)
	if openErr == nil {
		_, repoErr := target.Repository(t.repo)
		managed = t.harness || repoErr == nil
	}
	run := func(label string, action func(*Context) error) {
		if openErr != nil {
			report.add("hook", t.collection, label, "failed", "cannot open target collection: "+openErr.Error(), "", "", "")
			return
		}
		if opt.DryRun {
			report.add("hook", t.collection, label, "planned", "would run native collection action", "", "", "")
			return
		}
		if err := action(target); err != nil {
			report.add("hook", t.collection, label, "failed", "native collection action failed: "+err.Error(), "", "", "")
		} else {
			report.add("hook", t.collection, label, "ok", "completed", "", "", "")
		}
	}
	if !opt.NoSecrets {
		if managed {
			run("secrets:"+t.repo, func(target *Context) error {
				values := map[string]string{"repo": t.repo, "include_prod": "false"}
				if err := target.RunHook("secrets.link.pre", values); err != nil {
					return err
				}
				if _, err := target.LinkSecrets(SecretLinkOptions{Repo: t.repo}); err != nil {
					return err
				}
				return target.RunHook("secrets.link.post", values)
			})
		} else {
			report.add("hook", t.collection, "secrets:"+t.repo, "skipped", "unmanaged sibling; no registry secrets to link", "", "", "")
		}
	}
	if !t.harness || repoOnly {
		return
	}
	if !opt.NoEnv {
		run("env", (*Context).refreshEnv)
	}
	if opt.InstallTools {
		if last := report.Outcomes[len(report.Outcomes)-1]; !opt.NoEnv && last.Repo == "env" && last.Outcome == "failed" {
			report.add("hook", t.collection, "tools", "skipped", "environment refresh failed; nothing installed", "", "", "")
		} else if _, err := exec.LookPath("mise"); err != nil {
			report.add("hook", t.collection, "tools", "skipped", "mise not installed", "", "", "")
		} else {
			run("tools", (*Context).InstallMise)
		}
	}
	if !opt.NoSkills {
		run("skills", func(target *Context) error {
			if err := target.RunHook("skills.render.pre", nil); err != nil {
				return err
			}
			if _, err := target.RenderSkills(SkillRenderOptions{}); err != nil {
				return err
			}
			return target.RunHook("skills.render.post", nil)
		})
	}
	if !opt.NoMCP {
		if _, err := os.Stat(filepath.Join(targetDir, "harness", ".mcp-servers.yml")); os.IsNotExist(err) {
			report.add("hook", t.collection, "mcp", "skipped", "no MCP registry in target harness", "", "", "")
		} else {
			run("mcp", func(target *Context) error {
				outputs, err := target.RenderMCP()
				if err != nil {
					return err
				}
				if err := target.RunHook("mcp.render.pre", nil); err != nil {
					return err
				}
				if _, err := target.WriteMCP(outputs, false); err != nil {
					return err
				}
				return target.RunHook("mcp.render.post", nil)
			})
		}
	}
	if opt.ReloadStatus {
		c.catchUpReloadStatus(report, t, opt)
	}
}

// refreshEnv regenerates the collection env and mise config between the env
// hooks, writing .env.collection only when its content changed.
func (c *Context) refreshEnv() error {
	data, err := c.RenderEnv()
	if err != nil {
		return err
	}
	mise, err := c.RenderMise()
	if err != nil {
		return err
	}
	oldEnv, _ := os.ReadFile(filepath.Join(c.Collection, ".env.collection"))
	oldMise, _ := os.ReadFile(filepath.Join(c.Collection, "mise.toml"))
	changed := !bytes.Equal(oldEnv, data) || !bytes.Equal(oldMise, mise)
	if err := c.ValidateEnvSupport(); err != nil {
		return err
	}
	if err := c.RunHook("env.pre", nil); err != nil {
		return err
	}
	if changed {
		if err := c.WriteEnv(data); err != nil {
			return err
		}
	}
	if err := c.EnsureEnvSupport(); err != nil {
		return err
	}
	if err := c.TrustMise(); err != nil {
		return err
	}
	return c.RunHook("env.post", nil)
}
