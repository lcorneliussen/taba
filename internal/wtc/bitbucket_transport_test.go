package wtc

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const twgFixtureDir = "testdata/twg-bitbucket"

func twgFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(twgFixtureDir, name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// fakeTWG installs a synthetic `twg` on PATH. It answers --version with
// version, records every argv and the agent opt-in to calls, and answers a
// pull-request get with the fixture named by the PR number in fixtures.
// A fixture value of "exit:N" prints nothing and exits N.
func fakeTWG(t *testing.T, version string, fixtures map[string]string) (calls string) {
	t.Helper()
	bin := t.TempDir()
	calls = filepath.Join(bin, "calls")
	script := "#!/bin/sh\n" +
		"printf '%s|' \"TWG_AGENT_DEFAULTS=$TWG_AGENT_DEFAULTS\" \"$@\" >> '" + calls + "'\necho >> '" + calls + "'\n" +
		"if [ \"$1\" = --version ]; then echo '" + version + "'; exit 0; fi\n" +
		"case \"$4\" in\n"
	for number, fixture := range fixtures {
		if code, ok := strings.CutPrefix(fixture, "exit:"); ok {
			script += "  " + number + ") exit " + code + ";;\n"
			continue
		}
		path, err := filepath.Abs(filepath.Join(twgFixtureDir, fixture))
		if err != nil {
			t.Fatal(err)
		}
		status := "0"
		if strings.HasPrefix(fixture, "failure-") || strings.Contains(fixture, "partial") || strings.Contains(fixture, "denied") {
			status = "1"
		}
		script += "  " + number + ") cat '" + path + "'; exit " + status + ";;\n"
	}
	script += "esac\nexit 64\n"
	fixtureFile(t, filepath.Join(bin, "twg"), script, 0755)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	twgVersionChecks.Range(func(key, _ any) bool { twgVersionChecks.Delete(key); return true })
	return calls
}

func readCalls(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

func TestBitbucketClientSelection(t *testing.T) {
	for _, tc := range []struct {
		taba, wtc string
		want      bitbucketClient
		err       bool
	}{
		{"", "", bitbucketClientBB, false},
		{"bb", "", bitbucketClientBB, false},
		{"TWG", "", bitbucketClientTWG, false},
		{" twg ", "", bitbucketClientTWG, false},
		{"", "twg", bitbucketClientTWG, false},
		{"bb", "twg", bitbucketClientBB, false},
		{"rest", "", "", true},
		{"twg,bb", "", "", true},
	} {
		for _, key := range []string{"TABA_BITBUCKET_TRANSPORT", "WTC_BITBUCKET_TRANSPORT"} {
			t.Setenv(key, "")
			os.Unsetenv(key)
		}
		if tc.taba != "" {
			t.Setenv("TABA_BITBUCKET_TRANSPORT", tc.taba)
		}
		if tc.wtc != "" {
			t.Setenv("WTC_BITBUCKET_TRANSPORT", tc.wtc)
		}
		got, err := selectedBitbucketClient()
		if (err != nil) != tc.err || got != tc.want {
			t.Errorf("selection %q/%q = %q, %v", tc.taba, tc.wtc, got, err)
		}
	}
}

func TestTWGVersionSupport(t *testing.T) {
	for version, want := range map[string]bool{
		"1.4.4": true, "1.4.4\n": true, "v1.4.5": true, "1.5.0": true, "1.10.0": true, "1.4.5-beta.1": true, "1.4.4+build.7": true,
		"1.4.4-beta.1": false, "1.4.4-rc.1+build": false, "1.4.3": false, "1.3.9": false, "0.9.0": false, "2.0.0": false, "": false, "twg 1.4.4": false, "1.4": false,
	} {
		if got := twgVersionSupported(version); got != want {
			t.Errorf("twgVersionSupported(%q) = %v, want %v", version, got, want)
		}
	}
}

// TestTWGReaderContract runs the reader against every synthetic contract
// fixture: a usable answer must be complete and pinned to the requested
// repository and PR, and everything else must be a classified failure.
func TestTWGReaderContract(t *testing.T) {
	exitOne := &exec.ExitError{ProcessState: lookupExitState(t)}
	for _, tc := range []struct {
		fixture string
		exit    error
		want    bitbucketPR
		cause   string
	}{
		{fixture: "pr-open.json", want: bitbucketPR{State: "OPEN", Destination: "main", Head: "1111111111111111111111111111111111111111"}},
		{fixture: "pr-draft.json", want: bitbucketPR{State: "DRAFT", Destination: "main", Head: "1111111111111111111111111111111111111111"}},
		{fixture: "pr-merged.json", want: bitbucketPR{State: "MERGED", Destination: "main", Head: "1111111111111111111111111111111111111111", MergeCommit: "3333333333333333333333333333333333333333"}},
		{fixture: "pr-declined.json", want: bitbucketPR{State: "DECLINED", Destination: "main", Head: "1111111111111111111111111111111111111111"}},
		{fixture: "batch-one-ok.json", want: bitbucketPR{State: "OPEN", Destination: "main", Head: "1111111111111111111111111111111111111111"}},
		{fixture: "batch-partial.json", exit: exitOne, cause: bitbucketCauseIncomplete},
		{fixture: "batch-item-denied.json", exit: exitOne, cause: bitbucketCauseAuth},
		{fixture: "batch-empty.json", cause: bitbucketCauseIncomplete},
		{fixture: "pr-hydration-failure.json", cause: bitbucketCauseIncomplete},
		{fixture: "pr-other-repo.json", cause: bitbucketCauseIdentity},
		{fixture: "pr-other-id.json", cause: bitbucketCauseIdentity},
		{fixture: "pr-no-repository.json", cause: bitbucketCauseMissingField},
		{fixture: "pr-open-no-destination.json", cause: bitbucketCauseMissingField},
		{fixture: "pr-bad-state.json", cause: bitbucketCauseState},
		{fixture: "failure-command.json", exit: exitOne, cause: "TWG reported error TWG_COMMAND_FAILED"},
		{fixture: "failure-denied.json", exit: exitOne, cause: bitbucketCauseAuth},
		// A failure envelope is a failure even if the process claims success.
		{fixture: "failure-denied.json", cause: bitbucketCauseAuth},
		{fixture: "failure-hostile-code.json", exit: exitOne, cause: "TWG reported error unrecognized"},
	} {
		t.Run(tc.fixture, func(t *testing.T) {
			body := twgFixture(t, tc.fixture)
			reader := twgBitbucketReader{run: func(env []string, args ...string) ([]byte, error) {
				if len(args) == 2 && args[1] == "--version" {
					return []byte("1.4.4\n"), nil
				}
				return body, tc.exit
			}}
			twgVersionChecks.Range(func(key, _ any) bool { twgVersionChecks.Delete(key); return true })
			t.Setenv("PATH", fakeTWGDir(t)+":"+os.Getenv("PATH"))
			got, err := reader.PullRequest("example", "agent-harness", "7")
			if tc.cause == "" {
				if err != nil || got != tc.want {
					t.Fatalf("got %+v, %v; want %+v", got, err, tc.want)
				}
				return
			}
			var classified *bitbucketReadError
			if !errors.As(err, &classified) || !strings.HasPrefix(classified.cause, tc.cause) {
				t.Fatalf("got %+v, %v; want cause %q", got, err, tc.cause)
			}
			if got != (bitbucketPR{}) {
				t.Fatalf("failure returned facts: %+v", got)
			}
			for _, forbidden := range []string{"secret-value", "private.invalid", "\x1b"} {
				if strings.Contains(classified.cause, forbidden) {
					t.Fatalf("cause echoes response content: %q", classified.cause)
				}
			}
		})
	}
}

// fakeTWGDir provides a `twg` that exists for exec.LookPath only.
func fakeTWGDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	fixtureFile(t, filepath.Join(dir, "twg"), "#!/bin/sh\nexit 99\n", 0755)
	return dir
}

func TestTWGReaderInvocationIsExplicitAndBounded(t *testing.T) {
	calls := fakeTWG(t, "1.4.4", map[string]string{"7": "pr-open.json", "8": "pr-open.json"})
	reader := newBitbucketPRReader(bitbucketClientTWG, catchUpClientRun)
	if _, err := reader.PullRequest("example", "agent-harness", "7"); err != nil {
		t.Fatal(err)
	}
	// A second read in the same process does not ask for the version again;
	// it fails identity pinning because the fixture is PR 7.
	if _, err := reader.PullRequest("example", "agent-harness", "8"); err == nil {
		t.Fatal("PR 8 accepted a PR 7 response")
	}
	lines := readCalls(t, calls)
	if len(lines) != 3 || !strings.Contains(lines[0], "|--version|") {
		t.Fatalf("expected one version check and two reads: %q", lines)
	}
	want := "TWG_AGENT_DEFAULTS=1|bitbucket|pull-requests|get|7|--workspace|example|--repo|agent-harness|--strict|--output|json|--output-summary|none|"
	if lines[1] != want {
		t.Fatalf("argv = %q\nwant  %q", lines[1], want)
	}
	for _, forbidden := range []string{"--scope", "me", "--comments", "--statuses", "--full", "--diff", "--profile", "--site"} {
		if strings.Contains(lines[1], "|"+forbidden+"|") {
			t.Fatalf("read widened its scope or hydration with %s: %q", forbidden, lines[1])
		}
	}
	if os.Getenv("TWG_AGENT_DEFAULTS") != "" {
		t.Fatal("the agent opt-in leaked into this process's environment")
	}
}

func TestTWGReaderRefusesUnsupportedVersionWithoutReading(t *testing.T) {
	for _, version := range []string{"1.4.3", "2.0.0", "not-a-version"} {
		calls := fakeTWG(t, version, map[string]string{"7": "pr-open.json"})
		_, err := newBitbucketPRReader(bitbucketClientTWG, catchUpClientRun).PullRequest("example", "agent-harness", "7")
		var classified *bitbucketReadError
		if !errors.As(err, &classified) || !strings.HasPrefix(classified.cause, "unsupported TWG version") {
			t.Fatalf("%s: %v", version, err)
		}
		if lines := readCalls(t, calls); len(lines) != 1 {
			t.Fatalf("%s: read ran after an unsupported version: %q", version, lines)
		}
	}
}

func TestTWGReaderMissingCLIAndTimeout(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	twgVersionChecks.Range(func(key, _ any) bool { twgVersionChecks.Delete(key); return true })
	_, err := newBitbucketPRReader(bitbucketClientTWG, catchUpClientRun).PullRequest("example", "agent-harness", "7")
	if !errors.Is(err, exec.ErrNotFound) || !strings.Contains(catchUpLookupCause(err), "CLI not installed") {
		t.Fatalf("missing CLI: %v", err)
	}

	bin := t.TempDir()
	fixtureFile(t, filepath.Join(bin, "twg"), "#!/bin/sh\n[ \"$1\" = --version ] && { echo 1.4.4; exit 0; }\nexec sleep 5\n", 0755)
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	old := catchUpForgeTimeout
	catchUpForgeTimeout = 100 * time.Millisecond
	t.Cleanup(func() { catchUpForgeTimeout = old })
	started := time.Now()
	_, err = newBitbucketPRReader(bitbucketClientTWG, catchUpClientRun).PullRequest("example", "agent-harness", "7")
	if err == nil || !strings.Contains(catchUpLookupCause(err), "lookup timed out") {
		t.Fatalf("timeout: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("deadline did not stop the owned process: %s", elapsed)
	}
	if strings.Contains(err.Error(), bin) {
		t.Fatalf("timeout diagnostic names a local path: %v", err)
	}
}

// catchUpBitbucketFixture prepares a collection whose harness worktree is on
// branch topic with PR 7 enlisted against a Bitbucket origin, plus fake bb
// and twg clients that record whether they ran.
func catchUpBitbucketFixture(t *testing.T, twgFixture string) (c *Context, bbCalls, twgCalls string) {
	t.Helper()
	c = newWorkspaceFixture(t)
	fixtureGit(t, "-C", c.Harness, "switch", "-qc", "topic")
	owner := filepath.Join(c.Workspace, ".bare", "agent-harness.git")
	fixtureGit(t, "--git-dir="+owner, "remote", "set-url", "origin", "git@bitbucket.org:example/agent-harness.git")
	fixtureFile(t, filepath.Join(c.Collection, ".wtc-prs"), "agent-harness 7 topic - fixture\n", 0644)
	bin := filepath.Join(c.Workspace, "bin")
	bbCalls = filepath.Join(bin, "bb-calls")
	view, err := filepath.Abs(filepath.Join(twgFixtureDir, twgFixture))
	if err != nil {
		t.Fatal(err)
	}
	// The bb client answers with the same resource, so both clients see the
	// same synthetic PR.
	fixtureFile(t, filepath.Join(bin, "bb"), "#!/bin/sh\necho \"$@\" >> '"+bbCalls+"'\ncat '"+view+"'\n", 0755)
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	twgCalls = fakeTWG(t, "1.4.4", map[string]string{"7": twgFixture})
	return c, bbCalls, twgCalls
}

func catchUpRepoRow(t *testing.T, report CatchUpReport) CatchUpRow {
	t.Helper()
	for _, row := range report.Outcomes {
		if row.Kind == "repo" {
			return row
		}
	}
	t.Fatal("missing repo outcome")
	return CatchUpRow{}
}

// TestCatchUpTWGMatchesBBForTheSamePullRequest runs the real catch-up with
// each client against the same synthetic PR and requires the same public
// outcome.
func TestCatchUpTWGMatchesBBForTheSamePullRequest(t *testing.T) {
	for _, fixture := range []string{"pr-open.json", "pr-draft.json", "pr-merged.json", "pr-declined.json"} {
		t.Run(fixture, func(t *testing.T) {
			rows := map[string]string{}
			for _, client := range []string{"bb", "twg"} {
				c, bbCalls, twgCalls := catchUpBitbucketFixture(t, fixture)
				t.Setenv("TABA_BITBUCKET_TRANSPORT", client)
				report, _ := c.CatchUp(CatchUpOptions{Repos: []string{"harness"}, DryRun: true, NoSkills: true, NoMCP: true, NoEnv: true, NoSecrets: true})
				row := catchUpRepoRow(t, report)
				if strings.Contains(row.Reason, "lookup") || row.Outcome == "needs-owner" && strings.Contains(row.Reason, "failed") {
					t.Fatalf("%s: lookup failed: %+v", client, row)
				}
				encoded, _ := json.Marshal(struct {
					Outcome, Reason, NextActor, NextAction string
				}{row.Outcome, row.Reason, row.NextActor, strings.ReplaceAll(row.NextAction, row.CollectionPath, "<collection>")})
				rows[client] = string(encoded)
				usedBB, usedTWG := len(readCalls(t, bbCalls)) > 0, len(readCalls(t, twgCalls)) > 0
				if usedBB != (client == "bb") || usedTWG != (client == "twg") {
					t.Fatalf("%s selected, but bb ran=%v twg ran=%v", client, usedBB, usedTWG)
				}
			}
			if rows["bb"] != rows["twg"] {
				t.Fatalf("clients disagree:\nbb  %s\ntwg %s", rows["bb"], rows["twg"])
			}
		})
	}
}

// TestCatchUpTWGFailureNeverFallsBackOrMoves checks that every TWG failure
// leaves the branch alone with the public failure shape, and that bb is
// never asked instead.
func TestCatchUpTWGFailureNeverFallsBackOrMoves(t *testing.T) {
	for _, tc := range []struct{ fixture, want string }{
		{"failure-denied.json", "PR #7: twg bitbucket pull-requests get failed: authentication or authorization failed"},
		{"failure-command.json", "twg bitbucket pull-requests get failed: TWG reported error TWG_COMMAND_FAILED"},
		{"batch-partial.json", "twg bitbucket pull-requests get failed: incomplete or partial response"},
		{"pr-other-repo.json", "twg bitbucket pull-requests get failed: response does not match the requested repository or PR"},
		{"pr-hydration-failure.json", "incomplete or partial response"},
		{"pr-bad-state.json", "unrecognized PR state"},
		{"failure-hostile-code.json", "TWG reported error unrecognized"},
		{"batch-envelope-failures.json", "twg bitbucket pull-requests get failed: incomplete or partial response"},
		{"batch-malformed-partial.json", "twg bitbucket pull-requests get failed: malformed or conflicting completeness metadata"},
	} {
		t.Run(tc.fixture, func(t *testing.T) {
			c, bbCalls, _ := catchUpBitbucketFixture(t, tc.fixture)
			t.Setenv("TABA_BITBUCKET_TRANSPORT", "twg")
			before := fixtureGit(t, "-C", c.Harness, "rev-parse", "HEAD")
			report, err := c.CatchUp(CatchUpOptions{Repos: []string{"harness"}, DryRun: true, NoSkills: true, NoMCP: true, NoEnv: true, NoSecrets: true})
			if err == nil || report.ExitStatus != 1 {
				t.Fatalf("failure hidden: %+v %v", report, err)
			}
			row := catchUpRepoRow(t, report)
			if row.Outcome != "needs-owner" || !strings.Contains(row.Reason, tc.want) || !strings.Contains(row.Reason, "branch left untouched") || row.NextAction == "" {
				t.Fatalf("diagnostic: %+v; want %q", row, tc.want)
			}
			if lines := readCalls(t, bbCalls); len(lines) != 0 {
				t.Fatalf("TWG failure fell back to bb: %q", lines)
			}
			data, _ := json.Marshal(report)
			for _, forbidden := range []string{"secret-value", "private.invalid", "\\u001b"} {
				if strings.Contains(string(data), forbidden) || strings.Contains(report.Markdown(), forbidden) {
					t.Fatalf("report echoes response content %q", forbidden)
				}
			}
			if after := fixtureGit(t, "-C", c.Harness, "rev-parse", "HEAD"); after != before {
				t.Fatal("failure moved the branch")
			}
		})
	}
}

func TestCatchUpRejectsUnknownBitbucketClientSelection(t *testing.T) {
	c, bbCalls, twgCalls := catchUpBitbucketFixture(t, "pr-open.json")
	t.Setenv("TABA_BITBUCKET_TRANSPORT", "rest")
	report, err := c.CatchUp(CatchUpOptions{Repos: []string{"harness"}, DryRun: true, NoSkills: true, NoMCP: true, NoEnv: true, NoSecrets: true})
	if err == nil || report.ExitStatus != 1 {
		t.Fatalf("invalid selection hidden: %+v %v", report, err)
	}
	row := catchUpRepoRow(t, report)
	if row.Outcome != "needs-owner" || !strings.Contains(row.Reason, "unsupported TABA_BITBUCKET_TRANSPORT value") || strings.Contains(row.Reason, "rest") {
		t.Fatalf("diagnostic: %+v", row)
	}
	if len(readCalls(t, bbCalls))+len(readCalls(t, twgCalls)) != 0 {
		t.Fatal("a client ran despite an invalid selection")
	}
}

// TestCatchUpGitHubIgnoresBitbucketSelection keeps the selection scoped to
// Bitbucket: a GitHub origin never consults it, even when it is invalid.
func TestCatchUpGitHubIgnoresBitbucketSelection(t *testing.T) {
	c := newWorkspaceFixture(t)
	fixtureGit(t, "-C", c.Harness, "switch", "-qc", "topic")
	owner := filepath.Join(c.Workspace, ".bare", "agent-harness.git")
	fixtureGit(t, "--git-dir="+owner, "remote", "set-url", "origin", "git@github.com:example/agent-harness.git")
	fixtureFile(t, filepath.Join(c.Collection, ".wtc-prs"), "agent-harness 7 topic - fixture\n", 0644)
	bin := filepath.Join(c.Workspace, "bin")
	fixtureFile(t, filepath.Join(bin, "gh"), "#!/bin/sh\necho '{\"state\":\"OPEN\",\"isDraft\":false,\"baseRefName\":\"main\"}'\n", 0755)
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	t.Setenv("TABA_BITBUCKET_TRANSPORT", "rest")
	target := catchUpTarget{collection: "main", path: c.Harness, repo: "agent-harness", harness: true}
	if got := c.catchUpPRState(target, "topic"); got.state != "OPEN" || got.base != "main" || got.lookupFailure != "" {
		t.Fatalf("GitHub lookup consulted the Bitbucket selection: %+v", got)
	}
}

// TestTWGReaderNonZeroExitIsNeverAnAnswer keeps a complete-looking resource
// from counting when TWG itself reported failure through its exit status.
func TestTWGReaderNonZeroExitIsNeverAnAnswer(t *testing.T) {
	exit := &exec.ExitError{ProcessState: lookupExitState(t)}
	t.Setenv("PATH", fakeTWGDir(t)+":"+os.Getenv("PATH"))
	for _, fixture := range []string{"pr-open.json", "batch-one-ok.json"} {
		twgVersionChecks.Range(func(key, _ any) bool { twgVersionChecks.Delete(key); return true })
		body := twgFixture(t, fixture)
		reader := twgBitbucketReader{run: func(env []string, args ...string) ([]byte, error) {
			if len(args) == 2 && args[1] == "--version" {
				return []byte("1.4.4"), nil
			}
			return body, exit
		}}
		got, err := reader.PullRequest("example", "agent-harness", "7")
		if err == nil || got != (bitbucketPR{}) {
			t.Fatalf("%s: non-zero exit produced %+v, %v", fixture, got, err)
		}
	}
}

// TestTWGCompletenessMetadataAtEveryLevel varies the completeness metadata of
// a real-shaped one-item batch at the envelope, item and resource level. Only
// explicitly complete forms are usable; everything else is unavailable.
func TestTWGCompletenessMetadataAtEveryLevel(t *testing.T) {
	base := func(t *testing.T) map[string]any {
		var envelope map[string]any
		if err := json.Unmarshal(twgFixture(t, "batch-one-ok.json"), &envelope); err != nil {
			t.Fatal(err)
		}
		return envelope
	}
	item := func(e map[string]any) map[string]any { return e["items"].([]any)[0].(map[string]any) }
	data := func(e map[string]any) map[string]any { return item(e)["data"].(map[string]any) }
	failure := []any{map[string]any{"source": "pull-request", "operation": "get", "message": "synthetic", "statusCode": 502}}
	for _, tc := range []struct {
		name   string
		mutate func(map[string]any)
		cause  string // empty: usable
	}{
		{"complete", func(map[string]any) {}, ""},
		{"envelope failures empty", func(e map[string]any) { e["_failures"] = []any{} }, ""},
		{"envelope failures null", func(e map[string]any) { e["_failures"] = nil }, ""},
		{"envelope ok true", func(e map[string]any) { e["ok"] = true }, ""},
		{"envelope error null", func(e map[string]any) { e["error"] = nil }, ""},
		{"envelope failures nonempty", func(e map[string]any) { e["_failures"] = failure }, bitbucketCauseIncomplete},
		{"envelope failures object", func(e map[string]any) { e["_failures"] = map[string]any{} }, bitbucketCauseMalformed},
		{"envelope failures string", func(e map[string]any) { e["_failures"] = "none" }, bitbucketCauseMalformed},
		{"envelope partial true", func(e map[string]any) { e["partial"] = true }, bitbucketCauseIncomplete},
		{"envelope partial string", func(e map[string]any) { e["partial"] = "false" }, bitbucketCauseMalformed},
		{"envelope partial null", func(e map[string]any) { e["partial"] = nil }, bitbucketCauseMalformed},
		{"envelope ok string", func(e map[string]any) { e["ok"] = "true" }, bitbucketCauseMalformed},
		{"envelope error beside success", func(e map[string]any) { e["error"] = map[string]any{"code": "X"} }, bitbucketCauseMalformed},
		{"items null", func(e map[string]any) { e["items"] = nil }, bitbucketCauseMalformed},
		{"items object", func(e map[string]any) { e["items"] = map[string]any{} }, bitbucketCauseMalformed},
		{"item not object", func(e map[string]any) { e["items"] = []any{"7"} }, bitbucketCauseMalformed},
		{"item ok missing", func(e map[string]any) { delete(item(e), "ok") }, bitbucketCauseIncomplete},
		{"item ok string", func(e map[string]any) { item(e)["ok"] = "true" }, bitbucketCauseMalformed},
		{"item ok null", func(e map[string]any) { item(e)["ok"] = nil }, bitbucketCauseMalformed},
		{"item error beside ok", func(e map[string]any) { item(e)["error"] = map[string]any{"code": "X"} }, bitbucketCauseMalformed},
		{"item failures nonempty", func(e map[string]any) { item(e)["_failures"] = failure }, bitbucketCauseIncomplete},
		{"item failures wrong type", func(e map[string]any) { item(e)["_failures"] = 1 }, bitbucketCauseMalformed},
		{"item partial true", func(e map[string]any) { item(e)["partial"] = true }, bitbucketCauseIncomplete},
		{"item data missing", func(e map[string]any) { delete(item(e), "data") }, bitbucketCauseIncomplete},
		{"item data null", func(e map[string]any) { item(e)["data"] = nil }, bitbucketCauseIncomplete},
		{"item data array", func(e map[string]any) { item(e)["data"] = []any{} }, bitbucketCauseIncomplete},
		{"resource failures nonempty", func(e map[string]any) { data(e)["_failures"] = failure }, bitbucketCauseIncomplete},
		{"resource failures wrong type", func(e map[string]any) { data(e)["_failures"] = "x" }, bitbucketCauseMalformed},
		{"resource partial true", func(e map[string]any) { data(e)["partial"] = true }, bitbucketCauseIncomplete},
		{"resource error", func(e map[string]any) { data(e)["error"] = map[string]any{"message": "x"} }, bitbucketCauseMalformed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			envelope := base(t)
			tc.mutate(envelope)
			body, err := json.Marshal(envelope)
			if err != nil {
				t.Fatal(err)
			}
			for _, shape := range []string{"batch", "exit"} {
				exit := error(nil)
				if shape == "exit" {
					if tc.cause == "" {
						continue
					}
					exit = &exec.ExitError{ProcessState: lookupExitState(t)}
				}
				reader := twgBitbucketReader{run: func(env []string, args ...string) ([]byte, error) {
					if len(args) == 2 && args[1] == "--version" {
						return []byte("1.4.4"), nil
					}
					return body, exit
				}}
				twgVersionChecks.Range(func(key, _ any) bool { twgVersionChecks.Delete(key); return true })
				t.Setenv("PATH", fakeTWGDir(t)+":"+os.Getenv("PATH"))
				got, err := reader.PullRequest("example", "agent-harness", "7")
				if tc.cause == "" {
					if err != nil || got.State != "OPEN" {
						t.Fatalf("complete form refused: %+v %v", got, err)
					}
					continue
				}
				var classified *bitbucketReadError
				if got != (bitbucketPR{}) || err == nil {
					t.Fatalf("%s: incomplete form usable: %+v", shape, got)
				}
				if shape == "batch" && (!errors.As(err, &classified) || classified.cause != tc.cause) {
					t.Fatalf("cause = %v, want %q", err, tc.cause)
				}
			}
		})
	}
	// The bare resource form carries the same metadata at its top level.
	for name, mutate := range map[string]func(map[string]any){
		"bare failures":       func(r map[string]any) { r["_failures"] = []any{map[string]any{"source": "statuses"}} },
		"bare partial":        func(r map[string]any) { r["partial"] = true },
		"bare partial string": func(r map[string]any) { r["partial"] = "no" },
		"bare error":          func(r map[string]any) { r["error"] = map[string]any{"message": "x"} },
	} {
		var resource map[string]any
		if err := json.Unmarshal(twgFixture(t, "pr-open.json"), &resource); err != nil {
			t.Fatal(err)
		}
		mutate(resource)
		body, _ := json.Marshal(resource)
		if _, err := twgSinglePullRequest(body, "7"); err == nil {
			t.Errorf("%s: incomplete bare resource accepted", name)
		}
	}
}
