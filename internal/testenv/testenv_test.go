package testenv_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"taba.sh/taba/internal/testenv"
)

func TestMain(m *testing.M) { testenv.Main(m, fixture("first"), fixture("second")) }

// fixture logs its cleanup to TABA_TEST_CLEANUP_LOG, and fails the cleanup
// when TABA_TEST_FAIL_CLEANUP names it.
func fixture(name string) func() func() error {
	return func() func() error {
		return func() error {
			if log := os.Getenv("TABA_TEST_CLEANUP_LOG"); log != "" {
				file, err := os.OpenFile(log, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
				if err != nil {
					return err
				}
				fmt.Fprintln(file, name)
				file.Close()
			}
			if os.Getenv("TABA_TEST_FAIL_CLEANUP") == name {
				return fmt.Errorf("%s cleanup failed", name)
			}
			return nil
		}
	}
}

// A fixture's cleanup error fails a run whose tests passed, and cleanups run
// in reverse order.
func TestMainFailsOnACleanupError(t *testing.T) {
	log := filepath.Join(t.TempDir(), "cleanups")
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), "TABA_TEST_CLEANUP_LOG="+log, "TABA_TEST_FAIL_CLEANUP=second")
	out, err := cmd.CombinedOutput()
	if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 1 || !strings.Contains(string(out), "second cleanup failed") {
		t.Fatalf("run with a failing cleanup: %v\n%s", err, out)
	}
	if got, _ := os.ReadFile(log); string(got) != "second\nfirst\n" {
		t.Fatalf("cleanup order = %q, want second, then first", got)
	}
}

// A package whose tests skip testenv.Main inherits the developer's shell.
func TestEveryTestPackageCallsMain(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("module root: %v", err)
	}
	var checked []string
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || !entry.IsDir() {
			return err
		}
		name := entry.Name()
		if path != root && (strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") || name == "testdata") {
			return filepath.SkipDir
		}
		tests, err := filepath.Glob(filepath.Join(path, "*_test.go"))
		if err != nil || len(tests) == 0 {
			return err
		}
		dir, _ := filepath.Rel(root, path)
		checked = append(checked, dir)
		if !callsMain(t, tests) {
			t.Errorf("%s has tests but no TestMain calling testenv.Main; use:\n\n"+
				"import \"taba.sh/taba/internal/testenv\"\n\n"+
				"func TestMain(m *testing.M) { testenv.Main(m) }", dir)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(checked, filepath.Join("internal", "testenv")) {
		t.Fatalf("walk missed this package: %v", checked)
	}
}

func callsMain(t *testing.T, files []string) bool {
	t.Helper()
	for _, file := range files {
		parsed, err := parser.ParseFile(token.NewFileSet(), file, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range parsed.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || fn.Name.Name != "TestMain" || fn.Body == nil {
				continue
			}
			found := false
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Main" {
					if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "testenv" {
						found = true
					}
				}
				return !found
			})
			return found
		}
	}
	return false
}

// A test binary started from a collection shell sees what CI gives it. One
// started by a test keeps what that test and its package's fixtures set.
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
	shell := []string{"HOME=" + home, "WTC_CONFIG_ROOT=" + home, "TABA_COLLECTION=shell", "TABA_BITBUCKET_TRANSPORT=bb",
		"GIT_EDITOR=vi", "GIT_DIR=" + home, "GIT_CONFIG_GLOBAL=" + filepath.Join(home, ".gitconfig"),
		"GIT_TRACE2_EVENT=" + filepath.Join(home, "trace"), "WTC_TEST_KNOB=kept", "WTC_RECORDING_OUTPUT=kept",
		"REX_BLOCK=block:1", "REX_BIN_DIR=" + home, "HERDR_ENV=1", "HERDR_SESSION=shell", "HERDR_SOCKET_PATH=" + home, "HARNESS_HERDR_SESSION=shell"}

	outside := child(t, append(withoutMarker(os.Environ()), shell...))
	for _, gone := range []string{"WTC_CONFIG_ROOT", "TABA_COLLECTION", "TABA_BITBUCKET_TRANSPORT", "GIT_EDITOR", "GIT_DIR",
		"REX_BLOCK", "REX_BIN_DIR", "HERDR_ENV", "HERDR_SESSION", "HERDR_SOCKET_PATH", "HARNESS_HERDR_SESSION"} {
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

	parentConfig := filepath.Join(home, "parent-gitconfig")
	if err := os.WriteFile(parentConfig, []byte("[user]\n\tname = parent\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", parentConfig)
	t.Setenv("TABA_BITBUCKET_TRANSPORT", "bb")
	t.Setenv("WTC_CONFIG_ROOT", home)
	t.Setenv("GIT_EDITOR", "vi")
	inside := child(t, os.Environ())
	for _, name := range []string{"GIT_CONFIG_GLOBAL", "TABA_BITBUCKET_TRANSPORT", "WTC_CONFIG_ROOT", "GIT_EDITOR", "TABA_TEST_ENV"} {
		if inside.env[name] != os.Getenv(name) {
			t.Errorf("child changed %s to %q, want %q", name, inside.env[name], os.Getenv(name))
		}
	}
	if want := "global\tuser.name=parent"; inside.git != want {
		t.Errorf("child git config = %q, want %q", inside.git, want)
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
