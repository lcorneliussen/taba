package wtc

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"taba.sh/taba/internal/forgecli"
)

// Status refreshes must complete even when a forge or remote stops replying.
// statusCommand is for local tools: it reads stdout and stderr together, so a
// forge body, which is parsed, goes through statusJSON instead.
func statusCommand(timeout time.Duration, name string, args ...string) ([]byte, error) {
	return statusRun(timeout, (*exec.Cmd).CombinedOutput, name, args...)
}

func statusRun(timeout time.Duration, run func(*exec.Cmd) ([]byte, error), name string, args ...string) ([]byte, error) {
	return statusRunContext(context.Background(), timeout, run, name, args...)
}

func statusRunContext(parent context.Context, timeout time.Duration, run func(*exec.Cmd) ([]byte, error), name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = 200 * time.Millisecond
	out, err := run(cmd)
	return statusOutcome(parent, ctx, timeout, name, args, out, err)
}

// statusOutcome is the result of one bounded child call: the parent's
// cancellation, the timeout, or the child's own diagnostic. An exit error from
// a read of stdout alone carries stderr, which is then the whole diagnostic;
// otherwise the captured output is quoted, both streams for a combined read.
func statusOutcome(parent, ctx context.Context, timeout time.Duration, name string, args []string, out []byte, err error) ([]byte, error) {
	if parent.Err() != nil {
		return nil, fmt.Errorf("%s: %w", name, parent.Err())
	}
	if ctx.Err() != nil {
		return nil, fmt.Errorf("%s timed out after %s", name, timeout)
	}
	if err != nil {
		detail := out
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
			detail = exitErr.Stderr
		}
		return nil, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(detail)))
	}
	return out, nil
}

func statusGit(path string, args ...string) (string, error) {
	out, err := statusCommand(30*time.Second, "git", append([]string{"-C", path}, args...)...)
	return strings.TrimSpace(string(out)), err
}

// statusJSON reads forge JSON from stdout alone, so stderr cannot corrupt it.
func statusJSON(args ...string) ([]byte, error) {
	return statusJSONContext(context.Background(), args...)
}

func statusCommandContext(ctx context.Context, timeout time.Duration, name string, args ...string) ([]byte, error) {
	return statusRunContext(ctx, timeout, (*exec.Cmd).CombinedOutput, name, args...)
}
func statusGitContext(ctx context.Context, path string, args ...string) (string, error) {
	return statusResourceGit(ctx, append([]string{"-C", path}, args...), func() (string, error) {
		out, err := statusCommandContext(ctx, 30*time.Second, "git", append([]string{"-C", path}, args...)...)
		return strings.TrimSpace(string(out)), err
	})
}
func statusJSONContext(parent context.Context, args ...string) ([]byte, error) {
	timeout := 30 * time.Second
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	cmd := forgecli.CommandContext(ctx, args[0], args[1:]...)
	cmd.WaitDelay = 200 * time.Millisecond
	out, err := cmd.Output()
	return statusOutcome(parent, ctx, timeout, args[0], args[1:], out, err)
}
