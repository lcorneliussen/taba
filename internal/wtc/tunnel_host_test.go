package wtc

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestTunnelProductionForegroundCloudflareUsesMinimalEnvironmentAndConfirmedDisposal(t *testing.T) {
	for _, fails := range []bool{false, true} {
		t.Run(map[bool]string{false: "graceful", true: "unconfirmed-exit"}[fails], func(t *testing.T) {
			c := runtimeFixture(t)
			dir := filepath.Join(t.TempDir(), "owned-home")
			if err := os.Mkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
			var gate *os.File
			if fails {
				// The failing child must outlive startup identity so its URL and
				// exit are the ready-then-unconfirmed-disposal case, not an
				// exit before ownership (TestTunnelForegroundFixtureStartupOrdering).
				gate = foregroundFixtureGate(t, dir)
			}
			binary := filepath.Join(t.TempDir(), "cloudflared")
			script := foregroundCloudflareFixtureScript(fails, false)
			fixtureFile(t, binary, script, 0755)
			t.Setenv("CLOUDFLARE_API_TOKEN", "synthetic-inherited-token")
			t.Setenv("HTTP_PROXY", "http://invalid.example.test")
			t.Setenv("NODE_OPTIONS", "synthetic-inherited-option")
			d := testTunnelDefinition()
			d.Entry.Policy = tunnelPolicyEntry{Provider: "cloudflare", Mode: "quick", Access: "public", Inputs: tunnelInputRefs{Executable: binary}}
			call := tunnelEffectCall{Definition: d, Entry: tunnelEntryState{Provider: "cloudflare", Mode: "quick"}}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			readyCount := 0
			address, err := (&tunnelProviderRuntime{c: c}).runHost(ctx, call, dir, func(raw string) error {
				readyCount++
				if raw != "https://synthetic.trycloudflare.com" {
					t.Fatal("wrong host address", raw)
				}
				if !fails {
					cancel()
				} else if _, err := gate.WriteString("\n"); err != nil {
					t.Fatal(err)
				}
				return nil
			})
			if fails && (err == nil || err.Error() != "provider endpoint disposal not confirmed") || !fails && err != nil || readyCount != 1 || address.URL != "https://synthetic.trycloudflare.com" {
				t.Fatal("foreground start/disposal result", readyCount, err)
			}
			env, readErr := os.ReadFile(filepath.Join(dir, "seen-env"))
			if readErr != nil {
				t.Fatal(readErr)
			}
			for _, forbidden := range []string{"synthetic-inherited", "CLOUDFLARE_API_TOKEN=", "HTTP_PROXY=", "NODE_OPTIONS="} {
				if strings.Contains(string(env), forbidden) {
					t.Fatal("host inherited ambient provider/config controls")
				}
			}
		})
	}
}

func foregroundCloudflareFixtureScript(fails, holdAfterReady bool) string {
	script := "#!/bin/sh\n/usr/bin/env > \"$HOME/seen-env\"\n"
	if !fails {
		script += "trap 'exit 0' TERM\n"
	}
	if holdAfterReady {
		script += "trap > \"$HOME/cleanup-armed\"\n"
	}
	script += "printf 'https://synthetic.trycloudflare.com\\n'\n"
	if fails {
		// Hold the failing child until the test's readiness callback writes a
		// line to $HOME/ready-gate (foregroundFixtureGate), then exit unconfirmed.
		return script + "read ignored < \"$HOME/ready-gate\"\nexit 42\n"
	}
	if holdAfterReady {
		script += "if ! /usr/bin/grep -q TERM \"$HOME/cleanup-armed\"; then read ignored < \"$HOME/ready-gate\"; fi\n"
	}
	return script + "while :; do /bin/sleep 0.05; done\n"
}

func TestTunnelForegroundFixtureCleanupBeforeReadiness(t *testing.T) {
	for _, armed := range []bool{true, false} {
		name := "armed"
		if !armed {
			name = "unarmed"
		}
		t.Run(name, func(t *testing.T) {
			c := runtimeFixture(t)
			dir := t.TempDir()
			fifo := filepath.Join(dir, "ready-gate")
			if err := syscall.Mkfifo(fifo, 0600); err != nil {
				t.Fatal(err)
			}
			// Hold an unarmed shell after its URL until cancellation. An armed
			// shell must not block in read, where some shells defer trapped TERM.
			gate, err := os.OpenFile(fifo, os.O_RDWR, 0600)
			if err != nil {
				t.Fatal(err)
			}
			defer gate.Close()
			script := foregroundCloudflareFixtureScript(false, true)
			if !armed {
				script = strings.Replace(script, "trap 'exit 0' TERM\n", "", 1)
			}
			binary := filepath.Join(t.TempDir(), "cloudflared")
			fixtureFile(t, binary, script, 0755)
			d := testTunnelDefinition()
			d.Entry.Policy = tunnelPolicyEntry{Provider: "cloudflare", Mode: "quick", Access: "public", Inputs: tunnelInputRefs{Executable: binary}}
			call := tunnelEffectCall{Definition: d, Entry: tunnelEntryState{Provider: "cloudflare", Mode: "quick"}}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			readyCount := 0
			address, err := (&tunnelProviderRuntime{c: c}).runHost(ctx, call, dir, func(raw string) error {
				readyCount++
				if raw != "https://synthetic.trycloudflare.com" {
					t.Fatal("wrong host address", raw)
				}
				cancel()
				return nil
			})
			state, readErr := os.ReadFile(filepath.Join(dir, "cleanup-armed"))
			if readErr != nil || strings.Contains(string(state), "TERM") != armed {
				t.Fatal("wrong cleanup state when readiness was emitted", string(state), readErr, err)
			}
			if readyCount != 1 || address.URL != "https://synthetic.trycloudflare.com" {
				t.Fatal("readiness not observed exactly once", readyCount, address)
			}
			if armed && err != nil {
				t.Fatal("cleanup was not armed before readiness", err)
			}
			if !armed && (err == nil || !strings.Contains(err.Error(), "provider endpoint disposal not confirmed")) {
				t.Fatal("unarmed provider bypassed disposal refusal", err)
			}
		})
	}
}

// foregroundFixtureGate creates $HOME/ready-gate for a synthetic host and holds
// it open read-write, so the child's read blocks until the test writes a line;
// closing at cleanup releases a still-held child.
func foregroundFixtureGate(t *testing.T, home string) *os.File {
	t.Helper()
	fifo := filepath.Join(home, "ready-gate")
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	gate, err := os.OpenFile(fifo, os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { gate.Close() })
	return gate
}

// Startup identity is observed after Start and before stdout is read. Force an
// inconclusive first observation and let later ones happen only after the
// synthetic child has printed its URL. A child that
// exits right after its URL must fail closed before ownership; the gated
// fixture stays observable until readiness and still exits 42.
func TestTunnelForegroundFixtureStartupOrdering(t *testing.T) {
	const url = "https://synthetic.trycloudflare.com"
	for _, held := range []bool{false, true} {
		name := map[bool]string{false: "exit-before-identity", true: "held-until-ready"}[held]
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			script := foregroundCloudflareFixtureScript(true, false)
			var gate *os.File
			if held {
				gate = foregroundFixtureGate(t, dir)
			} else {
				script = "#!/bin/sh\nprintf '" + url + "\\n'\nexit 42\n"
			}
			binary := filepath.Join(t.TempDir(), "cloudflared")
			fixtureFile(t, binary, script, 0755)
			cmd := exec.Command(binary)
			cmd.Env = minimalTunnelEnvironment(dir)
			out, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err = cmd.Start(); err != nil {
				t.Fatal(err)
			}
			waited := false
			t.Cleanup(func() {
				if !waited {
					_ = cmd.Process.Kill() // this test's own child only
					_ = cmd.Wait()
				}
			})
			lines := bufio.NewReader(out)
			observations := 0
			read := func(pid int) (tunnelProcessIdentity, string) {
				observations++
				if observations > 1 {
					return readTunnelProcess(pid)
				}
				if line, err := lines.ReadString('\n'); err != nil || line != url+"\n" {
					t.Fatal("synthetic URL not emitted", line, err)
				}
				if !held {
					// EOF: the child has exited and is not reaped before Wait.
					if _, err := io.Copy(io.Discard, lines); err != nil {
						t.Fatal(err)
					}
				}
				return tunnelProcessIdentity{}, "unknown"
			}
			child, observeErr := observeTunnelChildWith(context.Background(), cmd.Process.Pid, read)
			if held {
				if observeErr != nil || child.PID != cmd.Process.Pid {
					t.Fatal("held fixture not identified before readiness", child, observeErr)
				}
				if _, err = gate.WriteString("\n"); err != nil {
					t.Fatal(err)
				}
			} else {
				if observeErr == nil || !strings.HasPrefix(observeErr.Error(), "owned child initialization unconfirmed") {
					t.Fatal("exit before identity did not fail closed", child, observeErr)
				}
				// stdout closes before the child is a zombie; a read in between
				// is still inconclusive, so only positive absence is required.
				if runtime.GOOS == "linux" && !strings.HasPrefix(observeErr.Error(), "owned child initialization unconfirmed (state absent, observations ") {
					t.Fatal("unexpected early-exit observation", observeErr)
				}
			}
			waitErr := cmd.Wait()
			waited = true
			if cmd.ProcessState == nil || cmd.ProcessState.ExitCode() != 42 {
				t.Fatal("synthetic child did not exit 42", waitErr)
			}
		})
	}
}

func TestTunnelNamedCloudflareHostReportsRejectedCommand(t *testing.T) {
	c := runtimeFixture(t)
	scope, err := c.ValidateControlRoot()
	if err != nil {
		t.Fatal(err)
	}
	capability, err := json.Marshal(map[string]any{"a": "synthetic-account", "t": "synthetic-tunnel", "s": make([]byte, 32)})
	if err != nil {
		t.Fatal(err)
	}
	fixtureFile(t, filepath.Join(scope.Root, "provider/host.token"), base64.StdEncoding.EncodeToString(capability)+"\n", 0600)
	d := testTunnelDefinition()
	// A relative executable makes the host command constructor refuse.
	d.Entry.Policy.Inputs = tunnelInputRefs{Executable: "cloudflared", HostingFile: "provider/host.token"}
	receipt := tunnelResourceRef{Provider: "cloudflare", Scope: "synthetic-account", Kind: "tunnel", ID: "synthetic-tunnel", Ownership: "owned"}
	call := tunnelEffectCall{Definition: d, Entry: tunnelEntryState{Provider: "cloudflare", Mode: "named", Resources: []tunnelResourceRef{receipt}}}
	_, err = (&tunnelProviderRuntime{c: c}).runHost(context.Background(), call, t.TempDir(), func(string) error {
		t.Fatal("rejected host reported ready")
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "explicit scoped Cloudflare host inputs required") {
		t.Fatal("rejected host command was not reported", err)
	}
}
