package testenv_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"taba.sh/taba/internal/testenv"
)

func TestMain(m *testing.M) { testenv.Main(m) }

// A test binary started from a collection shell sees what CI gives it; one
// started by a test keeps the environment that test gave it.
func TestMainIsolatesTheCallersShell(t *testing.T) {
	if report := os.Getenv("TABA_TEST_REPORT"); report != "" {
		writeReport(t, report)
		return
	}
	home := t.TempDir()
	hostConfig := "[user]\n\tname = host\n[url \"https://leak.invalid/\"]\n\tinsteadOf = https://example.com/\n"
	if err := os.WriteFile(filepath.Join(home, ".gitconfig"), []byte(hostConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	shell := []string{"HOME=" + home, "WTC_CONFIG_ROOT=" + home, "TABA_COLLECTION=shell", "GIT_EDITOR=vi",
		"GIT_DIR=" + home, "GIT_TRACE2_EVENT=" + filepath.Join(home, "trace"), "WTC_TEST_KNOB=kept", "WTC_RECORDING_OUTPUT=kept"}

	outside := child(t, append(withoutMarker(os.Environ()), shell...))
	for _, gone := range []string{"WTC_CONFIG_ROOT", "TABA_COLLECTION", "GIT_EDITOR", "GIT_DIR"} {
		if value, ok := outside.env[gone]; ok {
			t.Errorf("%s=%s reached the tests", gone, value)
		}
	}
	for _, kept := range []string{"WTC_TEST_KNOB", "WTC_RECORDING_OUTPUT", "GIT_TRACE2_EVENT"} {
		if _, ok := outside.env[kept]; !ok {
			t.Errorf("%s was removed", kept)
		}
	}
	if want := "global\tuser.name=fixture\nglobal\tuser.email=fixture@example.invalid"; outside.git != want {
		t.Errorf("git config = %q, want %q", outside.git, want)
	}
	if _, err := os.Stat(outside.env["TABA_TEST_ENV"]); !os.IsNotExist(err) {
		t.Errorf("isolated directory %s left behind: %v", outside.env["TABA_TEST_ENV"], err)
	}

	inside := child(t, append(os.Environ(), "WTC_CONFIG_ROOT="+home, "GIT_EDITOR=vi"))
	if inside.env["WTC_CONFIG_ROOT"] != home || inside.env["GIT_EDITOR"] != "vi" {
		t.Errorf("child lost the environment its test gave it: WTC_CONFIG_ROOT=%q GIT_EDITOR=%q",
			inside.env["WTC_CONFIG_ROOT"], inside.env["GIT_EDITOR"])
	}
	if inside.env["TABA_TEST_ENV"] != os.Getenv("TABA_TEST_ENV") {
		t.Errorf("child isolated again: %s", inside.env["TABA_TEST_ENV"])
	}
}

type report struct {
	env map[string]string
	git string
}

func child(t *testing.T, env []string) report {
	t.Helper()
	path := filepath.Join(t.TempDir(), "report")
	cmd := exec.Command(os.Args[0], "-test.run=^TestMainIsolatesTheCallersShell$")
	cmd.Env = append(env, "TABA_TEST_REPORT="+path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("child: %v\n%s", err, out)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	environment, git, _ := strings.Cut(string(data), "\x00\x00")
	r := report{env: map[string]string{}, git: git}
	for _, setting := range strings.Split(environment, "\x00") {
		name, value, _ := strings.Cut(setting, "=")
		r.env[name] = value
	}
	return r
}

func writeReport(t *testing.T, path string) {
	cmd := exec.Command("git", "config", "--list", "--show-scope")
	cmd.Dir = t.TempDir()
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	data := strings.Join(os.Environ(), "\x00") + "\x00\x00" + strings.TrimSpace(string(out))
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func withoutMarker(env []string) []string {
	return slices.DeleteFunc(slices.Clone(env), func(setting string) bool {
		return strings.HasPrefix(setting, "TABA_TEST_ENV=")
	})
}
