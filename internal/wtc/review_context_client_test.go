package wtc

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func resetReviewClaudeCapability(t *testing.T) {
	t.Helper()
	reset := func() {
		reviewClaudeHelp = struct {
			once sync.Once
			err  error
		}{}
	}
	reset()
	t.Cleanup(reset)
}

// fakeReviewClaude installs a claude client that prints the given help and
// otherwise records its working directory, arguments and environment.
func fakeReviewClaude(t *testing.T, help string) (record string) {
	t.Helper()
	bin := t.TempDir()
	record = filepath.Join(t.TempDir(), "claude.record")
	script := "#!/bin/sh\nif [ \"$1\" = --help ]; then printf '%s\\n' '" + help + "'; exit 0; fi\n" +
		"{ pwd -P; printf '%s\\n' \"$@\"; echo ENV; env; } > '" + record + "'\nexit 7\n"
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return record
}

func TestPublicReviewRefusesClientsWithoutContextIsolation(t *testing.T) {
	f := newReviewSentinelFixture(t)
	t.Setenv("WTC_REVIEW_AGENT_CMD", "")
	bundle := f.bundle(t)
	for _, agent := range []string{"codex", "grok"} {
		opt := reviewSentinelOptions
		opt.Strong, opt.Standard, opt.Fast, opt.Lead = "claude:opus", "claude:sonnet", agent+":", "claude:opus"
		_, err := f.c.RunReviewBundle(bundle.Dir, opt)
		if err == nil || !strings.Contains(err.Error(), "public review needs an isolated execution context") || !strings.Contains(err.Error(), "claude:<model>") {
			t.Fatalf("%s was not refused with guidance: %v", agent, err)
		}
	}
	if _, err := os.Stat(filepath.Join(bundle.Dir, "findings", "code.json")); err == nil {
		t.Fatal("a refused run still launched reviewers")
	}

	resetReviewClaudeCapability(t)
	fakeReviewClaude(t, "--safe-mode --strict-mcp-config")
	opt := reviewSentinelOptions
	opt.Strong, opt.Standard, opt.Fast, opt.Lead = "claude:opus", "claude:sonnet", "claude:haiku", "claude:opus"
	_, err := f.c.RunReviewBundle(bundle.Dir, opt)
	if err == nil || !strings.Contains(err.Error(), "--restricted") || !strings.Contains(err.Error(), "update the claude client") {
		t.Fatalf("claude without required options was not refused: %v", err)
	}
}

func TestPublicReviewClaudeLaunchContext(t *testing.T) {
	f := newReviewSentinelFixture(t)
	t.Setenv("WTC_REVIEW_AGENT_CMD", "")
	t.Setenv("ANTHROPIC_API_KEY", "synthetic-key")
	resetReviewClaudeCapability(t)
	record := fakeReviewClaude(t, strings.Join(reviewClaudeContextFlags, " "))
	bundle := f.bundle(t)
	opt := reviewSentinelOptions
	opt.Strong, opt.Standard, opt.Fast, opt.Lead = "claude:opus", "claude:sonnet", "claude:haiku", "claude:opus"
	if _, err := f.c.RunReviewBundle(bundle.Dir, opt); err == nil {
		t.Fatal("a failing client produced a passing run")
	}
	data, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	args, env, _ := strings.Cut(string(data), "\nENV\n")
	lines := strings.Split(args, "\n")
	cwd, argv := lines[0], lines[1:]
	collection, _ := filepath.EvalSymlinks(f.collection)
	if strings.HasPrefix(cwd, collection) || !strings.HasSuffix(cwd, "/bundle") {
		t.Fatalf("claude working directory: %s", cwd)
	}
	joined := strings.Join(argv, " ")
	for _, want := range []string{"--safe-mode", "--restricted", "--strict-mcp-config", "--no-session-persistence", "--permission-prompts none", "--tools Read,Grep,Glob,Write,Edit,Agent"} {
		if !strings.Contains(joined, want) {
			t.Errorf("claude launched without %s: %s", want, joined)
		}
	}
	source := ""
	for i, arg := range argv {
		if arg == "--add-dir" && i+1 < len(argv) {
			source = argv[i+1]
		}
	}
	if source == "" || strings.HasPrefix(source, collection) || !strings.Contains(joined, "Edit(/"+source+"/**)") || !strings.HasPrefix("/"+source, "//") {
		t.Fatalf("source snapshot is not denied with an absolute rule: %s", joined)
	}
	if strings.Contains(joined, "Bash") {
		t.Fatalf("public reviewer received a command tool: %s", joined)
	}
	record2, err := os.ReadFile(filepath.Join(bundle.Dir, "execution-context.json"))
	if err != nil || !strings.Contains(string(record2), `"mode": "isolated-public"`) || !strings.Contains(string(record2), "file tools only") || !strings.Contains(string(record2), "administrator-managed") {
		t.Fatalf("native provenance does not disclose its boundaries: %s %v", record2, err)
	}
	if !strings.Contains(env, "ANTHROPIC_API_KEY=synthetic-key") || strings.Contains(env, "SENTINEL_PARENT_VALUE") || strings.Contains(env, "WANTED_VALUE") {
		t.Fatalf("claude environment policy: %s", env)
	}
}

func TestPublicReviewRefusesContextBelowAgentConfiguration(t *testing.T) {
	f := newReviewSentinelFixture(t)
	if err := os.WriteFile(filepath.Join(f.tmp, "AGENTS.md"), []byte("SENTINEL\n"), 0644); err != nil {
		t.Fatal(err)
	}
	bundle := f.bundle(t)
	_, err := f.c.RunReviewBundle(bundle.Dir, reviewSentinelOptions)
	if err == nil || !strings.Contains(err.Error(), "carries agent configuration (AGENTS.md)") || !strings.Contains(err.Error(), "TMPDIR") {
		t.Fatalf("context below agent configuration was accepted: %v", err)
	}
	inside := filepath.Join(f.collection, "tmp")
	if err := os.MkdirAll(inside, 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", inside)
	_, err = f.c.RunReviewBundle(bundle.Dir, reviewSentinelOptions)
	if err == nil || !strings.Contains(err.Error(), "inside the collection") {
		t.Fatalf("context inside the collection was accepted: %v", err)
	}
}

// Private review keeps its collection context; only its read-only rule for the
// worktree is corrected. A single leading slash is project-relative in claude
// permission rules, so the rule must start with //.
func TestPrivateReviewClaudeDeniesWorktreeEditsFromRoot(t *testing.T) {
	f := newReviewSentinelFixture(t)
	t.Setenv("WTC_REVIEW_AGENT_CMD", "")
	record := fakeReviewClaude(t, "")
	concern := filepath.Join(f.collection, "harness", "review", "concerns", "code.md")
	if err := os.MkdirAll(filepath.Dir(concern), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(concern, []byte("---\nid: code\napplies: '*.go'\n---\nReview code.\n"), 0644); err != nil {
		t.Fatal(err)
	}
	bundle, err := f.c.BuildReviewBundle(ReviewBundleOptions{Repo: "app", Base: "main", Private: true, NoCatchUp: true})
	if err != nil || bundle.Manifest.Public {
		t.Fatalf("private bundle: %+v %v", bundle.Manifest, err)
	}
	opt := reviewSentinelOptions
	opt.Strong, opt.Standard, opt.Fast, opt.Lead = "claude:opus", "claude:sonnet", "claude:haiku", "claude:opus"
	_, _ = f.c.RunReviewBundle(bundle.Dir, opt)
	data, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	args, _, _ := strings.Cut(string(data), "\nENV\n")
	worktree := bundle.Manifest.RepoDir
	if !strings.Contains(args, "\nEdit(/"+worktree+"/**)\n") || strings.Contains(args, "\nEdit("+worktree+"/**)\n") || strings.Contains(args, "Write("+worktree) {
		t.Fatalf("private worktree deny rule is not root-anchored: %s", args)
	}
	if cwd, _, _ := strings.Cut(args, "\n"); !strings.HasSuffix(cwd, filepath.Base(bundle.Dir)) {
		t.Fatalf("private review moved out of its bundle: %s", cwd)
	}
}
