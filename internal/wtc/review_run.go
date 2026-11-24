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
	"taba.sh/taba/internal/prompts"
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

// reviewRunDirs separates what an agent may see from what the runner keeps.
// A private run uses the bundle and the collection worktree directly; a public
// run uses an isolated execution context (see review_context.go).
type reviewRunDirs struct {
	bundle, runner, repo string
	exec                 *reviewExecContext
}

// RunReviewBundle runs a review that nothing interrupts but its own timeouts.
func (c *Context) RunReviewBundle(bundle string, opt ReviewRunOptions) (ReviewRunResult, error) {
	return c.RunReviewBundleContext(context.Background(), bundle, opt)
}

// errReviewInterrupted marks a run its caller cancelled. The error also wraps
// the context's cause: context.Canceled for a plain cancel, or the signal for
// a signal context.
var errReviewInterrupted = errors.New("review run interrupted")

// RunReviewBundleContext runs a review that stops when ctx is done: no further
// reviewer starts, each running reviewer's process group is stopped and waited
// for, an isolated context is removed only after that, and the bundle keeps no
// summary or verdict that could be posted for the interrupted run.
func (c *Context) RunReviewBundleContext(ctx context.Context, bundle string, opt ReviewRunOptions) (_ ReviewRunResult, err error) {
	if cause := context.Cause(ctx); cause != nil {
		return ReviewRunResult{}, fmt.Errorf("%w before it started: %w", errReviewInterrupted, cause)
	}
	bundle, err = filepath.Abs(bundle)
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
	if manifest.Public {
		// Refuse a client that cannot isolate before probing one that can,
		// so the actionable refusal is not hidden by an unrelated client check.
		agents := reviewSpecAgents(opt.Strong, opt.Standard, opt.Fast, opt.Lead)
		sort.SliceStable(agents, func(i, j int) bool { return agents[i] != "claude" && agents[j] == "claude" })
		for _, agent := range agents {
			if err := reviewPublicClient(agent); err != nil {
				return ReviewRunResult{}, err
			}
		}
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
	checks, err := prompts.Render(c.reviewPrompts(manifest), "review.checks-suffix", nil)
	if err != nil {
		return ReviewRunResult{}, err
	}
	concernTemplate = []byte(reviewCheckPrompt(string(concernTemplate), checks))
	leadTemplate = []byte(reviewCheckPrompt(string(leadTemplate), checks))
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
	dirs := reviewRunDirs{bundle: bundle, runner: bundle, repo: manifest.RepoDir}
	callerBundle := bundle
	// Registered before the isolated context's finish, so it runs after the
	// results return: an interrupted run leaves nothing a poster would accept.
	defer func() {
		if errors.Is(err, errReviewInterrupted) {
			for _, name := range []string{"summary.md", "verdict"} {
				if removeErr := os.Remove(filepath.Join(callerBundle, name)); removeErr != nil && !errors.Is(removeErr, fs.ErrNotExist) {
					err = errors.Join(err, removeErr)
				}
			}
		}
	}()
	if manifest.Public {
		// Assign, not declare: the deferred finish must report into the
		// function's named err, not a block-scoped copy.
		var x *reviewExecContext
		x, err = newReviewExecContext(c.Collection, bundle, manifest)
		if err != nil {
			return ReviewRunResult{}, err
		}
		dirs = reviewRunDirs{bundle: x.bundle, runner: x.runner, repo: x.source, exec: x}
		defer func() {
			if finishErr := x.finish(callerBundle); finishErr != nil && err == nil {
				err = finishErr
			}
		}()
	} else {
		var stage *reviewStage
		stage, err = newReviewStage(bundle)
		if err != nil {
			return ReviewRunResult{}, err
		}
		// Prompts, logs and statistics live in the stage as well, as they
		// lived beside the results before: a launcher that finds the bundle
		// from its prompt's path still writes into the stage.
		dirs.bundle, dirs.runner = stage.dir, stage.dir
		defer func() {
			if finishErr := stage.finish(callerBundle); finishErr != nil && err == nil {
				err = finishErr
			}
		}()
	}
	// From here on bundle is the directory agents use; the caller's bundle
	// receives the results when an isolated run finishes.
	bundle = dirs.bundle
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
launch:
	for _, path := range concernFiles {
		id := strings.TrimSuffix(filepath.Base(path), ".md")
		if len(selected) > 0 && !selected[id] {
			continue
		}
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			break launch
		}
		if ctx.Err() != nil {
			<-sem
			break launch
		}
		count++
		wg.Add(1)
		go func(id, path string) {
			defer wg.Done()
			defer func() { <-sem }()
			entry := c.runReviewConcern(ctx, dirs, manifest, id, path, string(concernTemplate), opt)
			mu.Lock()
			log.WriteString(entry)
			mu.Unlock()
		}(id, path)
	}
	// Every launched reviewer's leader has been waited for here, and a
	// stopped or timed-out reviewer's process group swept, so an isolated
	// context or stage is removed only after that. A descendant that left its
	// group, or that a finished reviewer left running, is not waited for; the
	// context or stage fences what it writes to the paths it was given.
	wg.Wait()
	var auditErr error
	if dirs.exec != nil {
		// The runner clears a selected concern's earlier results before it
		// launches that concern, so removal is allowed there. An interrupted
		// phase is audited too: what returns to the caller was checked.
		auditErr = dirs.exec.checkPhase("concern", func(rel string, existed, removed bool) bool {
			id, ok := reviewFindingPath(rel)
			return ok && (len(selected) == 0 || selected[id]) && available[id]
		})
	}
	if cause := context.Cause(ctx); cause != nil {
		return ReviewRunResult{}, errors.Join(fmt.Errorf("%w during concern reviews: %w", errReviewInterrupted, cause), auditErr)
	}
	if auditErr != nil {
		return ReviewRunResult{}, auditErr
	}
	for _, path := range concernFiles {
		id := strings.TrimSuffix(filepath.Base(path), ".md")
		if err := validateReviewFindings(filepath.Join(bundle, "findings", id+".json"), id); err != nil {
			log.WriteString(reviewConcernError(dirs, id, err.Error()))
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
	if dirs.exec != nil {
		if err := dirs.exec.snapshotBundle(); err != nil {
			return ReviewRunResult{}, err
		}
	}
	leadPrompt := filepath.Join(dirs.runner, ".prompts", "lead.md")
	if err := renderReviewPrompt(leadPrompt, string(leadTemplate), map[string]string{
		"BUNDLE": bundle, "REPO_DIR": dirs.repo, "SUMMARY_FILE": filepath.Join(bundle, "summary.md"),
		"VERDICT_FILE": filepath.Join(bundle, "verdict"), "MANIFEST": filepath.Join(bundle, "manifest.env"),
	}); err != nil {
		return ReviewRunResult{}, err
	}
	leadAgent, leadModel, leadOutput, leadStats, err := runReviewChain(ctx, dirs, leadPrompt, opt.Lead, opt.Timeout, filepath.Join(dirs.runner, "stats", "lead.json"))
	if dirs.exec != nil {
		if auditErr := dirs.exec.checkPhase("lead", func(rel string, existed, removed bool) bool {
			_, finding := reviewFindingPath(rel)
			if removed {
				return false
			}
			return rel == "summary.md" || rel == "verdict" || finding && existed && strings.HasSuffix(rel, ".json")
		}); auditErr != nil {
			if cause := context.Cause(ctx); cause != nil {
				return ReviewRunResult{}, errors.Join(fmt.Errorf("%w during the lead review: %w", errReviewInterrupted, cause), auditErr)
			}
			return ReviewRunResult{}, auditErr
		}
	}
	if writeErr := os.WriteFile(filepath.Join(dirs.runner, ".logs", "lead.out"), leadOutput, 0644); writeErr != nil {
		return ReviewRunResult{}, writeErr
	}
	leadStatus := "ok"
	if err != nil {
		leadStatus = "error"
	}
	leadStats.Status = leadStatus
	if writeErr := writeReviewStats(dirs.runner, "lead", leadStats); writeErr != nil {
		return ReviewRunResult{}, writeErr
	}
	if cause := context.Cause(ctx); cause != nil {
		return ReviewRunResult{}, fmt.Errorf("%w during the lead review: %w", errReviewInterrupted, cause)
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
	stats, err := reviewStatsTable(dirs.runner, time.Since(started))
	if err != nil {
		return ReviewRunResult{}, err
	}
	final := strings.TrimRight(strings.Join(clean, "\n"), "\n")
	if checkpoints := reviewConvergenceReport(convergence); checkpoints != "" {
		final += "\n\n" + checkpoints
	}
	final += "\n\n" + reviewChecksReport(bundle, manifest)
	if dirs.exec != nil {
		final += "\n\n" + dirs.exec.report()
	}
	final += "\n\n" + stats + "\n\n" + statusLine + "\n"
	final = reviewModeBody(alignReviewHeading(final, verdict), manifest)
	if err := os.WriteFile(summaryPath, []byte(final), 0644); err != nil {
		return ReviewRunResult{}, err
	}
	return ReviewRunResult{Bundle: callerBundle, Verdict: verdict, Blockers: blockers, Round: manifest.Round, Concerns: count}, nil
}

// A private run's reviewers work in a per-run copy of the bundle, a stage
// at <bundle>/.taba-run-*/<bundle name>: the working directory keeps the
// bundle's name and its ancestors, so the collection context reviewers load is
// unchanged. When every reviewer of the run has stopped, the runner returns
// the run's own changes and its runner directories to the bundle and removes
// the stage. A process that outlived the run, such as a descendant that left
// its reviewer's process group, can then write only into a stage that is gone
// or no longer read: never a result of this run or a later one. This fences
// the paths a reviewer is given; it is not a sandbox, and such a process can
// still write wherever its permissions allow.
const (
	reviewStagePrefix = ".taba-run-"
	// Runs are assumed to end within this age; a stage older than that is
	// swept as left behind by an interrupted cleanup.
	reviewStageMaxAge = 24 * time.Hour
)

type reviewStage struct {
	root, dir string
	initial   map[string]string
}

func newReviewStage(bundle string) (*reviewStage, error) {
	sweepReviewStages(bundle)
	root, err := os.MkdirTemp(bundle, reviewStagePrefix)
	if err != nil {
		return nil, fmt.Errorf("review run stage: %w", err)
	}
	stage := &reviewStage{root: root, dir: filepath.Join(root, filepath.Base(bundle))}
	if err := copyReviewStage(bundle, stage.dir); err != nil {
		_ = removeReviewContext(root)
		return nil, fmt.Errorf("review run stage: %w", err)
	}
	if stage.initial, err = reviewTreeFiles(stage.dir); err != nil {
		_ = removeReviewContext(root)
		return nil, fmt.Errorf("review run stage: %w", err)
	}
	return stage, nil
}

// finish returns the run's changes and runner directories, then removes the
// stage. The stage is removed even when returning fails, so nothing written
// later is read. When the results cannot return, an earlier summary and
// verdict are removed too, as for an untrusted public run, so they cannot be
// posted for this run.
func (s *reviewStage) finish(bundle string) error {
	err := returnReviewTree(s.dir, bundle, s.initial, "review run stage")
	if err != nil {
		for _, name := range []string{"summary.md", "verdict"} {
			if removeErr := os.Remove(filepath.Join(bundle, name)); removeErr != nil && !errors.Is(removeErr, fs.ErrNotExist) {
				err = errors.Join(err, removeErr)
			}
		}
	}
	for _, dir := range reviewRunnerDirs {
		if _, statErr := os.Lstat(filepath.Join(s.dir, dir)); statErr != nil {
			continue
		}
		if removeErr := os.RemoveAll(filepath.Join(bundle, dir)); removeErr != nil {
			err = errors.Join(err, removeErr)
			continue
		}
		if copyErr := copyReviewRunnerDir(filepath.Join(s.dir, dir), filepath.Join(bundle, dir)); copyErr != nil {
			err = errors.Join(err, fmt.Errorf("review run stage: %w", copyErr))
		}
	}
	if removeErr := removeReviewContext(s.root); removeErr != nil {
		err = errors.Join(err, fmt.Errorf("review run stage: %w", removeErr))
	}
	return err
}

// copyReviewRunnerDir returns a stage's runner directory: its directories and
// regular files. A link or special file a reviewer left there is neither
// followed nor allowed to stop the rest from returning; it is reported.
func copyReviewRunnerDir(src, dest string) error {
	var irregular []string
	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dest, rel)
		switch {
		case d.IsDir():
			return os.MkdirAll(target, 0755)
		case d.Type().IsRegular():
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			return os.WriteFile(target, data, 0644)
		}
		irregular = append(irregular, filepath.ToSlash(filepath.Join(filepath.Base(src), rel)))
		return nil
	})
	if err == nil && len(irregular) > 0 {
		err = fmt.Errorf("%s is not a regular file; not returned", strings.Join(irregular, ", "))
	}
	return err
}

// copyReviewStage copies the bundle into its stage: directories, regular
// files and links as they are, without poster state or other stages. Special
// files are left out.
func copyReviewStage(bundle, stage string) error {
	if err := os.MkdirAll(stage, 0700); err != nil {
		return err
	}
	return filepath.WalkDir(bundle, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(bundle, path)
		if err != nil || rel == "." {
			return err
		}
		rel = filepath.ToSlash(rel)
		top := strings.SplitN(rel, "/", 2)[0]
		if containsString(reviewPosterFiles, rel) || strings.HasPrefix(top, reviewStagePrefix) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		target := filepath.Join(stage, filepath.FromSlash(rel))
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			// Modes are set explicitly: the umask must not make the copy
			// differ from the bundle it is compared with on return.
			if err := os.MkdirAll(target, 0700); err != nil {
				return err
			}
			return os.Chmod(target, info.Mode().Perm()|0700)
		case d.Type()&fs.ModeSymlink != 0:
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		case d.Type().IsRegular():
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if err := os.WriteFile(target, data, 0600); err != nil {
				return err
			}
			return os.Chmod(target, info.Mode().Perm())
		}
		return nil
	})
}

// sweepReviewStages removes stages an interrupted cleanup left behind, once
// they are older than any run is assumed to last (reviewStageMaxAge).
func sweepReviewStages(bundle string) {
	paths, _ := filepath.Glob(filepath.Join(bundle, reviewStagePrefix+"*"))
	for _, path := range paths {
		if info, err := os.Lstat(path); err == nil && info.IsDir() && time.Since(info.ModTime()) > reviewStageMaxAge {
			_ = removeReviewContext(path)
		}
	}
}

// reviewFindingPath reports the concern of a findings/<id>.json or .raw path.
func reviewFindingPath(rel string) (string, bool) {
	name, ok := strings.CutPrefix(rel, "findings/")
	if !ok || strings.Contains(name, "/") {
		return "", false
	}
	for _, ext := range []string{".json", ".raw"} {
		if id, ok := strings.CutSuffix(name, ext); ok && id != "" {
			return id, true
		}
	}
	return "", false
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

func (c *Context) runReviewConcern(ctx context.Context, dirs reviewRunDirs, manifest ReviewManifest, id, path, template string, opt ReviewRunOptions) string {
	bundle := dirs.bundle
	body, err := os.ReadFile(path)
	if err != nil {
		return reviewConcernError(dirs, id, err.Error())
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
		_ = writeReviewStats(dirs.runner, id, reviewRunStats{Status: "skipped"})
		return "skipped " + id + " (no downstream)\n"
	}
	spec := opt.Standard
	if tier == "strong" {
		spec = opt.Strong
	} else if tier == "fast" {
		spec = opt.Fast
	}
	prompt := filepath.Join(dirs.runner, ".prompts", id+".md")
	findings := filepath.Join(bundle, "findings", id+".json")
	_ = os.Remove(findings)
	_ = os.Remove(filepath.Join(bundle, "findings", id+".raw"))
	if err := renderReviewPrompt(prompt, template, map[string]string{
		"BUNDLE": bundle, "REPO_DIR": dirs.repo, "CONCERN_FILE": path, "CONCERN_ID": id,
		"FINDINGS_FILE": findings, "MANIFEST": filepath.Join(bundle, "manifest.env"),
	}); err != nil {
		return reviewConcernError(dirs, id, err.Error())
	}
	_, _, output, stats, runErr := runReviewChain(ctx, dirs, prompt, spec, opt.Timeout, filepath.Join(dirs.runner, "stats", id+".json"))
	_ = os.WriteFile(filepath.Join(dirs.runner, ".logs", id+".out"), output, 0644)
	if dirs.exec != nil {
		// A link or special file at any path the runner reads or writes for
		// this concern is left as the reviewer made it, unread and not
		// written through (a FIFO would block, a link would redirect the
		// write), so the phase audit rejects it.
		for _, path := range []string{findings, filepath.Join(bundle, "findings", id+".raw")} {
			if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
				stats.Status = "error"
				_ = writeReviewStats(dirs.runner, id, stats)
				return "ERROR " + id + ": " + filepath.Base(path) + " is not a regular file\n"
			}
		}
	}
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
	_ = writeReviewStats(dirs.runner, id, stats)
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

func reviewConcernError(dirs reviewRunDirs, id, reason string) string {
	_ = writeReviewStub(dirs.bundle, id, "error", reason)
	_ = writeReviewStats(dirs.runner, id, reviewRunStats{Status: "error"})
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

func runReviewChain(ctx context.Context, dirs reviewRunDirs, prompt, specs string, timeout time.Duration, statsPath string) (string, string, []byte, reviewRunStats, error) {
	bundle := dirs.bundle
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
		output, elapsed, err := launchReviewAgent(ctx, dirs, prompt, agent, model, timeout, statsPath)
		limited := reviewAgentLimited(output, err)
		if limited && err == nil {
			err = fmt.Errorf("%s reported a review usage limit", agent)
		}
		attempt := collectReviewStats(statsPath, agent, model, elapsed, "", output)
		addReviewUsage(&total, attempt, i > 0)
		total.Agent, total.Model = agent, attempt.Model
		// A run its caller stopped starts no fallback reviewer.
		if err == nil || i == len(list)-1 || !limited || ctx.Err() != nil {
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

// reviewStopGrace is how long a reviewer's process group has to exit after
// SIGTERM before the rest of the group is killed.
const reviewStopGrace = 5 * time.Second

func launchReviewAgent(parent context.Context, dirs reviewRunDirs, prompt, agent, model string, timeout time.Duration, statsPath string) ([]byte, time.Duration, error) {
	bundle, repoDir, x := dirs.bundle, dirs.repo, dirs.exec
	if err := context.Cause(parent); err != nil {
		return nil, 0, fmt.Errorf("review agent not started: %w", err)
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	var cmd *exec.Cmd
	custom := envcompat.Getenv("WTC_REVIEW_AGENT_CMD")
	if custom != "" {
		parts := strings.Fields(custom)
		cmd = exec.CommandContext(ctx, parts[0], append(parts[1:], agent, model, prompt, bundle)...)
	} else {
		switch {
		case x != nil && agent == "claude":
			cmd = exec.CommandContext(ctx, "claude", x.claudeArgs(model)...)
		case x != nil:
			if err := reviewPublicClient(agent); err != nil {
				return nil, 0, err
			}
			return nil, 0, fmt.Errorf("unsupported review agent %q", agent)
		case agent == "claude":
			args := []string{"-p", "--output-format", "json"}
			if model != "" {
				args = append(args, "--model", model)
			}
			args = append(args, "--add-dir", repoDir, "--permission-mode", "acceptEdits", "--disallowedTools", claudeDenyEdits(repoDir), "Bash(git * --ou*)", "--allowedTools", "Read", "Grep", "Glob", "Write", "Edit", "Bash(git log:*)", "Bash(git show:*)", "Bash(ls:*)", "Agent")
			cmd = exec.CommandContext(ctx, "claude", args...)
		case agent == "codex":
			args := []string{"exec", "--json"}
			if model != "" {
				args = append(args, "-m", model)
			}
			args = append(args, "-C", bundle, "--skip-git-repo-check", "-s", "workspace-write", "-")
			cmd = exec.CommandContext(ctx, "codex", args...)
		case agent == "grok":
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
	// The reviewer leads a process group of its own, so the runner can stop
	// it with its descendants. That group is not the terminal's foreground
	// group, so a terminal interrupt reaches only the runner, which stops the
	// group here when the caller's context is done.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// On timeout or interrupt the whole group gets SIGTERM, first checking
	// through the process handle that the leader has not been reaped, so its
	// pid still names the group this runner created. WaitDelay bounds the
	// grace: if the leader or a descendant holding the output pipe has not
	// finished by then, Wait kills the leader, closes the pipes and returns,
	// and stopReviewGroup kills the rest of the group.
	cmd.Cancel = func() error {
		if err := cmd.Process.Signal(syscall.Signal(0)); err != nil {
			return err
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	}
	cmd.WaitDelay = reviewStopGrace
	if x != nil {
		cmd.Env = x.env(agent, statsPath)
		x.noteClient(agent)
	} else {
		cmd.Env = append(os.Environ(), "WTC_REVIEW_STATS_FILE="+statsPath)
	}
	promptBody, err := os.ReadFile(prompt)
	if err != nil {
		return nil, 0, err
	}
	cmd.Stdin = bytes.NewReader(promptBody)
	start := time.Now()
	output, err := cmd.CombinedOutput()
	elapsed := time.Since(start)
	if ctx.Err() != nil {
		stopReviewGroup(cmd)
		if cause := context.Cause(parent); cause != nil {
			return output, elapsed, fmt.Errorf("review agent stopped: %w", cause)
		}
		return output, elapsed, fmt.Errorf("review agent timed out after %s", timeout)
	}
	return output, elapsed, err
}

// stopReviewGroup kills what remains of a stopped reviewer's process group
// right after Wait has returned. It signals only the group this runner created
// for cmd, by the leader's pid. While any member of that group is alive the
// id names this group: Linux keeps a pid allocated while any task still uses
// it as a group id, and the macOS (XNU) pid allocator skips ids of existing
// process groups and sessions. Once the group is empty the signal finds
// nothing (ESRCH), unless in the moment since Wait the pid space wrapped and a
// new process made itself a group leader under that id. A descendant that
// left the group (setsid, setpgid) is not the runner's to stop and is not
// signalled.
func stopReviewGroup(cmd *exec.Cmd) {
	if cmd.Process == nil || cmd.Process.Pid <= 0 {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}

// reviewPrompts resolves registry prompts for a bundle. A public bundle never
// carries project prompt overrides (#221).
func (c *Context) reviewPrompts(manifest ReviewManifest) prompts.Context {
	ctx := c.Prompts()
	if manifest.Public {
		ctx.Audience = prompts.Public
	}
	return ctx
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
