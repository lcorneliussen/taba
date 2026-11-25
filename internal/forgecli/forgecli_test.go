package forgecli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeNodeForge puts a bb on PATH that behaves like Node writing --json in one
// call and exiting: a regular file receives the whole body, a pipe only the
// first 64KiB. It also writes to stderr, which must stay out of the body.
func fakeNodeForge(t *testing.T) {
	t.Helper()
	bin := t.TempDir()
	body := `{"description":"` + strings.Repeat("x", 80000) + `","state":"OPEN"}`
	if err := os.WriteFile(filepath.Join(bin, "body.json"), []byte(body+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
echo "bb: notice on stderr" >&2
if [ -f /dev/stdout ]; then
  cat "` + bin + `/body.json"
else
  head -c 65536 "` + bin + `/body.json"
fi
`
	if err := os.WriteFile(filepath.Join(bin, "bb"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestOutputReadsPastThePipeCap(t *testing.T) {
	fakeNodeForge(t)
	piped, err := exec.Command("bb", "pr", "view", "1", "--json").Output()
	if err != nil || len(piped) != 65536 {
		t.Fatalf("the fake forge CLI did not cap the pipe: %d bytes, %v", len(piped), err)
	}
	full, err := Command("bb", "pr", "view", "1", "--json").Output()
	if err != nil || len(full) <= 65536 || strings.Contains(string(full), "stderr") {
		t.Fatalf("Output lost or mixed the body: %d bytes, %v", len(full), err)
	}
}

func TestOutputKeepsStderrOutOfTheBody(t *testing.T) {
	var stderr bytes.Buffer
	cmd := Command("sh", "-c", "echo body; echo loud >&2")
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil || strings.TrimSpace(string(out)) != "body" || strings.TrimSpace(stderr.String()) != "loud" {
		t.Fatalf("streams were mixed: out %q, stderr %q, err %v", out, stderr.String(), err)
	}
}

func TestOutputCapturesStderrOnFailure(t *testing.T) {
	out, err := Command("sh", "-c", "echo body; echo broken >&2; exit 3").Output()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || strings.TrimSpace(string(exitErr.Stderr)) != "broken" {
		t.Fatalf("stderr was not kept: %v", err)
	}
	if strings.TrimSpace(string(out)) != "body" {
		t.Fatalf("stdout of a failed call was lost: %q", out)
	}
}

func TestRunWritesStdoutWhereTheCallerSaysAndDiscardsNil(t *testing.T) {
	file, err := os.Create(filepath.Join(t.TempDir(), "streamed"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := Command("sh", "-c", "echo streamed").Run(file); err != nil {
		t.Fatal(err)
	}
	wrote, err := os.ReadFile(file.Name())
	if err != nil || strings.TrimSpace(string(wrote)) != "streamed" {
		t.Fatalf("Run did not write stdout: %q, %v", wrote, err)
	}
	if err := Command("sh", "-c", "echo streamed").Run(nil); err != nil {
		t.Fatalf("Run with no stdout: %v", err)
	}
}

func TestDirEnvAndStdinReachTheChild(t *testing.T) {
	dir := t.TempDir()
	cmd := Command("sh", "-c", `pwd; echo "$FORGE_FIXTURE"; cat`)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "FORGE_FIXTURE=set")
	cmd.Stdin = strings.NewReader("piped in\n")
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	// macOS resolves the temporary directory through a symlink.
	if got := strings.Split(string(out), "\n"); !strings.HasSuffix(got[0], dir) || got[1] != "set" || got[2] != "piped in" {
		t.Fatalf("Dir, Env or Stdin did not reach the child: %q", out)
	}
}

// A WaitDelay stops the wait for a stream an exited child's own child still
// holds. Without one, Output waits for that grandchild; with one it reports
// exec.ErrWaitDelay. The body is on a file either way, so it still arrives.
func TestWaitDelayBoundsTheWaitAndKeepsTheBody(t *testing.T) {
	cmd := Command("sh", "-c", `sh -c 'sleep 1; echo late >&2' & echo body; exit 0`)
	cmd.WaitDelay = 50 * time.Millisecond
	start := time.Now()
	out, err := cmd.Output()
	if !errors.Is(err, exec.ErrWaitDelay) || time.Since(start) > time.Second {
		t.Fatalf("WaitDelay did not reach the child: %v after %s", err, time.Since(start))
	}
	if strings.TrimSpace(string(out)) != "body" {
		t.Fatalf("the body was lost: %q", out)
	}
}

func TestCommandContextCancellationStopsTheChild(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := CommandContext(ctx, "sh", "-c", "echo late").Output(); err == nil {
		t.Fatal("a cancelled context must fail the call")
	}
}
