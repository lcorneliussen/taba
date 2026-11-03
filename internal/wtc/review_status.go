package wtc

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type ReviewStatus struct {
	State      string `json:"state"`
	PR         int    `json:"pr"`
	Verdict    string `json:"verdict,omitempty"`
	Blockers   *int   `json:"blockers,omitempty"`
	Round      *int   `json:"round,omitempty"`
	ReviewHead string `json:"review_head,omitempty"`
	PRHead     string `json:"pr_head,omitempty"`
	CommentURL string `json:"comment_url,omitempty"`
	CommentID  string `json:"comment_id,omitempty"`
}

type reviewComment struct {
	ID      any    `json:"id"`
	Body    string `json:"body"`
	Content struct {
		Raw string `json:"raw"`
	} `json:"content"`
	CreatedAt string `json:"createdAt"`
	CreatedOn string `json:"created_on"`
	Created   string `json:"created_at"`
	URL       string `json:"url"`
	HTMLURL   string `json:"html_url"`
	Links     struct {
		HTML struct {
			Href string `json:"href"`
		} `json:"html"`
	} `json:"links"`
}

var reviewStatusLine = regexp.MustCompile(`wtc-review v1 head=([0-9a-f]{12,40}) verdict=([a-z-]+) blockers=([0-9]+) round=([0-9]+)`)
var reviewIssueCommentID = regexp.MustCompile(`issuecomment-([0-9]+)`)

func ParseReviewStatusComments(data []byte, pr int, head, forge, slug, collection string, trusted bool) (ReviewStatus, error) {
	status := ReviewStatus{State: "none", PR: pr, PRHead: head}
	var root any
	if err := json.Unmarshal(data, &root); err != nil {
		return status, fmt.Errorf("review comments: %w", err)
	}
	var raw []any
	switch v := root.(type) {
	case []any:
		raw = v
	case map[string]any:
		if a, ok := v["comments"].([]any); ok {
			raw = a
		} else if a, ok := v["values"].([]any); ok {
			raw = a
		}
	default:
		return status, fmt.Errorf("review comments: expected list or object")
	}
	comments := make([]reviewComment, 0, len(raw))
	for _, item := range raw {
		b, err := json.Marshal(item)
		if err != nil {
			return status, err
		}
		var c reviewComment
		if err := json.Unmarshal(b, &c); err != nil {
			return status, err
		}
		comments = append(comments, c)
	}
	sort.SliceStable(comments, func(i, j int) bool { return commentTime(comments[i]) < commentTime(comments[j]) })
	for _, c := range comments {
		body := c.Content.Raw
		if body == "" {
			body = c.Body
		}
		matches := reviewStatusLine.FindAllStringSubmatch(body, -1)
		if len(matches) == 0 {
			continue
		}
		m := matches[len(matches)-1]
		blockers, _ := strconv.Atoi(m[3])
		round, _ := strconv.Atoi(m[4])
		url := c.Links.HTML.Href
		if url == "" {
			url = c.URL
		}
		if url == "" {
			url = c.HTMLURL
		}
		cid := ""
		if found := reviewIssueCommentID.FindStringSubmatch(url); len(found) > 0 {
			cid = found[1]
		} else if c.ID != nil {
			cid = fmt.Sprint(c.ID)
		}
		status = ReviewStatus{State: "stale", PR: pr, Verdict: m[2], Blockers: &blockers, Round: &round, ReviewHead: m[1], PRHead: head, CommentURL: url, CommentID: cid}
		if SameReviewSHA(m[1], head) {
			status.State = "current"
		}
	}
	if trusted && status.State == "current" {
		if !reviewReceiptExists(collection, forge, slug, strconv.Itoa(pr), status.ReviewHead, status.CommentID, status.Verdict) {
			status.State = "untrusted"
		}
	}
	return status, nil
}

func commentTime(c reviewComment) string {
	if c.CreatedOn != "" {
		return c.CreatedOn
	}
	if c.CreatedAt != "" {
		return c.CreatedAt
	}
	return c.Created
}

func SameReviewSHA(local, remote string) bool {
	return len(local) >= 12 && len(remote) >= 12 && strings.HasPrefix(local, remote)
}

// reviewReceiptPaths lists a receipt's location under each spelling, the one
// new receipts use first. Either proves the post; revoking must clear both.
func reviewReceiptPaths(collection string, fields ...string) []string {
	h := sha256.New()
	for _, field := range fields {
		h.Write([]byte(field))
		h.Write([]byte{0})
	}
	name := hex.EncodeToString(h.Sum(nil))
	return []string{filepath.Join(collection, ".wtc-review-posted", name), filepath.Join(collection, ".taba-review-posted", name)}
}

func reviewReceiptPath(collection string, fields ...string) string {
	return reviewReceiptPaths(collection, fields...)[0]
}

func removeReviewReceipts(collection string, fields ...string) {
	for _, path := range reviewReceiptPaths(collection, fields...) {
		_ = os.Remove(path)
	}
}

func reviewReceiptExists(collection string, fields ...string) bool {
	for _, path := range reviewReceiptPaths(collection, fields...) {
		if _, err := os.Stat(path); err == nil {
			return true
		}
	}
	return false
}

func (c *Context) ReviewRepo(repo string) (worktree, slug, forge string, err error) {
	if !prRepoName.MatchString(repo) || repo == "." || repo == ".." || strings.Contains(repo, "..") {
		return "", "", "", fmt.Errorf("invalid repository name %q", repo)
	}
	worktree = c.WorktreeDir(repo)
	if _, err := os.Stat(filepath.Join(worktree, ".git")); err != nil {
		return "", "", "", fmt.Errorf("no worktree for %s", repo)
	}
	remote := ""
	for _, r := range c.Registry.Repos {
		if r.Answers(repo) || repo == "harness" && r.Name == c.Config.Harness.Name {
			remote = r.Remote
			break
		}
	}
	if remote == "" {
		remoteBytes, e := exec.Command("git", "-C", worktree, "remote", "get-url", "origin").Output()
		if e != nil {
			return "", "", "", fmt.Errorf("origin remote for %s: %w", repo, e)
		}
		remote = strings.TrimSpace(string(remoteBytes))
	}
	for _, host := range []struct{ domain, name string }{{"github.com", "github"}, {"bitbucket.org", "bitbucket"}} {
		if i := strings.Index(remote, host.domain); i >= 0 {
			tail := remote[i+len(host.domain):]
			if !strings.HasPrefix(tail, "/") && !strings.HasPrefix(tail, ":") {
				continue
			}
			slug = strings.TrimSuffix(strings.TrimPrefix(tail, tail[:1]), ".git")
			if len(strings.Split(slug, "/")) == 2 && !strings.ContainsAny(slug, " \r\n?#") {
				return worktree, slug, host.name, nil
			}
		}
	}
	return "", "", "", fmt.Errorf("unsupported forge remote for %s", repo)
}

func (c *Context) EnlistedReviewPR(repo string) (string, error) {
	worktree := c.WorktreeDir(repo)
	branchBytes, err := exec.Command("git", "-C", worktree, "branch", "--show-current").Output()
	if err != nil {
		return "", err
	}
	branch := strings.TrimSpace(string(branchBytes))
	if branch == "" {
		return "", fmt.Errorf("no PR given and %s is detached", repo)
	}
	rows, err := c.ListPRs()
	if err != nil {
		return "", err
	}
	for _, row := range rows {
		if row.Branch == branch && (row.Repo == repo || repo == "harness" && row.Repo == c.Config.Harness.Name) {
			return row.Number, nil
		}
	}
	return "", fmt.Errorf("no PR enlisted for %s on %s", repo, branch)
}

func (c *Context) GetReviewStatus(repo, number string, trusted bool) (ReviewStatus, error) {
	worktree, slug, forge, err := c.ReviewRepo(repo)
	if err != nil {
		return ReviewStatus{}, err
	}
	if number == "" {
		number, err = c.EnlistedReviewPR(repo)
		if err != nil {
			return ReviewStatus{}, err
		}
	}
	if !prNumber.MatchString(number) {
		return ReviewStatus{}, fmt.Errorf("invalid PR number %q", number)
	}
	pr, _ := strconv.Atoi(number)
	var head, comments []byte
	if forge == "github" {
		head, err = exec.Command("gh", "pr", "view", number, "--repo", slug, "--json", "headRefOid").Output()
		if err == nil {
			comments, err = exec.Command("gh", "pr", "view", number, "--repo", slug, "--json", "comments").Output()
		}
	} else {
		view := exec.Command("bb", "pr", "view", number, "--json")
		view.Dir = worktree
		head, err = forgeOutput(view)
		if err == nil {
			list := exec.Command("bb", "pr", "comments", "list", number, "--all", "--json")
			list.Dir = worktree
			comments, err = forgeOutput(list)
		}
	}
	if err != nil {
		return ReviewStatus{}, fmt.Errorf("read PR #%s from %s: %w", number, forge, err)
	}
	var prInfo struct {
		HeadRefOid string `json:"headRefOid"`
		Source     struct {
			Commit struct {
				Hash string `json:"hash"`
			} `json:"commit"`
		} `json:"source"`
	}
	if err := json.Unmarshal(head, &prInfo); err != nil {
		return ReviewStatus{}, err
	}
	prHead := prInfo.HeadRefOid
	if prHead == "" {
		prHead = prInfo.Source.Commit.Hash
	}
	if prHead == "" {
		return ReviewStatus{}, fmt.Errorf("PR #%s has no head commit", number)
	}
	return ParseReviewStatusComments(comments, pr, prHead, forge, slug, c.Collection, trusted)
}
