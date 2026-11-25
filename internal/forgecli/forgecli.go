// Package forgecli runs the forge command-line clients (gh, bb, twg). A
// forge call is one of two things, and a *exec.Cmd cannot tell them apart: a
// read, whose stdout is a body something parses, or a run, whose output goes
// to a terminal or nowhere. Cmd keeps them apart, and Output is the only way
// to read stdout.
package forgecli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"time"
)

// waitDelay bounds how long Wait waits for the streams of an exited child.
const waitDelay = 200 * time.Millisecond

// Cmd is one forge CLI call. Dir, Env, Stdin and Stderr mean what the
// exec.Cmd fields of those names mean; stdout is the package's own, so that a
// body cannot be read from a pipe.
type Cmd struct {
	Dir    string
	Env    []string
	Stdin  io.Reader
	Stderr io.Writer

	cmd *exec.Cmd
}

// Command prepares name with args. Use CommandContext where a deadline or a
// cancellation has to reach the child.
func Command(name string, args ...string) *Cmd {
	return &Cmd{cmd: exec.Command(name, args...)}
}

// CommandContext prepares name with args, killed when ctx is done.
func CommandContext(ctx context.Context, name string, args ...string) *Cmd {
	return &Cmd{cmd: exec.CommandContext(ctx, name, args...)}
}

// Output runs the call and returns its stdout, read back from a temporary
// regular file. A Node-based forge CLI writes a --json body in one call and
// exits; on a pipe only the first 64KiB of that write arrives, on a file the
// whole body does. Stderr stays out of the body: with no Stderr set it is
// captured into the returned *exec.ExitError, as exec's own Output does.
func (c *Cmd) Output() ([]byte, error) {
	file, err := os.CreateTemp("", "wtc-forge-*.out")
	if err != nil {
		return nil, err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	stderr := c.Stderr
	var captured bytes.Buffer
	if stderr == nil {
		stderr = &captured
	}
	runErr := c.run(file, stderr)
	var exitErr *exec.ExitError
	if c.Stderr == nil && errors.As(runErr, &exitErr) {
		exitErr.Stderr = captured.Bytes()
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	out, err := io.ReadAll(file)
	if err != nil {
		return nil, err
	}
	return out, runErr
}

// Run runs the call and writes its stdout to stdout, for a call whose output
// nothing parses. A nil stdout discards it, as exec does.
func (c *Cmd) Run(stdout io.Writer) error {
	return c.run(stdout, c.Stderr)
}

func (c *Cmd) run(stdout, stderr io.Writer) error {
	c.cmd.Dir = c.Dir
	c.cmd.Env = c.Env
	c.cmd.Stdin = c.Stdin
	c.cmd.Stderr = stderr
	c.cmd.Stdout = stdout
	c.cmd.WaitDelay = waitDelay
	return c.cmd.Run()
}
