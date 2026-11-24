package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"taba.sh/taba/internal/retry"
)

// The reviewer stand-in: the concern starts a descendant that would write a
// finding into the bundle much later, records both pids, reports ready and
// waits. The lead, if reached, writes a passing result.
const reviewInterruptAgent = `#!/bin/sh
case "$(basename "$3")" in
 lead.md)
  echo lead >> "$REVIEW487_DIR/started"
  echo '**Local review: pass**' > "$4/summary.md"
  echo pass > "$4/verdict";;
 *)
  echo concern >> "$REVIEW487_DIR/started"
  ( sleep 300; echo '{"concern":"code","status":"ok","findings":[]}' > "$4/findings/code.json"; echo late > "$REVIEW487_DIR/late" ) &
  echo $! > "$REVIEW487_DIR/desc.pid"
  echo $$ > "$REVIEW487_DIR/child.pid"
  echo ready > "$REVIEW487_DIR/ready"
  wait;;
esac
`

// An interrupt, termination or hangup of the actual review run command stops
// its reviewers and their descendants before the command exits, for a signal
// to the command alone and for one to its whole process group as a terminal
// sends on Ctrl-C, in private and isolated public runs.
func TestReviewRunCommandSignalStopsReviewers(t *testing.T) {
	for _, tc := range []struct {
		name   string
		public bool
		sig    syscall.Signal
		group  bool
		post   bool
	}{
		{"private-int", false, syscall.SIGINT, false, false},
		{"private-term", false, syscall.SIGTERM, false, false},
		{"private-hup", false, syscall.SIGHUP, false, false},
		{"private-int-group", false, syscall.SIGINT, true, false},
		{"public-int", true, syscall.SIGINT, false, false},
		{"public-term", true, syscall.SIGTERM, false, false},
		{"public-hup", true, syscall.SIGHUP, false, false},
		{"public-int-group-post", true, syscall.SIGINT, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if signal.Ignored(tc.sig) {
				t.Skipf("%v is ignored in this test process, so the command would inherit it ignored", tc.sig)
			}
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			collection := filepath.Join(root, "sample")
			app := filepath.Join(collection, "app")
			bundle := filepath.Join(collection, "bundle")
			bin := filepath.Join(root, "bin")
			ev := filepath.Join(root, "ev")
			tmp := filepath.Join(root, "tmp")
			write := func(path, body string, mode os.FileMode) {
				t.Helper()
				if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(body), mode); err != nil {
					t.Fatal(err)
				}
			}
			for _, dir := range []string{ev, tmp} {
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
			}
			write(filepath.Join(app, "code.go"), "package app\n", 0644)
			for _, args := range [][]string{{"init", "-q"}, {"add", "."}, {"-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-q", "-m", "fixture"}} {
				if out, err := exec.Command("git", append([]string{"-C", app}, args...)...).CombinedOutput(); err != nil {
					t.Fatalf("git %v: %s %v", args, out, err)
				}
			}
			out, err := exec.Command("git", "-C", app, "rev-parse", "HEAD").Output()
			if err != nil {
				t.Fatal(err)
			}
			head := strings.TrimSpace(string(out))
			write(filepath.Join(collection, "harness", ".harness-repos.yml"), "repos:\n  - name: app\n    remote: https://github.com/example/app.git\n", 0644)
			manifest, _ := json.Marshal(map[string]any{"repo": "app", "pr": "7", "forge": "github", "slug": "example/app", "head_sha": head, "round": 1, "repo_dir": app, "public": tc.public})
			write(filepath.Join(bundle, "manifest.json"), string(manifest), 0644)
			write(filepath.Join(bundle, "concerns", "code.md"), "---\nid: code\ntier: fast\n---\nReview code.\n", 0644)
			// A previous round's result must not be posted for this run.
			write(filepath.Join(bundle, "summary.md"), "**Local review: pass**\n", 0644)
			write(filepath.Join(bundle, "verdict"), "pass\n", 0644)
			write(filepath.Join(bin, "gh"), `#!/bin/sh
case "$*" in
 *'--json headRefOid'*) echo '{"headRefOid":"`+head+`","statusCheckRollup":[]}';;
 *'api graphql'*'nameWithOwner'*) echo '{"data":{"repository":{"id":"R_app","nameWithOwner":"example/app","pullRequest":{"number":7,"baseRepository":{"id":"R_app","nameWithOwner":"example/app"}}}}}';;
 *'api -X PATCH'*) { echo PATCH; cat; echo; } >> "$FORGE_LOG"; echo '{"id":301}';;
 *'api -X POST'*) { echo POST; cat; echo; } >> "$FORGE_LOG"; echo '{"id":301}';;
 *) echo "unexpected forge invocation: $*" >&2; exit 8;;
esac
`, 0755)
			write(filepath.Join(bin, "agent"), reviewInterruptAgent, 0755)
			forgeLog := filepath.Join(root, "forge.log")
			args := []string{"wtc", "review", "run", bundle, "--collection", collection, "--hosted-first=false", "--fast", "test:", "--lead", "test:"}
			if tc.post {
				args = append(args, "--post")
			}
			encoded, _ := json.Marshal(args)
			cmd := exec.Command(os.Args[0], "-test.run=^TestReviewProgressCommandChild$")
			cmd.Env = append(os.Environ(),
				"WTC_PROGRESS_ARGS="+string(encoded),
				"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
				"TMPDIR="+tmp,
				"FORGE_LOG="+forgeLog,
				"WTC_FORGE_MIN_INTERVAL=0",
				"WTC_REVIEW_AGENT_CMD="+filepath.Join(bin, "agent"),
				// Public reviewers receive only allowlisted variables.
				"WTC_REVIEW_ENV=REVIEW487_DIR",
				"REVIEW487_DIR="+ev,
			)
			// The command leads its own group, standing in for a terminal's
			// foreground job, so a group signal reaches nothing else here.
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			var output strings.Builder
			cmd.Stdout, cmd.Stderr = &output, &output
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			waited := make(chan error, 1)
			go func() { waited <- cmd.Wait() }()
			pids := map[string]int{}
			t.Cleanup(func() {
				// Only the fixture's own recorded pids, if an assertion
				// failed while they were alive.
				for _, pid := range pids {
					_ = syscall.Kill(pid, syscall.SIGKILL)
				}
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			})
			unrelated := exec.Command("sleep", "300")
			if err := unrelated.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = unrelated.Process.Kill(); _ = unrelated.Wait() })

			poll := func(what string, done func() bool) {
				t.Helper()
				err := retry.Poll(context.Background(), retry.Policy{Every: 10 * time.Millisecond, Within: 30 * time.Second}, func(context.Context) (bool, error) {
					return done(), nil
				})
				if err != nil {
					t.Fatalf("%s: %v\n%s", what, err, output.String())
				}
			}
			poll("reviewer ready", func() bool {
				_, err := os.Stat(filepath.Join(ev, "ready"))
				return err == nil
			})
			for _, name := range []string{"child", "desc"} {
				data, err := os.ReadFile(filepath.Join(ev, name+".pid"))
				pid, convErr := strconv.Atoi(strings.TrimSpace(string(data)))
				if err != nil || convErr != nil || pid <= 1 {
					t.Fatalf("%s pid: %q %v %v", name, data, err, convErr)
				}
				pids[name] = pid
			}
			target := cmd.Process.Pid
			if tc.group {
				target = -target
			}
			if err := syscall.Kill(target, tc.sig); err != nil {
				t.Fatal(err)
			}
			var runErr error
			select {
			case runErr = <-waited:
			case <-time.After(30 * time.Second):
				t.Fatalf("command did not exit after %v\n%s", tc.sig, output.String())
			}
			var exit *exec.ExitError
			if !errors.As(runErr, &exit) || exit.ExitCode() != 1 || !strings.Contains(output.String(), "review run interrupted") || !strings.Contains(output.String(), "signal received") {
				t.Fatalf("command result after %v: %v\n%s", tc.sig, runErr, output.String())
			}
			// The command waited for its reviewers before exiting; only the
			// system's reaping of the reparented descendant can lag.
			poll("reviewer and descendant stopped", func() bool {
				return errors.Is(syscall.Kill(pids["child"], 0), syscall.ESRCH) && errors.Is(syscall.Kill(pids["desc"], 0), syscall.ESRCH)
			})
			if err := unrelated.Process.Signal(syscall.Signal(0)); err != nil {
				t.Fatalf("unrelated process was signalled: %v", err)
			}
			if _, err := os.Stat(filepath.Join(ev, "late")); !os.IsNotExist(err) {
				t.Fatalf("descendant wrote after the command exited: %v", err)
			}
			if started, err := os.ReadFile(filepath.Join(ev, "started")); err != nil || string(started) != "concern\n" {
				t.Fatalf("reviewers started after the signal: %q %v", started, err)
			}
			for _, name := range []string{"summary.md", "verdict"} {
				if _, err := os.Stat(filepath.Join(bundle, name)); !os.IsNotExist(err) {
					t.Fatalf("%s left for posting: %v", name, err)
				}
			}
			if data, err := os.ReadFile(filepath.Join(bundle, "findings", "code.json")); err != nil || !strings.Contains(string(data), `"status": "error"`) {
				t.Fatalf("interrupted concern recorded as %s %v", data, err)
			}
			if left, _ := filepath.Glob(filepath.Join(tmp, "taba-review-ctx-*")); len(left) != 0 {
				t.Fatalf("isolated context left: %v", left)
			}
			if tc.post {
				log, err := os.ReadFile(forgeLog)
				if err != nil || strings.Count(string(log), "POST") != 1 || strings.Count(string(log), "PATCH") != 1 || !strings.Contains(string(log), "verdict=error") || strings.Contains(string(log), "verdict=pass") {
					t.Fatalf("interrupted run was not published as failed: %s %v", log, err)
				}
			}
		})
	}
}

// A signal the process ignores, as under nohup, stays ignored for a review
// run; the others still stop it.
func TestReviewRunSignalContextKeepsIgnoredSignals(t *testing.T) {
	if os.Getenv("REVIEW487_IGNORED_CHILD") == "" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestReviewRunSignalContextKeepsIgnoredSignals$", "-test.v")
		cmd.Env = append(os.Environ(), "REVIEW487_IGNORED_CHILD=1")
		if out, err := cmd.CombinedOutput(); err != nil || !strings.Contains(string(out), "--- PASS") {
			t.Fatalf("child: %v\n%s", err, out)
		}
		return
	}
	signal.Ignore(syscall.SIGHUP)
	ctx, stop := reviewRunSignalContext(context.Background())
	defer stop()
	if err := syscall.Kill(os.Getpid(), syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ctx.Done():
	case <-time.After(30 * time.Second):
		t.Fatal("interrupt did not stop the run")
	}
	if cause := context.Cause(ctx); cause == nil || !strings.Contains(cause.Error(), "interrupt") {
		t.Fatalf("stopped by %v, want the interrupt; an ignored hangup must not stop it", cause)
	}
}
