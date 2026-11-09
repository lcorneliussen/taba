package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"taba.sh/taba/internal/wtc"
)

func TestTabaEnvironmentConflictKeepsJSONOutputClean(t *testing.T) {
	collection := filepath.Join(t.TempDir(), "demo")
	harness := filepath.Join(collection, "harness")
	if err := os.MkdirAll(harness, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(harness, ".harness-repos.yml"), []byte("repos:\n  - name: widget\n    remote: https://example.invalid/widget.git\n    default_ref: origin/main\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(harness, ".wtc-cli-version"), []byte("0.1.1\n"), 0644); err != nil {
		t.Fatal(err)
	}
	legacy, current := filepath.Join(t.TempDir(), "legacy-private-value"), filepath.Join(t.TempDir(), "current-private-value")
	cmd := exec.Command("go", "run", ".", "env", "setup", "--collection", collection, "--dry-run", "--json")
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "WTC_CONFIG_ROOT=") && !strings.HasPrefix(value, "TABA_CONFIG_ROOT=") {
			cmd.Env = append(cmd.Env, value)
		}
	}
	cmd.Env = append(cmd.Env, "WTC_CONFIG_ROOT="+legacy, "TABA_CONFIG_ROOT="+current)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("env preview failed: %v, %s", err, &stderr)
	}
	if !json.Valid(stdout.Bytes()) || !strings.Contains(stdout.String(), current) || !strings.Contains(stdout.String(), "WTC_CONFIG_ROOT='"+current+"'") || !strings.Contains(stdout.String(), "TABA_CONFIG_ROOT='"+current+"'") || strings.Contains(stdout.String(), legacy) {
		t.Fatalf("JSON output should carry the selected root under both spellings: %s", &stdout)
	}
	if strings.Count(stderr.String(), "TABA_CONFIG_ROOT and WTC_CONFIG_ROOT differ; using TABA_CONFIG_ROOT") != 1 || strings.Contains(stderr.String(), legacy) || strings.Contains(stderr.String(), current) {
		t.Fatalf("expected one value-free stderr warning: %s", &stderr)
	}
	if _, err := os.Stat(filepath.Join(collection, ".env.collection")); !os.IsNotExist(err) {
		t.Fatal("dry run wrote the generated environment")
	}
}

func TestCompatibility(t *testing.T) {
	cases := []struct {
		requirement, version string
		want                 bool
	}{
		{">=0.4,<0.5", "0.4.2", true},
		{">=0.4,<0.5", "0.5.0", false},
		{">=0.4,<0.5", "dev", false},
		{"invalid", "0.4.2", false},
		{">=0.4.1,<0.5", "0.4.2-dev.3+dev.gabc1234", true},
		{">=0.4.1,<0.5", "0.4.1+dev.gabc1234.dirty", true},
		{">=0.4,<0.5", "0.5.1-dev.1+dev.gabc1234", false},
	}
	for _, c := range cases {
		if got := compatible(c.requirement, c.version); got != c.want {
			t.Errorf("compatible(%q,%q)=%v", c.requirement, c.version, got)
		}
	}
}

func TestEnvPreflightsPinAndShowsMiseDryRun(t *testing.T) {
	collection := filepath.Join(t.TempDir(), "demo")
	harness := filepath.Join(collection, "harness")
	if err := os.MkdirAll(harness, 0755); err != nil {
		t.Fatal(err)
	}
	registry := "repos:\n  - name: example\n    remote: https://example.invalid/example.git\n    default_ref: origin/main\n"
	if err := os.WriteFile(filepath.Join(harness, ".harness-repos.yml"), []byte(registry), 0644); err != nil {
		t.Fatal(err)
	}
	envPath := filepath.Join(collection, ".env.collection")
	misePath := filepath.Join(collection, "mise.toml")
	for _, path := range []string{envPath, misePath} {
		if err := os.WriteFile(path, []byte("keep\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	pinPath := filepath.Join(harness, ".wtc-cli-version")
	if err := os.WriteFile(pinPath, []byte("latest\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "run", ".", "env", "--collection", collection)
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("accepted invalid pin: %s", out)
	}
	for _, path := range []string{envPath, misePath} {
		data, err := os.ReadFile(path)
		if err != nil || string(data) != "keep\n" {
			t.Fatalf("changed %s on invalid pin: %q, %v", path, data, err)
		}
	}
	if err := os.WriteFile(pinPath, []byte("0.1.1\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command("go", "run", ".", "env", "--collection", collection, "--dry-run")
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), `"github:lcorneliussen/taba" = "0.1.1"`) {
		t.Fatalf("dry run did not show pin: %s, %v", out, err)
	}
	cmd = exec.Command("go", "run", ".", "env")
	out, err = cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "list") || !strings.Contains(string(out), "setup") {
		t.Fatalf("bare env did not show its commands: %s, %v", out, err)
	}
	cmd = exec.Command("go", "run", ".", "env", "setup", "--collection", collection, "--dry-run")
	out, err = cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), `"github:lcorneliussen/taba" = "0.1.1"`) {
		t.Fatalf("named setup dry run did not show pin: %s, %v", out, err)
	}
}

func TestDevBuildVersionsPassTheHarnessRange(t *testing.T) {
	for _, describe := range []string{"v0.1.39-0-g69e3a79", "v0.1.39-3-g69e3a79", "v0.1.39-3-g69e3a79-dirty"} {
		v, err := wtc.DevVersion(describe)
		if err != nil || !compatible(">=0.1.39,<0.2", v) {
			t.Fatalf("dev build %s (%s) should satisfy the range its release does: %v", v, describe, err)
		}
	}
}

func TestTabaConfigInputsAtCLIEntryPoint(t *testing.T) {
	collection := filepath.Join(t.TempDir(), "demo")
	harness := filepath.Join(collection, "harness")
	if err := os.MkdirAll(harness, 0755); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{
		".harness-repos.yml": "repos:\n  - name: widget\n    remote: https://example.invalid/widget.git\n    default_ref: origin/main\n",
		"taba.toml":          "[herdr]\nsession = 'alias-session'\n",
		".taba-version":      "0.1.54\n",
	} {
		if err := os.WriteFile(filepath.Join(harness, name), []byte(data), 0644); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("go", "run", ".", "env", "setup", "--collection", collection, "--dry-run", "--json")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("alias config preview failed: %v, %s", err, &stderr)
	}
	var result struct {
		Data envResult `json:"data"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("invalid JSON preview: %s, %v", &stdout, err)
	}
	if !result.Data.DryRun || !strings.Contains(result.Data.Mise, `"github:lcorneliussen/taba" = "0.1.54"`) || !strings.Contains(result.Data.Env, "alias-session") {
		t.Fatalf("preview did not use selected inputs: %+v", result.Data)
	}
	for _, path := range []string{filepath.Join(harness, "wtc.toml"), filepath.Join(harness, ".wtc-cli-version"), filepath.Join(collection, ".env.collection")} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("dry run created %s: %v", path, err)
		}
	}
}

func TestUpgradeRolloutRefusesVersion(t *testing.T) {
	cmd := exec.Command("go", "run", ".", "upgrade", "--rollout", "--version", "0.1.1")
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "cannot be combined with --version") {
		t.Fatalf("--rollout --version accepted: %v %s", err, out)
	}
}
