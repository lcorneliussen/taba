package wtc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Opt-in native compatibility gate. The caller supplies an independently
// checksum-verified published artifact; this test never downloads or installs it.
func TestTunnelPublished024ForwardSchemaFence(t *testing.T) {
	binary := os.Getenv("WTC_TEST_OLD_TABA")
	expected := os.Getenv("WTC_TEST_OLD_TABA_SHA256")
	if binary == "" && expected == "" {
		t.Skip("explicit published artifact and checksum required")
	}
	if !filepath.IsAbs(binary) || !validTunnelDigest(expected) {
		t.Fatal("explicit absolute artifact and SHA256 required")
	}
	data, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != expected {
		t.Fatal("published artifact checksum mismatch")
	}
	for _, schema := range []int{1, 2} {
		t.Run(fmt.Sprintf("schema-%d", schema), func(t *testing.T) {
			c := newWorkspaceFixture(t)
			made, err := c.NewCollection(NewOptions{Slug: "old-client-fence", Repos: []string{"widget"}})
			if err != nil {
				t.Fatal(err)
			}
			target, err := OpenCollection(made.Collection)
			if err != nil {
				t.Fatal(err)
			}
			// Prevent version handover: the published executable is the only CLI under
			// test. Every workspace, owner, credential root and executable is synthetic.
			for _, harness := range []string{c.Harness, target.Harness} {
				if err = os.Remove(filepath.Join(harness, ".wtc-cli-version")); err != nil {
					t.Fatal(err)
				}
			}
			temp := t.TempDir()
			pre := filepath.Join(temp, "pre-hook")
			teardown := filepath.Join(temp, "teardown")
			runner := filepath.Join(temp, "runner-calls")
			fixtureFile(t, filepath.Join(c.Harness, "hooks/wtc/retire.pre.sh"), "#!/bin/sh\ntouch \"$FAKE_PRE_HOOK\"\n", 0755)
			fixtureFile(t, filepath.Join(target.Collection, "widget/.harness/runtime-teardown.sh"), "#!/bin/sh\ntouch \"$FAKE_TEARDOWN\"\n", 0755)
			fakebin := filepath.Join(temp, "bin")
			fixtureFile(t, filepath.Join(fakebin, "herdr"), "#!/bin/sh\nprintf '[]\\n'\n", 0755)
			for _, name := range []string{"wtc", "taba", "mise", "ngrok", "devtunnel", "cloudflared", "tailscale", "npm", "node", "curl", "ssh"} {
				fixtureFile(t, filepath.Join(fakebin, name), "#!/bin/sh\nexit 91\n", 0755)
			}
			fakeRunner := filepath.Join(fakebin, "dekit")
			fixtureFile(t, fakeRunner, "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$FAKE_RUNNER_CALLS\"\ncase \"$*\" in\n *'runner status'*) printf '{\"status\":\"absent\"}\\n';;\n *'runner stop'*) printf 'Runner is not running.\\n' >&2;exit 1;;\n *) exit 92;;\nesac\n", 0755)
			m := runtimeManifest{Schema: schema, Root: target.runtimeRoot(), Binary: fakeRunner, Resources: []runtimeResource{{Repo: "widget", Dir: "widget", State: "provisioned"}}}
			encoded, err := json.Marshal(m)
			if err != nil {
				t.Fatal(err)
			}
			fixtureFile(t, filepath.Join(m.Root, "manifest.json"), string(encoded), 0600)
			home := filepath.Join(temp, "home")
			if err = os.Mkdir(home, 0700); err != nil {
				t.Fatal(err)
			}
			env := []string{"HOME=" + home, "PATH=" + fakebin + ":/usr/bin:/bin:/usr/sbin:/sbin", "TMPDIR=" + temp, "TABA_DEV_BUILD=off", "WTC_DEV_BUILD=off", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0", "GIT_NO_LAZY_FETCH=1", "WTC_HARNESS_REPO=agent-harness", "WTC_CONFIG_ROOT=" + c.ConfigRoot, "TABA_CONFIG_ROOT=" + c.ConfigRoot, "FAKE_PRE_HOOK=" + pre, "FAKE_TEARDOWN=" + teardown, "FAKE_RUNNER_CALLS=" + runner}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			version := exec.CommandContext(ctx, binary, "--version")
			version.Dir = c.Collection
			version.Env = env
			out, err := version.CombinedOutput()
			if err != nil || !strings.Contains(string(out), "0.2.4") {
				t.Fatalf("unexpected native version: %s %v", out, err)
			}
			command := exec.CommandContext(ctx, binary, "retire", "old-client-fence", "--force")
			command.Dir = c.Collection
			command.Env = env
			out, runErr := command.CombinedOutput()
			t.Logf("published 0.2.4 schema=%d exit=%v output=%s", schema, runErr, out)
			if _, err = os.Stat(pre); err != nil {
				t.Fatalf("old-client pre-hook limitation was not exercised: %v", err)
			}
			_, teardownErr := os.Stat(teardown)
			_, memberErr := os.Stat(filepath.Join(target.Collection, "widget"))
			calls, _ := os.ReadFile(runner)
			if schema == 2 {
				if runErr == nil || !os.IsNotExist(teardownErr) || memberErr != nil || len(calls) != 0 {
					t.Fatalf("forward schema did not block runner/teardown/worktree deletion: run=%v teardown=%v member=%v calls=%s", runErr, teardownErr, memberErr, calls)
				}
			} else if runErr != nil || teardownErr != nil || !os.IsNotExist(memberErr) || !strings.Contains(string(calls), "runner stop") {
				t.Fatalf("schema1 control did not execute synthetic teardown: run=%v teardown=%v member=%v calls=%s", runErr, teardownErr, memberErr, calls)
			}
		})
	}
}
