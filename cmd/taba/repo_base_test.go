package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"taba.sh/taba/internal/wtc"
)

func repositoryBaseCLI(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var asJSON bool
	root := &cobra.Command{Use: "taba", SilenceErrors: true, SilenceUsage: true}
	root.PersistentFlags().BoolVar(&asJSON, "json", false, "")
	addRepoBaseCommands(root, &asJSON)
	root.SetArgs(args)
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	previous := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	runErr := root.Execute()
	os.Stdout = previous
	w.Close()
	out, _ := io.ReadAll(r)
	r.Close()
	return string(out), runErr
}
func TestRepositoryBaseCommandsUseExplicitReceiver(t *testing.T) {
	source, receiver := filepath.Join(t.TempDir(), "source"), filepath.Join(t.TempDir(), "receiver")
	for _, collection := range []string{source, receiver} {
		if err := os.MkdirAll(filepath.Join(collection, "harness"), 0700); err != nil {
			t.Fatal(err)
		}
		body := "repos:\n  - name: widget\n    aliases: [legacy]\n    remote: https://example.invalid/widget.git\n    default_ref: origin/main\n"
		if err := os.WriteFile(filepath.Join(collection, "harness", ".harness-repos.yml"), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(source)
	for _, step := range []struct {
		args []string
		want string
	}{
		{[]string{"repo", "base", "set", "legacy", "beta", "--reason", "Feature staging", "--collection", receiver, "--json"}, "origin/beta"},
		{[]string{"repo", "base", "show", "widget", "--collection", receiver, "--json"}, "origin/beta"},
		{[]string{"repo", "base", "show", "widget", "--json"}, "origin/main"},
		{[]string{"repo", "base", "clear", "legacy", "--collection", receiver, "--json"}, "origin/main"},
	} {
		out, err := repositoryBaseCLI(t, step.args...)
		if err != nil {
			t.Fatalf("%v: %s %v", step.args, out, err)
		}
		var result struct {
			Data struct {
				EffectiveRef string `json:"effective_ref"`
			}
		}
		if json.Unmarshal([]byte(out), &result) != nil || result.Data.EffectiveRef != step.want {
			t.Fatalf("wrong result: %s", out)
		}
	}
	if _, err := repositoryBaseCLI(t, "repo", "base", "set", "widget", "beta"); err == nil {
		t.Fatal("missing reason accepted")
	}
	out, err := repositoryBaseCLI(t, "repo", "base", "set", "widget", "beta", "--reason", "Feature staging")
	if err != nil || !strings.Contains(out, `reason: "Feature staging"`) || !strings.Contains(out, "checkout unchanged") {
		t.Fatalf("text output: %s %v", out, err)
	}
}

func TestRepositoryBaseCommandOrphanOutcomeHasNoResolution(t *testing.T) {
	collection := filepath.Join(t.TempDir(), "receiver")
	if err := os.MkdirAll(filepath.Join(collection, "harness"), 0700); err != nil {
		t.Fatal(err)
	}
	registry := filepath.Join(collection, "harness", ".harness-repos.yml")
	original := "repos:\n  - name: widget\n    remote: https://example.invalid/widget.git\n    default_ref: origin/main\n"
	if err := os.WriteFile(registry, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(collection)
	if _, err := repositoryBaseCLI(t, "repo", "base", "set", "widget", "beta", "--reason", "Staging work"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(registry, []byte(strings.ReplaceAll(original, "widget", "renamed")), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := repositoryBaseCLI(t, "repo", "base", "show", "widget"); err == nil || !strings.Contains(err.Error(), "clear widget") {
		t.Fatalf("missing recovery instruction: %v", err)
	}
	out, err := repositoryBaseCLI(t, "repo", "base", "clear", "widget", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var result struct{ Data map[string]any }
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(err)
	}
	if result.Data["cleared"] != true || result.Data["orphaned"] != true || result.Data["exact_key"] != "widget" {
		t.Fatalf("wrong cleanup outcome: %s", out)
	}
	for _, key := range []string{"resolution", "effective_ref", "source"} {
		if _, exists := result.Data[key]; exists {
			t.Fatalf("fabricated resolution in cleanup: %s", out)
		}
	}
	if _, err := repositoryBaseCLI(t, "repo", "base", "clear", "widget"); err == nil {
		t.Fatal("unknown unstored key cleared")
	}
}

func TestRepositoryBaseCommandsKeepReusedAliasOrphanSeparate(t *testing.T) {
	collection := t.TempDir()
	if err := os.MkdirAll(filepath.Join(collection, "harness"), 0700); err != nil {
		t.Fatal(err)
	}
	registry := "repos:\n  - name: replacement\n    aliases: [widget]\n    remote: https://example.invalid/replacement.git\n    default_ref: origin/main\n"
	if err := os.WriteFile(filepath.Join(collection, "harness", ".harness-repos.yml"), []byte(registry), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(collection, ".wtc"), 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(collection, ".wtc", "repo-bases.toml")
	if err := os.WriteFile(file, []byte("schema_version=1\n[repos.widget]\nbase='beta'\nreason='Old canonical owner'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "init", "-q", "-b", "main", filepath.Join(collection, "widget")).CombinedOutput(); err != nil {
		t.Fatalf("synthetic worktree: %s %v", out, err)
	}
	call := func(args ...string) map[string]any {
		t.Helper()
		args = append(args, "--collection", collection, "--json")
		out, err := repositoryBaseCLI(t, args...)
		if err != nil {
			t.Fatalf("%v: %s %v", args, out, err)
		}
		var result struct {
			Data map[string]any `json:"data"`
		}
		if err := json.Unmarshal([]byte(out), &result); err != nil {
			t.Fatal(err)
		}
		return result.Data
	}
	for _, selector := range []string{"widget", "replacement"} {
		if _, err := repositoryBaseCLI(t, "repo", "base", "show", selector, "--collection", collection); err == nil || !strings.Contains(err.Error(), "clear widget") {
			t.Fatalf("CLI hid retained alias intent for %s: %v", selector, err)
		}
	}
	base := call("repo", "base", "set", "replacement", "gamma", "--reason", "Explicit new owner")
	if base["repository"] != "replacement" || base["effective_ref"] != "origin/gamma" {
		t.Fatalf("explicit alias did not select current canonical owner: %+v", base)
	}
	base = call("repo", "base", "show", "widget", "--unmanaged")
	if base["effective_ref"] != "origin/HEAD" || base["source"] != "fallback" || base["reason"] != nil {
		t.Fatalf("CLI browse applied orphan or alias owner override: %+v", base)
	}
	before, _ := os.ReadFile(file)
	if _, err := repositoryBaseCLI(t, "repo", "base", "clear", "replacement", "--collection", collection); err == nil || !strings.Contains(err.Error(), "clear widget") {
		t.Fatalf("CLI canonical clear hid retained related intent: %v", err)
	}
	after, _ := os.ReadFile(file)
	if string(before) != string(after) {
		t.Fatal("CLI refused canonical clear mutated store")
	}
	cleared := call("repo", "base", "clear", "widget")
	if cleared["orphaned"] != true || cleared["exact_key"] != "widget" || cleared["resolution"] != nil {
		t.Fatalf("CLI orphan cleanup followed alias: %+v", cleared)
	}
	base = call("repo", "base", "show", "replacement")
	if base["effective_ref"] != "origin/gamma" || base["reason"] != "Explicit new owner" {
		t.Fatalf("CLI orphan cleanup lost current intent: %+v", base)
	}
	if err := os.WriteFile(file, []byte("schema_version=2\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := repositoryBaseCLI(t, "repo", "base", "show", "widget", "--unmanaged", "--collection", collection); err == nil {
		t.Fatal("CLI unmanaged fallback bypassed corrupt store")
	}
}

func TestRepositoryBaseSetEmptyRefDoesNotClear(t *testing.T) {
	collection := t.TempDir()
	if err := os.MkdirAll(filepath.Join(collection, "harness"), 0700); err != nil {
		t.Fatal(err)
	}
	registry := "repos:\n  - name: widget\n    remote: https://example.invalid/widget.git\n    default_ref: origin/main\n"
	if err := os.WriteFile(filepath.Join(collection, "harness", ".harness-repos.yml"), []byte(registry), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := repositoryBaseCLI(t, "repo", "base", "set", "widget", "beta", "--reason", "Keep this intent", "--collection", collection); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(collection, ".wtc", "repo-bases.toml")
	before, _ := os.ReadFile(file)
	if _, err := repositoryBaseCLI(t, "repo", "base", "set", "widget", "", "--reason", "Empty variable", "--collection", collection); err == nil {
		t.Error("empty set ref silently cleared intent")
	}
	after, _ := os.ReadFile(file)
	if string(before) != string(after) {
		t.Error("empty set mutated persisted intent")
	}
	if _, err := repositoryBaseCLI(t, "repo", "base", "clear", "widget", "--collection", collection); err != nil {
		t.Fatalf("explicit clear failed: %v", err)
	}
}

func TestRepositoryBaseBrowseFinalComparison(t *testing.T) {
	var runtime string
	var nvim bool
	for _, name := range []string{"luajit", "lua", "lua5.4", "lua5.3", "nvim"} {
		if path, err := exec.LookPath(name); err == nil {
			runtime, nvim = path, name == "nvim"
			break
		}
	}
	if runtime == "" {
		if os.Getenv("WTC_TEST_REQUIRE_RUNTIME") == "1" {
			t.Fatal("Lua comparison runtime is required for this test job")
		}
		t.Skip("isolated Lua comparison check needs Lua/LuaJIT/Neovim; dynamic local proof is recorded separately")
	}
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	git := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("fixture git %v: %s %v", args, out, err)
		}
		return strings.TrimSpace(string(out))
	}
	if err := os.Mkdir(repo, 0700); err != nil {
		t.Fatal(err)
	}
	git("init", "-q", "-b", "main")
	file := filepath.Join(repo, "tracked.txt")
	if err := os.WriteFile(file, []byte("base\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "base")
	git("update-ref", "refs/remotes/origin/main", "HEAD")
	git("checkout", "-qb", "topic")
	if err := os.WriteFile(file, []byte("committed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "topic")
	if err := os.WriteFile(file, []byte("dirty\n"), 0600); err != nil {
		t.Fatal(err)
	}
	base := git("merge-base", "HEAD", "origin/main")
	commands := [][]string{{"branch", "--show-current"}, {"status", "--porcelain=v1", "-uno"}, {"ls-files", "-o", "--exclude-standard"}, {"diff", "--numstat", base}}
	for _, ref := range []string{"origin/main", "origin/missing"} {
		commands = append(commands, []string{"rev-parse", "--verify", ref + "^{commit}"}, []string{"merge-base", "HEAD", ref}, []string{"rev-list", "--count", ref + "..HEAD"}, []string{"rev-list", "--count", "HEAD.." + ref}, []string{"diff", "--numstat", ref})
	}
	var script strings.Builder
	script.WriteString("local responses={}\n")
	for _, args := range commands {
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		var stdout, stderr strings.Builder
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		code := 0
		if err := cmd.Run(); err != nil {
			if exit, ok := err.(*exec.ExitError); ok {
				code = exit.ExitCode()
			} else {
				t.Fatal(err)
			}
		}
		fmt.Fprintf(&script, "responses[%q]={code=%d,stdout=%q,stderr=%q}\n", strings.Join(args, "|"), code, stdout.String(), stderr.String())
	}
	view, err := wtc.ReadDefault("browse/wtc-browse.lua")
	if err != nil {
		t.Fatal(err)
	}
	prefix, _, found := strings.Cut(string(view), "local tree_ns =")
	if !found {
		t.Fatal("comparison test boundary missing")
	}
	// Run the actual comparison/collection functions without loading UI plugins.
	script.WriteString("local source=[====[" + prefix + "]====]\n")
	script.WriteString(`
local cases={
 {name='missing configured',configured='origin/missing',error='unavailable comparison ref'},
 {name='valid PR replaces missing configured',configured='origin/missing',pr='main'},
 {name='missing final PR target',configured='origin/main',pr='missing',error='unavailable comparison ref'},
 {name='corrupt state before valid PR',configured='origin/main',pr='main',corrupt=true,error='cannot resolve receiving repository base'},
}
for _,case in ipairs(cases) do
 vim={g={wtc_browse_root='/fixture',wtc_base_command='serving-cli'},fs={normalize=function(x)return x end},uv={now=function()return 0 end},fn={getcwd=function()return '/fixture' end,executable=function()return case.pr and 1 or 0 end},json={decode=function()return {data={effective_ref=case.configured}} end},log={levels={ERROR=1,DEBUG=2}}}
 vim.list_extend=function(a,b)for _,v in ipairs(b)do a[#a+1]=v end;return a end
 vim.system=function(args,opts)
  local result
  if args[1]=='serving-cli' then result={code=case.corrupt and 1 or 0,stdout='data',stderr='malformed state'}
  elseif args[1]=='gh' then result={code=case.pr and 0 or 1,stdout=case.pr or '',stderr=''}
  elseif args[1]=='git' then
   local parts={};for i=4,#args do parts[#parts+1]=args[i] end
   local key=table.concat(parts,'|');result=responses[key]
   assert(result,'unexpected Git command '..key)
   if case.deleted and key:find('origin/main',1,true) then result={code=128,stdout='',stderr='deleted ref'} end
  else error('unexpected command') end
  return {wait=function()return result end}
 end
 local api=assert((loadstring or load)(source..'\nreturn {collect=collect_changes,level=comparison_error_level}'))()
 local ok,result=pcall(api.collect,{path='/fixture/widget',name='widget'})
 if case.error then
  assert(not ok and tostring(result):find(case.error,1,true),case.name..' did not return its truthful error: '..tostring(result))
 else
  assert(ok, tostring(result));assert(result.ref=='origin/main' and result.ahead==1 and result.behind==0 and #result.files==1 and result.files[1].path=='tracked.txt' and result.files[1].kind=='dirty','tracked changes/counts lost after PR selection')
  case.deleted=true
  local cached,cached_error=pcall(api.collect,{path='/fixture/widget',name='widget'})
  assert(not cached and tostring(cached_error):find('unavailable comparison ref',1,true),'cached selected ref was not revalidated')
  assert(api.level(cached_error)==vim.log.levels.ERROR,'comparison watch error is hidden at DEBUG')
  assert(api.level('unrelated watch failure')==vim.log.levels.DEBUG,'unrelated watch error policy changed')
 end
 print('PASS '..case.name)
end
`)
	path := filepath.Join(root, "comparison.lua")
	if err := os.WriteFile(path, []byte(script.String()), 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{path}
	if nvim {
		args = []string{"--headless", "-u", "NONE", "-i", "NONE", "--noplugin", "-l", path}
	}
	cmd := exec.Command(runtime, args...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "XDG_CONFIG_HOME="+filepath.Join(root, "config"), "XDG_DATA_HOME="+filepath.Join(root, "data"), "XDG_STATE_HOME="+filepath.Join(root, "state"), "XDG_CACHE_HOME="+filepath.Join(root, "cache"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("actual Lua comparison functions: %s %v", out, err)
	}
	t.Logf("%s", out)
}
