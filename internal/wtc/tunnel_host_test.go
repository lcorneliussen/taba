package wtc

import (
	"context"
	"os"
	"path/filepath"
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
				}
				return nil
			})
			if fails && err == nil || !fails && err != nil || readyCount != 1 || address.URL != "https://synthetic.trycloudflare.com" {
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
		return script + "exit 42\n"
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
