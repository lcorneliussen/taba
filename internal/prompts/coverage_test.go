package prompts

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// Producers that hand text to an agent. Each dispatch must sit in a function
// that receives prompts.Text and send that value's String(), so the payload
// can only come from Render.
var producerDirs = []string{"../../cmd/taba", "../wtc"}

// Assembly helpers that must take their instruction text as prompts.Text.
var textHelpers = map[string]bool{"reviewCheckPrompt": true}

func isLiteral(e ast.Expr, value string) bool {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return false
	}
	s, err := strconv.Unquote(lit.Value)
	return err == nil && s == value
}

func containsLiteral(e ast.Node, value string) bool {
	found := false
	ast.Inspect(e, func(n ast.Node) bool {
		if expr, ok := n.(ast.Expr); ok && isLiteral(expr, value) {
			found = true
		}
		return !found
	})
	return found
}

func isPromptText(e ast.Expr) bool {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Text" {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "prompts"
}

func textParams(fn *ast.FuncDecl) map[string]bool {
	params := map[string]bool{}
	for _, field := range fn.Type.Params.List {
		if isPromptText(field.Type) {
			for _, name := range field.Names {
				params[name.Name] = true
			}
		}
	}
	return params
}

func sendsParam(call *ast.CallExpr, params map[string]bool) bool {
	found := false
	for _, arg := range call.Args {
		ast.Inspect(arg, func(n ast.Node) bool {
			inner, ok := n.(*ast.CallExpr)
			if !ok {
				return !found
			}
			sel, ok := inner.Fun.(*ast.SelectorExpr)
			if ok && sel.Sel.Name == "String" {
				if id, ok := sel.X.(*ast.Ident); ok && params[id.Name] {
					found = true
				}
			}
			return !found
		})
	}
	return found
}

func dispatchKind(call *ast.CallExpr) string {
	for i := 0; i+1 < len(call.Args); i++ {
		if isLiteral(call.Args[i], "agent") && isLiteral(call.Args[i+1], "prompt") {
			return "herdr agent prompt"
		}
	}
	if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "WriteFile" && len(call.Args) > 0 && containsLiteral(call.Args[0], "HANDOFF.md") {
		return "launch note write"
	}
	return ""
}

type producerFile struct {
	path string
	file *ast.File
}

func producerFiles(t *testing.T) []producerFile {
	t.Helper()
	var files []producerFile
	fset := token.NewFileSet()
	for _, dir := range producerDirs {
		matches, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		for _, path := range matches {
			if strings.HasSuffix(path, "_test.go") {
				continue
			}
			file, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			files = append(files, producerFile{path, file})
		}
	}
	if len(files) == 0 {
		t.Fatal("no producer files found")
	}
	return files
}

func TestAgentDispatchUsesRenderedPrompts(t *testing.T) {
	sinks := 0
	for _, f := range producerFiles(t) {
		for _, decl := range f.file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			params := textParams(fn)
			if textHelpers[fn.Name.Name] && len(params) == 0 {
				t.Errorf("%s: %s must take its instruction text as prompts.Text", f.path, fn.Name.Name)
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				kind := dispatchKind(call)
				if kind == "" {
					return true
				}
				sinks++
				if !sendsParam(call, params) {
					t.Errorf("%s: %s in %s must send a prompts.Text parameter's String(), not assembled text", f.path, kind, fn.Name.Name)
				}
				return true
			})
		}
	}
	if sinks < 3 {
		t.Fatalf("found %d dispatch sites; the scan is no longer seeing the producers", sinks)
	}
}

func TestEveryEmbeddedPromptIsRenderedAndEveryRenderIsEmbedded(t *testing.T) {
	embedded := map[string]bool{}
	root := "../wtc/defaults/prompts"
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		def, err := Parse(rel, raw)
		if err != nil {
			return err
		}
		if def.ID != IDFor(rel) || def.Version < 1 || (def.Audience != Private && def.Audience != Public) || def.Inputs == nil {
			t.Errorf("%s: frontmatter id/version/inputs/audience = %q/%d/%v/%q", rel, def.ID, def.Version, def.Inputs, def.Audience)
		}
		embedded[def.ID] = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	rendered := map[string]bool{}
	for _, f := range producerFiles(t) {
		ast.Inspect(f.file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) < 2 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Render" {
				return true
			}
			if pkg, ok := sel.X.(*ast.Ident); !ok || pkg.Name != "prompts" {
				return true
			}
			lit, ok := call.Args[1].(*ast.BasicLit)
			if !ok {
				t.Errorf("%s: prompts.Render needs a literal prompt id so coverage is auditable", f.path)
				return true
			}
			id, _ := strconv.Unquote(lit.Value)
			rendered[id] = true
			if !embedded[id] {
				t.Errorf("%s: renders %q, which has no embedded default", f.path, id)
			}
			return true
		})
	}
	var unused []string
	for id := range embedded {
		if !rendered[id] {
			unused = append(unused, id)
		}
	}
	sort.Strings(unused)
	if len(unused) > 0 {
		t.Errorf("embedded prompts never rendered: %v", unused)
	}
}
