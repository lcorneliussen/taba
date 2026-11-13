package wtc

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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
			script := "#!/bin/sh\n/usr/bin/env > \"$HOME/seen-env\"\nprintf 'https://synthetic.trycloudflare.com\\n'\n"
			if fails {
				script += "exit 42\n"
			} else {
				script += "trap 'exit 0' TERM\nwhile :; do /bin/sleep 0.05; done\n"
			}
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
