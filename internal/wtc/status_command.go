package wtc

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Status refreshes must complete even when a forge or remote stops replying.
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
	return statusRun(30*time.Second, forgeOutput, args[0], args[1:]...)
}

func statusCommandContext(ctx context.Context, timeout time.Duration, name string, args ...string) ([]byte, error) {
	return statusRunContext(ctx, timeout, (*exec.Cmd).CombinedOutput, name, args...)
}
func statusGitContext(ctx context.Context, path string, args ...string) (string, error) {
	out, err := statusCommandContext(ctx, 30*time.Second, "git", append([]string{"-C", path}, args...)...)
	return strings.TrimSpace(string(out)), err
}
func statusJSONContext(ctx context.Context, args ...string) ([]byte, error) {
	return statusRunContext(ctx, 30*time.Second, forgeOutput, args[0], args[1:]...)
}
