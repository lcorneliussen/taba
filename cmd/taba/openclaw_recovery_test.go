package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"taba.sh/taba/internal/openclaw"
)

func recoveryOutput(t *testing.T, run func() error) ([]byte, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	original := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = original; r.Close(); w.Close() }()
	runErr := run()
	w.Close()
	os.Stdout = original
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return out, runErr
}

func TestFinishRecoveryCommandOwnGatewayAndFailureJSON(t *testing.T) {
	state, g := harnessGateway(t)
	dir := filepath.Join(state, "worktrees", "api", "recovery")
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		t.Fatal(err)
	}
	g(state, "-C", filepath.Join(state, "repos", "api"), "worktree", "add", "-q", "-b", "openclaw/recovery", dir)
	if err := os.WriteFile(filepath.Join(dir, "local.txt"), []byte("local edits\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	g(dir, "add", "local.txt")
	g(dir, "stash", "push", "-qm", "saved edits")
	head, stash := g(dir, "rev-parse", "HEAD"), g(dir, "rev-parse", "refs/stash")
	ref := "refs/wtc-catch-up/" + stash + "-123"
	g(dir, "update-ref", ref, stash)
	marker := g(dir, "rev-parse", "--path-format=absolute", "--git-path", "wtc-catch-up-recovery")
	if err := os.WriteFile(marker, []byte(ref+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	output, err := recoveryOutput(t, func() error { return catchUpCheckout("", "", true) })
	var failure struct {
		OK   bool             `json:"ok"`
		Data openclaw.CatchUp `json:"data"`
	}
	if err == nil || json.Unmarshal(output, &failure) != nil || failure.OK || !failure.Data.RecoveryRequired || failure.Data.RecoveryRef != ref {
		t.Fatalf("failure lost operation data: %s %v", output, err)
	}
	g(dir, "stash", "apply", "--index", ref)
	before := g(dir, "status", "--porcelain")
	output, err = recoveryOutput(t, func() error { return finishCheckoutRecovery(ref, head, true) })
	var done struct {
		OK   bool                        `json:"ok"`
		Data openclaw.RecoveryCompletion `json:"data"`
	}
	if err != nil || json.Unmarshal(output, &done) != nil || !done.OK || done.Data.RecoveryRef != ref {
		t.Fatalf("finish: %s %v", output, err)
	}
	if g(dir, "status", "--porcelain") != before || g(dir, "rev-parse", "HEAD") != head || g(dir, "rev-parse", "refs/stash") != stash {
		t.Fatal("finish changed local work")
	}
	// A detached copy outside the gateway-managed paths is not its owner.
	other := filepath.Join(state, "other-copy")
	g(dir, "worktree", "add", "-q", "--detach", other)
	t.Chdir(other)
	if err := finishCheckoutRecovery(ref, head, false); err == nil || !strings.Contains(err.Error(), "owning gateway-managed checkout") {
		t.Fatalf("foreign copy accepted: %v", err)
	}
}

func TestFinishRecoveryCommandRejectsSandboxAndMixedModes(t *testing.T) {
	t.Setenv(openclaw.GatewayGitEnv, "http://127.0.0.1:1")
	if err := finishCheckoutRecovery("ref", "head", false); err == nil || !strings.Contains(err.Error(), "owning gateway worktree") {
		t.Fatalf("sandbox accepted: %v", err)
	}
	for _, args := range [][]string{{"--all"}, {"--continue"}, {"--dest", "main"}, {"--from", "other"}, {"other-collection"}} {
		root := &cobra.Command{Use: "taba", SilenceUsage: true, SilenceErrors: true}
		asJSON := true
		addCatchUpCommand(root, &asJSON)
		root.SetArgs(append([]string{"catch-up", "--finish-recovery", "ref", "--expected-head", "head"}, args...))
		if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "takes only") {
			t.Fatalf("mixed mode %v: %v", args, err)
		}
	}
}

func TestFinishRecoveryRequiresPairedNonEmptyArgumentsBeforeHostLookup(t *testing.T) {
	t.Setenv(openclaw.GatewayGitEnv, "http://127.0.0.1:1")
	for _, args := range [][]string{
		{"--finish-recovery", "ref"}, {"--expected-head", "head"},
		{"--finish-recovery=", "--expected-head=head"},
		{"--finish-recovery=ref", "--expected-head="},
		{"--finish-recovery= ", "--expected-head=head"},
	} {
		root := &cobra.Command{Use: "taba", SilenceUsage: true, SilenceErrors: true}
		asJSON := false
		addCatchUpCommand(root, &asJSON)
		root.SetArgs(append([]string{"catch-up"}, args...))
		if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "requires both --finish-recovery and --expected-head") {
			t.Fatalf("missing pair %v should fail before gateway lookup: %v", args, err)
		}
	}
}
