package wtc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"taba.sh/taba/internal/envcompat"
	"time"
)

type statusBuildRequest struct {
	Collection string `json:"collection"`
	Repo       string `json:"repo"`
	Worktree   string `json:"worktree"`
	Slug       string `json:"slug"`
	Forge      string `json:"forge,omitempty"`
	Branch     string `json:"branch"`
	Tier       string `json:"tier"`
}

// errStatusBuildDefault is how a hook hands one request back to the built-in
// provider, so a harness can customize some repositories and keep the rest.
var errStatusBuildDefault = fmt.Errorf("use the default build provider")

type statusLimitedBuffer struct {
	bytes.Buffer
	limit    int
	overflow bool
}

func (b *statusLimitedBuffer) Write(data []byte) (int, error) {
	if b.Len()+len(data) > b.limit {
		b.overflow = true
		return 0, fmt.Errorf("output too large")
	}
	return b.Buffer.Write(data)
}

func (c *Context) statusBuildHookPath() (string, error) {
	path := filepath.Join(c.Harness, "hooks", "wtc", "status.build.sh")
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Mode()&0111 == 0 {
		return "", fmt.Errorf("status build hook must be executable: %s", path)
	}
	return path, nil
}

func (c *Context) statusBuildFromHook(path string, request statusBuildRequest) (*StatusBuild, error) {
	payload, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, path)
	command.WaitDelay = 200 * time.Millisecond
	command.Dir = c.Collection
	command.Env = append(append(os.Environ(), envcompat.Both("WTC_COLLECTION", filepath.Base(c.Collection))...), envcompat.Both("WTC_CONFIG_ROOT", c.ConfigRoot)...)
	command.Stdin = bytes.NewReader(payload)
	stdout := statusLimitedBuffer{limit: 1 << 20}
	command.Stdout = &stdout
	stderr := statusLimitedBuffer{limit: 4096}
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		if stdout.overflow || stderr.overflow {
			return nil, fmt.Errorf("status build hook output too large")
		}
		if ctx.Err() != nil {
			return nil, fmt.Errorf("status build hook timed out")
		}
		return nil, fmt.Errorf("status build hook: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	if strings.TrimSpace(stdout.String()) == "" || strings.TrimSpace(stdout.String()) == "null" {
		return nil, nil
	}
	var build struct {
		StatusBuild
		UseDefault bool `json:"use_default"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &build); err != nil {
		return nil, fmt.Errorf("status build hook JSON: %w", err)
	}
	if build.UseDefault {
		return nil, errStatusBuildDefault
	}
	build.HeadInBuild, build.Error = nil, ""
	if build.Branch == "" {
		build.Branch = request.Branch
	}
	if build.Branch != request.Branch {
		return nil, fmt.Errorf("status build hook returned a different branch")
	}
	if build.Checks != nil {
		switch *build.Checks {
		case "SUCCESS", "FAILURE", "PENDING", "NONE", "ERROR", "EXPECTED":
		default:
			return nil, fmt.Errorf("status build hook returned an unknown check state")
		}
	}
	if build.URL != nil && *build.URL != "" {
		parsed, err := url.Parse(*build.URL)
		if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" {
			return nil, fmt.Errorf("status build hook returned an invalid build URL")
		}
	}
	if build.SHA != nil {
		lower := strings.ToLower(*build.SHA)
		build.SHA = &lower
	}
	if build.SHA != nil && !statusCommitHash.MatchString(*build.SHA) {
		return nil, fmt.Errorf("status build hook returned an invalid commit")
	}
	if build.Checks == nil && build.Build == nil {
		return nil, nil
	}
	return &build.StatusBuild, nil
}

var statusCommitHash = regexp.MustCompile(`^[0-9a-f]{7,64}$`)

type statusBuildTier struct {
	Tier, Branch string
}

// statusBuildTiers maps the registry onto build tiers. production_ref, or
// release_ref, is the branch that deploys. When both are unset, a development
// ref of dev deploys from master and every other development ref deploys from
// itself. That one branch is production. A different production ref keeps the
// development ref as the test tier.
func statusBuildTiers(repo Repo) []statusBuildTier {
	tip := strings.TrimPrefix(repo.DefaultRef, "origin/")
	if tip == "" {
		tip = "main"
	}
	prod := strings.TrimPrefix(repo.ProductionRef, "origin/")
	if prod == "" {
		prod = strings.TrimPrefix(repo.ReleaseRef, "origin/")
	}
	if prod == "" {
		if tip == "dev" {
			prod = "master"
		} else {
			prod = tip
		}
	}
	if prod == tip {
		return []statusBuildTier{{"prod", tip}}
	}
	return []statusBuildTier{{"tip", tip}, {"prod", prod}}
}

func (c *Context) statusBuild(hook string, request statusBuildRequest) *StatusBuild {
	build, err := (*StatusBuild)(nil), errStatusBuildDefault
	if hook != "" {
		build, err = c.statusBuildFromHook(hook, request)
	}
	if err == errStatusBuildDefault {
		build, err = statusForgeBuild(request.Forge, request.Slug, request.Branch)
	}
	if errors.Is(err, errForgeBudget) || errors.Is(err, errForgeCooling) {
		return nil
	}
	if err != nil {
		// One unreachable provider must not cost the rest of the refresh.
		message := strings.TrimSpace(err.Error())
		if len(message) > 300 {
			message = message[:300] + "…"
		}
		return &StatusBuild{Branch: request.Branch, Checks: statusString("ERROR"), Error: message}
	}
	return build
}

// statusHeadInBuild answers whether head is contained in the build commit.
// A build commit the local repository has not fetched yet is unknown, not
// "outside the build".
func statusHeadInBuild(worktree, head, sha string) *bool {
	if worktree == "" || head == "" || sha == "" {
		return nil
	}
	if _, err := statusGit(worktree, "cat-file", "-e", sha+"^{commit}"); err != nil {
		return nil
	}
	_, err := statusGit(worktree, "merge-base", "--is-ancestor", head, sha)
	if err == nil {
		yes := true
		return &yes
	}
	if exit, ok := errors.AsType[*exec.ExitError](err); ok && exit.ExitCode() == 1 {
		no := false
		return &no
	}
	return nil
}

func (c *Context) statusBuildFacts(snapshot *StatusSnapshot) error {
	hook, err := c.statusBuildHookPath()
	if err != nil {
		return err
	}
	type job struct {
		repo string
		tier statusBuildTier
		req  statusBuildRequest
	}
	jobs := []job{}
	worktrees := map[string]string{}
	tiers := map[string][]statusBuildTier{}
	harnessName, _ := c.HarnessRepoName()
	for _, row := range snapshot.Repos {
		repo, err := c.Repository(row.Repo)
		if err != nil {
			// Auxiliary worktrees can be checked out without a registry entry.
			// The build provider only has a branch contract for registered repos.
			continue
		}
		if _, seen := worktrees[repo.Name]; seen {
			continue
		}
		worktrees[repo.Name] = row.Worktree
		slug, forge := c.statusRecordForge(PRRecord{Repo: row.Repo}, snapshot.Repos)
		if hook == "" && forge == "" {
			continue
		}
		tiers[repo.Name] = statusBuildTiers(repo)
		for _, tier := range tiers[repo.Name] {
			jobs = append(jobs, job{repo.Name, tier, statusBuildRequest{Collection: filepath.Base(c.Collection),
				Repo: row.Repo, Worktree: row.Worktree, Slug: slug, Forge: forge, Branch: tier.Branch, Tier: tier.Tier}})
		}
	}
	if len(jobs) == 0 {
		return nil
	}
	previousNote := statusAuditNote
	statusAuditNote = func(message string) { c.statusProgress(message) }
	defer func() { statusAuditNote = previousNote }()
	results := make([]*StatusBuild, len(jobs))
	limit := make(chan struct{}, 4)
	var workers sync.WaitGroup
	var done atomic.Int32
	for i, j := range jobs {
		workers.Add(1)
		go func() {
			defer workers.Done()
			limit <- struct{}{}
			defer func() { <-limit }()
			results[i] = c.statusBuild(hook, j.req)
			c.statusProgress(fmt.Sprintf("Checking builds %d/%d", done.Add(1), len(jobs)))
		}()
	}
	workers.Wait()
	builds := map[string]map[string]*StatusBuild{}
	for i, j := range jobs {
		if results[i] == nil {
			continue
		}
		if results[i].Error != "" {
			c.statusProgress(fmt.Sprintf("%s %s build: %s", j.repo, j.tier.Tier, results[i].Error))
		}
		if builds[j.repo] == nil {
			builds[j.repo] = map[string]*StatusBuild{}
		}
		builds[j.repo][j.tier.Tier] = results[i]
	}
	for i := range snapshot.Repos {
		row := &snapshot.Repos[i]
		repo, err := c.Repository(row.Repo)
		if err != nil {
			continue
		}
		head, _ := statusGit(row.Worktree, "rev-parse", "HEAD")
		attribute := func(build *StatusBuild) *StatusBuild {
			if build == nil {
				return nil
			}
			own := *build
			if own.SHA != nil {
				own.HeadInBuild = statusHeadInBuild(row.Worktree, head, *own.SHA)
			}
			return &own
		}
		row.Tip = attribute(builds[repo.Name]["tip"])
		row.Prod = attribute(builds[repo.Name]["prod"])
	}
	for i := range snapshot.PRs {
		pr := &snapshot.PRs[i]
		name := pr.Repo
		if name == "harness" && harnessName != "" {
			name = harnessName
		}
		if repo, err := c.Repository(name); err == nil {
			name = repo.Name
		}
		statusFollowPR(pr, worktrees[name], tiers[name], builds[name])
	}
	return nil
}

func statusFollowLabel(build *StatusBuild, contained *bool) string {
	if build == nil {
		return ""
	}
	label := ""
	if build.Build != nil && *build.Build != "" {
		label = " #" + *build.Build
	}
	switch {
	case build.Error != "":
		return "error"
	case contained != nil && !*contained:
		return "waiting" + label
	case build.Checks != nil:
		return statusGlyph(*build.Checks) + label
	}
	return strings.TrimSpace(label)
}

// statusFollowPR carries a merged PR through the test and production builds:
// it is followed until each tier has a passing build that contains the merge.
// The follow starts at the tier that builds the PR's base branch; a PR merged
// into a branch no tier builds (a stacked or release branch) is not followed.
func statusFollowPR(pr *StatusPRRow, worktree string, tiers []statusBuildTier, builds map[string]*StatusBuild) {
	if pr.State != "MERGED" || pr.Archived || pr.OnBranch || pr.MergeCommit == "" {
		// A merge recorded before merge commits were kept has nothing to
		// attribute; it shows as plainly merged rather than as unknown.
		return
	}
	start := -1
	for i, tier := range tiers {
		if pr.Base == tier.Branch || (pr.Base == "" && i == 0) {
			start = i
			break
		}
	}
	if start < 0 {
		return
	}
	followed := false
	for _, t := range tiers[start:] {
		tier := struct {
			stage string
			build *StatusBuild
			label **string
		}{t.Tier, builds[t.Tier], &pr.FollowTipBuild}
		if t.Tier == "prod" {
			tier.label = &pr.FollowProdBuild
		}
		if tier.build == nil {
			continue
		}
		followed = true
		var contained *bool
		if tier.build.SHA != nil {
			contained = statusHeadInBuild(worktree, pr.MergeCommit, *tier.build.SHA)
		}
		*tier.label = statusString(statusFollowLabel(tier.build, contained))
		if pr.Follow != nil {
			continue
		}
		checks := "PENDING"
		switch {
		case tier.build.Error != "":
			checks = "ERROR"
		case contained == nil:
			// Without both commits the build cannot be attributed to the merge.
			checks = "UNKNOWN"
		case !*contained:
			checks = "WAITING"
		case tier.build.Checks != nil:
			checks = *tier.build.Checks
		}
		if checks != "SUCCESS" {
			pr.Follow = &StatusFollow{Stage: tier.stage, Checks: checks, Build: tier.build.Build, URL: tier.build.URL}
		}
	}
	if pr.Follow == nil && followed {
		pr.Follow = &StatusFollow{Stage: "done", Checks: "SUCCESS"}
	}
}
