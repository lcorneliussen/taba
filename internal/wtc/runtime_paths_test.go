package wtc

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"gopkg.in/yaml.v3"
)

func runtimeDeclarationNames(t *testing.T, c *Context) []string {
	t.Helper()
	files, err := c.runtimeDeclarationFiles()
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, file := range files {
		out = append(out, file.rel)
	}
	return out
}

func TestRuntimeDeclarationsPrecedence(t *testing.T) {
	for _, name := range []string{"TABA_RUNTIME_DECLARATIONS", "WTC_RUNTIME_DECLARATIONS"} {
		t.Setenv(name, "")
	}
	c := runtimeFixture(t)
	if got := runtimeDeclarationNames(t, c); !reflect.DeepEqual(got, []string{"dekit.yaml", ".harness/dekit.tasks.yaml"}) {
		t.Fatalf("default %v", got)
	}
	c.Config.Runtime.Declarations = []string{"services/tasks.yaml"}
	if got := runtimeDeclarationNames(t, c); !reflect.DeepEqual(got, []string{"services/tasks.yaml"}) {
		t.Fatalf("a configured list replaces the default: %v", got)
	}
	t.Setenv("WTC_RUNTIME_DECLARATIONS", "ops/dekit.yaml")
	if got := runtimeDeclarationNames(t, c); !reflect.DeepEqual(got, []string{"ops/dekit.yaml"}) {
		t.Fatalf("the environment wins over the file: %v", got)
	}
	t.Setenv("TABA_RUNTIME_DECLARATIONS", " dekit.yaml , extra/tasks.yaml ")
	if got := runtimeDeclarationNames(t, c); !reflect.DeepEqual(got, []string{"dekit.yaml", "extra/tasks.yaml"}) {
		t.Fatalf("TABA_ wins over WTC_, entries trimmed: %v", got)
	}
	t.Setenv("TABA_RUNTIME_DECLARATIONS", "  ")
	t.Setenv("WTC_RUNTIME_DECLARATIONS", "")
	if got := runtimeDeclarationNames(t, c); !reflect.DeepEqual(got, []string{"services/tasks.yaml"}) {
		t.Fatalf("an empty variable counts as unset: %v", got)
	}
	files, _ := c.runtimeDeclarationFiles()
	if files[0].native {
		t.Fatal("only a file named dekit.yaml is a native project")
	}
}

func TestRuntimeDeclarationsRefused(t *testing.T) {
	t.Setenv("TABA_RUNTIME_DECLARATIONS", "")
	for entry, want := range map[string]string{
		"":                 "is empty",
		"/etc/dekit.yaml":  "relative to the repository",
		"~/dekit.yaml":     "relative to the repository",
		"../dekit.yaml":    "inside the repository",
		"a/../dekit.yaml":  "clean path",
		"./dekit.yaml":     "clean path",
		"svc/":             "clean path",
		".git/dekit.yaml":  "inside .git",
		"svc/*.yaml":       "without glob",
		"a\\b.yaml":        "without glob",
		"svc/tasks\n.yaml": "control characters",
	} {
		c := runtimeFixture(t)
		c.Config.Runtime.Declarations = []string{entry}
		if _, err := c.runtimeDeclarationFiles(); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v, want %q", entry, err, want)
		}
	}
	c := runtimeFixture(t)
	c.Config.Runtime.Declarations = []string{}
	if _, err := c.runtimeDeclarationFiles(); err == nil || !strings.Contains(err.Error(), "names no file") {
		t.Errorf("empty list: %v", err)
	}
	c.Config.Runtime.Declarations = []string{"dekit.yaml", "dekit.yaml"}
	if _, err := c.runtimeDeclarationFiles(); err == nil || !strings.Contains(err.Error(), "twice") {
		t.Errorf("duplicate: %v", err)
	}
	t.Setenv("TABA_RUNTIME_DECLARATIONS", "dekit.yaml,../x.yaml")
	if _, err := c.runtimeDeclarationFiles(); err == nil || !strings.Contains(err.Error(), "TABA_RUNTIME_DECLARATIONS") {
		t.Errorf("an invalid environment value names its variable: %v", err)
	}
	// Discovery shows the configuration error instead of the services.
	items := c.RuntimeEnlisted()
	if len(items) == 0 {
		t.Fatal("no diagnostic")
	}
	for _, item := range items {
		if item.Kind != "configuration" || !strings.Contains(item.Error, "TABA_RUNTIME_DECLARATIONS") {
			t.Fatalf("discovery %+v", items)
		}
	}
}

func TestRuntimeConfiguredDeclarationFiles(t *testing.T) {
	t.Setenv("TABA_RUNTIME_DECLARATIONS", "")
	task := "{cmd: [sh, -c, ':']}"
	c, repo := rootDekitFixture(t, map[string]string{
		"ops/dekit.yaml":            "load: [svc/*.yaml]\ntasks: {db/main: " + task + "}\n",
		"ops/svc/web.yaml":          "tasks: {web/main: " + task + "}\n",
		"ops/run/.keep":             "",
		"services/tasks.yaml":       "tasks: {api/main: {cmd: [sh, -c, ':'], x-wtc: {url: 'http://127.0.0.1:3000'}}}\n",
		".harness/dekit.tasks.yaml": "tasks: {ignored/main: " + task + "}\n",
		"dekit.yaml":                "tasks: {ignored-root/main: " + task + "}\n",
	})
	c.Config.Runtime.Declarations = []string{"ops/dekit.yaml", "services/tasks.yaml"}
	m, native := rootDekitPlanTasks(t, c)
	var paths []string
	for _, task := range m.Tasks {
		paths = append(paths, task.Path)
	}
	if want := []string{"widget/api/main", "widget/db/main", "widget/web/main"}; !reflect.DeepEqual(paths, want) {
		t.Fatalf("tasks %v, want %v (replacement, not append)", paths, want)
	}
	base, _ := filepath.EvalSymlinks(repo)
	if native["widget/db/main"]["cwd"] != filepath.Join(base, "ops") {
		t.Fatalf("a project in a subdirectory runs in it: %v", native["widget/db/main"]["cwd"])
	}
	for _, task := range m.Tasks {
		if task.Path == "widget/api/main" && (task.Declaration.File != "services/tasks.yaml" || task.URL != "http://127.0.0.1:3000") {
			t.Fatalf("a configured taba task file keeps x-wtc: %+v %+v", task, task.Declaration)
		}
		if task.Path == "widget/web/main" && task.Declaration.File != "ops/svc/web.yaml" {
			t.Fatalf("provenance is repository-relative: %+v", task.Declaration)
		}
	}
	if got := c.namedTunnelDeclaredURLs("widget"); got["widget/api/main"].literal != "http://127.0.0.1:3000" {
		t.Fatalf("named tunnels read configured files through the same loader: %+v", got)
	}
	if _, err := c.AddService(ServiceOptions{Service: "widget/extra", Cmd: []string{"true"}, Shared: true, NoStart: true}); err == nil || !strings.Contains(err.Error(), "do not read") {
		t.Fatalf("--shared into a file the configuration ignores: %v", err)
	}
	// A sub-project's loads stay in its directory.
	fixtureFile(t, filepath.Join(repo, "ops", "dekit.yaml"), "load: [../services/tasks.yaml]\n", 0644)
	if _, _, err := c.runtimePlan("dekit"); err == nil || !strings.Contains(err.Error(), "outside its project directory") {
		t.Fatalf("escape from a sub-project: %v", err)
	}
}

func TestRuntimeConfiguredFilesAreContained(t *testing.T) {
	t.Setenv("TABA_RUNTIME_DECLARATIONS", "")
	c, repo := rootDekitFixture(t, map[string]string{})
	outside := filepath.Join(t.TempDir(), "secret.yaml")
	fixtureFile(t, outside, "tasks: {leak: {cmd: [sh]}}\n", 0600)
	if err := os.MkdirAll(filepath.Join(repo, "svc"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(repo, "svc", "tasks.yaml")); err != nil {
		t.Fatal(err)
	}
	c.Config.Runtime.Declarations = []string{"svc/tasks.yaml"}
	for _, check := range []func() string{
		func() string { _, _, err := c.runtimePlan("dekit"); return fmtError(err) },
		func() string { return c.RuntimeEnlisted()[0].Error },
	} {
		if got := check(); !strings.Contains(got, "regular file") || strings.Contains(got, "leak") {
			t.Fatalf("a symlinked configured file must not be read: %q", got)
		}
	}
	if err := os.Remove(filepath.Join(repo, "svc", "tasks.yaml")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Dir(outside), filepath.Join(repo, "linked")); err != nil {
		t.Fatal(err)
	}
	c.Config.Runtime.Declarations = []string{"linked/secret.yaml"}
	if _, _, err := c.runtimePlan("dekit"); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("a configured file under a symlinked directory: %v", err)
	}
}

func fmtError(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func TestRuntimeConfiguredPathCacheInvalidation(t *testing.T) {
	t.Setenv("TABA_RUNTIME_DECLARATIONS", "")
	c, repo := rootDekitFixture(t, map[string]string{})
	// The cache revalidates with a fresh context, which reads the environment.
	t.Setenv("TABA_RUNTIME_DECLARATIONS", "svc/tasks.yaml")
	c.runtimeDeclarationsCache = newRuntimeDeclarationCache()
	if got := c.RuntimeEnlisted(); len(got) != 0 {
		t.Fatalf("nothing declared yet: %+v", got)
	}
	fixtureFile(t, filepath.Join(repo, "svc", "tasks.yaml"), "tasks: {web: {cmd: [sh, -c, ':']}}\n", 0644)
	if got := c.RuntimeEnlisted(); len(got) != 1 || got[0].Path != "widget/web" {
		t.Fatalf("a created configured file is not seen: %+v", got)
	}
}

func TestRuntimeHealthPath(t *testing.T) {
	for _, name := range []string{"TABA_RUNTIME_HEALTH_PATH", "WTC_RUNTIME_HEALTH_PATH"} {
		t.Setenv(name, "")
	}
	c := runtimeFixture(t)
	for value, ok := range map[string]bool{
		"":                             true,
		"/health":                      true,
		"/api/v1/health":               true,
		"/ready%20now":                 true,
		"health":                       false,
		"//evil.example/health":        false,
		"http://127.0.0.1/health":      false,
		"https://user:pw@host/x":       false,
		"/health?token=secret":         false,
		"/health#x":                    false,
		"/a/../health":                 false,
		"/./health":                    false,
		"/bad%zz":                      false,
		"/with space":                  false,
		"/" + strings.Repeat("a", 256): false,
	} {
		c.Config.Runtime.HealthPath = value
		got, err := c.runtimeHealthPath()
		if ok && (err != nil || got != value) || !ok && err == nil {
			t.Errorf("%q: %q %v", value, got, err)
		}
	}
	c.Config.Runtime.HealthPath = "/from-file"
	t.Setenv("WTC_RUNTIME_HEALTH_PATH", "/legacy")
	if got, _ := c.runtimeHealthPath(); got != "/legacy" {
		t.Fatalf("environment over file: %q", got)
	}
	t.Setenv("TABA_RUNTIME_HEALTH_PATH", "/current")
	if got, _ := c.runtimeHealthPath(); got != "/current" {
		t.Fatalf("TABA_ over WTC_: %q", got)
	}
	t.Setenv("TABA_RUNTIME_HEALTH_PATH", "http://other.example/x")
	if _, err := c.runtimeHealthPath(); err == nil || !strings.Contains(err.Error(), "TABA_RUNTIME_HEALTH_PATH") {
		t.Fatalf("invalid environment value: %v", err)
	}
}

// healthPlan renders the fixture's service with a resolved port.
func healthPlan(t *testing.T, metadata, ready string) (*Context, []runtimeTask, map[string]map[string]any, error) {
	t.Helper()
	c := runtimeFixture(t)
	spec := "tasks:\n web/server:\n  cmd: [echo, synthetic]\n  x-wtc: " + metadata + "\n"
	if ready != "" {
		spec += "  ready: " + ready + "\n"
	}
	fixtureFile(t, filepath.Join(c.Collection, "widget/.harness/dekit.tasks.yaml"), spec, 0644)
	fixtureFile(t, filepath.Join(c.Collection, ".env.collection"), "WIDGET_PORT=4173\n", 0600)
	m, data, err := c.runtimePlan(c.Config.Runtime.Binary)
	if err != nil {
		return c, nil, nil, err
	}
	var native struct {
		Tasks map[string]map[string]any `yaml:"tasks"`
	}
	if err := yaml.Unmarshal(data, &native); err != nil {
		t.Fatal(err)
	}
	return c, m.Tasks, native.Tasks, nil
}

func TestRuntimeHealthPathShapesOnlyTabaReadiness(t *testing.T) {
	for _, name := range []string{"TABA_RUNTIME_HEALTH_PATH", "WTC_RUNTIME_HEALTH_PATH", "TABA_RUNTIME_DECLARATIONS"} {
		t.Setenv(name, "")
	}
	for _, tc := range []struct {
		name, metadata, ready, health string
		wantReady                     map[string]any
		wantHealth                    string
	}{
		{"default stays tcp", "{port_env: WIDGET_PORT}", "", "", map[string]any{"tcp": "127.0.0.1:4173", "timeout": "60s"}, ""},
		{"health path", "{port_env: WIDGET_PORT}", "", "/health", map[string]any{"http": "http://127.0.0.1:4173/health", "timeout": "60s"}, "http://127.0.0.1:4173/health"},
		{"timeout only", "{port_env: WIDGET_PORT}", "{timeout: 5s}", "/health", map[string]any{"http": "http://127.0.0.1:4173/health", "timeout": "5s"}, "http://127.0.0.1:4173/health"},
		{"explicit ready kept", "{port_env: WIDGET_PORT}", "{log: READY}", "/health", map[string]any{"log": "READY"}, ""},
		{"explicit http kept", "{port_env: WIDGET_PORT}", "{http: 'http://127.0.0.1:4173/own'}", "/health", map[string]any{"http": "http://127.0.0.1:4173/own"}, ""},
		{"explicit url keeps its check", "{port_env: WIDGET_PORT, url: 'http://127.0.0.1:3000'}", "", "/health", map[string]any{"http": "http://127.0.0.1:4173/health", "timeout": "60s"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("TABA_RUNTIME_HEALTH_PATH", tc.health)
			_, tasks, native, err := healthPlan(t, tc.metadata, tc.ready)
			if err != nil {
				t.Fatal(err)
			}
			if got := native["widget/web/server"]["ready"]; !reflect.DeepEqual(got, tc.wantReady) {
				t.Fatalf("ready %v, want %v", got, tc.wantReady)
			}
			if tasks[0].HealthURL != tc.wantHealth {
				t.Fatalf("health_url %q, want %q", tasks[0].HealthURL, tc.wantHealth)
			}
		})
	}
	t.Run("invalid is a render error", func(t *testing.T) {
		t.Setenv("TABA_RUNTIME_HEALTH_PATH", "//other.example")
		if _, _, _, err := healthPlan(t, "{port_env: WIDGET_PORT}", ""); err == nil || !strings.Contains(err.Error(), "URL path") {
			t.Fatalf("%v", err)
		}
	})
	t.Run("an unset health path leaves spec and definition alone", func(t *testing.T) {
		t.Setenv("TABA_RUNTIME_HEALTH_PATH", "")
		c, before, _, err := healthPlan(t, "{port_env: WIDGET_PORT}", "")
		if err != nil {
			t.Fatal(err)
		}
		plan := func() runtimeTask {
			m, _, err := c.runtimePlan(c.Config.Runtime.Binary)
			if err != nil {
				t.Fatal(err)
			}
			return m.Tasks[0]
		}
		t.Setenv("TABA_RUNTIME_HEALTH_PATH", "/health")
		if plan().Spec == before[0].Spec {
			t.Fatal("a configured health path must change the readiness spec")
		}
		t.Setenv("TABA_RUNTIME_HEALTH_PATH", "")
		if again := plan(); again.Spec != before[0].Spec || again.Definition != before[0].Definition {
			t.Fatal("without a health path the spec and definition must not change")
		}
	})
}

func TestRuntimeAnsweringUsesHealthURL(t *testing.T) {
	items := []RuntimeItem{
		{Path: "a", Kind: "service", State: "ready", URL: "http://127.0.0.1:4173", HealthURL: "http://127.0.0.1:4173/health"},
		{Path: "b", Kind: "service", State: "ready", URL: "http://127.0.0.1:3000"},
	}
	var asked []string
	var mu sync.Mutex
	runtimeAnswering(items, func(target string) (bool, string) {
		mu.Lock()
		defer mu.Unlock()
		asked = append(asked, target)
		return true, ""
	})
	if len(asked) != 2 || !strings.Contains(strings.Join(asked, " "), "http://127.0.0.1:4173/health") || !strings.Contains(strings.Join(asked, " "), "http://127.0.0.1:3000") {
		t.Fatalf("asked %v", asked)
	}
}
