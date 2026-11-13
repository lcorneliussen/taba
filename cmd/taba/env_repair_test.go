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

func TestGeneratedRootRepairCLI(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "taba")
	buildVersioned(t, bin, "9.0.0")
	for _, external := range []bool{false, true} {
		t.Run(map[bool]string{false: "owned", true: "external"}[external], func(t *testing.T) {
			workspace, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			collection := filepath.Join(workspace, "task")
			config := filepath.Join(collection, ".wtc", "config")
			if err := os.MkdirAll(config, 0755); err != nil {
				t.Fatal(err)
			}
			write := func(path, value string) {
				t.Helper()
				if err := os.WriteFile(path, []byte(value), 0600); err != nil {
					t.Fatal(err)
				}
			}
			read := func(path string) string {
				t.Helper()
				b, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				return string(b)
			}
			root := filepath.Join(collection, "new-control")
			if external {
				root = filepath.Join(workspace, "shared-control")
			}
			if err := os.MkdirAll(root, 0700); err != nil {
				t.Fatal(err)
			}
			// A normal renderer rejects this escaped provider source; repair must
			// never inspect provider children or resolve their contents.
			if err := os.Symlink(workspace, filepath.Join(root, "gh")); err != nil {
				t.Fatal(err)
			}
			write(filepath.Join(config, "repos.yml"), "repos: []\n")
			policy := "[dev]\nauto_source = 'cli'\n[secrets]\nroot = '" + root + "'\n"
			write(filepath.Join(config, "wtc.toml"), policy)
			generated := filepath.Join(collection, ".env.collection")
			old := "# Generated environment\nWTC_CONFIG_ROOT='/old-control'\nTABA_CONFIG_ROOT='/old-control'\nCOLLECTION_PORT_BASE=42000\nGH_CONFIG_DIR='/old-control/gh'\nJIRA_CONFIG_FILE='/old-control/jira/config'\nTWG_SITE='old-site'\nAPP_SETTING='kept'\n"
			write(generated, old)
			local := filepath.Join(collection, ".env.collection.local")
			localData := "# User configuration\nLOCAL_SETTING='kept'\n"
			write(local, localData)
			mise := filepath.Join(collection, "mise.toml")
			write(mise, "# preserve user support config\n")
			mockBin := t.TempDir()
			marker := filepath.Join(workspace, "executed")
			hooks := filepath.Join(config, "hooks", "wtc")
			if err := os.MkdirAll(hooks, 0755); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"env.pre.sh", "env.post.sh"} {
				path := filepath.Join(hooks, name)
				write(path, "#!/bin/sh\ntouch '"+marker+"'\n")
				if err := os.Chmod(path, 0755); err != nil {
					t.Fatal(err)
				}
			}
			for _, name := range []string{"mise", "gh", "jira", "twg"} {
				path := filepath.Join(mockBin, name)
				write(path, "#!/bin/sh\ntouch '"+marker+"'\nexit 91\n")
				if err := os.Chmod(path, 0755); err != nil {
					t.Fatal(err)
				}
			}
			run := func(repair bool, extra ...string) (string, error) {
				args := []string{"--json", "env", "setup", "--collection", collection}
				if repair {
					args = append(args, "--repair-generated-roots")
				} else {
					args = append(args, "--skip-hooks")
				}
				cmd := exec.Command(bin, append(args, extra...)...)
				cmd.Dir = collection
				cmd.Env = []string{"HOME=" + t.TempDir(), "PATH=" + mockBin + ":/usr/bin:/bin"}
				if repair {
					cmd.Env = append(cmd.Env, "TABA_CONFIG_ROOT=/foreign-caller", "WTC_CONFIG_ROOT=/other-caller")
				}
				out, err := cmd.CombinedOutput()
				return string(out), err
			}
			if out, err := run(false, "--dry-run"); err == nil {
				t.Fatalf("ordinary setup accepted stale roots: %s", out)
			}
			if external {
				if out, err := run(true, "--dry-run"); err == nil || !strings.Contains(out, "allow_external") {
					t.Fatalf("caller aliases granted external access: %v %s", err, out)
				}
				write(filepath.Join(config, "wtc.toml"), policy+"allow_external = true\n")
			}
			out, err := run(true, "--dry-run")
			if err != nil {
				t.Fatalf("repair preview: %v %s", err, out)
			}
			var payload struct {
				Data struct {
					Env     string `json:"env"`
					Changed bool   `json:"changed"`
				} `json:"data"`
				Summary string `json:"summary"`
			}
			if err := json.Unmarshal([]byte(out), &payload); err != nil {
				t.Fatal(err)
			}
			preview := payload.Data.Env
			if !strings.Contains(preview, "WTC_CONFIG_ROOT='"+root+"'") || !strings.Contains(preview, "TABA_CONFIG_ROOT='"+root+"'") || strings.Contains(preview, "old-control") || strings.Contains(preview, "TWG_SITE=") || !strings.Contains(preview, "APP_SETTING='kept'") || !strings.Contains(payload.Summary, "reopen") {
				t.Fatalf("unsafe repair preview: %s", out)
			}
			if read(generated) != old || read(local) != localData {
				t.Fatal("preview mutated configuration")
			}
			for _, form := range [][]string{{"env", "setup"}, {"env"}} {
				args := append(append([]string{}, form...), "--collection", collection, "--repair-generated-roots", "--dry-run")
				plain := exec.Command(bin, args...)
				plain.Dir = collection
				plain.Env = []string{"HOME=" + t.TempDir(), "PATH=" + mockBin + ":/usr/bin:/bin"}
				var guidance bytes.Buffer
				plain.Stderr = &guidance
				raw, err := plain.Output()
				if err != nil || string(raw) != preview || !strings.Contains(guidance.String(), "refresh or reopen") {
					t.Fatalf("plain preview is not env-only with separate guidance: %v\nstdout: %s\nstderr: %s", err, raw, guidance.String())
				}
			}
			legacy := exec.Command(bin, "--json", "env", "--collection", collection, "--repair-generated-roots", "--dry-run")
			legacy.Dir = collection
			legacy.Env = []string{"HOME=" + t.TempDir(), "PATH=" + mockBin + ":/usr/bin:/bin"}
			if raw, err := legacy.CombinedOutput(); err != nil || json.Unmarshal(raw, &payload) != nil || payload.Data.Env != preview {
				t.Fatalf("legacy repair required dev bypass: %v %s", err, raw)
			}
			for _, asJSON := range []bool{false, true} {
				args := []string{"env"}
				if asJSON {
					args = append([]string{"--json"}, args...)
				}
				current := exec.Command(bin, args...)
				current.Dir = collection
				// Isolate the ordinary help-dispatch control from the intentionally
				// unavailable dev source; repair itself below has no bypass.
				current.Env = []string{"HOME=" + t.TempDir(), "PATH=" + mockBin + ":/usr/bin:/bin", "TABA_DEV_BUILD=off", "WTC_DEV_BUILD=off"}
				if raw, err := current.CombinedOutput(); err != nil || read(generated) != old || !strings.Contains(string(raw), "Usage:") {
					t.Fatalf("env without setup flags no longer shows help: %v %s", err, raw)
				}
				current = exec.Command(bin, append(args, "--repair-generated-roots")...)
				current.Dir = collection
				current.Env = []string{"HOME=" + t.TempDir(), "PATH=" + mockBin + ":/usr/bin:/bin"}
				raw, err := current.CombinedOutput()
				if err != nil || read(generated) != preview || read(local) != localData {
					t.Fatalf("current-directory legacy repair did not write: %v %s", err, raw)
				}
				if asJSON && (json.Unmarshal(raw, &payload) != nil || payload.Data.Env != preview || !payload.Data.Changed) {
					t.Fatalf("current-directory legacy repair omitted JSON result: %s", raw)
				}
				write(generated, old)
			}
			for _, flags := range [][]string{{"--all"}, {"--run-hooks"}} {
				if out, err := run(true, flags...); err == nil {
					t.Fatalf("repair widened effects: %s", out)
				}
			}
			write(local, localData+"WTC_CONFIG_ROOT='/conflicting-user-root'\n")
			if out, err := run(true); err == nil || !strings.Contains(out, ".env.collection.local") || read(generated) != old {
				t.Fatalf("local override was bypassed: %v %s", err, out)
			}
			write(local, localData)
			otherGenerated := filepath.Join(workspace, "other.env")
			write(otherGenerated, old)
			if err := os.Remove(generated); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(otherGenerated, generated); err != nil {
				t.Fatal(err)
			}
			if out, err := run(true); err == nil || read(otherGenerated) != old {
				t.Fatalf("repair followed generated symlink: %v %s", err, out)
			}
			if err := os.Remove(generated); err != nil {
				t.Fatal(err)
			}
			write(generated, old+"touch '"+marker+"'\n")
			if out, err := run(true); err == nil || !strings.Contains(out, "nonliteral") {
				t.Fatalf("repair evaluated generated shell: %v %s", err, out)
			}
			write(generated, old)
			if out, err := run(true); err != nil {
				t.Fatalf("repair write: %v %s", err, out)
			}
			if read(generated) != preview || read(local) != localData || read(mise) != "# preserve user support config\n" {
				t.Fatal("repair changed unexpected data")
			}
			out, err = run(true)
			if err != nil || json.Unmarshal([]byte(out), &payload) != nil || payload.Data.Changed {
				t.Fatalf("repair not idempotent: %v %s", err, out)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatal("repair executed a tool")
			}
			if err := os.Remove(filepath.Join(root, "gh")); err != nil {
				t.Fatal(err)
			}
			// The recovery assertion above uses a released binary with automatic
			// dev selection enabled. Ordinary setup can now open the collection;
			// disable the synthetic missing dev source for this final preview.
			write(filepath.Join(config, "wtc.toml"), strings.ReplaceAll(read(filepath.Join(config, "wtc.toml")), "auto_source = 'cli'", "auto_source = ''"))
			if out, err := run(false, "--dry-run"); err != nil {
				t.Fatalf("ordinary setup remains inaccessible after repair: %v %s", err, out)
			}
		})
	}
}
