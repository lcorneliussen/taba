package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestInvocationBrandingAtCLIEntryPoint(t *testing.T) {
	bin := t.TempDir()
	binary := filepath.Join(bin, "wtc")
	buildVersioned(t, binary, "0.1.60")
	for _, name := range []string{"taba", "unreleased-dev"} {
		if err := os.Symlink(binary, filepath.Join(bin, name)); err != nil {
			t.Fatal(err)
		}
	}
	for _, invocation := range []struct{ executable, name string }{{"wtc", "wtc"}, {"taba", "taba"}, {"unreleased-dev", "wtc"}} {
		t.Run(invocation.executable, func(t *testing.T) {
			root, configRoot := t.TempDir(), t.TempDir()
			run := func(args ...string) (string, string, error) {
				t.Helper()
				cmd := exec.Command(filepath.Join(bin, invocation.executable), args...)
				cmd.Dir = root
				cmd.Env = append(os.Environ(), "WTC_DEV_BUILD=off", "WTC_DEV_DELEGATED_FROM=", "OPENCLAW_STATE_DIR=", "OPENCLAW_SHELL=", "WTC_CONFIG_ROOT="+configRoot, "TABA_CONFIG_ROOT="+configRoot)
				var stdout, stderr bytes.Buffer
				cmd.Stdout, cmd.Stderr = &stdout, &stderr
				err := cmd.Run()
				return stdout.String(), stderr.String(), err
			}
			for _, check := range []struct {
				args []string
				want []string
			}{
				{[]string{"--help"}, []string{invocation.name + " [command]"}},
				{[]string{"--version"}, []string{invocation.name + " version 0.1.60"}},
				{[]string{"svc", "add", "--help"}, []string{
					invocation.name + " svc add", invocation.name + " svc tunnel", "svc/<repo>.yaml",
					invocation.name + " svc add web --port-env PORT -- npm run dev",
					invocation.name + " svc add ext.app/api --port-env API_PORT --shared -- ./bin/dev",
					invocation.name + ` svc add worker --ready-log "worker started" -- mise run worker`,
				}},
				{[]string{"status", "--help"}, []string{invocation.name + " status", "WTC_CONFIG_ROOT/wtc.env", "WTC_STATUS_WATCH"}},
				{[]string{"openclaw", "install", "--help"}, []string{"openclaw plugins reload wtc"}},
			} {
				stdout, stderr, err := run(check.args...)
				if err != nil || stderr != "" || strings.Contains(stdout, "{{command}}") {
					t.Fatalf("%v failed: %v, stdout=%q, stderr=%q", check.args, err, stdout, stderr)
				}
				for _, want := range check.want {
					if !strings.Contains(stdout, want) {
						t.Fatalf("%v output lacks %q: %s", check.args, want, stdout)
					}
				}
				if strings.Contains(stdout, "openclaw plugins reload taba") {
					t.Fatal("plugin ID was rewritten")
				}
			}
			for _, check := range []struct {
				args []string
				want string
			}{
				{[]string{"env", "setup", "--dry-run"}, "run `" + invocation.name + " init`"},
				{[]string{"svc", "add", "web", "npm", "run", "dev"}, "such as " + invocation.name + " svc add web -- npm run dev"},
				{[]string{"docs", "literal wtc {{command}}"}, `unknown embedded document "literal wtc {{command}}"; run ` + invocation.name + " docs"},
			} {
				stdout, stderr, err := run(check.args...)
				if err == nil || stdout != "" || !strings.HasPrefix(stderr, invocation.name+": ") || !strings.Contains(stderr, check.want) {
					t.Fatalf("%v diagnostic mismatch: %v, stdout=%q, stderr=%q", check.args, err, stdout, stderr)
				}
			}
			target := filepath.Join(root, "literal wtc {{command}}")
			stdout, stderr, err := run("harness", "init", target, "--remote", "https://example.invalid/wtc/project.git", "--cli-version", "0.1.60", "--dry-run", "--json")
			var result struct {
				OK   bool `json:"ok"`
				Data struct {
					Directory string   `json:"directory"`
					Files     []string `json:"files"`
				} `json:"data"`
			}
			if err != nil || stderr != "" || json.Unmarshal([]byte(stdout), &result) != nil || !result.OK || result.Data.Directory != target {
				t.Fatalf("JSON payload was changed: %v, %q, %q", err, stdout, stderr)
			}
			files := strings.Join(result.Data.Files, ",")
			if !strings.Contains(files, ".wtc-cli-version") || !strings.Contains(files, "wtc.toml") || strings.Contains(files, "taba") {
				t.Fatalf("generated names changed: %v", result.Data.Files)
			}
			if _, err := os.Stat(target); !os.IsNotExist(err) {
				t.Fatalf("JSON dry run created the harness: %v", err)
			}
		})
	}
}

func TestBrowseProcessAcceptsBothCommandNames(t *testing.T) {
	for _, name := range []string{"wtc", "taba", "/opt/bin/wtc", "/opt/bin/taba", "taba.exe"} {
		if !openBrowseCommand([]string{name, "browse", "--here"}) {
			t.Errorf("browse process not recognized: %s", name)
		}
	}
	for _, argv := range [][]string{nil, {"taba"}, {"taba", "status"}, {"unrelated", "browse"}} {
		if openBrowseCommand(argv) {
			t.Errorf("unrelated process recognized as browse: %v", argv)
		}
	}
}
