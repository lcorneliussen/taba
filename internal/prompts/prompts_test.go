package prompts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"taba.sh/taba/internal/testenv"
)

func TestMain(m *testing.M) { testenv.Main(m) }

const greeting = "---\nid: demo.greet\nversion: 2\ninputs: [name]\naudience: private\n---\nHello {{name}}, run `{{command}} status`.\n"

func fixture(t *testing.T) (Context, string) {
	t.Helper()
	harness := t.TempDir()
	return Context{Harness: harness, Defaults: fstest.MapFS{
		"demo/greet.md":  {Data: []byte(greeting)},
		"demo/public.md": {Data: []byte("---\nid: demo.public\nversion: 1\ninputs: []\naudience: public\n---\nPublic text.\n")},
	}}, harness
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestRenderEmbeddedDefault(t *testing.T) {
	ctx, _ := fixture(t)
	text, err := Render(ctx, "demo.greet", map[string]string{"name": "{{command}} Ada"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(text.String(), "Hello {{command}} Ada, run `") || strings.HasSuffix(text.String(), "\n") {
		t.Fatalf("values must be inserted verbatim in one pass: %q", text)
	}
	p := text.Provenance()
	if p.Source != SourceEmbedded || p.EmbeddedVersion != 2 || p.File != "embedded:prompts/demo/greet.md" {
		t.Fatalf("provenance = %+v", p)
	}
}

func TestRenderRejectsMissingAndUndeclaredInputs(t *testing.T) {
	ctx, _ := fixture(t)
	if _, err := Render(ctx, "demo.greet", nil); err == nil || !strings.Contains(err.Error(), `missing input "name"`) {
		t.Fatalf("missing input: %v", err)
	}
	if _, err := Render(ctx, "demo.greet", map[string]string{"name": "x", "extra": "y"}); err == nil || !strings.Contains(err.Error(), `undeclared input "extra"`) {
		t.Fatalf("undeclared input: %v", err)
	}
	if _, err := Render(ctx, "demo.unknown", nil); err == nil {
		t.Fatal("unknown id rendered")
	}
}

func TestProjectOverrideWinsWithBase(t *testing.T) {
	ctx, harness := fixture(t)
	write(t, filepath.Join(harness, "prompts", "demo", "greet.md"), "---\nid: demo.greet\nversion: 2\n---\nHi {{name}}.\n")
	write(t, filepath.Join(harness, "prompts", BaseDir, "demo", "greet.md"), greeting)
	text, err := Render(ctx, "demo.greet", map[string]string{"name": "Ada"})
	if err != nil {
		t.Fatal(err)
	}
	p := text.Provenance()
	if text.String() != "Hi Ada." || p.Source != SourceProject || p.BaseVersion != 2 || !p.BaseCurrent {
		t.Fatalf("override = %q %+v", text, p)
	}
	write(t, filepath.Join(harness, "prompts", BaseDir, "demo", "greet.md"), strings.Replace(greeting, "version: 2", "version: 1", 1))
	res, err := Resolve(ctx, "demo.greet")
	if err != nil || res.Provenance.BaseCurrent || res.Provenance.BaseVersion != 1 {
		t.Fatalf("stale base = %+v (%v)", res.Provenance, err)
	}
}

func TestOverrideWithoutFrontmatterIsBody(t *testing.T) {
	ctx, harness := fixture(t)
	write(t, filepath.Join(harness, "prompts", "demo", "greet.md"), "Yo {{name}}\n")
	text, err := Render(ctx, "demo.greet", map[string]string{"name": "Ada"})
	if err != nil || text.String() != "Yo Ada" {
		t.Fatalf("plain override = %q (%v)", text, err)
	}
	if p := text.Provenance(); p.Source != SourceProject || p.BaseVersion != 0 || p.BaseFile != "" {
		t.Fatalf("override without a recorded base reports one: %+v", p)
	}
}

func TestOverrideFailsOnUnknownPlaceholderOrWrongID(t *testing.T) {
	ctx, harness := fixture(t)
	override := filepath.Join(harness, "prompts", "demo", "greet.md")
	write(t, override, "Hi {{nickname}}\n")
	if _, err := Render(ctx, "demo.greet", map[string]string{"name": "Ada"}); err == nil || !strings.Contains(err.Error(), "{{nickname}}") {
		t.Fatalf("unknown placeholder: %v", err)
	}
	write(t, override, "---\nid: demo.other\n---\nHi\n")
	if _, err := Render(ctx, "demo.greet", map[string]string{"name": "Ada"}); err == nil || !strings.Contains(err.Error(), "demo.other") {
		t.Fatalf("misplaced override: %v", err)
	}
}

func TestPublicAudienceIgnoresProjectOverrides(t *testing.T) {
	ctx, harness := fixture(t)
	write(t, filepath.Join(harness, "prompts", "demo", "public.md"), "Private project text.\n")
	ctx.Audience = Public
	text, err := Render(ctx, "demo.public", nil)
	if err != nil {
		t.Fatal(err)
	}
	if text.String() != "Public text." || len(text.Provenance().Skipped) != 1 {
		t.Fatalf("public render = %q %+v", text, text.Provenance())
	}
	if _, err := Render(ctx, "demo.greet", map[string]string{"name": "Ada"}); err == nil || !strings.Contains(err.Error(), "public audience") {
		t.Fatalf("private prompt rendered for public audience: %v", err)
	}
}

func TestListReportsEffectiveSources(t *testing.T) {
	ctx, harness := fixture(t)
	write(t, filepath.Join(harness, "prompts", "demo", "public.md"), "Mine.\n")
	list, err := List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].ID != "demo.greet" || list[0].Source != SourceEmbedded || list[1].Source != SourceProject {
		t.Fatalf("list = %+v", list)
	}
	ctx.Audience = Public
	list, err = List(ctx)
	if err != nil || len(list) != 1 || list[0].ID != "demo.public" || list[0].Source != SourceEmbedded {
		t.Fatalf("public list = %+v (%v)", list, err)
	}
}

func TestParseRejectsMalformedFrontmatter(t *testing.T) {
	for _, raw := range []string{
		"---\nid: a.b\n",
		"---\nversion: x\n---\n",
		"---\nowner: me\n---\n",
		"---\ninputs: a, b\n---\n",
		"---\ninputs: [a]\n---\n{{b}}\n",
	} {
		if _, err := Parse("a/b.md", []byte(raw)); err == nil {
			t.Errorf("accepted %q", raw)
		}
	}
}
