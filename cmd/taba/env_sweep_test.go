package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnvSweepUsesTargetRegistriesAndKeepsGoing(t *testing.T) {
	workspace := t.TempDir()
	for name, offset := range map[string]string{"alpha": "1", "beta": "2"} {
		dir := filepath.Join(workspace, name)
		harness := filepath.Join(dir, "harness")
		if err := os.MkdirAll(harness, 0755); err != nil {
			t.Fatal(err)
		}
		registry := "repos:\n  - name: " + name + "\n    remote: https://example.invalid/fixture.git\n    default_ref: origin/main\n    port_offset: " + offset + "\n"
		if err := os.WriteFile(filepath.Join(harness, ".harness-repos.yml"), []byte(registry), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".env.collection"), []byte("COLLECTION_PORT_BASE=42000\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	alphaLocal := filepath.Join(workspace, "alpha", ".env.collection.local")
	if err := os.WriteFile(alphaLocal, []byte("LOCAL_ONLY=kept\n"), 0600); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(workspace, "alpha", "hook-ran")
	hookDir := filepath.Join(workspace, "alpha", "harness", "hooks", "wtc")
	if err := os.MkdirAll(hookDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hookDir, "env.pre.sh"), []byte("#!/bin/sh\nprintf 'yes\\n' > '"+marker+"'\n"), 0755); err != nil {
		t.Fatal(err)
	}
	brokenHarness := filepath.Join(workspace, "broken", "harness")
	if err := os.MkdirAll(brokenHarness, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(brokenHarness, ".harness-repos.yml"), []byte("repos: []\n"), 0644); err != nil {
		t.Fatal(err)
	}
	renderBroken := filepath.Join(workspace, "render-broken", "harness")
	if err := os.MkdirAll(renderBroken, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(renderBroken, ".harness-repos.yml"), []byte("repos:\n  - name: fixture\n    remote: https://example.invalid/fixture.git\n    default_ref: origin/main\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(renderBroken, ".wtc-cli-version"), []byte("invalid\n"), 0644); err != nil {
		t.Fatal(err)
	}
	mockBin := t.TempDir()
	trustMarker := filepath.Join(workspace, "mise-trust-ran")
	if err := os.WriteFile(filepath.Join(mockBin, "mise"), []byte("#!/bin/sh\nprintf 'trusted\\n' >> '"+trustMarker+"'\n"), 0755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "wtc")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	run := func(dry, withHooks bool) map[string]any {
		args := []string{"--json", "env", "--all", "--collection", filepath.Join(workspace, "alpha")}
		if dry {
			args = append(args, "--dry-run")
		}
		if withHooks {
			args = append(args, "--run-hooks")
		}
		cmd := exec.Command(bin, args...)
		cmd.Env = append(os.Environ(), "PATH="+mockBin+":/usr/bin:/bin", "WTC_CONFIG_ROOT="+filepath.Join(workspace, "control"))
		out, err := cmd.Output()
		if err == nil {
			t.Fatalf("sweep accepted broken target: %s", out)
		}
		var payload map[string]any
		if jsonErr := json.Unmarshal(out, &payload); jsonErr != nil {
			t.Fatalf("JSON: %v\n%s", jsonErr, out)
		}
		if payload["ok"] != false {
			t.Fatalf("failure not reported: %v", payload)
		}
		return payload
	}
	for _, dry := range []bool{true, false} {
		payload := run(dry, false)
		data := payload["data"].(map[string]any)
		if data["failed"] != float64(2) || len(data["results"].([]any)) != 4 {
			t.Fatalf("wrong sweep: %v", data)
		}
		if data["hooks_run"] != false {
			t.Fatalf("default sweep ran target hooks: %v", data)
		}
		byName := map[string]map[string]any{}
		for _, raw := range data["results"].([]any) {
			item := raw.(map[string]any)
			byName[filepath.Base(item["collection"].(string))] = item
		}
		if byName["broken"]["error"] == nil || byName["render-broken"]["error"] == nil || byName["alpha"]["error"] != nil || byName["beta"]["error"] != nil {
			t.Fatalf("lost per-target failure status: %v", byName)
		}
		for _, name := range []string{"alpha", "beta"} {
			dir := filepath.Join(workspace, name)
			env, err := os.ReadFile(filepath.Join(dir, ".env.collection"))
			if err != nil {
				t.Fatal(err)
			}
			if dry {
				if string(env) != "COLLECTION_PORT_BASE=42000\n" {
					t.Fatalf("dry run wrote %s: %s", name, env)
				}
			} else if !strings.Contains(string(env), strings.ToUpper(name)+"_PORT=4200"+map[string]string{"alpha": "1", "beta": "2"}[name]) {
				t.Fatalf("%s did not use its registry: %s", name, env)
			}
		}
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Fatal("default sweep invoked target hook")
		}
		if _, err := os.Stat(trustMarker); !os.IsNotExist(err) {
			t.Fatal("default sweep trusted target mise configuration")
		}
	}
	if data := run(false, true)["data"].(map[string]any); data["hooks_run"] != true {
		t.Fatalf("explicit hook opt-in was ignored: %v", data)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("opted-in target hook did not run: %v", err)
	}
	if _, err := os.Stat(trustMarker); err != nil {
		t.Fatalf("opted-in mise trust did not run: %v", err)
	}
	if data, err := os.ReadFile(alphaLocal); err != nil || string(data) != "LOCAL_ONLY=kept\n" {
		t.Fatalf("local environment changed: %s, %v", data, err)
	}
}

func TestEnvSweepDryRunReservesFreshPortBlocks(t *testing.T) {
	workspace := t.TempDir()
	for _, name := range []string{"alpha", "beta"} {
		harness := filepath.Join(workspace, name, "harness")
		if err := os.MkdirAll(harness, 0755); err != nil {
			t.Fatal(err)
		}
		registry := "repos:\n  - name: fixture\n    remote: https://example.invalid/fixture.git\n    default_ref: origin/main\n    port_offset: 1\n"
		if err := os.WriteFile(filepath.Join(harness, ".harness-repos.yml"), []byte(registry), 0644); err != nil {
			t.Fatal(err)
		}
	}
	bin := filepath.Join(t.TempDir(), "wtc")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	args := []string{"--json", "env", "--all", "--collection", filepath.Join(workspace, "alpha")}
	cmd := exec.Command(bin, append(args, "--dry-run")...)
	cmd.Env = append(os.Environ(), "PATH=/usr/bin:/bin", "WTC_CONFIG_ROOT="+filepath.Join(workspace, "control"))
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("preview failed: %v\n%s", err, out)
	}
	var payload struct {
		Data struct {
			Results []struct {
				Collection string `json:"collection"`
				Env        string `json:"env"`
			} `json:"results"`
		} `json:"data"`
	}
	if err := json.Unmarshal(out, &payload); err != nil || len(payload.Data.Results) != 2 {
		t.Fatalf("preview JSON: %v\n%s", err, out)
	}
	previews := map[string]string{}
	for _, item := range payload.Data.Results {
		previews[item.Collection] = item.Env
		if _, err := os.Stat(filepath.Join(item.Collection, ".env.collection")); !os.IsNotExist(err) {
			t.Fatal("dry run wrote environment")
		}
	}
	if !strings.Contains(previews[filepath.Join(workspace, "alpha")], "COLLECTION_PORT_BASE=42000\n") ||
		!strings.Contains(previews[filepath.Join(workspace, "beta")], "COLLECTION_PORT_BASE=42100\n") {
		t.Fatalf("preview reused a port block: %v", previews)
	}
	cmd = exec.Command(bin, args...)
	cmd.Env = append(os.Environ(), "PATH=/usr/bin:/bin", "WTC_CONFIG_ROOT="+filepath.Join(workspace, "control"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("write failed: %v\n%s", err, out)
	}
	for dir, preview := range previews {
		actual, err := os.ReadFile(filepath.Join(dir, ".env.collection"))
		if err != nil || string(actual) != preview {
			t.Fatalf("preview differed from write for %s: %v\npreview=%s\nactual=%s", dir, err, preview, actual)
		}
	}
}
