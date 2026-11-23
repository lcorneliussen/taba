package wtc

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReviewRunnerUsesSeparateProcessResultsAndClosesIncompleteCoverage(t *testing.T) {
	collection := filepath.Join(t.TempDir(), "sample")
	harness := filepath.Join(collection, "harness")
	bundle := filepath.Join(collection, "bundle")
	for _, dir := range []string{harness, filepath.Join(harness, "review", "prompts"), filepath.Join(bundle, "concerns"), filepath.Join(collection, "app", ".git")} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(harness, ".harness-repos.yml"), []byte("repos:\n  - name: app\n    remote: https://github.com/example/app.git\n"), 0644); err != nil {
		t.Fatal(err)
	}
	manifest := ReviewManifest{Repo: "app", PR: "7", Forge: "github", Slug: "example/app", HeadSHA: "1234567890abcdef1234567890abcdef12345678", Round: 3, RepoDir: filepath.Join(collection, "app")}
	if err := writeReviewManifest(bundle, manifest); err != nil {
		t.Fatal(err)
	}
	before, after := 20, 40
	if err := writeReviewConvergence(bundle, reviewConvergence{Round: 3, PatchLines: &after, PreviousRound: 2, PreviousPatchLines: &before, GrowthThreshold: 50, ScopeGrew: true, PreviousMajorConcerns: []string{"code"}}); err != nil {
		t.Fatal(err)
	}
	for path, body := range map[string]string{
		filepath.Join(harness, "review", "prompts", "concern.md"): "{{FINDINGS_FILE}}\n{{CONCERN_ID}}\n",
		filepath.Join(harness, "review", "prompts", "lead.md"):    "{{SUMMARY_FILE}}\n{{VERDICT_FILE}}\n",
		filepath.Join(bundle, "concerns", "code.md"):              "---\ntier: fast\n---\nReview code.\n",
		filepath.Join(bundle, "concerns", "compat.md"):            "---\nneeds: downstream\n---\nReview compatibility.\n",
	} {
		if err := os.WriteFile(path, []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	launcher := filepath.Join(collection, "review-agent.sh")
	script := `#!/bin/sh
if [ "$(basename "$3")" = lead.md ]; then
  summary=$(sed -n '1p' "$3")
  verdict=$(sed -n '2p' "$3")
  printf '**Local review: pass**\n' > "$summary"
  printf 'pass\n' > "$verdict"
  printf '{"input_tokens":1200,"output_tokens":50,"cache_read_tokens":200,"cost_usd":0.02,"turns":2,"model":"sample-model"}\n' > "$WTC_REVIEW_STATS_FILE"
  exit 0
fi
findings=$(sed -n '1p' "$3")
id=$(sed -n '2p' "$3")
if [ "${BLOCKER_CONCERN:-}" = "$id" ]; then
  printf '{"concern":"%s","status":"issues","findings":[{"severity":"blocker","title":"Breaks execution"}]}\n' "$id" > "$findings"
  exit 0
fi
printf '{"concern":"%s","status":"ok","findings":[]}\n' "$id" > "$findings"
printf '{"input_tokens":100,"output_tokens":10,"cache_read_tokens":20,"cost_usd":0.01,"turns":1}\n' > "$WTC_REVIEW_STATS_FILE"
if [ "${FAIL_CONCERN:-}" = "$id" ]; then exit 3; fi
`
	if err := os.WriteFile(launcher, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	checkBin := t.TempDir()
	if err := os.WriteFile(filepath.Join(checkBin, "gh"), []byte("#!/bin/sh\nprintf '%s' '{\"headRefOid\":\"1234567890abcdef1234567890abcdef12345678\",\"statusCheckRollup\":[{\"conclusion\":\"FAILURE\",\"status\":\"COMPLETED\"}]}'\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", checkBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("WTC_FORGE_MIN_INTERVAL", "0")
	t.Setenv("WTC_REVIEW_AGENT_CMD", launcher)
	c, err := OpenCollection(collection)
	if err != nil {
		t.Fatal(err)
	}
	opt := ReviewRunOptions{Strong: "test:", Standard: "test:", Fast: "test:", Lead: "test:", Parallel: 2}
	got, err := c.RunReviewBundle(bundle, opt)
	if err != nil || got.Verdict != "pass" || got.Concerns != 2 {
		t.Fatalf("healthy run: %+v %v", got, err)
	}
	redSummary, err := os.ReadFile(filepath.Join(bundle, "summary.md"))
	if err != nil || !strings.Contains(string(redSummary), "Confirmed failing check on the reviewed head") || !strings.Contains(string(redSummary), "Required-check policy is unknown") {
		t.Fatalf("passing code review hid red checks: %s %v", redSummary, err)
	}
	statsData, err := os.ReadFile(filepath.Join(bundle, "stats", "code.json"))
	var stats reviewRunStats
	if err != nil || json.Unmarshal(statsData, &stats) != nil || stats.InputTokens == nil || *stats.InputTokens != 100 {
		t.Fatalf("custom launcher usage was lost: %s %v", statsData, err)
	}
	summary, err := os.ReadFile(filepath.Join(bundle, "summary.md"))
	if err != nil || !strings.Contains(string(summary), "| code | test: |") || !strings.Contains(string(summary), "| lead | test:sample-model |") || !strings.Contains(string(summary), "100 / 10 (20)") || !strings.Contains(string(summary), "1.2k / 50 (200)") || !strings.Contains(string(summary), "1.3k / 60 (220)") || !strings.Contains(string(summary), "$0.03") {
		t.Fatalf("run statistics table missing: %s %v", summary, err)
	}
	if !strings.Contains(string(summary), "Scope checkpoint:") || strings.Contains(string(summary), "Design checkpoint:") {
		t.Fatalf("checkpoint observations disagree with findings: %s", summary)
	}
	skipped, err := os.ReadFile(filepath.Join(bundle, "findings", "compat.json"))
	if err != nil || !strings.Contains(string(skipped), `"skipped"`) {
		t.Fatalf("downstream concern was not skipped: %s %v", skipped, err)
	}
	t.Setenv("FAIL_CONCERN", "code")
	got, err = c.RunReviewBundle(bundle, opt)
	if err != nil || got.Verdict != "error" {
		t.Fatalf("failed concern could pass: %+v %v", got, err)
	}
	var file reviewFindingFile
	data, err := os.ReadFile(filepath.Join(bundle, "findings", "code.json"))
	if err != nil || json.Unmarshal(data, &file) != nil || file.Status != "error" {
		t.Fatalf("failed concern result: %s %v", data, err)
	}
	summary, err = os.ReadFile(filepath.Join(bundle, "summary.md"))
	if err != nil || strings.Count(string(summary), "wtc-review v1 head=") != 1 {
		t.Fatalf("status line missing or duplicated: %s %v", summary, err)
	}
	t.Setenv("FAIL_CONCERN", "")
	t.Setenv("BLOCKER_CONCERN", "code")
	got, err = c.RunReviewBundle(bundle, opt)
	if err != nil || got.Verdict != "changes-requested" || got.Blockers != 1 {
		t.Fatalf("open blocker did not close gate: %+v %v", got, err)
	}
	summary, err = os.ReadFile(filepath.Join(bundle, "summary.md"))
	if err != nil || !strings.Contains(string(summary), "**Local review: changes-requested**") || strings.Contains(string(summary), "**Local review: pass**") {
		t.Fatalf("runner verdict and summary heading disagree: %s %v", summary, err)
	}
	if !strings.Contains(string(summary), "Design checkpoint: major findings remain in code") {
		t.Fatalf("runner omitted the repeated-major design question: %s", summary)
	}
	stale := filepath.Join(bundle, "stats", "unselected.json")
	if err := os.WriteFile(stale, []byte(`{"agent":"old","input_tokens":99999,"status":"ok"}`), 0644); err != nil {
		t.Fatal(err)
	}
	opt.Only = []string{"code"}
	if _, err := c.RunReviewBundle(bundle, opt); err != nil {
		t.Fatal(err)
	}
	summary, err = os.ReadFile(filepath.Join(bundle, "summary.md"))
	if err != nil || strings.Contains(string(summary), "unselected") || strings.Contains(string(summary), "99999") {
		t.Fatalf("selected run retained prior statistics: %s %v", summary, err)
	}
	if err := os.Remove(filepath.Join(bundle, "findings", "compat.json")); err != nil {
		t.Fatal(err)
	}
	got, err = c.RunReviewBundle(bundle, opt)
	if err != nil || got.Verdict != "error" {
		t.Fatalf("missing unselected concern opened gate: %+v %v", got, err)
	}
}

func TestReviewStatsTableUsesWallAndAgentTime(t *testing.T) {
	bundle := t.TempDir()
	if err := os.Mkdir(filepath.Join(bundle, "stats"), 0755); err != nil {
		t.Fatal(err)
	}
	in, out, read := int64(10), int64(2), int64(0)
	cost := 0.01
	for name, stats := range map[string]reviewRunStats{
		"code": {Agent: "test", Seconds: 70, Status: "ok", InputTokens: &in, OutputTokens: &out, CacheReadTokens: &read, CostUSD: &cost, CostPartial: true},
		"lead": {Agent: "test", Seconds: 65, Status: "ok"},
	} {
		if err := writeReviewStats(bundle, name, stats); err != nil {
			t.Fatal(err)
		}
	}
	table, err := reviewStatsTable(bundle, 80*time.Second)
	if err != nil || !strings.Contains(table, "| code | test: | 1m10s | 10 / 2 (0) | $0.01+ |") || !strings.Contains(table, "| **Total** | | 2m15s | 10+ / 2+ (0+) | $0.01+ |") || !strings.Contains(table, "Wall-clock for the whole run: 1m20s (agent time summed: 2m15s).") {
		t.Fatalf("wrong time totals: %s %v", table, err)
	}
	if err := os.Remove(filepath.Join(bundle, "stats", "code.json")); err != nil {
		t.Fatal(err)
	}
	table, err = reviewStatsTable(bundle, 80*time.Second)
	if err != nil || !strings.Contains(table, "| **Total** | | 1m05s | - / - (-) | - |") {
		t.Fatalf("unknown usage displayed as zero: %s %v", table, err)
	}
}

func TestReviewFallbackAggregatesCurrentAttempts(t *testing.T) {
	bundle := t.TempDir()
	prompt := filepath.Join(bundle, "prompt.md")
	if err := os.WriteFile(prompt, []byte("Review"), 0644); err != nil {
		t.Fatal(err)
	}
	launcher := filepath.Join(bundle, "agent.sh")
	script := `#!/bin/sh
if [ "$1" = first ]; then
  printf '{"input_tokens":100,"output_tokens":10,"cache_read_tokens":0,"cost_usd":0.02}\n' > "$WTC_REVIEW_STATS_FILE"
  printf 'usage limit\n'
  exit 1
fi
printf '{"input_tokens":20,"output_tokens":2,"cache_read_tokens":0,"cost_usd":0.01}\n' > "$WTC_REVIEW_STATS_FILE"
printf 'complete\n'
`
	if err := os.WriteFile(launcher, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WTC_REVIEW_AGENT_CMD", launcher)
	statsPath := filepath.Join(bundle, "stats.json")
	agent, _, output, stats, err := runReviewChain(reviewRunDirs{bundle: bundle, runner: bundle, repo: bundle}, prompt, "first: second:", time.Minute, statsPath)
	if err != nil || agent != "second" || !strings.Contains(string(output), "complete") {
		t.Fatalf("fallback failed: %s %s %v", agent, output, err)
	}
	if stats.InputTokens == nil || *stats.InputTokens != 120 || stats.CostUSD == nil || *stats.CostUSD < 0.029 || *stats.CostUSD > 0.031 || stats.Agent != "second" || stats.UsagePartial || stats.CostPartial {
		t.Fatalf("fallback did not aggregate current attempts: %+v", stats)
	}
}

func TestReviewQuotaFallbackDiscardsIncompleteAttemptOutput(t *testing.T) {
	bundle := t.TempDir()
	if err := os.Mkdir(filepath.Join(bundle, "findings"), 0755); err != nil {
		t.Fatal(err)
	}
	prompt := filepath.Join(bundle, "code.md")
	if err := os.WriteFile(prompt, []byte("review"), 0644); err != nil {
		t.Fatal(err)
	}
	launcher := filepath.Join(bundle, "agent.sh")
	script := `#!/bin/sh
if [ "$1" = first ]; then
  echo '{"concern":"code","status":"ok","findings":[]}' > "$4/findings/code.json"
  echo '{"type":"error","message":"usage limit exceeded"}'
  exit 0
fi
echo '{"type":"turn.completed"}'
`
	if err := os.WriteFile(launcher, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WTC_REVIEW_AGENT_CMD", launcher)
	agent, _, _, _, err := runReviewChain(reviewRunDirs{bundle: bundle, runner: bundle, repo: bundle}, prompt, "first: second:", time.Minute, filepath.Join(bundle, "stats.json"))
	if err != nil || agent != "second" {
		t.Fatalf("structured limit did not fallback: %s %v", agent, err)
	}
	if _, err := os.Stat(filepath.Join(bundle, "findings", "code.json")); !os.IsNotExist(err) {
		t.Fatalf("limited agent's output survived fallback: %v", err)
	}
	for _, tc := range []struct {
		output  string
		limited bool
	}{
		{`{"is_error":true,"result":"quota exceeded"}`, true},
		{`{"type":"error","message":"rate limit"}`, true},
		{`{"error":{"message":"session limit"}}`, true},
		{"usage limit\n", true},
		{`{"type":"result","result":"Fix the quota calculation"}`, false},
		{`{"type":"agent_message","text":"A usage limit test is missing"}`, false},
	} {
		if got := reviewAgentLimited([]byte(tc.output), nil); got != tc.limited {
			t.Fatalf("limit classification %q = %t", tc.output, got)
		}
	}
}

func TestDefaultCommentsConcernAppliesToCodeAndConfig(t *testing.T) {
	body, err := ReadDefault("review/concerns/comments.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"src/app.go", "lib/app.rb", "web/app.tsx", "hosts/Program.cs", "scripts/run.sh", "Dockerfile", "config/service.yml"} {
		if !reviewConcernApplies(string(body), path+"\n") {
			t.Fatalf("comments concern missed %s", path)
		}
	}
	if reviewConcernApplies(string(body), "README.md\ndocs/guide.md\n") {
		t.Fatal("code comments review ran for prose-only diff")
	}
}

func TestReviewStatsParseBuiltinUsage(t *testing.T) {
	t.Setenv("WTC_REVIEW_AGENT_CMD", "")
	for _, tc := range []struct {
		agent, output               string
		input, outputTokens, cached int64
	}{
		{"codex", "{\"type\":\"turn.completed\",\"usage\":{\"input_tokens\":300,\"cached_input_tokens\":100,\"output_tokens\":20}}\n{\"type\":\"turn.completed\",\"usage\":{\"input_tokens\":70,\"cached_input_tokens\":10,\"output_tokens\":5}}\n", 260, 25, 110},
		{"claude", "{\"type\":\"result\",\"usage\":{\"input_tokens\":12,\"output_tokens\":3,\"cache_read_input_tokens\":4},\"total_cost_usd\":0.05,\"num_turns\":2}", 12, 3, 4},
		{"grok", "{\"text\":\"done\",\"usage\":{\"input_tokens\":9,\"output_tokens\":2,\"cache_read_input_tokens\":1}}", 9, 2, 1},
	} {
		t.Run(tc.agent, func(t *testing.T) {
			stats := collectReviewStats(filepath.Join(t.TempDir(), "stats.json"), tc.agent, "", 0, "ok", []byte(tc.output))
			if stats.InputTokens == nil || *stats.InputTokens != tc.input || stats.OutputTokens == nil || *stats.OutputTokens != tc.outputTokens || stats.CacheReadTokens == nil || *stats.CacheReadTokens != tc.cached {
				t.Fatalf("wrong %s usage: %+v", tc.agent, stats)
			}
			if tc.agent == "claude" && (stats.CostUSD == nil || *stats.CostUSD != 0.05 || stats.Turns == nil || *stats.Turns != 2) {
				t.Fatalf("Claude cost or turns missing: %+v", stats)
			}
		})
	}
}
