package main

import (
	"crypto/sha256"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"taba.sh/taba/internal/openclaw"
	"taba.sh/taba/internal/wtc"
)

// Include git refs, indexes and config, as well as collection links and files.
func evictionSnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	result := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		info, err := d.Info()
		if err != nil {
			return err
		}
		value := info.Mode().String()
		if d.Type()&os.ModeSymlink != 0 {
			target, e := os.Readlink(path)
			if e != nil {
				return e
			}
			value += " " + target
		} else if !d.IsDir() {
			data, e := os.ReadFile(path)
			if e != nil {
				return e
			}
			value += fmt.Sprintf(" %x", sha256.Sum256(data))
		}
		result[rel] = value
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func evictionPlan(t *testing.T, worktree, base string, separate bool, worker string) (string, error) {
	t.Helper()
	savedOut, savedErr := os.Stdout, os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	discard, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout, os.Stderr = w, discard
	defer func() { os.Stdout, os.Stderr = savedOut, savedErr; r.Close(); w.Close(); discard.Close() }()
	runErr := addRepoIntents([]string{"api"}, worktree, base, "", separate, false, worker, "synthetic task")
	w.Close()
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(out), runErr
}

func evictionGateway(t *testing.T) string {
	t.Helper()
	for _, name := range []string{"WTC_CONFIG_ROOT", "TABA_CONFIG_ROOT", "WTC_GATEWAY_GIT", "TABA_GATEWAY_GIT", "WTC_DEV_BUILD", "TABA_DEV_BUILD"} {
		t.Setenv(name, "")
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	state, _ := harnessGateway(t)
	withGatewayConfig(t, `{"harnesses":[{"name":"platform","remote":"https://github.com/example-org/platform-harness.git","ref":"main"}]}`)
	return state
}

func retainedFixture(t *testing.T, state, name string, missing map[string]bool) string {
	t.Helper()
	folder := openclaw.NewCollectionFolder(state, name, "platform")
	if err := wtc.RecordGatewayHarness(folder, "platform", "https://github.com/example-org/platform-harness.git"); err != nil {
		t.Fatal(err)
	}
	var repos []wtc.LinkedRepo
	for _, repo := range []string{"platform-harness", "api"} {
		target := filepath.Join(state, "repos", repo)
		if missing[repo] {
			target = filepath.Join(state, "worktrees", "synthetic-"+repo, name)
		}
		repos = append(repos, wtc.LinkedRepo{Name: repo, Remote: "https://github.com/example-org/" + repo + ".git", DefaultRef: "origin/main", Path: target})
	}
	if _, err := wtc.LinkGatewayCollection(folder, repos); err != nil {
		t.Fatal(err)
	}
	return folder
}

func TestAddRepoMissingRetainedCheckoutRefusesBeforeWrites(t *testing.T) {
	for _, tc := range []struct {
		name     string
		missing  map[string]bool
		separate bool
		worker   string
	}{
		{"all-missing", map[string]bool{"api": true, "platform-harness": true}, false, ""},
		{"api-missing", map[string]bool{"api": true}, false, ""},
		{"harness-missing", map[string]bool{"platform-harness": true}, false, ""},
		{"separate", map[string]bool{"api": true}, true, ""},
		{"worker-target", map[string]bool{"api": true}, false, "probe"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := evictionGateway(t)
			target := "327-probe"
			if tc.worker != "" {
				target += "--" + tc.worker
			}
			retainedFixture(t, state, target, tc.missing)
			before := evictionSnapshot(t, state)
			out, err := evictionPlan(t, "327-probe", "origin/main", tc.separate, tc.worker)
			if err == nil || !strings.Contains(err.Error(), "missing checkout") || !strings.Contains(err.Error(), "unknown") {
				t.Errorf("want missing checkout with unknown ownership/recovery: output=%s error=%v", out, err)
			}
			if out != "" {
				t.Errorf("partial plan printed: %s", out)
			}
			if !reflect.DeepEqual(before, evictionSnapshot(t, state)) {
				t.Error("refusal changed files, links, git refs, index or config")
			} else {
				t.Log("refusal snapshot unchanged: files, links, refs, index and config; no plan output")
			}
		})
	}
}

func TestAddRepoRetainedObservationErrorsRefuseBeforeWrites(t *testing.T) {
	for _, kind := range []string{"ambiguous", "legacy-ambiguous", "escaping-link", "escaping-ancestor", "link-loop", "not-directory", "unsafe-folder", "dangling-folder", "unreadable-collection"} {
		t.Run(kind, func(t *testing.T) {
			if kind == "unreadable-collection" && os.Geteuid() == 0 {
				t.Skip("root can read chmod-zero directories")
			}
			state := evictionGateway(t)
			folder := retainedFixture(t, state, "327-probe", nil)
			switch kind {
			case "ambiguous", "legacy-ambiguous":
				other := filepath.Join(state, "wtc", "other", "327-probe")
				if kind == "legacy-ambiguous" {
					other = filepath.Join(state, "wtc-collections", "327-probe")
				}
				if err := os.MkdirAll(other, 0700); err != nil {
					t.Fatal(err)
				}
			case "escaping-link":
				os.Remove(filepath.Join(folder, "api"))
				if err := os.Symlink(filepath.Join(t.TempDir(), "missing"), filepath.Join(folder, "api")); err != nil {
					t.Fatal(err)
				}
			case "escaping-ancestor":
				os.Remove(filepath.Join(folder, "api"))
				outside := filepath.Join(t.TempDir(), "absent")
				if err := os.Symlink(outside, filepath.Join(state, "escape")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(state, "escape", "api"), filepath.Join(folder, "api")); err != nil {
					t.Fatal(err)
				}
			case "link-loop":
				os.Remove(filepath.Join(folder, "api"))
				if err := os.Symlink("api", filepath.Join(folder, "api")); err != nil {
					t.Fatal(err)
				}
			case "not-directory":
				os.Remove(filepath.Join(folder, "api"))
				file := filepath.Join(state, "file")
				os.WriteFile(file, []byte("fixture"), 0600)
				if err := os.Symlink(file, filepath.Join(folder, "api")); err != nil {
					t.Fatal(err)
				}
			case "dangling-folder":
				if err := os.Rename(folder, folder+"-saved"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(state, "missing-collection"), folder); err != nil {
					t.Fatal(err)
				}
			case "unsafe-folder":
				other := filepath.Join(t.TempDir(), "outside")
				if err := os.Rename(folder, other); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(other, folder); err != nil {
					t.Fatal(err)
				}
			}
			before := evictionSnapshot(t, state)
			if kind == "unreadable-collection" {
				if err := os.Chmod(folder, 0); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(folder, 0755) })
			}
			out, err := evictionPlan(t, "327-probe", "origin/main", false, "")
			if kind == "unreadable-collection" {
				if e := os.Chmod(folder, 0755); e != nil {
					t.Fatal(e)
				}
			}
			if err == nil {
				t.Errorf("unsafe observation accepted: %s", out)
			}
			if out != "" {
				t.Errorf("partial plan: %s", out)
			}
			if !reflect.DeepEqual(before, evictionSnapshot(t, state)) {
				t.Error("observation error changed state")
			}
		})
	}
}

func TestAddRepoHealthyAndFreshPlansRemainAvailable(t *testing.T) {
	for _, kind := range []string{"fresh", "healthy-duplicate", "worker-distinct", "legitimate-addition"} {
		t.Run(kind, func(t *testing.T) {
			state := evictionGateway(t)
			worker := ""
			if kind != "fresh" {
				folder := retainedFixture(t, state, "327-probe", nil)
				if kind == "worker-distinct" {
					worker = "probe"
					os.Remove(filepath.Join(folder, "api"))
					os.Symlink(filepath.Join(state, "missing-task-api"), filepath.Join(folder, "api"))
				}
				if kind == "legitimate-addition" {
					os.Remove(filepath.Join(folder, "api"))
				}
			}
			out, err := evictionPlan(t, "327-probe", "origin/main", false, worker)
			if err != nil || !strings.Contains(out, `"tool": "sessions_spawn"`) || !strings.Contains(out, `"worktreeBaseRef": "origin/main"`) {
				t.Fatalf("control changed: %s %v", out, err)
			}
		})
	}
}

func TestAddRepoWorkerTaskAmbiguityRefusesBeforeWrites(t *testing.T) {
	state := evictionGateway(t)
	retainedFixture(t, state, "327-probe", nil)
	if err := os.MkdirAll(filepath.Join(state, "wtc", "other", "327-probe"), 0755); err != nil {
		t.Fatal(err)
	}
	before := evictionSnapshot(t, state)
	out, err := evictionPlan(t, "327-probe", "origin/main", false, "probe")
	if err == nil || !strings.Contains(err.Error(), "ambiguous") || out != "" {
		t.Fatalf("worker task ambiguity: %s %v", out, err)
	}
	if !reflect.DeepEqual(before, evictionSnapshot(t, state)) {
		t.Error("worker refusal changed state")
	}
}

func TestAddRepoSandboxDoesNotInferFromLocalRetainedLinks(t *testing.T) {
	state := evictionGateway(t)
	retainedFixture(t, state, "327-probe", map[string]bool{"api": true, "platform-harness": true})
	sandboxMetadataServer(t, 200, strings.ReplaceAll(sandboxMetadata, "42-fix", "327-probe"))
	t.Setenv("OPENCLAW_STATE_DIR", state)
	before := evictionSnapshot(t, state)
	out, err := evictionPlan(t, "327-probe", "", false, "")
	if err != nil || !strings.Contains(out, `"projectId": "api-id"`) {
		t.Fatalf("sandbox metadata control changed: %s %v", out, err)
	}
	if !reflect.DeepEqual(before, evictionSnapshot(t, state)) {
		t.Error("sandbox plan changed local gateway evidence")
	}
}
