package openclaw

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"taba.sh/taba/internal/branding"
	"time"
)

// A sandboxed session works in a private checkout: one commit deep, no
// origin, and Git metadata of its own, of which only the files reach the
// managed worktree. The wtc plugin serves the gateway's clones to the
// sandbox, read-only, at the address in GatewayGitEnv, and takes back the
// session's branch: so the sandbox can read history, merge its base with
// Git's own conflict state, and keep its commits.

// GatewayGitEnv names the address of the gateway's git service, as the
// sandbox reaches it: http://<gateway>:<port>. The operator sets it in
// agents.defaults.sandbox.docker.env.
const GatewayGitEnv = "WTC_GATEWAY_GIT"

// GatewayRemote is the remote a sandbox's checkout reads the gateway
// through. Not origin: a checkout without an origin is how wtc knows it is a
// sandbox's private copy, which the gateway publishes.
const GatewayRemote = "gateway"

// SandboxSession is what the gateway tells a sandbox about its session.
type SandboxSession struct {
	// Repo is the session's repository, as the gateway serves it.
	Repo   string `json:"repo"`
	Branch string `json:"branch"`
	// Base is the branch the session merges and publishes to, as
	// origin/<base> on the gateway.
	Base string `json:"base"`
	Head string `json:"head"`
	// Repos are the collection's repositories the sandbox may read.
	Repos []string `json:"repos"`
}

// GatewaySession describes the managed worktree at dir for its sandbox,
// after fetching its base when that is older than FetchBaseInterval. The
// plugin adds the repository names.
func GatewaySession(dir string) (SandboxSession, error) {
	c, err := OpenCheckout(dir, "")
	if err != nil {
		return SandboxSession{}, err
	}
	FetchBases([]string{c.Dir}, FetchBaseInterval)
	head, err := git(c.Dir, "rev-parse", "HEAD")
	if err != nil {
		return SandboxSession{}, err
	}
	return SandboxSession{Branch: c.Branch(), Base: c.Dest, Head: head}, nil
}

var fullHash = regexp.MustCompile(`^[0-9a-f]{40}$`)

// Delivery is what the gateway made of a sandbox's branch.
type Delivery struct {
	Branch  string `json:"branch"`
	Head    string `json:"head"`
	Commits int    `json:"commits"`
	Merges  int    `json:"merges"`
	Note    string `json:"note,omitempty"`
}

// AcceptSandboxBranch moves the managed worktree at dir to commit, the
// session's branch as its sandbox has it, with the commits the sandbox made
// from the bundle at bundle (empty when the gateway has them all). It
// accepts only a branch that extends the gateway's, whose merges merge
// origin's branches and leave no conflict markers behind. The files are not
// touched: they reach the worktree from the sandbox, as every edit does.
// Only the branch and the index move, so that the files that arrive are the
// commit's and the edits not committed yet stay pending.
func AcceptSandboxBranch(dir, commit, bundle string) (Delivery, error) {
	c, err := OpenCheckout(dir, "")
	if err != nil {
		return Delivery{}, err
	}
	d := Delivery{Branch: c.Branch()}
	if !fullHash.MatchString(commit) {
		return d, fmt.Errorf("%q is not a commit hash", commit)
	}
	if what := c.inProgress(); what != "" {
		return d, fmt.Errorf("%s is in progress in the gateway's worktree: it has to finish first", what)
	}
	// One per branch: worktrees of a clone share its refs.
	incoming := "refs/wtc/sandbox-incoming/" + d.Branch
	defer func() { _, _ = git(c.Dir, "update-ref", "-d", incoming) }()
	if bundle != "" {
		if _, err := git(c.Dir, "bundle", "verify", "--quiet", bundle); err != nil {
			return d, fmt.Errorf("the bundle does not apply to the gateway's clone: %w", err)
		}
		if _, err := git(c.Dir, "fetch", "--quiet", "--no-tags", "--no-write-fetch-head", bundle, "+HEAD:"+incoming); err != nil {
			return d, err
		}
	}
	if !gitOK(c.Dir, "cat-file", "-e", commit+"^{commit}") {
		return d, fmt.Errorf("the gateway does not have %.12s, and the bundle did not bring it", commit)
	}
	head, err := git(c.Dir, "rev-parse", "HEAD")
	if err != nil {
		return d, err
	}
	if commit == head {
		d.Head, d.Note = head, "the gateway already has it"
		return d, nil
	}
	if !gitOK(c.Dir, "merge-base", "--is-ancestor", head, commit) {
		return d, fmt.Errorf(branding.Text("%.12s does not extend %s at %.12s: the gateway's branch moved. Run {{command}} catch-up again, which starts from it"), commit, d.Branch, head)
	}
	commits, err := git(c.Dir, "rev-list", "--parents", head+".."+commit, "--not", "--remotes=origin")
	if err != nil {
		return d, err
	}
	for _, line := range strings.Split(commits, "\n") {
		hashes := strings.Fields(line)
		if len(hashes) == 0 {
			continue
		}
		d.Commits++
		if trailer := gatewayTrailer(c.Dir, hashes[0]); trailer != "" {
			return d, fmt.Errorf("commit %.12s carries %s, which only the gateway writes: credit and publication records are the gateway's. Amend the message without it", hashes[0], trailer)
		}
		if len(hashes) < 3 {
			continue
		}
		d.Merges++
		for _, parent := range hashes[2:] {
			if !mergedFromOrigin(c.Dir, parent) {
				return d, fmt.Errorf("merge %.12s merges %.12s, which is on none of origin's branches: a sandbox merges what the gateway fetched (origin/%s)", hashes[0], parent, c.Dest)
			}
		}
		if files := conflictMarkers(c.Dir, hashes[0]); len(files) > 0 {
			return d, fmt.Errorf("merge %.12s still has conflict markers in %s: resolve them, then commit again", hashes[0], strings.Join(files, ", "))
		}
	}
	if _, err := git(c.Dir, "update-ref", "-m", "wtc: the session's branch from its sandbox", "refs/heads/"+d.Branch, commit, head); err != nil {
		return d, err
	}
	if _, err := git(c.Dir, "read-tree", commit); err != nil {
		return d, fmt.Errorf("%s is at %.12s, but its index could not follow: %w", d.Branch, commit, err)
	}
	_, _ = git(c.Dir, "update-index", "-q", "--refresh")
	d.Head = commit
	return d, nil
}

// gatewayTrailer is the first line of commit's message that only the
// gateway may write: the publication marker publishedHead trusts, or a
// co-author trailer, which the gateway adds for verified people only.
func gatewayTrailer(dir, commit string) string {
	body, _ := git(dir, "log", "-1", "--format=%B", commit)
	for _, line := range strings.Split(body, "\n") {
		l := strings.ToLower(strings.TrimSpace(line))
		if strings.HasPrefix(l, strings.ToLower(publicationMarker)) || strings.HasPrefix(l, "co-authored-by:") {
			return strings.TrimSpace(line)
		}
	}
	return ""
}

func mergedFromOrigin(dir, commit string) bool {
	out, err := git(dir, "for-each-ref", "--count=1", "--contains", commit, "--format=%(refname)", "refs/remotes/origin/")
	return err == nil && out != ""
}

// conflictMarkers lists the files where a merge commit added a conflict
// marker that neither parent has: the combined diff shows only the hunks
// the merge resolved.
func conflictMarkers(dir, merge string) []string {
	out, err := git(dir, "diff-tree", "--cc", "--no-color", "--no-renames", merge)
	if err != nil {
		return nil
	}
	var files []string
	file := ""
	for _, line := range strings.Split(out, "\n") {
		if name, ok := strings.CutPrefix(line, "diff --cc "); ok {
			file = name
			continue
		}
		// Combined diff lines start with one column per parent: "++" is a
		// line neither parent has.
		if len(line) < 3 || line[:2] != "++" {
			continue
		}
		if isConflictMarker(line[2:]) {
			if len(files) == 0 || files[len(files)-1] != file {
				files = append(files, file)
			}
		}
	}
	return files
}

// stagedConflictMarkers lists the files where the index adds a conflict
// marker that neither side of the merge has: one a side brings, in a test
// fixture say, is not left over.
func stagedConflictMarkers(dir string) []string {
	added := func(against string) map[string]map[string]bool {
		out, err := git(dir, "diff", "--cached", "--no-color", "--no-renames", "-U0", against)
		markers := map[string]map[string]bool{}
		if err != nil {
			return markers
		}
		file := ""
		for _, line := range strings.Split(out, "\n") {
			if name, ok := strings.CutPrefix(line, "+++ b/"); ok {
				file = name
				continue
			}
			if body, ok := strings.CutPrefix(line, "+"); ok && isConflictMarker(body) {
				if markers[file] == nil {
					markers[file] = map[string]bool{}
				}
				markers[file][body] = true
			}
		}
		return markers
	}
	ours, theirs := added("HEAD"), added("MERGE_HEAD")
	var files []string
	for file, lines := range ours {
		for line := range lines {
			if theirs[file][line] {
				files = append(files, file)
				break
			}
		}
	}
	sort.Strings(files)
	return files
}

func isConflictMarker(line string) bool {
	return line == "<<<<<<<" || line == ">>>>>>>" || strings.HasPrefix(line, "<<<<<<< ") || strings.HasPrefix(line, ">>>>>>> ")
}

// SandboxGateway is the gateway's git service, as a sandbox reaches it.
type SandboxGateway interface {
	Session() (SandboxSession, error)
	// RemoteURL is where the sandbox fetches repo from.
	RemoteURL(repo string) string
	// Deliver hands the gateway the session's branch at commit, with the
	// commits it lacks in the bundle at bundle, or none.
	Deliver(commit, bundle string) (Delivery, error)
}

// HTTPGateway is the wtc plugin's git service at a base URL.
type HTTPGateway struct {
	URL    string
	Client *http.Client
}

// GatewayFromEnv is the gateway named by GatewayGitEnv, or nil.
func GatewayFromEnv(getenv func(string) string) *HTTPGateway {
	u := strings.TrimRight(strings.TrimSpace(getenv(GatewayGitEnv)), "/")
	if u == "" {
		return nil
	}
	return &HTTPGateway{URL: u, Client: &http.Client{Timeout: 5 * time.Minute}}
}

func (g *HTTPGateway) RemoteURL(repo string) string { return g.URL + "/" + url.PathEscape(repo) }

func (g *HTTPGateway) Session() (SandboxSession, error) {
	var s SandboxSession
	err := g.do(http.MethodGet, "/wtc/session", nil, &s)
	return s, err
}

func (g *HTTPGateway) Deliver(commit, bundle string) (Delivery, error) {
	var body io.Reader = bytes.NewReader(nil)
	if bundle != "" {
		f, err := os.Open(bundle)
		if err != nil {
			return Delivery{}, err
		}
		defer f.Close()
		body = f
	}
	var d Delivery
	err := g.do(http.MethodPost, "/wtc/deliver?commit="+url.QueryEscape(commit), body, &d)
	return d, err
}

func (g *HTTPGateway) do(method, path string, body io.Reader, into any) error {
	req, err := http.NewRequest(method, g.URL+path, body)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/x-git-bundle")
	}
	resp, err := g.Client.Do(req)
	if err != nil {
		return fmt.Errorf("the gateway's git service at %s: %w", g.URL, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		var e struct{ Error string }
		if json.Unmarshal(data, &e) == nil && e.Error != "" {
			return errors.New(e.Error)
		}
		return fmt.Errorf("the gateway's git service answered %s", resp.Status)
	}
	return json.Unmarshal(data, into)
}

// SandboxCatchUp is the result of wtc catch-up in a sandbox.
type SandboxCatchUp struct {
	Branch string `json:"branch"`
	Base   string `json:"base"`
	Merged bool   `json:"merged"`
	// Conflicts are the files the merge stopped on, for the agent to
	// resolve before wtc catch-up --continue.
	Conflicts []string `json:"conflicts,omitempty"`
	// Reset is set when the sandbox's history did not contain the
	// gateway's branch, so its commits were dropped and its files kept.
	Reset     bool      `json:"reset,omitempty"`
	Delivered *Delivery `json:"delivered,omitempty"`
	// Unstashed are the files where the edits not committed yet conflict
	// with the merge after putting them back.
	Unstashed []string `json:"unstashedConflicts,omitempty"`
	Note      string   `json:"note,omitempty"`
}

// stashFile records the stash a catch-up set aside, until it is put back.
const stashFile = "wtc-catch-up-stash"

// SandboxCatchUpCheckout merges the session's base into the sandbox's
// checkout at dir with the history the gateway serves, and delivers the
// branch, with the sandbox's own commits, back to the gateway. A conflicted
// merge stays in progress for the agent to resolve; cont finishes it.
// Edits not committed are set aside around the merge and put back after.
func SandboxCatchUpCheckout(dir string, gw SandboxGateway, cont bool) (SandboxCatchUp, error) {
	var r SandboxCatchUp
	top, err := git(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return r, errors.New(branding.Text("not in a git checkout: run {{command}} catch-up in the repository's checkout"))
	}
	if gitOK(top, "remote", "get-url", "origin") {
		return r, errors.New(branding.Text("this checkout has an origin, so it is not a sandbox's private copy: {{command}} catch-up in a sandbox works on the session's checkout"))
	}
	s, err := gw.Session()
	if err != nil {
		return r, err
	}
	r.Branch, r.Base = s.Branch, s.Base
	if current, _ := git(top, "branch", "--show-current"); current != s.Branch {
		return r, fmt.Errorf("the checkout is on %q, but the session's branch is %s", current, s.Branch)
	}
	if err := ConfigureGatewayRemote(top, gw.RemoteURL(s.Repo)); err != nil {
		return r, err
	}
	if err := fetchGateway(top); err != nil {
		return r, err
	}
	gateway := "refs/remotes/" + GatewayRemote + "/" + s.Branch
	base := "refs/remotes/origin/" + s.Base
	if !gitOK(top, "rev-parse", "--verify", "--quiet", base) {
		return r, fmt.Errorf("the gateway has no origin/%s to merge", s.Base)
	}
	c := &Checkout{Dir: top}
	if !cont {
		if what := c.inProgress(); what != "" {
			return r, fmt.Errorf(branding.Text("%s is in progress: finish it, then run {{command}} catch-up --continue"), what)
		}
		if !gitOK(top, "merge-base", "--is-ancestor", gateway, "HEAD") {
			// The gateway committed since (a publication): its branch wins,
			// the files stay as they are.
			if _, err := git(top, "reset", "--quiet", gateway); err != nil {
				return r, err
			}
			r.Reset = true
		}
		if err := stash(top); err != nil {
			return r, err
		}
		if gitOK(top, "merge-base", "--is-ancestor", base, "HEAD") {
			r.Note = "already contains origin/" + s.Base
		} else if _, err := git(top, append(identity(top), "merge", "--no-edit", "--quiet", "origin/"+s.Base)...); err != nil {
			conflicts, _ := git(top, "diff", "--name-only", "--diff-filter=U")
			if r.Conflicts = strings.Fields(conflicts); len(r.Conflicts) == 0 {
				_, _ = git(top, "merge", "--abort")
				return r, errors.Join(fmt.Errorf("merging origin/%s failed", s.Base), err, unstash(top, &r))
			}
			return r, nil
		} else {
			r.Merged = true
		}
	} else if c.inProgress() == "a merge" {
		if conflicts, _ := git(top, "diff", "--name-only", "--diff-filter=U"); conflicts != "" {
			r.Conflicts = strings.Fields(conflicts)
			return r, fmt.Errorf(branding.Text("still unresolved: %s. Resolve them and git add each, then run {{command}} catch-up --continue"), strings.Join(r.Conflicts, ", "))
		}
		if files := stagedConflictMarkers(top); len(files) > 0 {
			return r, fmt.Errorf(branding.Text("conflict markers are still in %s: resolve them, git add, then run {{command}} catch-up --continue"), strings.Join(files, ", "))
		}
		if _, err := git(top, append(identity(top), "commit", "--quiet", "--no-edit")...); err != nil {
			return r, err
		}
		r.Merged = true
	} else if what := c.inProgress(); what != "" {
		return r, fmt.Errorf("%s is in progress, not a catch-up's merge", what)
	}
	head, err := git(top, "rev-parse", "HEAD")
	if err != nil {
		return r, err
	}
	if !gitOK(top, "merge-base", "--is-ancestor", gateway, head) {
		return r, errors.Join(fmt.Errorf(branding.Text("%.12s does not extend the gateway's %s: run {{command}} catch-up again"), head, s.Branch), unstash(top, &r))
	}
	if head != mustRev(top, gateway) {
		bundle, err := os.CreateTemp("", "wtc-catch-up-*.bundle")
		if err != nil {
			return r, err
		}
		bundle.Close()
		defer os.Remove(bundle.Name())
		path := bundle.Name()
		// Everything the gateway has is left out; a branch with nothing
		// new, as after a fast-forward to the base, needs no bundle.
		if _, err := git(top, "bundle", "create", "--quiet", path, "HEAD", "^"+gateway, "^"+base); err != nil {
			path = ""
		}
		d, err := gw.Deliver(head, path)
		if err != nil {
			return r, errors.Join(fmt.Errorf("the gateway did not take the branch: %w", err), unstash(top, &r))
		}
		r.Delivered = &d
		_ = fetchGateway(top)
	}
	return r, unstash(top, &r)
}

func mustRev(dir, ref string) string {
	h, _ := git(dir, "rev-parse", ref)
	return h
}

// ConfigureGatewayRemote points the gateway remote at url: the session's and
// the collection's branches of this repository under gateway/, and the
// gateway's fetch of origin as origin/, so that origin/<base> means what it
// means on the gateway.
func ConfigureGatewayRemote(dir, url string) error {
	if gitOK(dir, "remote", "get-url", GatewayRemote) {
		if _, err := git(dir, "remote", "set-url", GatewayRemote, url); err != nil {
			return err
		}
	} else if _, err := git(dir, "remote", "add", "--no-tags", GatewayRemote, url); err != nil {
		return err
	}
	_, _ = git(dir, "config", "--unset-all", "remote."+GatewayRemote+".fetch")
	for _, spec := range []string{"+refs/heads/" + BranchPrefix + "*:refs/remotes/" + GatewayRemote + "/" + BranchPrefix + "*", "+refs/remotes/origin/*:refs/remotes/origin/*"} {
		if _, err := git(dir, "config", "--add", "remote."+GatewayRemote+".fetch", spec); err != nil {
			return err
		}
	}
	return nil
}

func fetchGateway(dir string) error {
	args := []string{"fetch", "--quiet", "--prune", GatewayRemote}
	if out, _ := git(dir, "rev-parse", "--is-shallow-repository"); out == "true" {
		args = append(args, "--unshallow")
	}
	if _, err := git(dir, args...); err != nil {
		return fmt.Errorf("cannot fetch from the gateway: %w", err)
	}
	return nil
}

// identity lets git commit a merge in a sandbox that has no identity
// configured.
func identity(dir string) []string {
	if name, _ := git(dir, "config", "user.name"); name != "" {
		if email, _ := git(dir, "config", "user.email"); email != "" {
			return nil
		}
	}
	return []string{"-c", "user.name=wtc", "-c", "user.email=wtc@localhost.invalid"}
}

// stash sets aside the edits not committed yet, untracked files included,
// except what OpenClaw mounts into the checkout.
func stash(dir string) error {
	if out, _ := git(dir, "status", "--porcelain", "--untracked-files=all", "--", ".", ":(exclude)"+strings.TrimSuffix(sandboxSkills, "/")); out == "" {
		return nil
	}
	if _, err := git(dir, append(identity(dir), "stash", "push", "--quiet", "--include-untracked", "-m", "wtc catch-up", "--", ".", ":(exclude)"+strings.TrimSuffix(sandboxSkills, "/"))...); err != nil {
		return fmt.Errorf("cannot set the uncommitted edits aside: %w", err)
	}
	hash, err := git(dir, "rev-parse", "refs/stash")
	if err != nil {
		return err
	}
	p, err := git(dir, "rev-parse", "--path-format=absolute", "--git-path", stashFile)
	if err != nil {
		return err
	}
	return os.WriteFile(p, []byte(hash+"\n"), 0o644)
}

// unstash puts back what stash set aside. Conflicts with the merge are left
// in the files, for the agent to resolve.
func unstash(dir string, r *SandboxCatchUp) error {
	p, err := git(dir, "rev-parse", "--path-format=absolute", "--git-path", stashFile)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	hash := strings.TrimSpace(string(data))
	_ = os.Remove(p)
	if _, err := git(dir, "stash", "apply", "--quiet", hash); err != nil {
		// The stash is kept as a copy: the agent drops it once the
		// conflicts in the files are resolved.
		conflicts, _ := git(dir, "diff", "--name-only", "--diff-filter=U")
		if r.Unstashed = strings.Fields(conflicts); len(r.Unstashed) == 0 {
			return fmt.Errorf("the catch-up is done, but the uncommitted edits could not be put back (%w). They are in stash %.12s: move the files in the way aside, then git stash apply %.12s", err, hash, hash)
		}
		// The markers are in the files; the index goes back to HEAD, so the
		// edits are ordinary uncommitted ones again.
		_, _ = git(dir, "reset", "--quiet")
		r.Note = fmt.Sprintf("the uncommitted edits conflict with the merge in %s; they are also kept in stash %.12s: resolve the markers, then git stash drop it", strings.Join(r.Unstashed, ", "), hash)
		return nil
	}
	if list, err := git(dir, "stash", "list", "--format=%H"); err == nil {
		for i, h := range strings.Split(list, "\n") {
			if h == hash {
				_, _ = git(dir, "stash", "drop", "--quiet", "stash@{"+strconv.Itoa(i)+"}")
				break
			}
		}
	}
	return nil
}
