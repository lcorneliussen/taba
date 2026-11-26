package wtc

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func runtimeTags553Fixture(t *testing.T) *Context {
	t.Helper()
	t.Setenv("TABA_RUNTIME_DECLARATIONS", "")
	c, _ := rootDekitFixture(t, map[string]string{
		"dekit.yaml": `load:
  - file: services/web.yaml
    at: web
tasks:
  device/main: {cmd: [sh, -c, ':'], tags: [lab, device, z-last, a-first]}
  plain/main: {cmd: [sh, -c, ':']}
  empty/main: {cmd: [sh, -c, ':'], tags: ~}
`,
		"services/web.yaml": "tasks:\n  tunnel: {cmd: [sh, -c, ':'], tags: [public, endpoint]}\n",
	})
	fixtureFile(t, c.runtimeLocalFragment("widget"), "tasks:\n  db/main: {cmd: [sh, -c, ':'], tags: [accessory, postgres], x-wtc: {port_env: DB_PORT}}\n", 0600)
	fixtureFile(t, runtimeSharedFragment(c.Harness), "tasks:\n  tools/main: {cmd: [sh, -c, ':'], tags: [harness]}\n", 0644)
	return c
}

var runtimeTags553Want = map[string]struct {
	kind string
	tags []string
}{
	"widget/device/main": {"service", []string{"lab", "device", "z-last", "a-first"}},
	"widget/plain/main":  {"service", nil},
	"widget/empty/main":  {"service", nil},
	"widget/web/tunnel":  {"endpoint", []string{"public", "endpoint"}},
	"widget/db/main":     {"accessory", []string{"accessory", "postgres"}},
	"harness/tools/main": {"service", []string{"harness"}},
}

func runtimeTags553Check(t *testing.T, name string, items []RuntimeItem) {
	t.Helper()
	seen := 0
	for _, item := range items {
		want, ok := runtimeTags553Want[item.Path]
		if !ok {
			continue
		}
		seen++
		if item.Kind != want.kind || !reflect.DeepEqual(item.Tags, want.tags) {
			t.Errorf("%s %s: kind %q tags %q, want %q %q", name, item.Path, item.Kind, item.Tags, want.kind, want.tags)
		}
	}
	if seen != len(runtimeTags553Want) {
		t.Fatalf("%s: %d of %d tasks in %+v", name, seen, len(runtimeTags553Want), items)
	}
}

func TestRuntimeTags553AsWrittenInEveryDeclaration(t *testing.T) {
	c := runtimeTags553Fixture(t)
	runtimeTags553Check(t, "enlisted", c.RuntimeEnlisted())
	runtimeTags553Check(t, "status", c.RuntimeStatus())
	m, _ := rootDekitPlanTasks(t, c)
	var planned []RuntimeItem
	for _, task := range m.Tasks {
		planned = append(planned, task.RuntimeItem)
	}
	runtimeTags553Check(t, "plan", planned)
}

func TestRuntimeTags553SurviveRenderAndRunnerObservation(t *testing.T) {
	c := runtimeTags553Fixture(t)
	if _, err := c.RenderRuntime(false); err != nil {
		t.Fatal(err)
	}
	m, err := c.runtimeManifest()
	if err != nil {
		t.Fatal(err)
	}
	var recorded []RuntimeItem
	for _, task := range m.Tasks {
		recorded = append(recorded, task.RuntimeItem)
	}
	runtimeTags553Check(t, "manifest", recorded)
	runtimeTags553Check(t, "enlisted after render", c.RuntimeEnlisted())
	// dekit's own ls adds runner tags such as autostart; the declaration's stay.
	fixtureFile(t, filepath.Join(c.runtimeRoot(), "running"), "", 0600)
	fixtureFile(t, filepath.Join(c.runtimeRoot(), "fake-tasks.json"), `{"tasks":[{"path":"widget/device/main","state":"ready","tags":["autostart","lab"]},{"path":"widget/web/tunnel","state":"failed","exit_code":3,"tags":["endpoint"]}]}`, 0600)
	got := c.RuntimeStatus()
	runtimeTags553Check(t, "live", got)
	for _, item := range got {
		if item.Path == "widget/web/tunnel" && (item.State != "failed" || item.ExitCode == nil || *item.ExitCode != 3) {
			t.Fatalf("lost live facts: %+v", item)
		}
	}
}

func TestRuntimeTags553ComeOnlyFromTheRender(t *testing.T) {
	c := runtimeFixture(t)
	shared := runtimeSharedFragment(filepath.Join(c.Collection, "widget"))
	// Rendered without tags: before this change, or before the tags were added.
	rendered := []RuntimeItem{{Path: "widget/web/tunnel", Kind: "endpoint", State: "ready", Source: shared, URL: "https://example.invalid"}}
	got := c.runtimeWithDeclarations(rendered)
	if got[0].Tags != nil || got[0].Declaration == nil || got[0].State != "ready" {
		t.Fatalf("a rendered task shows tags it was not rendered with: %+v", got[0])
	}
	own := []RuntimeItem{{Path: "widget/web/tunnel", Kind: "endpoint", State: "ready", Source: shared, Tags: []string{"recorded"}}}
	if got := c.runtimeWithDeclarations(own); !reflect.DeepEqual(got[0].Tags, []string{"recorded"}) {
		t.Fatalf("declaration replaced recorded tags: %+v", got[0])
	}
	declared := c.runtimeWithDeclarations(nil)
	if i := slices.IndexFunc(declared, func(item RuntimeItem) bool { return item.Path == "widget/web/tunnel" }); i < 0 || !reflect.DeepEqual(declared[i].Tags, []string{"endpoint"}) {
		t.Fatalf("a task not rendered yet does not show its declared tags: %+v", declared)
	}
}

func TestRuntimeTags553OnlyInJSON(t *testing.T) {
	tagged := RuntimeItem{Collection: "example", Path: "widget/web/tunnel", Kind: "endpoint", State: "ready", Tags: []string{"lab", "endpoint"}}
	plain := tagged
	plain.Tags = nil
	data, err := json.Marshal([]RuntimeItem{tagged, plain})
	if err != nil {
		t.Fatal(err)
	}
	var decoded []map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded[0]["tags"], []any{"lab", "endpoint"}) {
		t.Fatalf("tags in JSON: %s", data)
	}
	if _, ok := decoded[1]["tags"]; ok {
		t.Fatalf("a task without tags has a tags field: %s", data)
	}
	if RuntimeText([]RuntimeItem{tagged}) != RuntimeText([]RuntimeItem{plain}) {
		t.Fatal("tags changed the text output")
	}
}

func TestRuntimeTags553RefusesWhatIsNotAListOfStrings(t *testing.T) {
	for name, tags := range map[string]string{
		"number":       "[1]",
		"boolean":      "[lab, true]",
		"null":         "[~]",
		"object":       "[{lab: true}]",
		"string":       "lab",
		"object value": "{lab: true}",
	} {
		t.Run(name, func(t *testing.T) {
			for file, write := range map[string]func(c *Context, repo string){
				"dekit.yaml": func(c *Context, repo string) {
					fixtureFile(t, filepath.Join(repo, "dekit.yaml"), "tasks: {web: {cmd: [sh, -c, ':'], tags: "+tags+"}}\n", 0644)
				},
				"svc file": func(c *Context, repo string) {
					fixtureFile(t, c.runtimeLocalFragment("widget"), "tasks: {web: {cmd: [sh, -c, ':'], tags: "+tags+"}}\n", 0600)
				},
			} {
				c, repo := rootDekitFixture(t, nil)
				write(c, repo)
				const want = "widget/web: tags must be a list of strings"
				items := c.RuntimeEnlisted()
				if len(items) != 1 || items[0].Kind != "configuration" || !strings.Contains(items[0].Error, want) {
					t.Fatalf("%s discovery %+v, want %q", file, items, want)
				}
				if _, _, err := c.runtimePlan("dekit"); err == nil || !strings.Contains(err.Error(), want) {
					t.Fatalf("%s render error %v, want %q", file, err, want)
				}
			}
		})
	}
}
