package wtc

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"taba.sh/taba/internal/branding"
)

type HostedReviewReport struct {
	Provider string `json:"provider"`
	State    string `json:"state"`
	Head     string `json:"head,omitempty"`
	URL      string `json:"url,omitempty"`
	Body     string `json:"body,omitempty"`
}

type HostedReviewFinding struct {
	Provider string `json:"provider"`
	Thread   string `json:"thread"`
	File     string `json:"file"`
	Line     *int   `json:"line"`
	Outdated bool   `json:"outdated"`
	Head     string `json:"head"`
	URL      string `json:"url"`
	Body     string `json:"body"`
}

type ReviewActivity struct {
	Head     string                `json:"head"`
	Reports  []HostedReviewReport  `json:"reports"`
	Findings []HostedReviewFinding `json:"findings"`
	Waiting  []string              `json:"waiting"`
}

func hostedReviewProvider(login string) string {
	switch strings.ToLower(login) {
	case "copilot-pull-request-reviewer", "copilot-pull-request-reviewer[bot]", "github-copilot[bot]":
		return "Copilot"
	case "cursor", "cursor[bot]", "bugbot", "bugbot[bot]":
		return "Cursor"
	}
	return ""
}

func hostedReviewCheck(name string) string {
	name = strings.ToLower(name)
	if strings.Contains(name, "copilot") {
		return "Copilot"
	}
	if strings.Contains(name, "bugbot") || strings.Contains(name, "cursor") {
		return "Cursor"
	}
	return ""
}

var hostedReviewFailure = regexp.MustCompile(`(?i)unable to review|failed to review|review failed|review limit|usage limit|rate limit|quota.{0,50}(exhausted|exceeded)|budget.{0,50}(exhausted|exceeded)|insufficient.{0,30}credits`)

const hostedThreadsQuery = `query($owner:String!,$repo:String!,$number:Int!,$endCursor:String){repository(owner:$owner,name:$repo){pullRequest(number:$number){headRefOid reviewThreads(first:100,after:$endCursor){pageInfo{hasNextPage endCursor} nodes{id isResolved isOutdated path line comments(first:1){nodes{body url author{login} originalCommit{oid}}}}}}}}`

// Hosted evidence never substitutes for the independent local gate. All open
// bot threads are kept, including outdated ones and ones predating our summary.
func (c *Context) GetReviewActivity(repo, number string) (ReviewActivity, error) {
	worktree, slug, forge, err := c.ReviewRepo(repo)
	if err != nil {
		return ReviewActivity{}, err
	}
	if number == "" {
		number, err = c.EnlistedReviewPR(repo)
	}
	if err != nil {
		return ReviewActivity{}, err
	}
	if !prNumber.MatchString(number) {
		return ReviewActivity{}, fmt.Errorf("review activity needs a PR number")
	}
	if forge != "github" {
		return ReviewActivity{}, fmt.Errorf("hosted review activity currently supports GitHub")
	}
	meta, err := reviewForgeCommand(worktree, "gh", []string{"pr", "view", number, "--repo", slug, "--json", "headRefOid,reviewRequests,statusCheckRollup"}, nil)
	if err != nil {
		return ReviewActivity{}, err
	}
	reviews, err := reviewForgeCommand(worktree, "gh", []string{"api", fmt.Sprintf("repos/%s/pulls/%s/reviews?per_page=100", slug, number), "--paginate", "--slurp"}, nil)
	if err != nil {
		return ReviewActivity{}, err
	}
	owner, name, _ := strings.Cut(slug, "/")
	threads, err := reviewForgeCommand(worktree, "gh", []string{"api", "graphql", "-f", "query=" + hostedThreadsQuery, "-f", "owner=" + owner, "-f", "repo=" + name, "-F", "number=" + number, "--paginate", "--slurp"}, nil)
	if err != nil {
		return ReviewActivity{}, err
	}
	return parseReviewActivity(meta, reviews, threads)
}

func parseReviewActivity(meta, reviews, threads []byte) (ReviewActivity, error) {
	var pr struct {
		Head     string `json:"headRefOid"`
		Requests []struct {
			Login string `json:"login"`
		} `json:"reviewRequests"`
		Checks []struct{ Name, Status, Conclusion, State string } `json:"statusCheckRollup"`
	}
	if err := json.Unmarshal(meta, &pr); err != nil {
		return ReviewActivity{}, err
	}
	if len(pr.Head) != 40 {
		return ReviewActivity{}, fmt.Errorf("hosted activity has no full PR head")
	}
	activity := ReviewActivity{Head: pr.Head, Reports: []HostedReviewReport{}, Findings: []HostedReviewFinding{}, Waiting: []string{}}
	latest := map[string]HostedReviewReport{}
	var reviewPages [][]struct {
		User        struct{ Login string } `json:"user"`
		State, Body string
		Head        string `json:"commit_id"`
		URL         string `json:"html_url"`
	}
	if err := json.Unmarshal(reviews, &reviewPages); err != nil {
		return activity, err
	}
	for _, page := range reviewPages {
		for _, review := range page {
			provider := hostedReviewProvider(review.User.Login)
			if provider == "" {
				continue
			}
			state := "reported"
			if review.Head != pr.Head {
				state = "stale"
			} else if review.State == "PENDING" {
				state = "waiting"
			} else if review.State == "DISMISSED" || hostedReviewFailure.MatchString(review.Body) {
				state = "unavailable"
			}
			latest[provider] = HostedReviewReport{Provider: provider, State: state, Head: review.Head, URL: review.URL, Body: review.Body}
		}
	}
	waiting := map[string]bool{}
	for _, request := range pr.Requests {
		if provider := hostedReviewProvider(request.Login); provider != "" {
			waiting[provider] = true
		}
	}
	for provider, report := range latest {
		if report.State == "waiting" {
			waiting[provider] = true
		}
		if report.State == "unavailable" {
			delete(waiting, provider)
		}
	}
	activeChecks := map[string]bool{}
	for _, check := range pr.Checks {
		provider := hostedReviewCheck(check.Name)
		if provider == "" {
			continue
		}
		if check.Status != "COMPLETED" && check.Status != "" || check.State == "PENDING" || check.State == "EXPECTED" {
			activeChecks[provider] = true
		}
		if check.Conclusion == "FAILURE" || check.Conclusion == "TIMED_OUT" || check.Conclusion == "CANCELLED" || check.Conclusion == "ACTION_REQUIRED" || check.Conclusion == "STARTUP_FAILURE" || check.Conclusion == "STALE" || check.State == "ERROR" || check.State == "FAILURE" {
			latest[provider] = HostedReviewReport{Provider: provider, State: "unavailable", Head: pr.Head, Body: "Hosted review check did not complete successfully: " + check.Name}
		}
	}
	for provider, report := range latest {
		if report.State == "unavailable" {
			delete(waiting, provider)
		}
	}
	for provider := range activeChecks {
		waiting[provider] = true
	}
	for _, provider := range []string{"Copilot", "Cursor"} {
		report, ok := latest[provider]
		if !ok {
			report = HostedReviewReport{Provider: provider, State: "absent"}
		}
		if waiting[provider] {
			report.State = "waiting"
			activity.Waiting = append(activity.Waiting, provider)
		}
		activity.Reports = append(activity.Reports, report)
	}
	var threadPages []struct {
		Errors []json.RawMessage `json:"errors"`
		Data   struct {
			Repository struct {
				PR *struct {
					Head    string `json:"headRefOid"`
					Threads struct {
						PageInfo struct{ HasNextPage bool } `json:"pageInfo"`
						Nodes    []struct {
							ID, Path string
							Line     *int
							Resolved bool `json:"isResolved"`
							Outdated bool `json:"isOutdated"`
							Comments struct {
								Nodes []struct {
									Body, URL      string
									Author         struct{ Login string }
									OriginalCommit struct{ OID string }
								}
							}
						}
					} `json:"reviewThreads"`
				} `json:"pullRequest"`
			}
		}
	}
	if err := json.Unmarshal(threads, &threadPages); err != nil {
		return activity, err
	}
	if len(threadPages) == 0 {
		return activity, fmt.Errorf("hosted review threads unavailable")
	}
	for i, page := range threadPages {
		p := page.Data.Repository.PR
		if len(page.Errors) > 0 || p == nil {
			return activity, fmt.Errorf("hosted review thread query failed")
		}
		if p.Head != pr.Head {
			return activity, fmt.Errorf("PR head changed while reading hosted reviews; retry")
		}
		if i == len(threadPages)-1 && p.Threads.PageInfo.HasNextPage {
			return activity, fmt.Errorf("hosted review threads were truncated")
		}
		for _, thread := range p.Threads.Nodes {
			if thread.Resolved || len(thread.Comments.Nodes) == 0 {
				continue
			}
			comment := thread.Comments.Nodes[0]
			provider := hostedReviewProvider(comment.Author.Login)
			if provider == "" {
				continue
			}
			activity.Findings = append(activity.Findings, HostedReviewFinding{Provider: provider, Thread: thread.ID, File: thread.Path, Line: thread.Line, Outdated: thread.Outdated, Head: comment.OriginalCommit.OID, URL: comment.URL, Body: comment.Body})
		}
	}
	return activity, nil
}

func saveHostedReviewActivity(bundle string, activity ReviewActivity) error {
	if err := os.MkdirAll(filepath.Join(bundle, "prior"), 0755); err != nil {
		return err
	}
	var out strings.Builder
	fmt.Fprintf(&out, "# Hosted review evidence\n\nSnapshot for head `%s`. Reported means a review was submitted, not approval. An independent local review is still required.\n", activity.Head)
	for _, report := range activity.Reports {
		fmt.Fprintf(&out, "\n## %s: %s\n\n%s\n\n%s\n", report.Provider, report.State, report.URL, report.Body)
	}
	for _, finding := range activity.Findings {
		fmt.Fprintf(&out, "\n## Open %s thread: %s\n\n%s\n\nOriginal head `%s`; outdated: %t. Read replies on the forge before treating this as unresolved work.\n\n%s\n", finding.Provider, finding.File, finding.URL, finding.Head, finding.Outdated, finding.Body)
	}
	return os.WriteFile(filepath.Join(bundle, "prior", "hosted-reviews.md"), []byte(out.String()), 0644)
}

// Preflight runs before a progress comment or any agent launch. A deferred run
// is a scheduling decision, not a failed review.
func (c *Context) PrepareHostedReviewBundle(bundle string) error {
	data, err := os.ReadFile(filepath.Join(bundle, "manifest.json"))
	if err != nil {
		return err
	}
	var manifest ReviewManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return err
	}
	if len(manifest.HeadSHA) < 12 || manifest.Round < 1 || manifest.RepoDir == "" {
		return fmt.Errorf("incomplete review manifest")
	}
	worktree, slug, forge, err := c.ReviewRepo(manifest.Repo)
	if err != nil {
		return err
	}
	if manifest.RepoDir != worktree || manifest.Slug != slug || manifest.Forge != forge {
		return fmt.Errorf("bundle repository does not match this collection")
	}
	if manifest.PR == "" || manifest.Forge != "github" {
		return nil
	}
	activity, err := c.GetReviewActivity(manifest.Repo, manifest.PR)
	if err != nil {
		return fmt.Errorf("inspect hosted reviews: %w", err)
	}
	if activity.Head != manifest.HeadSHA {
		return fmt.Errorf("stale bundle: hosted activity is on another PR head")
	}
	if err := saveHostedReviewActivity(bundle, activity); err != nil {
		return err
	}
	if len(activity.Waiting) > 0 {
		return fmt.Errorf(branding.Text("hosted review is waiting for %s; inspect {{command}} review activity, let it finish, then re-run local review"), strings.Join(activity.Waiting, ", "))
	}
	if len(activity.Findings) > 0 {
		return fmt.Errorf("%d open hosted review threads; address or answer and resolve them before starting local review", len(activity.Findings))
	}
	return nil
}
