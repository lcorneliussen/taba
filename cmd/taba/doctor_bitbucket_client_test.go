package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestDoctorReportsBitbucketClientAsAdvisory runs the real doctor in a
// synthetic collection: an unusable selection is reported, never echoed, and
// does not fail doctor.
func TestDoctorReportsBitbucketClientAsAdvisory(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "taba")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %s: %v", out, err)
	}
	workspace := filepath.Join(root, "workspace")
	collection := filepath.Join(workspace, "sample")
	for path, body := range map[string]string{
		filepath.Join(collection, "harness", ".harness-repos.yml"): "repos:\n  - name: harness\n    remote: https://example.invalid/harness.git\n    default_ref: origin/main\n",
		filepath.Join(collection, "harness", "taba.toml"):          "",
		filepath.Join(collection, "SCOPE.md"):                      "task\n",
		filepath.Join(workspace, ".config", "taba.env"):            "TABA_BITBUCKET_TRANSPORT=secret-value\n",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	run := func(args ...string) (string, error) {
		cmd := exec.Command(binary, args...)
		cmd.Dir = collection
		env := []string{}
		for _, kv := range os.Environ() {
			if !strings.HasPrefix(kv, "TABA_BITBUCKET_TRANSPORT=") && !strings.HasPrefix(kv, "WTC_BITBUCKET_TRANSPORT=") {
				env = append(env, kv)
			}
		}
		cmd.Env = append(env, "XDG_CONFIG_HOME="+filepath.Join(root, "xdg-config"), "XDG_DATA_HOME="+filepath.Join(root, "xdg-data"), "WTC_CONFIG_ROOT="+filepath.Join(workspace, ".config"), "TABA_CONFIG_ROOT="+filepath.Join(workspace, ".config"))
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	out, err := run("doctor", "--json")
	var result struct {
		OK   bool             `json:"ok"`
		Data []map[string]any `json:"data"`
	}
	if jsonErr := json.Unmarshal([]byte(out), &result); jsonErr != nil {
		t.Fatalf("doctor --json: %v %v\n%s", err, jsonErr, out)
	}
	var row map[string]any
	for _, check := range result.Data {
		if check["name"] == "bitbucket-client" {
			row = check
		}
	}
	if row == nil || row["ok"] != false || !strings.Contains(row["detail"].(string), "advisory: unsupported TABA_BITBUCKET_TRANSPORT value from taba.env") {
		t.Fatalf("missing advisory row: %v", result.Data)
	}
	required := true
	for _, check := range result.Data {
		switch check["name"] {
		case "git", "registry", "compatibility", "requirements":
			required = required && check["ok"] == true
		}
	}
	if !required {
		t.Fatalf("fixture's required checks are not green; the advisory property is untested:\n%s", out)
	}
	if !result.OK || err != nil {
		t.Fatalf("advisory row failed doctor: ok=%v err=%v\n%s", result.OK, err, out)
	}
	if strings.Contains(out, "secret-value") {
		t.Fatal("doctor echoed the configured value")
	}
	text, _ := run("doctor")
	if !strings.Contains(text, "bitbucket-client") || strings.Contains(text, "secret-value") {
		t.Fatalf("text doctor row: %s", text)
	}
}
