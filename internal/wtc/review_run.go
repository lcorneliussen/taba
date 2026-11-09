package wtc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"taba.sh/taba/internal/envcompat"
)

type ReviewRunOptions struct {
	Strong   string
	Standard string
	Fast     string
	Lead     string
	Parallel int
	Timeout  time.Duration
	Only     []string
}

type ReviewRunResult struct {
	Bundle   string `json:"bundle"`
	Verdict  string `json:"verdict"`
	Blockers int    `json:"blockers"`
	Round    int    `json:"round"`
	Concerns int    `json:"concerns"`
}

type reviewFindingFile struct {
	Concern  string `json:"concern"`
	Status   string `json:"status"`
	Notes    string `json:"notes"`
	Findings []struct {
		Severity string `json:"severity"`
		Title    string `json:"title"`
		Prior    string `json:"prior"`
	} `json:"findings"`
}

type reviewRunStats struct {
	Agent            string   `json:"agent"`
	Model            string   `json:"model"`
	Seconds          float64  `json:"seconds"`
	Status           string   `json:"status"`
	InputTokens      *int64   `json:"input_tokens"`
	OutputTokens     *int64   `json:"output_tokens"`
	CacheReadTokens  *int64   `json:"cache_read_tokens"`
	CacheWriteTokens *int64   `json:"cache_write_tokens"`
	CostUSD          *float64 `json:"cost_usd"`
	Turns            *int64   `json:"turns"`
	UsagePartial     bool     `json:"usage_partial,omitempty"`
	CostPartial      bool     `json:"cost_partial,omitempty"`
}

func reviewEnv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func (c *Context) RunReviewBundle(bundle string, opt ReviewRunOptions) (ReviewRunResult, error) {
	bundle, err := filepath.Abs(bundle)
	if err != nil {
		return ReviewRunResult{}, err
	}
	manifestData, err := os.ReadFile(filepath.Join(bundle, "manifest.json"))
	if err != nil {
		return ReviewRunResult{}, fmt.Errorf("native review bundle manifest: %w", err)
	}
	var manifest ReviewManifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		return ReviewRunResult{}, err
	}
	if len(manifest.HeadSHA) < 12 || manifest.Round < 1 || manifest.RepoDir == "" {
		return ReviewRunResult{}, fmt.Errorf("incomplete review manifest")
	}
	worktree, slug, forge, err := c.ReviewRepo(manifest.Repo)
	if err != nil {
		return ReviewRunResult{}, err
	}
	if manifest.RepoDir != worktree || manifest.Slug != slug || manifest.Forge != forge {
		return ReviewRunResult{}, fmt.Errorf("bundle repository does not match this collection")
	}
	if opt.Strong == "" {
		opt.Strong = reviewEnv("HARNESS_REVIEW_STRONG", "claude:opus")
	}
	if opt.Standard == "" {
		opt.Standard = reviewEnv("HARNESS_REVIEW_STANDARD", "claude:sonnet")
	}
	if opt.Fast == "" {
		opt.Fast = reviewEnv("HARNESS_REVIEW_FAST", "claude:haiku")
	}
	if opt.Lead == "" {
		opt.Lead = reviewEnv("HARNESS_REVIEW_LEAD", opt.Strong)
	}
	if opt.Parallel == 0 {
		opt.Parallel = 4
	}
	if opt.Parallel < 1 || opt.Parallel > 64 {
		return ReviewRunResult{}, fmt.Errorf("parallel must be between 1 and 64")
	}
	if opt.Timeout == 0 {
		seconds, err := strconv.Atoi(reviewEnv("HARNESS_REVIEW_TIMEOUT", "900"))
		if err != nil || seconds < 1 {
			return ReviewRunResult{}, fmt.Errorf("HARNESS_REVIEW_TIMEOUT must be positive seconds")
		}
		opt.Timeout = time.Duration(seconds) * time.Second
	}
	concernTemplate, err := c.reviewPromptTemplate(manifest, "concern.md")
	if err != nil {
		return ReviewRunResult{}, err
	}
	leadTemplate, err := c.reviewPromptTemplate(manifest, "lead.md")
	if err != nil {
		return ReviewRunResult{}, err
	}
	if _, err := writeReviewChecks(bundle, manifest); err != nil {
		return ReviewRunResult{}, err
	}
	concernTemplate = []byte(reviewCheckPrompt(string(concernTemplate)))
	leadTemplate = []byte(reviewCheckPrompt(string(leadTemplate)))
	for _, dir := range []string{"findings", ".logs", ".prompts", "stats"} {
		if err := os.MkdirAll(filepath.Join(bundle, dir), 0755); err != nil {
			return ReviewRunResult{}, err
		}
	}
	if stale, err := filepath.Glob(filepath.Join(bundle, "stats", "*.json")); err == nil {
		for _, path := range stale {
			if err := os.Remove(path); err != nil {
				return ReviewRunResult{}, err
			}
		}
	}
	concernFiles, err := filepath.Glob(filepath.Join(bundle, "concerns", "*.md"))
	if err != nil {
		return ReviewRunResult{}, err
	}
	sort.Strings(concernFiles)
	selected := map[string]bool{}
	for _, id := range opt.Only {
		selected[id] = true
	}
	available := map[string]bool{}
	for _, path := range concernFiles {
		available[strings.TrimSuffix(filepath.Base(path), ".md")] = true
	}
	for id := range selected {
		if !available[id] {
			return ReviewRunResult{}, fmt.Errorf("unknown review concern %q", id)
		}
	}
	if len(concernFiles) == 0 {
		return ReviewRunResult{}, fmt.Errorf("review bundle has no concerns")
	}
	sem := make(chan struct{}, opt.Parallel)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var log bytes.Buffer
	started := time.Now()
	count := 0
	for _, path := range concernFiles {
		id := strings.TrimSuffix(filepath.Base(path), ".md")
		if len(selected) > 0 && !selected[id] {
			continue
		}
		count++
		wg.Add(1)
		sem <- struct{}{}
		go func(id, path string) {
			defer wg.Done()
			defer func() { <-sem }()
			entry := c.runReviewConcern(bundle, manifest, id, path, string(concernTemplate), opt)
			mu.Lock()
			log.WriteString(entry)
			mu.Unlock()
		}(id, path)
	}
	wg.Wait()
	for _, path := range concernFiles {
		id := strings.TrimSuffix(filepath.Base(path), ".md")
		if err := validateReviewFindings(filepath.Join(bundle, "findings", id+".json"), id); err != nil {
			log.WriteString(reviewConcernError(bundle, id, err.Error()))
		}
	}
	if err := os.WriteFile(filepath.Join(bundle, "run.log"), log.Bytes(), 0644); err != nil {
		return ReviewRunResult{}, err
	}
	_ = os.Remove(filepath.Join(bundle, "summary.md"))
	_ = os.Remove(filepath.Join(bundle, "verdict"))
	if _, err := refreshReviewConvergence(bundle, manifest); err != nil {
		return ReviewRunResult{}, err
	}
	leadPrompt := filepath.Join(bundle, ".prompts", "lead.md")
	if err := renderReviewPrompt(leadPrompt, string(leadTemplate), map[string]string{
		"BUNDLE": bundle, "REPO_DIR": manifest.RepoDir, "SUMMARY_FILE": filepath.Join(bundle, "summary.md"),
		"VERDICT_FILE": filepath.Join(bundle, "verdict"), "MANIFEST": filepath.Join(bundle, "manifest.env"),
	}); err != nil {
		return ReviewRunResult{}, err
	}
	leadAgent, leadModel, leadOutput, leadStats, err := runReviewChain(bundle, manifest.RepoDir, leadPrompt, opt.Lead, opt.Timeout, filepath.Join(bundle, "stats", "lead.json"))
	if writeErr := os.WriteFile(filepath.Join(bundle, ".logs", "lead.out"), leadOutput, 0644); writeErr != nil {
		return ReviewRunResult{}, writeErr
	}
	leadStatus := "ok"
	if err != nil {
		leadStatus = "error"
	}
	leadStats.Status = leadStatus
	if writeErr := writeReviewStats(bundle, "lead", leadStats); writeErr != nil {
		return ReviewRunResult{}, writeErr
	}
	if err != nil {
		return ReviewRunResult{}, fmt.Errorf("lead review failed: %w", err)
	}
	summaryPath := filepath.Join(bundle, "summary.md")
	summary, err := os.ReadFile(summaryPath)
	if err != nil || len(bytes.TrimSpace(summary)) == 0 {
		return ReviewRunResult{}, fmt.Errorf("lead wrote no summary.md")
	}
	verdictData, err := os.ReadFile(filepath.Join(bundle, "verdict"))
	if err != nil {
		return ReviewRunResult{}, fmt.Errorf("lead wrote no verdict")
	}
	verdict := strings.TrimSpace(string(verdictData))
	if verdict != "pass" && verdict != "pass-with-notes" && verdict != "changes-requested" && verdict != "error" {
		return ReviewRunResult{}, fmt.Errorf("invalid lead verdict %q", verdict)
	}
	blockers, errors, err := countReviewFindings(filepath.Join(bundle, "findings"))
	if err != nil {
		return ReviewRunResult{}, err
	}
	if errors > 0 {
		verdict = "error"
	} else if blockers > 0 {
		verdict = "changes-requested"
	}
	convergence, err := refreshReviewConvergence(bundle, manifest)
	if err != nil {
		return ReviewRunResult{}, err
	}
	if err := os.WriteFile(filepath.Join(bundle, "verdict"), []byte(verdict+"\n"), 0644); err != nil {
		return ReviewRunResult{}, err
	}
	statusLine := fmt.Sprintf("`wtc-review v1 head=%s verdict=%s blockers=%d round=%d lead=%s:%s`", manifest.HeadSHA, verdict, blockers, manifest.Round, leadAgent, leadModel)
	var clean []string
	for _, line := range strings.Split(string(summary), "\n") {
		if !strings.Contains(line, "wtc-review v1 head=") {
			clean = append(clean, line)
		}
	}
	stats, err := reviewStatsTable(bundle, time.Since(started))
	if err != nil {
		return ReviewRunResult{}, err
	}
	final := strings.TrimRight(strings.Join(clean, "\n"), "\n")
	if checkpoints := reviewConvergenceReport(convergence); checkpoints != "" {
		final += "\n\n" + checkpoints
	}
	final += "\n\n" + reviewChecksReport(bundle, manifest) + "\n\n" + stats + "\n\n" + statusLine + "\n"
	final = reviewModeBody(alignReviewHeading(final, verdict), manifest)
	if err := os.WriteFile(summaryPath, []byte(final), 0644); err != nil {
		return ReviewRunResult{}, err
	}
	return ReviewRunResult{Bundle: bundle, Verdict: verdict, Blockers: blockers, Round: manifest.Round, Concerns: count}, nil
}

var reviewHeading = regexp.MustCompile(`(?m)^(?:🟢 |🟡 |🔴 )?\*\*Local review: (pass|pass-with-notes|changes-requested|error)\*\*[^\n]*`)

func alignReviewHeading(summary, verdict string) string {
	match := reviewHeading.FindStringSubmatchIndex(summary)
	if len(match) < 4 || summary[match[2]:match[3]] == verdict {
		return summary
	}
	heading := "**Local review: " + verdict + "** — The runner adjusted the lead verdict; see the findings and gate record."
	return summary[:match[0]] + heading + summary[match[1]:]
}

func (c *Context) runReviewConcern(bundle string, manifest ReviewManifest, id, path, template string, opt ReviewRunOptions) string {
	body, err := os.ReadFile(path)
	if err != nil {
		return reviewConcernError(bundle, id, err.Error())
	}
	tier, needs := "standard", ""
	for _, line := range strings.Split(string(body), "\n") {
		if strings.HasPrefix(line, "tier:") {
			tier = strings.TrimSpace(strings.TrimPrefix(line, "tier:"))
		}
		if strings.HasPrefix(line, "needs:") {
			needs = strings.TrimSpace(strings.TrimPrefix(line, "needs:"))
		}
	}
	if needs == "downstream" && !hasReviewSnapshots(bundle) {
		_ = writeReviewStub(bundle, id, "skipped", "needs downstream repos; the bundle has none")
		_ = writeReviewStats(bundle, id, reviewRunStats{Status: "skipped"})
		return "skipped " + id + " (no downstream)\n"
	}
	spec := opt.Standard
	if tier == "strong" {
		spec = opt.Strong
	} else if tier == "fast" {
		spec = opt.Fast
	}
	prompt := filepath.Join(bundle, ".prompts", id+".md")
	findings := filepath.Join(bundle, "findings", id+".json")
	_ = os.Remove(findings)
	_ = os.Remove(filepath.Join(bundle, "findings", id+".raw"))
	if err := renderReviewPrompt(prompt, template, map[string]string{
		"BUNDLE": bundle, "REPO_DIR": manifest.RepoDir, "CONCERN_FILE": path, "CONCERN_ID": id,
		"FINDINGS_FILE": findings, "MANIFEST": filepath.Join(bundle, "manifest.env"),
	}); err != nil {
		return reviewConcernError(bundle, id, err.Error())
	}
	_, _, output, stats, runErr := runReviewChain(bundle, manifest.RepoDir, prompt, spec, opt.Timeout, filepath.Join(bundle, "stats", id+".json"))
	_ = os.WriteFile(filepath.Join(bundle, ".logs", id+".out"), output, 0644)
	status := "ok"
	if runErr == nil {
		runErr = validateReviewFindings(findings, id)
	}
	if runErr != nil {
		status = "error"
		if raw, e := os.ReadFile(findings); e == nil {
			_ = os.WriteFile(filepath.Join(bundle, "findings", id+".raw"), raw, 0644)
		} else {
			_ = os.WriteFile(filepath.Join(bundle, "findings", id+".raw"), output, 0644)
		}
		_ = writeReviewStub(bundle, id, "error", runErr.Error())
	}
	stats.Status = status
	_ = writeReviewStats(bundle, id, stats)
	if runErr != nil {
		return "ERROR " + id + ": " + runErr.Error() + "\n"
	}
	return "done " + id + "\n"
}

func hasReviewSnapshots(bundle string) bool {
	for _, name := range []string{"downstream", "upstream"} {
		entries, err := os.ReadDir(filepath.Join(bundle, name))
		if err == nil && len(entries) > 0 {
			return true
		}
	}
	return false
}

func renderReviewPrompt(path, template string, values map[string]string) error {
	for key, value := range values {
		template = strings.ReplaceAll(template, "{{"+key+"}}", value)
	}
	return os.WriteFile(path, []byte(template), 0644)
}

func writeReviewStub(bundle, id, status, notes string) error {
	data, err := json.MarshalIndent(map[string]any{"concern": id, "status": status, "notes": notes, "findings": []any{}}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(bundle, "findings", id+".json"), append(data, '\n'), 0644)
}

func reviewConcernError(bundle, id, reason string) string {
	_ = writeReviewStub(bundle, id, "error", reason)
	_ = writeReviewStats(bundle, id, reviewRunStats{Status: "error"})
	return "ERROR " + id + ": " + reason + "\n"
}

func writeReviewStats(bundle, id string, stats reviewRunStats) error {
	data, err := json.Marshal(stats)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(bundle, "stats", id+".json"), append(data, '\n'), 0644)
}

func validateReviewFindings(path, id string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("no findings file written")
	}
	var file reviewFindingFile
	if err := json.Unmarshal(data, &file); err != nil {
		return fmt.Errorf("invalid findings JSON: %w", err)
	}
	if file.Concern != id || file.Findings == nil {
		return fmt.Errorf("invalid findings for %s", id)
	}
	switch file.Status {
	case "ok", "issues", "skipped", "error":
	default:
		return fmt.Errorf("invalid concern status %q", file.Status)
	}
	for _, finding := range file.Findings {
		if finding.Title == "" {
			return fmt.Errorf("finding has no title")
		}
		switch finding.Severity {
		case "blocker", "major", "minor", "nit":
		default:
			return fmt.Errorf("invalid finding severity %q", finding.Severity)
		}
	}
	return nil
}

func countReviewFindings(dir string) (blockers, errors int, err error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return 0, 0, err
	}
	for _, path := range paths {
		data, e := os.ReadFile(path)
		if e != nil {
			return 0, 0, e
		}
		var file reviewFindingFile
		if e := json.Unmarshal(data, &file); e != nil {
			return 0, 0, e
		}
		if file.Status == "error" {
			errors++
		}
		for _, finding := range file.Findings {
			if finding.Severity == "blocker" && finding.Prior != "addressed" {
				blockers++
			}
		}
	}
	return blockers, errors, nil
}

var reviewLimitMessage = regexp.MustCompile(`(?i)session limit|usage limit|rate limit|too many requests|overloaded|quota|HTTP/? ?429|status 429|hit your .{0,40}limit`)

func runReviewChain(bundle, repoDir, prompt, specs string, timeout time.Duration, statsPath string) (string, string, []byte, reviewRunStats, error) {
	list := strings.Fields(strings.ReplaceAll(specs, ",", " "))
	if len(list) == 0 {
		return "", "", nil, reviewRunStats{}, fmt.Errorf("no review agent specified")
	}
	var total reviewRunStats
	for i, spec := range list {
		if i > 0 {
			id := strings.TrimSuffix(filepath.Base(prompt), ".md")
			outputs := []string{filepath.Join(bundle, "findings", id+".json")}
			if id == "lead" {
				outputs = []string{filepath.Join(bundle, "summary.md"), filepath.Join(bundle, "verdict")}
			}
			for _, path := range outputs {
				if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
					return "", "", nil, total, err
				}
			}
		}
		parts := strings.SplitN(spec, ":", 2)
		agent, model := parts[0], ""
		if len(parts) == 2 {
			model = parts[1]
		}
		_ = os.Remove(statsPath)
		output, elapsed, err := launchReviewAgent(bundle, repoDir, prompt, agent, model, timeout, statsPath)
		limited := reviewAgentLimited(output, err)
		if limited && err == nil {
			err = fmt.Errorf("%s reported a review usage limit", agent)
		}
		attempt := collectReviewStats(statsPath, agent, model, elapsed, "", output)
		addReviewUsage(&total, attempt, i > 0)
		total.Agent, total.Model = agent, attempt.Model
		if err == nil || i == len(list)-1 || !limited {
			return agent, model, output, total, err
		}
	}
	return "", "", nil, total, fmt.Errorf("review agent chain exhausted")
}

func reviewAgentLimited(output []byte, err error) bool {
	if err != nil {
		return reviewLimitMessage.Match(output)
	}
	for _, line := range bytes.Split(output, []byte("\n")) {
		var event struct {
			Type    string          `json:"type"`
			IsError bool            `json:"is_error"`
			Error   json.RawMessage `json:"error"`
		}
		if json.Unmarshal(line, &event) == nil {
			if (event.Type == "error" || event.IsError || len(event.Error) > 0 && string(event.Error) != "null") && reviewLimitMessage.Match(line) {
				return true
			}
		} else if len(bytes.TrimSpace(output)) < 1024 && reviewLimitMessage.Match(line) {
			return true
		}
	}
	return false
}

func launchReviewAgent(bundle, repoDir, prompt, agent, model string, timeout time.Duration, statsPath string) ([]byte, time.Duration, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	var cmd *exec.Cmd
	custom := envcompat.Getenv("WTC_REVIEW_AGENT_CMD")
	if custom != "" {
		parts := strings.Fields(custom)
		cmd = exec.CommandContext(ctx, parts[0], append(parts[1:], agent, model, prompt, bundle)...)
	} else {
		switch agent {
		case "claude":
			args := []string{"-p", "--output-format", "json"}
			if model != "" {
				args = append(args, "--model", model)
			}
			args = append(args, "--add-dir", repoDir, "--permission-mode", "acceptEdits", "--disallowedTools", "Edit("+repoDir+"/**)", "Write("+repoDir+"/**)", "Bash(git * --ou*)", "--allowedTools", "Read", "Grep", "Glob", "Write", "Edit", "Bash(git log:*)", "Bash(git show:*)", "Bash(ls:*)", "Agent")
			cmd = exec.CommandContext(ctx, "claude", args...)
		case "codex":
			args := []string{"exec", "--json"}
			if model != "" {
				args = append(args, "-m", model)
			}
			args = append(args, "-C", bundle, "--skip-git-repo-check", "-s", "workspace-write", "-")
			cmd = exec.CommandContext(ctx, "codex", args...)
		case "grok":
			args := []string{"--prompt-file", prompt, "--output-format", "json", "--cwd", bundle, "--sandbox", "workspace", "--always-approve", "--no-alt-screen", "--disable-web-search", "--no-subagents", "--verbatim", "--disallowed-tools", "web_search,web_fetch"}
			if model != "" {
				args = append(args, "--model", model)
			}
			args = append(args, "--reasoning-effort", reviewEnv("HARNESS_REVIEW_GROK_EFFORT", "low"))
			cmd = exec.CommandContext(ctx, "grok", args...)
		default:
			return nil, 0, fmt.Errorf("unsupported review agent %q", agent)
		}
	}
	cmd.Dir = bundle
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Env = append(os.Environ(), "WTC_REVIEW_STATS_FILE="+statsPath)
	promptBody, err := os.ReadFile(prompt)
	if err != nil {
		return nil, 0, err
	}
	cmd.Stdin = bytes.NewReader(promptBody)
	start := time.Now()
	output, err := cmd.CombinedOutput()
	elapsed := time.Since(start)
	if ctx.Err() == context.DeadlineExceeded {
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		return output, elapsed, fmt.Errorf("review agent timed out after %s", timeout)
	}
	return output, elapsed, err
}

// reviewPromptTemplate returns the harness override for a private bundle, or the
// embedded default when the bundle is public or the harness has no override.
func (c *Context) reviewPromptTemplate(manifest ReviewManifest, name string) ([]byte, error) {
	if !manifest.Public {
		body, err := os.ReadFile(filepath.Join(c.Harness, "review", "prompts", name))
		if !errors.Is(err, fs.ErrNotExist) {
			return body, err
		}
	}
	return ReadDefault("review/prompts/" + name)
}
