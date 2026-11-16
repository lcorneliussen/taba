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
	commands := [][]string{{"branch", "--show-current"}, {"status", "--porcelain=v1", "-uno"}, {"ls-files", "-o", "--exclude-standard"}, {"diff", "--numstat", base}, {"diff", "--no-ext-diff", base}}
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
	prefix, _, found := strings.Cut(string(view), "local function start()")
	if !found {
		t.Fatal("comparison test boundary missing")
	}
	_, startup, found := strings.Cut(string(view), "    for _, t in ipairs(trees) do\n")
	if !found {
		t.Fatal("startup loop boundary missing")
	}
	startup, _, found = strings.Cut(startup, "    vim.cmd.tabfirst()")
	if !found {
		t.Fatal("startup loop end missing")
	}
	_, events, found := strings.Cut(string(view), "  vim.api.nvim_create_autocmd({ \"TabEnter\", \"UIEnter\" }, {")
	if !found {
		t.Fatal("tab event boundary missing")
	}
	_, tabenter, found := strings.Cut(events, "    callback = function()\n")
	if !found {
		t.Fatal("tab callback boundary missing")
	}
	tabenter, _, found = strings.Cut(tabenter, "    end,\n  })\nend\n\nlocal function start_once()")
	if !found {
		t.Fatal("tab callback end missing")
	}
	// Run actual functions, callbacks and the startup loop without UI plugins.
	script.WriteString("local source=[====[" + prefix + "]====]\n")
	script.WriteString("local startup_source=[====[for _, t in ipairs(trees) do\n" + startup + "]====]\n")
	script.WriteString("local tabenter_source=[====[" + tabenter + "]====]\n")
	script.WriteString(`
local cases={
 {name='missing configured',configured='origin/missing',error='unavailable comparison ref'},
 {name='valid PR replaces missing configured',configured='origin/missing',pr='main'},
 {name='missing final PR target',configured='origin/main',pr='missing',error='unavailable comparison ref'},
 {name='corrupt state before valid PR',configured='origin/main',pr='main',corrupt=true,error='cannot resolve receiving repository base'},
}
for _,case in ipairs(cases) do
 vim={api={nvim_create_namespace=function()return 1 end},g={wtc_browse_root='/fixture',wtc_base_command='serving-cli'},fs={normalize=function(x)return x end},uv={now=function()return 0 end},fn={getcwd=function()return '/fixture' end,executable=function()return case.pr and 1 or 0 end},json={decode=function()return {data={effective_ref=case.configured}} end},log={levels={ERROR=1,DEBUG=2}}}
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

for _,bad_name in ipairs({'a-broken\ncontrol','z-broken\ncontrol'})do
 local healthy_name='m-healthy'
 local missing,corrupt,now=true,false,0
 local current_name=bad_name
 local buffers,commands,notifications,keys,tabs={},{},{},{},{}
 local function tabvars()local values={};return setmetatable({},{__index=values,__newindex=values})end
 vim={g={wtc_browse_root='/fixture',wtc_base_command='serving-cli'},t=tabvars(),
  fs={normalize=function(x)return x end,basename=function()return 'fixture'end},
  uv={now=function()return now end,fs_scandir=function()return {i=0}end,
   fs_scandir_next=function(fd)fd.i=fd.i+1;return ({bad_name,healthy_name})[fd.i],'directory'end,
   fs_stat=function()return {mtime={sec=1}}end},
  fn={getcwd=function()return '/fixture'end,executable=function()return 0 end,exists=function()return 2 end,fnameescape=function(s)return s end},
  json={decode=function(ref)return {data={effective_ref=ref}}end},
  log={levels={ERROR=1,DEBUG=2}},
  api={nvim_create_namespace=function()return 1 end,
   nvim_buf_set_lines=function(buf,_,_,_,lines)buffers[buf]=lines end,
   nvim_buf_clear_namespace=function()end,nvim_buf_set_extmark=function()end,
   nvim_tabpage_get_var=function(tab,key)if key=='wtc_kind'then return 'repo'end;assert(key=='wtc_repo');return tab==0 and current_name or tabs[tab].wtc_repo end,
   nvim_set_current_tabpage=function(tab)vim.t=tabs[tab];current_name=tabs[tab].wtc_repo end,
   nvim_get_current_win=function()return 1 end,nvim_get_current_buf=function()return 1 end,
   nvim_buf_get_name=function()return '/fixture/'..current_name..'/tracked.txt'end,
   nvim_list_tabpages=function()local ids={};for i=1,#tabs do ids[i]=i end;return ids end},
  keymap={set=function(_,key,fn)keys[key]=fn end},
  cmd=function(s)commands[#commands+1]=s end,
  notify=function(message,level)notifications[#notifications+1]={message=message,level=level}end}
 vim.bo=setmetatable({},{__index=function(t,k)local v={};rawset(t,k,v);return v end})
 vim.list_extend=function(a,b)for _,v in ipairs(b)do a[#a+1]=v end;return a end
 vim.system=function(args,opts)
  local result
  if args[1]=='serving-cli'then
   if corrupt then result={code=1,stdout='',stderr='malformed or ambiguous receiving state: unavailable comparison ref text'}
   else result={code=0,stdout=args[6]==bad_name and missing and 'origin/missing' or 'origin/main',stderr=''}end
  elseif args[1]=='git'then
   local parts={};for i=4,#args do parts[#parts+1]=args[i]end
   local key=table.concat(parts,'|');result=responses[key];assert(result,'unexpected Git command '..key)
   if missing and args[3]=='/fixture/'..bad_name and args[4]=='rev-parse'then result={code=128,stdout='',stderr='deleted ref'}end
  else error('unexpected command')end
  return {wait=function()return result end}
 end
 local startup_function='\nlocal function test_startup(trees) local ensure_index_buf=function(_,repo)return repo end;local ensure_unified=function()return false end; '..startup_source..' end\n'
 local event_function='local function test_tabenter() '..tabenter_source..' end\n'
 local editor_seams='local observed_inline;apply_inline_diff=function(base)observed_inline=base end;load_file_in_win=function()end;show_tree=function()end;apply_tabline=function()end;'
 local api=assert((loadstring or load)(source..startup_function..event_function..editor_seams..'return {all=snapshots_all,flat=flatten_tree,nest=nest_changes,fp=fingerprint,fill=fill_hunk_buf,render=render_index,meta=buf_meta,map=map_search,startup=test_startup,open=open_changed_file,tabenter=test_tabenter,inline=function()return observed_inline end,clear_inline=function()observed_inline=nil end}'))()
 local snaps=api.all()
 local bad,healthy
 for _,snap in ipairs(snaps)do if snap.tree.name==bad_name then bad=snap else healthy=snap end end
 assert(#snaps==2 and bad.comparison_error and not bad.ref and not bad.mb and not bad.ahead and not bad.behind,'error snapshot invented comparison facts')
 assert(healthy.ahead==1 and healthy.behind==0 and #healthy.files==1 and healthy.files[1].path=='tracked.txt','healthy counts/files lost')
 local nested=api.nest(snaps)
 assert(not nested[bad_name].clean and not nested[bad_name].ahead and not nested[bad_name].behind,'error node invented clean/counts')
 local lines,meta,hls=api.flat(nested)
 local error_row,healthy_file=false,false
 for i,line in ipairs(lines)do
  if line:find('unavailable comparison ref',1,true)then
   assert(not meta[i] and not line:find('clean',1,true) and not line:find('[%c]'),'error row actionable, clean or unescaped')
   assert(hls[i][2]=='error','error row lacks diagnostic kind');error_row=true
  end
  if meta[i]and meta[i].repo==healthy_name and meta[i].path=='tracked.txt'then healthy_file=true end
 end
 assert(error_row and healthy_file,'tree lost error or healthy navigation mapping')
 api.fill(1)
 local hunks=table.concat(buffers[1],'\n')
 assert(hunks:find('# ERROR unavailable comparison ref',1,true)and hunks:find('tracked.txt',1,true),'combined hunks lost error or healthy content')
 assert(api.meta[1].hunk_at[healthy_name] and api.meta[1].hunk_at[healthy_name..'/tracked.txt'] and not api.meta[1].hunk_at[bad_name],'combined hunk marks lost or fabricated')
 api.render(2,nil)
 error_row,healthy_file=false,false
 for i,line in ipairs(buffers[2])do
  if line:find('ERROR unavailable comparison ref',1,true)then assert(not api.meta[2].index[i]and not line:find('[%c]')and not line:find('clean',1,true),'alternate error row actionable/clean/unescaped');error_row=true end
  if api.meta[2].index[i]and api.meta[2].index[i].repo==healthy_name and api.meta[2].index[i].path=='tracked.txt'then healthy_file=true end
 end
 assert(error_row and healthy_file,'alternate index lost error or healthy navigation')
 tabs={}
 local command=vim.cmd
 vim.cmd={tabnew=function()vim.t=tabvars();tabs[#tabs+1]=vim.t end,tcd=function()end}
 vim.api.nvim_tabpage_set_var=function(tab,key,value)if tab==0 then vim.t[key]=value else tabs[tab][key]=value end end
 vim.api.nvim_set_current_buf=function(buf)vim.t.selected=buf end
 local trees={};for _,snap in ipairs(snaps)do trees[#trees+1]=snap.tree end
 api.startup(trees)
 local bad_tab,healthy_tab
 for _,tab in ipairs(tabs)do if tab.wtc_repo==bad_name then bad_tab=tab else healthy_tab=tab end end
 assert(#tabs==2 and bad_tab and not bad_tab.wtc_base and bad_tab.selected==bad_name and healthy_tab and healthy_tab.wtc_base==healthy.mb and healthy_tab.selected==healthy_name,'startup loop aborted or lost healthy/error repo tabs')
 vim.cmd=command
 api.map()
 vim.t=bad_tab;vim.t.wtc_base='stale-base'
 keys['<leader>gD']()
 assert(#commands==0 and #notifications==1 and notifications[1].level==vim.log.levels.ERROR and notifications[1].message==bad.comparison_error,'known unavailable comparison fell back in Diffview')
 current_name=healthy_name;vim.t=healthy_tab;vim.t.wtc_base='valid-base';keys['<leader>gD']()
 assert(commands[1]=='DiffviewOpen valid-base -- /fixture/'..healthy_name,'healthy Diffview action changed')
 vim.t.wtc_base=nil;keys['<leader>gD']()
 assert(commands[2]=='DiffviewOpen HEAD -- /fixture/'..healthy_name,'missing base alone invented known failure')
 local before=api.fp(snaps);missing=false
 local recovered=api.all();assert(api.fp(recovered)~=before,'error recovery did not change fingerprint')
 local recovered_bad
 for _,snap in ipairs(recovered)do if snap.tree.name==bad_name then recovered_bad=snap end end
 assert(not recovered_bad.comparison_error and recovered_bad.ahead==1 and #recovered_bad.files==1,'recovery lost actual comparison')
 current_name=bad_name;vim.t=bad_tab;keys['<leader>gD']()
 assert(bad_tab.wtc_base==recovered_bad.mb and commands[3]=='DiffviewOpen '..recovered_bad.mb..' -- /fixture/'..bad_name,'actual recovered startup tab kept unset base or compared HEAD: '..tostring(bad_tab.wtc_base)..' / '..tostring(commands[3]))
 assert(#commands==3 and commands[3]:find('DiffviewOpen '..recovered_bad.mb,1,true),'known error not cleared on verified recovery')
 missing=true
 local disappeared=api.all();local failed_again
 for _,snap in ipairs(disappeared)do if snap.tree.name==bad_name then failed_again=snap end end
 assert(failed_again.comparison_error and api.fp(disappeared)~=api.fp(recovered),'cached ref disappearance did not refresh error')
 keys['<leader>gD']();assert(#commands==3 and #notifications==2,'cached disappearance allowed stale comparison action')
 missing=false;bad_tab.wtc_base=nil
 api.fill(3)
 assert(bad_tab.wtc_base==recovered_bad.mb,'hunk-only recovery did not repair actual startup tab base')
 keys['<leader>gD']()
 assert(#commands==4 and commands[4]=='DiffviewOpen '..recovered_bad.mb..' -- /fixture/'..bad_name,'hunk recovery compared HEAD')
 assert(healthy_tab.wtc_base==nil,'known-error recovery changed unrelated missing-base-only tab')
 assert(rawget(bad_tab,'wtc_base')==nil and bad_tab.wtc_base==recovered_bad.mb,'fixture must model actual Neovim tab-variable proxy')
 vim.cmd=setmetatable({tcd=function()end},{__call=function(_,command)commands[#commands+1]=command end})
 vim.b={};vim.bo.buftype=''
 api.clear_inline();api.open(bad_name,'tracked.txt')
 assert(api.inline()==recovered_bad.mb,'open-file inline consumer missed recovered proxy base')
 api.clear_inline();api.tabenter()
 assert(api.inline()==recovered_bad.mb,'TabEnter inline consumer missed recovered proxy base')
 corrupt=true;now=60001
 local ok,err=pcall(api.all)
 assert(not ok and tostring(err):find('malformed or ambiguous receiving state',1,true),'untyped configured-state error swallowed by comparison text')
 print('PASS sibling isolation '..(bad_name:sub(1,1)=='a'and'broken first'or'broken last')..': tree/index/hunks/startup, counts/content/navigation, nonactionable escaped errors, cached disappearance/recovery, typed guards and proxy Diffview/open-file/TabEnter readers')
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
