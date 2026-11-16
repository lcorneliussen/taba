package wtc

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type observationPlan struct {
	Root         string
	Runner       string
	Tasks        string
	RunnerDelay  time.Duration
	ListDelay    time.Duration
	CancelMarker string
}

// The subprocess is a strict synthetic backend, never an installed runtime.
func TestRuntimeObservationBackend(t *testing.T) {
	if os.Getenv("WTC_OBSERVATION_FAKE") != "1" {
		return
	}
	args := os.Args
	for len(args) > 0 && args[0] != "--" {
		args = args[1:]
	}
	if len(args) == 0 {
		os.Exit(97)
	}
	args = args[1:]
	if reflect.DeepEqual(args, []string{"--version"}) {
		roots := filepath.SplitList(os.Getenv("WTC_OBSERVATION_ROOTS"))
		if len(roots) != 1 {
			os.Exit(98)
		}
		f, err := os.OpenFile(filepath.Join(roots[0], "fake-argv"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			os.Exit(98)
		}
		fmt.Fprintln(f, "--version")
		f.Close()
		fmt.Println("dekit 0.10.0")
		os.Exit(0)
	}
	if len(args) < 4 || args[0] != "-C" || args[2] != "--json" {
		os.Exit(97)
	}
	allowed := false
	for _, root := range filepath.SplitList(os.Getenv("WTC_OBSERVATION_ROOTS")) {
		allowed = allowed || root == args[1]
	}
	if !allowed {
		os.Exit(98)
	}
	var plan observationPlan
	data, err := os.ReadFile(filepath.Join(args[1], "fake-plan.json"))
	if err != nil || json.Unmarshal(data, &plan) != nil || plan.Root != args[1] {
		os.Exit(98)
	}
	command := strings.Join(args[3:], " ")
	if command != "runner status" && command != "ls" {
		os.Exit(99)
	}
	f, err := os.OpenFile(filepath.Join(plan.Root, "fake-argv"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		os.Exit(98)
	}
	fmt.Fprintln(f, strings.Join(args, " "))
	f.Close()
	if command == "runner status" {
		time.Sleep(plan.RunnerDelay)
		if plan.CancelMarker != "" {
			if os.WriteFile(plan.CancelMarker, []byte("cancel"), 0600) != nil {
				os.Exit(98)
			}
		}
		fmt.Println(plan.Runner)
	} else {
		time.Sleep(plan.ListDelay)
		fmt.Println(plan.Tasks)
	}
	os.Exit(0)
}

func observationFixture(t *testing.T) (*Context, *runtimeManifest, observationPlan) {
	t.Helper()
	t.Setenv("WTC_CONFIG_ROOT", "")
	t.Setenv("TABA_CONFIG_ROOT", "")
	c := fixture(t)
	root := c.runtimeRoot()
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(root, "fake-backend")
	fixtureFile(t, binary, "#!/bin/sh\nexec '"+strings.ReplaceAll(exe, "'", "'\\''")+"' -test.run=^TestRuntimeObservationBackend$ -- \"$@\"\n", 0700)
	t.Setenv("WTC_OBSERVATION_FAKE", "1")
	t.Setenv("WTC_OBSERVATION_ROOTS", root)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	c.Config.Runtime.Backend = "dekit"
	c.Config.Runtime.Binary = binary
	fixtureFile(t, filepath.Join(c.Harness, "wtc.toml"), "[runtime]\nbackend = 'dekit'\nbinary = '"+binary+"'\n", 0600)
	m := &runtimeManifest{Schema: 1, Root: root, Binary: binary, Resources: []runtimeResource{{Dir: "widget", Repo: "widget", State: "provision-hook-completed"}}}
	for _, name := range []string{"web", "stopped"} {
		path := "widget/" + name
		m.Tasks = append(m.Tasks, runtimeTask{RuntimeItem: RuntimeItem{Collection: filepath.Base(c.Collection), Dir: "widget", Repo: "widget", Path: path, Kind: "service"}, Log: filepath.Join(root, "logs", path+".log")})
	}
	if err := runtimeSaveManifest(m); err != nil {
		t.Fatal(err)
	}
	fixtureFile(t, filepath.Join(root, "dekit.yaml"), "tasks: {} # pending synthetic declaration\n", 0600)
	plan := observationPlan{Root: root, Runner: `{"status":"running"}`, Tasks: `{"tasks":[{"path":"widget/web","state":"ready"},{"path":"widget/stopped","state":"exited","exit_code":17,"reason":"task stopped"}]}`}
	writeObservationPlan(t, plan)
	old := runtimeAnswerCheck
	runtimeAnswerCheck = func(string) (bool, string) { t.Fatal("unexpected address probe"); return false, "" }
	t.Cleanup(func() { runtimeAnswerCheck = old })
	return c, m, plan
}
func writeObservationPlan(t *testing.T, plan observationPlan) {
	t.Helper()
	b, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	fixtureFile(t, filepath.Join(plan.Root, "fake-plan.json"), string(b), 0600)
}
func observationCalls(t *testing.T, m *runtimeManifest) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(m.Root, "fake-argv"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(b)
}
func observationUnchanged(t *testing.T, c *Context, before map[string]string) {
	t.Helper()
	after := discoverySnapshot(t, c.Collection)
	calls := filepath.Join(c.Collection, runtimeHome(c.Collection), "runtime", "fake-argv")
	delete(before, calls)
	delete(after, calls)
	if !reflect.DeepEqual(before, after) {
		for path, value := range after {
			if before[path] != value {
				t.Errorf("changed fixture path %s: before=%q after=%q", path, before[path], value)
			}
		}
		t.Fatal("observation changed fixture inputs")
	}
}
func observationUnknown(t *testing.T, items []RuntimeItem) {
	t.Helper()
	for _, item := range items[:2] {
		if item.State != "unknown" || item.Error == "" {
			t.Fatalf("unconfirmed task observation: %+v", item)
		}
	}
	if items[2].State != "provision-hook-completed" {
		t.Fatalf("lost resource fact: %+v", items)
	}
}

func TestRuntimeObservationPublicBudget(t *testing.T) {
	c, m, plan := observationFixture(t)
	plan.RunnerDelay = 7 * time.Second
	writeObservationPlan(t, plan)
	before := discoverySnapshot(t, c.Collection)
	start := time.Now()
	items := c.RuntimeStatus()
	elapsed := time.Since(start)
	observationUnknown(t, items)
	if elapsed > 9*time.Second {
		t.Fatalf("native observation exceeded budget allowance: %s", elapsed)
	}
	if strings.Contains(observationCalls(t, m), "--json ls") {
		t.Fatal("dispatched task list after runner timeout")
	}
	observationUnchanged(t, c, before)
}
func TestRuntimeObservationSharedBudget(t *testing.T) {
	c, m, plan := observationFixture(t)
	plan.RunnerDelay = 100 * time.Millisecond
	plan.ListDelay = 5 * time.Second
	writeObservationPlan(t, plan)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	items := c.runtimeStatusContext(ctx, m)
	observationUnknown(t, items)
	calls := observationCalls(t, m)
	if strings.Count(calls, "runner status") != 1 || strings.Count(calls, "--json ls") != 1 {
		t.Fatalf("calls: %s", calls)
	}
	if ctx.Err() != context.DeadlineExceeded {
		t.Fatal("task list did not consume the shared deadline")
	}
}

type observationCancelContext struct {
	context.Context
	cancel context.CancelFunc
	marker string
}

func (c observationCancelContext) Err() error {
	if _, err := os.Stat(c.marker); err == nil {
		c.cancel()
	}
	return c.Context.Err()
}
func TestRuntimeObservationCancellation(t *testing.T) {
	for _, between := range []bool{false, true} {
		t.Run(fmt.Sprint(between), func(t *testing.T) {
			c, m, plan := observationFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var observation context.Context = ctx
			if between {
				plan.CancelMarker = filepath.Join(t.TempDir(), "cancel")
				writeObservationPlan(t, plan)
				observation = observationCancelContext{ctx, cancel, plan.CancelMarker}
			} else {
				cancel()
			}
			items := c.runtimeStatusContext(observation, m)
			observationUnknown(t, items)
			calls := observationCalls(t, m)
			if strings.Contains(calls, "--json ls") || (!between && calls != "") {
				t.Fatalf("dispatched after cancellation: %s", calls)
			}
		})
	}
}
func TestRuntimeObservationReplies(t *testing.T) {
	for _, tc := range []struct {
		name, runner, tasks, want string
		hasError                  bool
	}{
		{"starting", `{"status":"starting"}`, "", "checking", false},
		{"absent", `{"status":"absent"}`, "", "absent", false},
		{"malformed", `{`, "", "unknown", true},
		{"no status", `{"version":"0.10.0"}`, "", "unknown", true},
		{"versionless running", `{"status":"running"}`, `{"tasks":[{"path":"widget/web","state":"starting","reason":"startup probe"},{"path":"widget/stopped","state":"exited","exit_code":17}]}`, "starting", false},
		{"missing task", `{"status":"running"}`, `{"tasks":[]}`, "missing", false},
		{"bad task JSON", `{"status":"running"}`, `{`, "unknown", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, m, plan := observationFixture(t)
			plan.Runner = tc.runner
			plan.Tasks = tc.tasks
			writeObservationPlan(t, plan)
			before := discoverySnapshot(t, c.Collection)
			for i := 0; i < 2; i++ {
				items := c.RuntimeStatus()
				if items[0].State != tc.want || (items[0].Error != "") != tc.hasError {
					t.Fatalf("items: %+v", items)
				}
				if tc.name == "starting" && !strings.Contains(items[0].Reason, "task state is unavailable") {
					t.Fatal("lost runner provenance")
				}
				if tc.name == "versionless running" && (items[1].State != "exited" || items[1].ExitCode == nil || *items[1].ExitCode != 17) {
					t.Fatal("lost stopped task fact")
				}
			}
			for _, line := range strings.Split(strings.TrimSpace(observationCalls(t, m)), "\n") {
				if !strings.HasSuffix(line, "runner status") && !strings.HasSuffix(line, "--json ls") {
					t.Fatalf("unexpected operation: %s", line)
				}
			}
			observationUnchanged(t, c, before)
		})
	}
}
func TestRuntimeObservationControlBudgetUnchanged(t *testing.T) {
	c, _, plan := observationFixture(t)
	plan.RunnerDelay = 5200 * time.Millisecond
	writeObservationPlan(t, plan)
	items := c.runtimeStatusOnly()
	if items[0].State != "ready" || items[1].State != "exited" {
		t.Fatalf("control polling inherited the shorter observation budget: %+v", items)
	}
}

func TestRuntimeObservationWorkspaceBudgetAndTrustedBinary(t *testing.T) {
	c, m, plan := observationFixture(t)
	poison := filepath.Join(m.Root, "foreign-backend")
	marker := filepath.Join(m.Root, "poison-executed")
	fixtureFile(t, poison, "#!/bin/sh\nprintf poison > '"+marker+"'\nexit 99\n", 0700)
	m.Binary = poison
	if err := runtimeSaveManifest(m); err != nil {
		t.Fatal(err)
	}
	plan.RunnerDelay = 100 * time.Millisecond
	plan.ListDelay = 7 * time.Second
	writeObservationPlan(t, plan)
	before := discoverySnapshot(t, c.Collection)
	var snapshot StatusSnapshot
	c.statusRuntime(&snapshot, true)
	observationUnknown(t, snapshot.Runtime)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("sweep executed manifest-selected binary")
	}
	if !strings.Contains(observationCalls(t, m), "--json ls") {
		t.Fatal("sweep did not reach task observation")
	}
	observationUnchanged(t, c, before)
}

func TestRuntimeObservationPreservesManagedFactsAndDeclarations(t *testing.T) {
	c, m, plan := observationFixture(t)
	m.Tasks[1].Kind = "endpoint"
	m.Tasks[1].ManagedTunnel = true
	m.Tasks[1].TunnelName = "named"
	m.Tasks[1].TunnelIntent = "on"
	m.Tasks[1].DefinitionID = "synthetic-definition"
	m.Tasks[1].URL = "https://example.invalid"
	m.Tasks[1].Registered = true
	plan.Runner = `{"status":"starting"}`
	writeObservationPlan(t, plan)
	before := discoverySnapshot(t, c.Collection)
	items := runtimeTaskDiagnostics(c.runtimeStatusObservation(m))
	if items[1].State != "checking" || !items[1].ManagedTunnel || items[1].TunnelIntent != "on" || items[1].DefinitionID != "synthetic-definition" || items[1].URL != "https://example.invalid" || !items[1].Registered {
		t.Fatalf("lost native-observation facts: %+v", items)
	}
	observationUnchanged(t, c, before)
	// Publication still requires the managed journal; an observed task does not grant ownership.
	m.Schema = 2
	if err := runtimeSaveManifest(m); err != nil {
		t.Fatal(err)
	}
	published := c.RuntimeStatus()
	found := false
	for _, item := range published {
		if strings.Contains(item.Error, "managed tunnel journal is missing") {
			found = true
		}
		if item.ManagedTunnel {
			t.Fatal("published managed ownership without journal")
		}
	}
	if !found {
		t.Fatalf("missing ownership diagnostic: %+v", published)
	}
	c, _, _ = observationFixture(t)
	fixtureGit(t, "-C", c.Harness, "init", "-q", "-b", "main")
	fixtureGit(t, "-C", c.Harness, "remote", "add", "origin", "https://example.invalid/harness-example.git")
	app := filepath.Join(c.Collection, "widget")
	fixtureFile(t, filepath.Join(app, "README.md"), "synthetic\n", 0600)
	fixtureGit(t, "-C", app, "init", "-q", "-b", "main")
	fixtureFile(t, c.runtimeLocalFragment("widget"), "tasks: {new: {cmd: [echo, synthetic]}}\n", 0600)
	before = discoverySnapshot(t, c.Collection)
	published = c.RuntimeStatus()
	found = false
	for _, item := range published {
		if item.Path == "widget/new" && item.State == "unconfigured" {
			found = true
		}
	}
	if !found {
		t.Fatalf("lost declaration: %+v", published)
	}
	observationUnchanged(t, c, before)
}
