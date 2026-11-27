package wtc

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

const miseSettings554Config = `[mise.tools]
bun = "1.2.3"

[mise.settings]
versions_host_exclude = ["example-org/*", "other-org/tool"]
jobs = 4
experimental = true
"quoted key" = "value"

[mise.settings.status]
show_tools = true

[mise.settings.npm.nested]
depth = 2
`

func miseSettings554Collection(t *testing.T, config string) *Context {
	t.Helper()
	c := fixture(t)
	if config != "" {
		fixtureFile(t, filepath.Join(c.Harness, "taba.toml"), config, 0644)
	}
	opened, err := OpenCollection(c.Collection)
	if err != nil {
		t.Fatal(err)
	}
	return opened
}

func miseSettings554Write(t *testing.T, c *Context) string {
	t.Helper()
	if err := c.EnsureEnvSupport(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(c.Collection, "mise.toml"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestMiseSettings554AreWrittenAsDeclared(t *testing.T) {
	c := miseSettings554Collection(t, miseSettings554Config)
	written := miseSettings554Write(t, c)

	var got struct {
		Tools    map[string]string `toml:"tools"`
		Settings map[string]any    `toml:"settings"`
		Env      map[string]any    `toml:"env"`
	}
	if _, err := toml.Decode(written, &got); err != nil {
		t.Fatalf("generated mise.toml does not parse: %v\n%s", err, written)
	}
	var declared struct {
		Mise struct {
			Settings map[string]any `toml:"settings"`
		} `toml:"mise"`
	}
	if _, err := toml.Decode(miseSettings554Config, &declared); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Settings, declared.Mise.Settings) {
		t.Fatalf("settings changed on the way:\n got %#v\nwant %#v", got.Settings, declared.Mise.Settings)
	}
	if got.Tools["bun"] != "1.2.3" || got.Env["_"] == nil {
		t.Fatalf("tools or env lost beside settings:\n%s", written)
	}
	tools, settings, env := strings.Index(written, "[tools]"), strings.Index(written, "[settings]"), strings.Index(written, "[env]")
	if !(tools < settings && settings < env) || strings.Count(written, "[settings]") != 1 {
		t.Fatalf("want one [settings] between [tools] and [env]:\n%s", written)
	}

	for range 3 {
		if again := miseSettings554Write(t, miseSettings554Collection(t, miseSettings554Config)); again != written {
			t.Fatalf("output not stable:\n%s\n---\n%s", written, again)
		}
	}
}

func TestMiseSettings554AbsentOrEmptyChangeNothing(t *testing.T) {
	without := miseSettings554Write(t, miseSettings554Collection(t, "[mise.tools]\nbun = \"1.2.3\"\n"))
	empty := miseSettings554Write(t, miseSettings554Collection(t, "[mise.tools]\nbun = \"1.2.3\"\n\n[mise.settings]\n"))
	if without != empty {
		t.Fatalf("an empty table changed the output:\n%s\n---\n%s", without, empty)
	}
	want := miseHeader + "[tools]\n\"bun\" = \"1.2.3\"\n\n" + miseEnv
	if without != want {
		t.Fatalf("output without settings changed:\n%s", without)
	}
}

func TestMiseSettings554ReplaceHandEditsLikeTheRestOfTheFile(t *testing.T) {
	c := miseSettings554Collection(t, "[mise.settings]\njobs = 4\n")
	path := filepath.Join(c.Collection, "mise.toml")
	fixtureFile(t, path, miseHeader+"[settings]\njobs = 1\nexperimental = true\n\n"+miseEnv, 0644)
	written := miseSettings554Write(t, c)
	if !strings.Contains(written, "[settings]\njobs = 4\n") || strings.Contains(written, "experimental") {
		t.Fatalf("hand-edited settings survived regeneration:\n%s", written)
	}
}

func TestMiseSettings554ThatAreNotATableFailOnlyTheRender(t *testing.T) {
	c := miseSettings554Collection(t, "[mise]\nsettings = \"versions_host_exclude\"\n")
	const before = "# rendered before\n"
	fixtureFile(t, filepath.Join(c.Collection, "mise.toml"), before, 0644)
	if err := c.EnsureEnvSupport(); err == nil || !strings.Contains(err.Error(), "mise.settings must be a table") {
		t.Fatalf("wrote mise.settings that is not a table: %v", err)
	}
	after, err := os.ReadFile(filepath.Join(c.Collection, "mise.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != before {
		t.Fatalf("a refused render changed mise.toml:\n%s", after)
	}
}
