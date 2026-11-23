package wtc

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"gopkg.in/yaml.v3"
)

// rootDekitFixture is a collection whose widget repository declares its tasks
// only in a root dekit.yaml tree.
func rootDekitFixture(t *testing.T, files map[string]string) (*Context, string) {
	t.Helper()
	c := runtimeFixture(t)
	repo := filepath.Join(c.Collection, "widget")
	if err := os.RemoveAll(filepath.Join(repo, ".harness")); err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		fixtureFile(t, filepath.Join(repo, filepath.FromSlash(name)), content, 0644)
	}
	return c, repo
}

func rootDekitPlanTasks(t *testing.T, c *Context) (*runtimeManifest, map[string]map[string]any) {
	t.Helper()
	m, data, err := c.runtimePlan("dekit")
	if err != nil {
		t.Fatal(err)
	}
	var native struct {
		Kernel map[string]any            `yaml:"kernel"`
		Tasks  map[string]map[string]any `yaml:"tasks"`
		OnInit any                       `yaml:"on_init"`
	}
	if err := yaml.Unmarshal(data, &native); err != nil {
		t.Fatal(err)
	}
	if native.Kernel["path"] != "dekit" || native.OnInit != nil {
		t.Fatalf("runner settings leaked into the collection runner: %s", data)
	}
	return m, native.Tasks
}

func TestRootDekitRendersNativeTreeUnderRepository(t *testing.T) {
	c, repo := rootDekitFixture(t, map[string]string{
		"dekit.yaml": `kernel: {path: bin/poison-kernel}
on_init: {command: start, target: web/main}
on_idle: {command: down}
log: {level: debug}
vars: {unused: 1}
defaults:
  cwd: app
  env: {SHARED: one, OTHER: two}
  stop: {signal: SIGINT, timeout: 30s}
load:
  - file: services/web.yaml
    at: web
tasks:
  db/main:
    cmd: [sh, -c, 'touch poison-task']
    ready: {http: "http://127.0.0.1:5432/"}
`,
		"services/web.yaml": `tasks:
  main:
    cmd: [sh, -c, 'touch poison-task']
    deps: [/db/main, helper]
    cwd: sub
    env: {OWN: yes}
    stop: {timeout: 5s}
  helper:
    cmd: [sh, -c, 'touch poison-task']
    script: tools/run.js
`,
		"app/.keep":          "",
		"services/sub/.keep": "",
	})
	poison := filepath.Join(c.Collection, "poison-env")
	fixtureFile(t, filepath.Join(c.Collection, ".env.collection.local"), "touch "+poison+"\n", 0600)
	m, native := rootDekitPlanTasks(t, c)
	base, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, task := range m.Tasks {
		paths = append(paths, task.Path)
	}
	if want := []string{"widget/db/main", "widget/web/helper", "widget/web/main"}; !reflect.DeepEqual(paths, want) {
		t.Fatalf("tasks %v, want %v", paths, want)
	}
	main := native["widget/web/main"]
	if !reflect.DeepEqual(main["deps"], []any{"widget/db/main", "widget/web/helper"}) {
		t.Fatalf("deps follow the mount and the repository root: %v", main["deps"])
	}
	if main["cwd"] != filepath.Join(base, "services", "sub") {
		t.Fatalf("a fragment's cwd is relative to the fragment: %v", main["cwd"])
	}
	if !reflect.DeepEqual(main["env"], map[string]any{"OWN": "yes"}) {
		t.Fatalf("a task's env replaces the defaults whole: %v", main["env"])
	}
	if !reflect.DeepEqual(main["stop"], map[string]any{"signal": "SIGINT", "timeout": "5s"}) {
		t.Fatalf("stop combines how and timeout: %v", main["stop"])
	}
	if log, _ := main["log"].(map[string]any); log["file"] != filepath.Join(m.Root, "logs", "widget", "web", "main.log") {
		t.Fatalf("collection log policy: %v", main["log"])
	}
	db := native["widget/db/main"]
	if db["cwd"] != filepath.Join(base, "app") || !reflect.DeepEqual(db["env"], map[string]any{"SHARED": "one", "OTHER": "two"}) {
		t.Fatalf("defaults apply from dekit.yaml: %v", db)
	}
	if helper := native["widget/web/helper"]; helper["script"] != filepath.Join(base, "services", "tools", "run.js") {
		t.Fatalf("script is relative to its fragment: %v", helper["script"])
	}
	for _, task := range m.Tasks {
		if task.Path == "widget/db/main" && (task.URL != "http://127.0.0.1:5432/" || task.PortEnv != "") {
			t.Fatalf("URL comes only from a literal ready.http: %+v", task)
		}
		want := RuntimeDeclaration{Scope: "repository", File: "dekit.yaml"}
		if strings.HasPrefix(task.Path, "widget/web/") {
			want = RuntimeDeclaration{Scope: "repository", File: "services/web.yaml", Mount: "web"}
		}
		if task.Declaration == nil || *task.Declaration != want {
			t.Fatalf("%s declaration %+v, want %+v", task.Path, task.Declaration, want)
		}
		if task.Source != filepath.Join(repo, filepath.FromSlash(want.File)) {
			t.Fatalf("%s source %s", task.Path, task.Source)
		}
	}
	for _, marker := range []string{poison, filepath.Join(repo, "poison-task"), filepath.Join(repo, "app", "poison-task")} {
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Fatalf("planning ran something: %s", marker)
		}
	}
}

func TestRootDekitKeepsHarnessSourceAndDigestInputs(t *testing.T) {
	c := runtimeFixture(t)
	m, _ := rootDekitPlanTasks(t, c)
	shared := runtimeSharedFragment(filepath.Join(c.Collection, "widget"))
	for _, task := range m.Tasks {
		if task.Source != shared || task.Declaration == nil || *task.Declaration != (RuntimeDeclaration{Scope: "repository", File: ".harness/dekit.tasks.yaml"}) {
			t.Fatalf("harness provenance changed: %+v", task)
		}
	}
	fixtureFile(t, c.runtimeLocalFragment("widget"), "tasks: {api/main: {cmd: [sh, -c, ':']}}\n", 0644)
	m, _ = rootDekitPlanTasks(t, c)
	for _, task := range m.Tasks {
		if task.Path == "widget/api/main" && (task.Source != c.runtimeLocalFragment("widget") || task.Declaration.Scope != "collection" || !strings.HasSuffix(task.Declaration.File, "svc/widget.yaml") || filepath.IsAbs(task.Declaration.File)) {
			t.Fatalf("local provenance: %+v %+v", task, task.Declaration)
		}
	}
}

func TestRootDekitDuplicatesAndHarnessLoad(t *testing.T) {
	task := "{cmd: [sh, -c, ':']}"
	for name, scenario := range map[string]struct {
		files map[string]string
		local string
		want  string
	}{
		"root and harness": {files: map[string]string{"dekit.yaml": "tasks: {web/main: " + task + "}\n", ".harness/dekit.tasks.yaml": "tasks: {web/main: " + task + "}\n"},
			want: "widget/web/main is defined in both dekit.yaml and .harness/dekit.tasks.yaml"},
		"root and local": {files: map[string]string{"dekit.yaml": "tasks: {web/main: " + task + "}\n"}, local: "tasks: {web/main: " + task + "}\n",
			want: "widget/web/main is defined in both dekit.yaml and the collection's"},
		"harness loaded at the root once": {files: map[string]string{"dekit.yaml": "load: [.harness/dekit.tasks.yaml]\ntasks: {db/main: " + task + "}\n", ".harness/dekit.tasks.yaml": "tasks: {web/main: " + task + "}\n"}},
		"harness loaded at a mount": {files: map[string]string{"dekit.yaml": "load: [{file: .harness/dekit.tasks.yaml, at: x}]\n", ".harness/dekit.tasks.yaml": "tasks: {web/main: " + task + "}\n"},
			want: `loads .harness/dekit.tasks.yaml at "x"`},
		"fragment twice at one mount": {files: map[string]string{"dekit.yaml": "load: [f/a.yaml, 'f/*.yaml']\n", "f/a.yaml": "tasks: {a: " + task + "}\n"},
			want: "task a is also declared in f/a.yaml"},
		"fragment at two mounts": {files: map[string]string{"dekit.yaml": "load: [{file: f/a.yaml, at: one}, {file: f/a.yaml, at: two}]\n", "f/a.yaml": "tasks: {a: " + task + "}\n"}},
		"task under task": {files: map[string]string{"dekit.yaml": "tasks: {web: " + task + ", web/dev: " + task + "}\n"},
			want: "a task can't have tasks under it"},
		"folder at task": {files: map[string]string{"dekit.yaml": "load: [f.yaml]\ntasks: {web: " + task + "}\n", "f.yaml": "tasks: {web/: {}}\n"},
			want: "can't be a folder too"},
	} {
		t.Run(name, func(t *testing.T) {
			c, _ := rootDekitFixture(t, scenario.files)
			if scenario.local != "" {
				fixtureFile(t, c.runtimeLocalFragment("widget"), scenario.local, 0644)
			}
			_, _, planErr := c.runtimePlan("dekit")
			items := c.RuntimeEnlisted()
			if scenario.want == "" {
				if planErr != nil {
					t.Fatal(planErr)
				}
				for _, item := range items {
					if item.Error != "" {
						t.Fatalf("diagnostic: %+v", item)
					}
				}
				return
			}
			if planErr == nil || !strings.Contains(planErr.Error(), scenario.want) {
				t.Fatalf("render error %v, want %q", planErr, scenario.want)
			}
			if len(items) != 1 || items[0].Kind != "configuration" || !strings.Contains(items[0].Error, scenario.want) {
				t.Fatalf("discovery %+v, want %q", items, scenario.want)
			}
		})
	}
}

func TestRootDekitRefusesWhatTabaWillNotRead(t *testing.T) {
	task := "{cmd: [sh, -c, ':']}"
	for name, scenario := range map[string]struct {
		files map[string]string
		setup func(t *testing.T, repo string)
		want  string
	}{
		"malformed":                 {files: map[string]string{"dekit.yaml": "tasks: [\n"}, want: "widget/dekit.yaml"},
		"two documents":             {files: map[string]string{"dekit.yaml": "tasks: {}\n---\ntasks: {}\n"}, want: "one YAML document"},
		"unknown key":               {files: map[string]string{"dekit.yaml": "services: {}\n"}, want: "field services not found"},
		"x-wtc":                     {files: map[string]string{"dekit.yaml": "tasks: {web: {cmd: [sh], x-wtc: {port_env: PORT}}}\n"}, want: "x-wtc is not a dekit task field"},
		"js":                        {files: map[string]string{"dekit.yaml": "tasks: {web: {cmd: !js '[\"sh\"]'}}\n"}, want: "!js values are not supported"},
		"js in fragment":            {files: map[string]string{"dekit.yaml": "load: [f.yaml]\n", "f.yaml": "tasks: {web: {cmd: [sh], env: {A: !js '1'}}}\n"}, want: "widget/f.yaml: line 1: !js"},
		"unsupported defaults":      {files: map[string]string{"dekit.yaml": "defaults: {type: job}\ntasks: {web: " + task + "}\n"}, want: "defaults.type is not supported"},
		"fragment defaults":         {files: map[string]string{"dekit.yaml": "load: [f.yaml]\n", "f.yaml": "defaults: {cwd: x}\n"}, want: "a fragment holds only tasks and load"},
		"fragment named dekit.yaml": {files: map[string]string{"dekit.yaml": "load: ['sub/*.yaml']\n", "sub/dekit.yaml": "tasks: {}\n"}, want: "is a project declaration"},
		"no match":                  {files: map[string]string{"dekit.yaml": "load: [none/*.yaml]\n"}, want: "matched no files"},
		"cycle":                     {files: map[string]string{"dekit.yaml": "load: [f/a.yaml]\n", "f/a.yaml": "load: [b.yaml]\n", "f/b.yaml": "load: [a.yaml]\n"}, want: "load cycle: f/a.yaml -> f/b.yaml -> f/a.yaml"},
		"escape": {files: map[string]string{"dekit.yaml": "load: [../outside.yaml]\n"}, setup: func(t *testing.T, repo string) {
			fixtureFile(t, filepath.Join(filepath.Dir(repo), "outside.yaml"), "tasks: {leak: "+task+"}\n", 0644)
		}, want: "leaves the repository"},
		"git directory": {files: map[string]string{"dekit.yaml": "load: [.git/x.yaml]\n"}, setup: func(t *testing.T, repo string) {
			fixtureFile(t, filepath.Join(repo, ".git", "x.yaml"), "tasks: {leak: "+task+"}\n", 0644)
		}, want: "not read from .git"},
		"symlinked root": {setup: func(t *testing.T, repo string) {
			inside := filepath.Join(repo, "real.yaml")
			fixtureFile(t, inside, "tasks: {web: "+task+"}\n", 0644)
			if err := os.Symlink("real.yaml", filepath.Join(repo, "dekit.yaml")); err != nil {
				t.Fatal(err)
			}
		}, want: "widget/dekit.yaml is a symlink"},
		"symlinked fragment inside": {files: map[string]string{"dekit.yaml": "load: [l.yaml]\n", "real/x.yaml": "tasks: {web: " + task + "}\n"}, setup: func(t *testing.T, repo string) {
			if err := os.Symlink("real/x.yaml", filepath.Join(repo, "l.yaml")); err != nil {
				t.Fatal(err)
			}
		}, want: "widget/l.yaml is a symlink"},
		"symlinked directory in a pattern": {files: map[string]string{"dekit.yaml": "load: ['svc/*/x.yaml']\n", "svc/real/x.yaml": "tasks: {web: " + task + "}\n"}, setup: func(t *testing.T, repo string) {
			outside := t.TempDir()
			fixtureFile(t, filepath.Join(outside, "x.yaml"), "tasks: {leak: "+task+"}\n", 0644)
			if err := os.Symlink(outside, filepath.Join(repo, "svc", "linked")); err != nil {
				t.Fatal(err)
			}
		}, want: "svc/linked is a symlink; a load pattern does not descend through symlinks"},
		"fifo fragment": {files: map[string]string{"dekit.yaml": "load: [f.yaml]\n"}, setup: func(t *testing.T, repo string) {
			if err := syscall.Mkfifo(filepath.Join(repo, "f.yaml"), 0600); err != nil {
				t.Skip(err)
			}
		}, want: "widget/f.yaml: expected a regular file"},
		"unreadable fragment": {files: map[string]string{"dekit.yaml": "load: [f.yaml]\n", "f.yaml": "tasks: {}\n"}, setup: func(t *testing.T, repo string) {
			if os.Geteuid() == 0 {
				t.Skip("root reads unreadable files")
			}
			if err := os.Chmod(filepath.Join(repo, "f.yaml"), 0); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(filepath.Join(repo, "f.yaml"), 0644) })
		}, want: "widget/f.yaml: permission denied"},
	} {
		t.Run(name, func(t *testing.T) {
			c, repo := rootDekitFixture(t, scenario.files)
			if scenario.setup != nil {
				scenario.setup(t, repo)
			}
			items := c.RuntimeEnlisted()
			if len(items) != 1 || items[0].Kind != "configuration" || items[0].State != "unknown" || !strings.Contains(items[0].Error, scenario.want) {
				t.Fatalf("discovery %+v, want %q", items, scenario.want)
			}
			if strings.Contains(items[0].Error, "leak") {
				t.Fatalf("read an outside file: %s", items[0].Error)
			}
			if _, _, err := c.runtimePlan("dekit"); err == nil || !strings.Contains(err.Error(), scenario.want) {
				t.Fatalf("render error %v, want %q", err, scenario.want)
			}
		})
	}
}

func TestRootDekitAbsentFileIsQuiet(t *testing.T) {
	c := runtimeFixture(t)
	for _, item := range c.RuntimeEnlisted() {
		if item.Error != "" || item.Declaration == nil {
			t.Fatalf("no dekit.yaml must change nothing: %+v", item)
		}
	}
}

func TestRootDekitRemoveRefusesNativeDeclarations(t *testing.T) {
	c, repo := rootDekitFixture(t, map[string]string{"dekit.yaml": "tasks: {web/main: {cmd: [sh, -c, ':']}}\n"})
	before, err := os.ReadFile(filepath.Join(repo, "dekit.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, shared := range []bool{false, true} {
		if _, err := c.RemoveService("widget/web", shared); err == nil || !strings.Contains(err.Error(), "widget/dekit.yaml") {
			t.Fatalf("shared=%v: %v", shared, err)
		}
	}
	if after, _ := os.ReadFile(filepath.Join(repo, "dekit.yaml")); string(after) != string(before) {
		t.Fatal("dekit.yaml was edited")
	}
}

func TestRootDekitCacheSeesLoadedFragments(t *testing.T) {
	c, repo := rootDekitFixture(t, map[string]string{"dekit.yaml": "load: ['svc/*.yaml']\n", "svc/a.yaml": "tasks: {a: {cmd: [sh, -c, ':']}}\n"})
	c.runtimeDeclarationsCache = newRuntimeDeclarationCache()
	if got := c.RuntimeEnlisted(); len(got) != 1 || got[0].Path != "widget/a" {
		t.Fatalf("first read: %+v", got)
	}
	fixtureFile(t, filepath.Join(repo, "svc", "a.yaml"), "tasks: {a: {cmd: [sh, -c, ':']}, b: {cmd: [sh, -c, ':']}}\n", 0644)
	if got := c.RuntimeEnlisted(); len(got) != 2 {
		t.Fatalf("changed fragment not seen: %+v", got)
	}
	fixtureFile(t, filepath.Join(repo, "svc", "c.yaml"), "tasks: {c: {cmd: [sh, -c, ':']}}\n", 0644)
	if got := c.RuntimeEnlisted(); len(got) != 3 {
		t.Fatalf("new glob match not seen: %+v", got)
	}
}

func TestRootDekitReadBounds(t *testing.T) {
	task := "{cmd: [sh, -c, ':']}"
	chain := map[string]string{"dekit.yaml": "load: [f/0.yaml]\n"}
	for i := 0; i <= runtimeDekitMaxDepth; i++ {
		chain[fmt.Sprintf("f/%d.yaml", i)] = fmt.Sprintf("load: [%d.yaml]\n", i+1)
	}
	chain[fmt.Sprintf("f/%d.yaml", runtimeDekitMaxDepth+1)] = "tasks: {deep: " + task + "}\n"
	many := map[string]string{"dekit.yaml": "load: ['many/*.yaml']\n"}
	for i := 0; i < runtimeDekitMaxFiles; i++ {
		many[fmt.Sprintf("many/%03d.yaml", i)] = "tasks: {}\n"
	}
	wide := map[string]string{"dekit.yaml": "load: ['wide/*.yaml']\n", "wide/a.yaml": "tasks: {}\n"}
	for i := 0; i < runtimeDekitMaxEntries; i++ {
		wide[fmt.Sprintf("wide/%05d.txt", i)] = ""
	}
	for name, scenario := range map[string]struct {
		files map[string]string
		want  string
	}{
		"depth":   {chain, "loads are nested deeper than 16 files"},
		"files":   {many, "loads more than 256 files"},
		"size":    {map[string]string{"dekit.yaml": "load: [big.yaml]\n", "big.yaml": "tasks: {}\n#" + strings.Repeat("x", runtimeDekitMaxBytes) + "\n"}, "widget/big.yaml: larger than 1048576 bytes"},
		"entries": {wide, "visits more than 10000 directory entries"},
	} {
		t.Run(name, func(t *testing.T) {
			c, _ := rootDekitFixture(t, scenario.files)
			items := c.RuntimeEnlisted()
			if len(items) != 1 || items[0].Kind != "configuration" || !strings.Contains(items[0].Error, scenario.want) {
				t.Fatalf("discovery %+v, want %q", items, scenario.want)
			}
		})
	}
	// The files bound itself still loads: dekit.yaml and 255 fragments.
	delete(many, "many/000.yaml")
	c, _ := rootDekitFixture(t, many)
	if _, _, err := c.runtimePlan("dekit"); err == nil || !strings.Contains(err.Error(), "no opted-in") {
		t.Fatalf("255 empty fragments: %v", err)
	}
}

func TestRootDekitDefaultsAreCopiedPerTask(t *testing.T) {
	defaults := map[string]any{"env": map[string]any{"A": "1"}, "add_path": []any{"/bin"}, "log": map[string]any{"mode": "append"}, "stop": map[string]any{"signal": "SIGINT", "timeout": "5s"}}
	one, two := map[string]any{"log": map[string]any{"dir": "x"}, "stop": map[string]any{"timeout": "1s"}}, map[string]any{}
	runtimeApplyDefaults(one, defaults)
	runtimeApplyDefaults(two, defaults)
	one["env"].(map[string]any)["PORT"] = "43100"
	one["add_path"].([]any)[0] = "/changed"
	one["log"].(map[string]any)["mode"] = "truncate"
	one["stop"].(map[string]any)["signal"] = "SIGKILL"
	if !reflect.DeepEqual(two["env"], map[string]any{"A": "1"}) || two["add_path"].([]any)[0] != "/bin" || two["log"].(map[string]any)["mode"] != "append" || two["stop"].(map[string]any)["signal"] != "SIGINT" {
		t.Fatalf("a task's change reached another task: %+v", two)
	}
	if !reflect.DeepEqual(defaults["env"], map[string]any{"A": "1"}) || defaults["stop"].(map[string]any)["signal"] != "SIGINT" {
		t.Fatalf("defaults changed: %+v", defaults)
	}
}

func TestRootDekitGlobstarSkipsUnmatchedFileLinks(t *testing.T) {
	c, repo := rootDekitFixture(t, map[string]string{"dekit.yaml": "load: ['**/*.tasks.yaml']\n", "svc/web.tasks.yaml": "tasks: {web: {cmd: [sh, -c, ':']}}\n", "bin/real": "#!/bin/sh\n"})
	if err := os.Symlink("real", filepath.Join(repo, "bin", "tool")); err != nil {
		t.Fatal(err)
	}
	if got := c.RuntimeEnlisted(); len(got) != 1 || got[0].Path != "widget/web" {
		t.Fatalf("a link to an unmatched file must not fail the load: %+v", got)
	}
}

func TestRootDekitCacheSeesCreatedLiteralLoad(t *testing.T) {
	c, repo := rootDekitFixture(t, map[string]string{"dekit.yaml": "load: [later.yaml]\n"})
	c.runtimeDeclarationsCache = newRuntimeDeclarationCache()
	if got := c.RuntimeEnlisted(); len(got) != 1 || !strings.Contains(got[0].Error, "matched no files") {
		t.Fatalf("missing literal load: %+v", got)
	}
	fixtureFile(t, filepath.Join(repo, "later.yaml"), "tasks: {web: {cmd: [sh, -c, ':']}}\n", 0644)
	if got := c.RuntimeEnlisted(); len(got) != 1 || got[0].Path != "widget/web" {
		t.Fatalf("created literal load not seen: %+v", got)
	}
}

func TestRootDekitPatternsNeverFollowLinks(t *testing.T) {
	c, repo := rootDekitFixture(t, map[string]string{"dekit.yaml": "load: ['svc/**/*.yaml']\n", "svc/web/tasks.yaml": "tasks: {web: {cmd: [sh, -c, ':']}}\n"})
	outside := t.TempDir()
	fixtureFile(t, filepath.Join(outside, "deep", "x.yaml"), "tasks: {leak: {cmd: [sh]}}\n", 0644)
	if err := os.Symlink(outside, filepath.Join(repo, "svc", "linked")); err != nil {
		t.Fatal(err)
	}
	if got := c.RuntimeEnlisted(); len(got) != 1 || got[0].Path != "widget/web" {
		t.Fatalf("** must not descend into a link: %+v", got)
	}
	// A dangling link named as a directory is refused too: its target is
	// never looked at.
	fixtureFile(t, filepath.Join(repo, "dekit.yaml"), "load: ['svc/*/tasks.yaml']\n", 0644)
	if err := os.Symlink(filepath.Join(outside, "missing"), filepath.Join(repo, "svc", "dangling")); err != nil {
		t.Fatal(err)
	}
	if got := c.RuntimeEnlisted(); len(got) != 1 || !strings.Contains(got[0].Error, "is a symlink; a load pattern does not descend") {
		t.Fatalf("a link in a pattern's middle must be refused: %+v", got)
	}
}

func TestRootDekitHarnessWithXWTCLoadedFromRoot(t *testing.T) {
	c, _ := rootDekitFixture(t, map[string]string{"dekit.yaml": "load: [.harness/dekit.tasks.yaml]\n", ".harness/dekit.tasks.yaml": "tasks: {web: {cmd: [sh], x-wtc: {port_env: PORT}}}\n"})
	if got := c.RuntimeEnlisted(); len(got) != 1 || !strings.Contains(got[0].Error, "must not be loaded from dekit.yaml") {
		t.Fatalf("diagnostic: %+v", got)
	}
}

func TestRootDekitRemoveRefusesHarnessLoadedFromRoot(t *testing.T) {
	c, repo := rootDekitFixture(t, map[string]string{"dekit.yaml": "load: [.harness/dekit.tasks.yaml]\n", ".harness/dekit.tasks.yaml": "tasks: {web/main: {cmd: [sh, -c, ':']}}\n"})
	before, err := os.ReadFile(filepath.Join(repo, ".harness", "dekit.tasks.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.RemoveService("widget/web", true); err == nil || !strings.Contains(err.Error(), "widget/.harness/dekit.tasks.yaml") {
		t.Fatalf("rm --shared of a harness file loaded by dekit.yaml: %v", err)
	}
	if after, _ := os.ReadFile(filepath.Join(repo, ".harness", "dekit.tasks.yaml")); string(after) != string(before) {
		t.Fatal("the loaded harness file was edited")
	}
}

func TestRootDekitAddSharedRefusesNativeLoadedHarness(t *testing.T) {
	t.Setenv("TABA_RUNTIME_DECLARATIONS", "")
	c, repo := rootDekitFixture(t, map[string]string{"dekit.yaml": "load: [.harness/dekit.tasks.yaml]\n", ".harness/dekit.tasks.yaml": "tasks: {web/main: {cmd: [sh, -c, ':']}}\n"})
	harness := filepath.Join(repo, ".harness", "dekit.tasks.yaml")
	before, err := os.ReadFile(harness)
	if err != nil {
		t.Fatal(err)
	}
	// With or without --port-env: the same refusal before anything is written,
	// matching svc rm, which refuses to edit the natively loaded file.
	for _, portEnv := range []string{"", "API_PORT"} {
		if _, err := c.AddService(ServiceOptions{Service: "widget/api", Cmd: []string{"true"}, Shared: true, NoStart: true, PortEnv: portEnv}); err == nil || !strings.Contains(err.Error(), "widget/dekit.yaml loads .harness/dekit.tasks.yaml") {
			t.Fatalf("add --shared (port env %q) into a natively loaded harness file: %v", portEnv, err)
		}
	}
	if _, err := c.RemoveService("widget/web", true); err == nil || !strings.Contains(err.Error(), "widget/.harness/dekit.tasks.yaml") {
		t.Fatalf("rm parity: %v", err)
	}
	if after, _ := os.ReadFile(harness); string(after) != string(before) {
		t.Fatal("the natively loaded harness file was written")
	}
}

func TestRootDekitDiscoveryShowsLiteralReadyHTTP(t *testing.T) {
	t.Setenv("TABA_RUNTIME_DECLARATIONS", "")
	c, _ := rootDekitFixture(t, map[string]string{
		"dekit.yaml":                "tasks:\n  web: {cmd: [sh, -c, ':'], ready: {http: 'http://127.0.0.1:3000/health'}}\n  secret: {cmd: [sh, -c, ':'], ready: {http: 'http://127.0.0.1:3001/?token=x'}}\n",
		".harness/dekit.tasks.yaml": "tasks:\n  api: {cmd: [sh, -c, ':'], ready: {http: 'http://127.0.0.1:4000/'}, x-wtc: {port_env: API_PORT}}\n",
	})
	want := map[string]string{"widget/web": "http://127.0.0.1:3000/health", "widget/secret": "", "widget/api": ""}
	// svc list/inventory and svc status both show declarations before render.
	for name, list := range map[string]func() []RuntimeItem{"enlisted": c.RuntimeEnlisted, "status": c.RuntimeStatus} {
		urls := map[string]string{}
		for _, item := range list() {
			if item.Error != "" {
				t.Fatalf("%s diagnostic: %+v", name, item)
			}
			if item.Answering != "" || item.State != "unconfigured" {
				t.Fatalf("%s probed or ran a declaration: %+v", name, item)
			}
			urls[item.Path] = item.URL
		}
		if !reflect.DeepEqual(urls, want) {
			t.Fatalf("%s URLs %v, want %v (port_env decides at render; a query is never shown)", name, urls, want)
		}
	}
}
