package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"taba.sh/taba/internal/wtc"
)

// rexTestFixture keeps Rex's sessions, windows, focus and split trees in a
// JSON file and answers the rex commands taba uses the way Rex does.
const rexTestFixture = `#!/usr/bin/env python3
import json, os, sys
args = sys.argv[1:]
if args and args[0] == "--autostart=false": args = args[1:]
state_path = os.environ["WTC_TEST_REX_STATE"]
with open(os.environ["WTC_TEST_REX_CALLS"], "a") as f: f.write(json.dumps(args, separators=(",", ":")) + "\n")
if os.environ.get("WTC_TEST_REX_DOWN"):
    print("no Rex server is running", file=sys.stderr); sys.exit(4)
try: state = json.load(open(state_path))
except FileNotFoundError: state = {"next": 0, "sessions": [], "blocks": {}}
def save(): json.dump(state, open(state_path, "w"))
def out(v): print(json.dumps(v))
def nid(kind):
    state["next"] += 1
    return kind + ":" + str(state["next"])
def opt(name, default=None):
    return args[args.index(name)+1] if name in args else default
def command():
    return args[args.index("--")+1:] if "--" in args else []
def session(sid): return next(s for s in state["sessions"] if s["session_id"] == sid)
def leaves(n):
    if n is None: return []
    if "block" in n: return [n["block"]]
    return leaves(n["before"]) + leaves(n["after"])
def window_of(bid):
    for s in state["sessions"]:
        for w in s["windows"]:
            if bid in leaves(w["tree"]): return s, w
    print("not found: " + bid, file=sys.stderr); sys.exit(1)
def new_block(label, cwd):
    bid = nid("block")
    state["blocks"][bid] = {"label": label, "fg": "-zsh", "cwd": cwd, "cmd": command()}
    return bid
def insert(n, anchor, bid, direction, pct):
    if "block" in n:
        if n["block"] != anchor: return n
        node = {"dir": "vertical" if direction in ("below", "above") else "horizontal", "ratio": 1 - pct/100.0}
        if direction in ("right", "below"): node["before"], node["after"] = n, {"block": bid}
        else: node["before"], node["after"] = {"block": bid}, n
        return node
    n["before"], n["after"] = insert(n["before"], anchor, bid, direction, pct), insert(n["after"], anchor, bid, direction, pct)
    return n
def remove(n, bid):
    if n is None: return None
    if "block" in n: return None if n["block"] == bid else n
    b, a = remove(n["before"], bid), remove(n["after"], bid)
    if b is None: return a
    if a is None: return b
    n["before"], n["after"] = b, a
    return n
def take(bid):
    s, w = window_of(bid)
    w["tree"] = remove(w["tree"], bid)
    if w.get("focused") == bid: w["focused"] = (leaves(w["tree"]) or [None])[0]
    return s, w
def prune():
    for s in state["sessions"]:
        s["windows"] = [w for w in s["windows"] if w["tree"] is not None]
        if not any(w["window_id"] == s.get("active") for w in s["windows"]):
            s["active"] = s["windows"][0]["window_id"] if s["windows"] else None
def render(n, x, y, w, h):
    if "block" in n: return {"rect": [x, y, w, h], "block_id": n["block"]}
    r = n["ratio"]
    if n["dir"] == "horizontal":
        b, a = render(n["before"], x, y, w*r, h), render(n["after"], x+w*r, y, w*(1-r), h)
    else:
        b, a = render(n["before"], x, y, w, h*r), render(n["after"], x, y+h*r, w, h*(1-r))
    return {"rect": [x, y, w, h], "split": {"before": b, "after": a}}
def block_view(b):
    v = {"block_id": b, "label": state["blocks"][b]["label"]}
    if state["blocks"][b].get("owner"): v["owner"] = state["blocks"][b]["owner"]
    return v
def fg_name(b): return os.path.basename(state["blocks"][b]["fg"].lstrip("-"))
a = args
if a[:1] == ["ls"]:
    out({"sessions": [{"session_id": s["session_id"], "label": s["label"]} for s in state["sessions"]]})
elif a[:2] == ["session", "inspect"]:
    s = session(a[2])
    out({"session_id": s["session_id"], "label": s["label"], "active_window_id": s.get("active"),
         "windows": [{"window_id": w["window_id"], "label": w["label"], "focused_block_id": w.get("focused"),
                      "layers": [{"kind": "tiled", "layout": render(w["tree"], 0, 0, 1, 1),
                                  "blocks": [block_view(b) for b in leaves(w["tree"])]}]} for w in s["windows"]]})
elif a[0] == "new":
    sid, wid = nid("session"), nid("window")
    bid = new_block("shell", opt("-c"))
    state["sessions"].append({"session_id": sid, "label": a[1], "active": wid,
                              "windows": [{"window_id": wid, "label": opt("--window", "Window " + wid[-4:]), "focused": bid, "tree": {"block": bid}}]})
    save(); out({"session_id": sid, "initial_windows": [{"window_id": wid, "block_ids": [bid]}]})
elif a[:2] == ["window", "new"]:
    s = session(opt("-s")); wid = nid("window")
    bid = new_block("shell", opt("-c"))
    s["windows"].append({"window_id": wid, "label": a[2], "focused": bid, "tree": {"block": bid}})
    if opt("--focus=false") is None and "--focus=false" not in a: s["active"] = wid
    save(); out({"window_id": wid, "block_ids": [bid]})
elif a[:2] == ["window", "rename"]:
    for s in state["sessions"]:
        for w in s["windows"]:
            if w["window_id"] == a[2]: w["label"] = a[3]
    save()
elif a[:2] == ["block", "rename"]:
    state["blocks"][a[2]]["label"] = a[3]; save()
elif a[:2] == ["block", "inspect"]:
    s, w = window_of(a[2])
    out({"session_id": s["session_id"], "block": {"block_id": a[2], "label": state["blocks"][a[2]]["label"], "window_id": w["window_id"]}})
elif a[:2] == ["block", "close"]:
    take(a[2]); prune(); del state["blocks"][a[2]]; save()
elif a[:2] == ["block", "call"]:
    bid = opt("-b"); b = state["blocks"][bid]; method = a[-1]
    if b.get("gone"): print("block not found: " + bid, file=sys.stderr); sys.exit(3)
    if method == os.environ.get("WTC_TEST_REX_BROKEN_CALL", "process" if os.environ.get("WTC_TEST_REX_BROKEN") else None): print("internal error", file=sys.stderr); sys.exit(1)
    if method == "process":
        out({"child": {"pid": b.get("child_pid", 99901), "name": "zsh", "argv0": "-zsh"},
             "foreground": {"pid": b.get("pid", 99902), "name": fg_name(bid), "argv0": b["fg"]},
             "ancestors": [{"pid": b.get("child_pid", 99901), "name": "zsh"}]})
    elif method == "program_status":
        st = b.get("status") or os.environ.get("WTC_TEST_REX_STATUS")
        owner = {"pid": b.get("pid", 99902), "name": os.environ.get("WTC_TEST_REX_STATUS_OWNER") or fg_name(bid)}
        out({"records": [{"state": st, "updated_at": "now", "owner": owner}] if st else []})
    elif method == "size":
        out({"columns": int(os.environ.get("WTC_TEST_REX_COLUMNS", "120")), "rows": 3, "owner": b.get("owner")})
elif a[0] == "split":
    anchor = opt("-b"); s, w = window_of(anchor)
    bid = new_block(opt("--label", "shell"), opt("-c"))
    w["tree"] = insert(w["tree"], anchor, bid, opt("--split", "right"), int(opt("--ratio", "50")))
    save(); out({"anchor_id": anchor, "block_ids": [bid]})
elif a[0] == "move":
    bid, direction, target = a[1], a[2], a[3]
    s, w = window_of(bid); focused = w.get("focused") == bid
    take(bid)
    s2, w2 = window_of(target); w2["tree"] = insert(w2["tree"], target, bid, direction, int(opt("--ratio", "50")))
    if focused: w2["focused"], s2["active"] = bid, w2["window_id"]
    prune(); save()
elif a[0] == "send":
    b = state["blocks"][opt("-b")]
    text = command()[0].lstrip("\x15")
    if text.endswith("\r"):
        b["ran"] = text[:-1]; b["fg"] = b["ran"].split("&& ")[-1].split()[0].strip("'")
    else:
        b["typed"] = text
    save()
elif a[0] == "send-key":
    b = state["blocks"][opt("-b")]; b["keys"] = b.get("keys", []) + a[3:]
    if "ctrl+c" in a[3:] and b.get("interruptible"): b["fg"] = "-zsh"
    save()
elif a[0] == "kill":
    state["sessions"] = [s for s in state["sessions"] if s["session_id"] != a[1]]; save()
elif a[0] == "capture":
    print(state["blocks"][opt("-b")].get("screen", "old output\nline one\nline two\nline three"))
elif a[:3] == ["api", "call", "session.focus_window"]:
    request = json.loads(a[3]); session(request["session_id"])["active"] = request["window_id"]; save()
elif a[:2] == ["api", "call"]:
    pass
elif a[0] == "do":
    if os.environ.get("WTC_TEST_REX_NO_REMOTE"): print("remote control is off", file=sys.stderr); sys.exit(1)
else:
    print("unexpected command: " + " ".join(a), file=sys.stderr); sys.exit(2)
`

type rexTestBlock struct {
	Label         string   `json:"label"`
	Fg            string   `json:"fg"`
	Cmd           []string `json:"cmd"`
	Ran           string   `json:"ran"`
	Owner         string   `json:"owner,omitempty"`
	PID           int      `json:"pid,omitempty"`
	ChildPID      int      `json:"child_pid,omitempty"`
	CWD           string   `json:"cwd"`
	Typed         string   `json:"typed,omitempty"`
	Status        string   `json:"status,omitempty"`
	Keys          []string `json:"keys,omitempty"`
	Interruptible bool     `json:"interruptible,omitempty"`
	Gone          bool     `json:"gone,omitempty"`
}

type rexTestWindow struct {
	ID      string `json:"window_id"`
	Label   string `json:"label"`
	Focused string `json:"focused"`
	Tree    any    `json:"tree"`
}

type rexTestSession struct {
	ID      string          `json:"session_id"`
	Label   string          `json:"label"`
	Active  string          `json:"active"`
	Windows []rexTestWindow `json:"windows"`
}

type rexTestState struct {
	Next     int                     `json:"next"`
	Sessions []rexTestSession        `json:"sessions"`
	Blocks   map[string]rexTestBlock `json:"blocks"`
}

// rexTestSetup puts the fixture on PATH as rex and returns its call log.
func rexTestSetup(t *testing.T, bin string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(bin, "rex"), []byte(rexTestFixture), 0755); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(os.Getenv("PATH"), bin+string(os.PathListSeparator)) {
		t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	}
	dir := t.TempDir()
	t.Setenv("WTC_TEST_REX_STATE", filepath.Join(dir, "rex-state.json"))
	calls := filepath.Join(dir, "rex-calls.log")
	t.Setenv("WTC_TEST_REX_CALLS", calls)
	return calls
}

func rexTestRead(t *testing.T) rexTestState {
	t.Helper()
	var state rexTestState
	data, err := os.ReadFile(os.Getenv("WTC_TEST_REX_STATE"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	return state
}

func rexTestWrite(t *testing.T, state rexTestState) {
	t.Helper()
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(os.Getenv("WTC_TEST_REX_STATE"), data, 0600); err != nil {
		t.Fatal(err)
	}
}

func rexTestEditBlock(t *testing.T, label string, edit func(*rexTestBlock)) string {
	t.Helper()
	state := rexTestRead(t)
	for id, block := range state.Blocks {
		if block.Label == label {
			edit(&block)
			state.Blocks[id] = block
			rexTestWrite(t, state)
			return id
		}
	}
	t.Fatalf("no %s pane: %+v", label, state.Blocks)
	return ""
}

func rexTestLabels(state rexTestState) map[string]int {
	labels := map[string]int{}
	for _, block := range state.Blocks {
		labels[block.Label]++
	}
	return labels
}

func rexTestCalls(t *testing.T) string {
	t.Helper()
	calls, err := os.ReadFile(os.Getenv("WTC_TEST_REX_CALLS"))
	if err != nil {
		t.Fatal(err)
	}
	return string(calls)
}

func rexTestOpen(t *testing.T, tools ...string) (*wtc.Context, string, string) {
	t.Helper()
	c, _, bin, _, collection := openTestFixture(t)
	rexTestSetup(t, bin)
	opt := openOptions{Session: "test", Multiplexer: "rex", NoAgent: true, NoBrowse: true, NoStatus: true, Tools: tools}
	if opened := openCollection(c, "sample", opt, "wide", true, true); opened.Error != "" {
		t.Fatalf("open failed: %+v", opened)
	}
	return c, bin, collection
}

func TestRexOpenCreatesRepairsAndSwitchesWorkspace(t *testing.T) {
	c, _, bin, _, collection := openTestFixture(t)
	calls := rexTestSetup(t, bin)
	unrelated, _, err := newTerminalHost("rex", "test").CreateWorkspace("other", t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	opt := openOptions{Session: "test", Multiplexer: "rex", NoAgent: true, NoBrowse: true, NoStatus: true, Tools: []string{"browse", "shell"}}
	if preview := openCollection(c, "sample", openOptions{Session: "test", Multiplexer: "rex", DryRun: true}, "wide", true, true); preview.Error != "" || !strings.Contains(strings.Join(preview.Actions, " "), "would create") {
		t.Fatalf("wrong preview: %+v", preview)
	}
	opened := openCollection(c, "sample", opt, "wide", true, true)
	if opened.Error != "" || !strings.Contains(strings.Join(opened.Actions, " "), "workspace created") {
		t.Fatalf("open failed: %+v", opened)
	}
	state := rexTestRead(t)
	state.Sessions = slices.DeleteFunc(state.Sessions, func(s rexTestSession) bool { return s.ID == unrelated })
	if len(state.Sessions) != 1 || state.Sessions[0].Label != "test/sample" || opened.Workspace != state.Sessions[0].ID {
		t.Fatalf("collection is not one Rex session labelled session/collection: %+v", state.Sessions)
	}
	if windows := state.Sessions[0].Windows; len(windows) != 1 || windows[0].Label != "main" {
		t.Fatalf("wide layout is not one tab called main: %+v", windows)
	}
	if labels := rexTestLabels(state); len(state.Blocks) != 5 || labels["agent"] != 1 || labels["status"] != 1 || labels["browse"] != 1 || labels["shell"] != 1 {
		t.Fatalf("wide layout has the wrong panes: %+v", state.Blocks)
	}
	env, err := openWorkspaceEnvironment(c, "test", "sample")
	if err != nil {
		t.Fatal(err)
	}
	for id, block := range state.Blocks {
		if block.CWD != collection {
			continue
		}
		want := append(append([]string{"/usr/bin/env"}, env...), rexLoginShell(), "-l")
		if !slices.Equal(block.Cmd, want) || block.CWD != collection {
			t.Fatalf("pane %s does not start in the collection with its environment:\n got %q in %s\nwant %q", id, block.Cmd, block.CWD, want)
		}
	}
	if !slices.Contains(env, "WTC_COLLECTION='sample'") || !slices.Contains(env, "WTC_AGENT_NAME=test--sample") {
		t.Fatalf("collection environment is incomplete: %q", env)
	}
	if log, _ := os.ReadFile(calls); strings.Contains(string(log), `"server"`) {
		t.Fatalf("a Rex server was started:\n%s", log)
	}
	if listed := openCollection(c, "sample", openOptions{Session: "test", Multiplexer: "rex", List: true}, "wide", true, true); listed.Layout != "wide" || !strings.Contains(strings.Join(listed.Actions, " "), "agent: empty") {
		t.Fatalf("wide workspace is not listed as wide: %+v", listed)
	}
	again := openCollection(c, "sample", opt, "wide", true, true)
	if again.Error != "" || len(rexTestRead(t).Blocks) != 5 {
		t.Fatalf("rerun changed a complete layout: %+v", again)
	}
	narrow := opt
	narrow.LayoutSet = true
	switched := openCollection(c, "sample", narrow, "narrow", true, true)
	if switched.Error != "" || !strings.Contains(strings.Join(switched.Actions, " "), "layout switched") {
		t.Fatalf("wide-to-narrow switch failed: %+v", switched)
	}
	sample := func() rexTestSession {
		t.Helper()
		for _, session := range rexTestRead(t).Sessions {
			if session.ID == opened.Workspace {
				return session
			}
		}
		t.Fatal("the collection's session is gone")
		return rexTestSession{}
	}
	state = rexTestRead(t)
	tabs := map[string]bool{}
	for _, window := range sample().Windows {
		tabs[window.Label] = true
	}
	if len(state.Blocks) != 5 || !tabs["main"] || !tabs["tools"] || len(tabs) != 2 {
		t.Fatalf("narrow layout did not keep the panes in main and tools tabs: %+v", state)
	}
	if listed := openCollection(c, "sample", openOptions{Session: "test", Multiplexer: "rex", List: true}, "wide", true, true); listed.Layout != "narrow" {
		t.Fatalf("stacked tabs are not reported as narrow: %+v", listed)
	}
	back := opt
	back.LayoutSet = true
	if wide := openCollection(c, "sample", back, "wide", true, true); wide.Error != "" || wide.Layout != "wide" || len(sample().Windows) != 1 {
		t.Fatalf("narrow-to-wide switch failed: %+v %+v", wide, rexTestRead(t))
	}
	if listed := openCollection(c, "sample", openOptions{Session: "test", Multiplexer: "rex", List: true}, "wide", true, true); listed.Layout != "wide" {
		t.Fatalf("switched workspace is not wide: %+v", listed)
	}
	for _, line := range strings.Split(rexTestCalls(t), "\n") {
		if strings.HasPrefix(line, `["session","inspect"`) && strings.Contains(line, `"`+unrelated+`"`) {
			t.Fatalf("classifying the collection inspected another session: %s", line)
		}
	}
}

func TestRexPanesGetTheLiteralEnvironment(t *testing.T) {
	dir := t.TempDir()
	report := filepath.Join(dir, "env")
	shell := filepath.Join(dir, "login shell")
	if err := os.WriteFile(shell, []byte("#!/bin/sh\n[ \"$1\" = -l ] && env > \"$REPORT\"\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SHELL", shell)
	values := map[string]string{
		"SPACED":   "two words",
		"DOLLAR":   "$HOME and ${PATH}",
		"SINGLE":   "it's",
		"QUOTED":   "'sample'",
		"DOUBLE":   `say "hi"`,
		"BACKTICK": "`id` $(id)",
		"UNICODE":  "Grüße ✓ 漢字",
		"ROOT":     "/Users/x/Library/Application Support/taba",
	}
	env := []string{"REPORT=" + report}
	for name, value := range values {
		env = append(env, name+"="+value)
	}
	args := rexTerminal(dir, env)
	if i := slices.Index(args, "--"); i < 0 || !slices.Contains(args[:i], "--shell=none") || args[i+1] != "/usr/bin/env" {
		t.Fatalf("pane does not start env(1) without a shell: %q", args)
	} else {
		cmd := exec.Command(args[i+1], args[i+2:]...)
		cmd.Env = []string{"PATH=/usr/bin:/bin"}
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("pane command failed: %v\n%s", err, out)
		}
	}
	data, err := os.ReadFile(report)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		if name, value, ok := strings.Cut(line, "="); ok {
			got[name] = value
		}
	}
	for name, value := range values {
		if got[name] != value {
			t.Errorf("%s reached the shell as %q, want %q", name, got[name], value)
		}
	}
	if plain := rexTerminal(dir, nil); slices.Contains(plain, "--") {
		t.Fatalf("a pane without settings does not get Rex's own shell: %q", plain)
	}
}

func TestRexOpenStartsEachAgentKind(t *testing.T) {
	for _, tc := range []struct {
		kind, program, action string
		promptArgument        bool
	}{
		{kind: "claude", program: "claude", action: "agent started", promptArgument: true},
		{kind: "codex", program: "codex", action: "agent started", promptArgument: true},
		{kind: "gemini", program: "gemini", action: "agent started; first prompt not sent"},
		{kind: "cursor", program: "cursor-agent", action: "agent started; first prompt not sent"},
		{kind: "devin", action: "agent start failed: taba cannot start agent kind \"devin\" in Rex"},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			c, _, bin, _, collection := openTestFixture(t)
			rexTestSetup(t, bin)
			if err := os.WriteFile(filepath.Join(collection, "HANDOFF.md"), []byte("# Task\n"), 0644); err != nil {
				t.Fatal(err)
			}
			opt := openOptions{Session: "test", Multiplexer: "rex", Agent: tc.kind, NoBrowse: true, NoStatus: true}
			opened := openCollection(c, "sample", opt, "wide", true, true)
			if !strings.Contains(strings.Join(opened.Actions, "; "), tc.action) {
				t.Fatalf("wrong start report: %+v", opened)
			}
			var ran string
			for _, block := range rexTestRead(t).Blocks {
				if block.Label == "agent" {
					ran = block.Ran
				}
			}
			if tc.program == "" {
				if ran != "" {
					t.Fatalf("an unknown kind ran %q", ran)
				}
				return
			}
			prompt, err := firstAgentPrompt(c.Prompts(), collection, tc.kind)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(ran, shellQuote(tc.program)) || strings.HasSuffix(ran, shellQuote(prompt.String())) != tc.promptArgument {
				t.Fatalf("agent command %q: want program %s, prompt argument %v", ran, tc.program, tc.promptArgument)
			}
			if tc.kind == "claude" && !strings.Contains(ran, "'--dangerously-skip-permissions' '--remote-control' 'test--sample'") {
				t.Fatalf("claude does not get the herdr defaults: %q", ran)
			}
			if strings.Contains(rexTestCalls(t), `"block.event"`) {
				t.Fatal("the first prompt was typed into the agent")
			}
		})
	}
}

func TestRexReportsTheStateOfTheForegroundAgent(t *testing.T) {
	c, _, bin, _, _ := openTestFixture(t)
	rexTestSetup(t, bin)
	opt := openOptions{Session: "test", Multiplexer: "rex", NoBrowse: true, NoStatus: true, NoFirstPrompt: true}
	if opened := openCollection(c, "sample", opt, "wide", true, true); opened.Error != "" {
		t.Fatalf("open failed: %+v", opened)
	}
	list := openOptions{Session: "test", Multiplexer: "rex", List: true}
	state := func() string {
		t.Helper()
		listed := openCollection(c, "sample", list, "wide", true, true)
		for _, action := range listed.Actions {
			if after, ok := strings.CutPrefix(action, "agent: "); ok {
				return after
			}
		}
		t.Fatalf("no agent state: %+v", listed)
		return ""
	}
	if got := state(); got != "claude unknown" {
		t.Fatalf("an agent that reports nothing is %q", got)
	}
	t.Setenv("WTC_TEST_REX_STATUS", "blocked")
	if got := state(); got != "claude blocked" {
		t.Fatalf("a reported state is listed as %q", got)
	}
	t.Setenv("WTC_TEST_REX_STATUS_OWNER", "zsh")
	if got := state(); got != "claude unknown" {
		t.Fatalf("a report from another process with the same pid counts: %q", got)
	}
	t.Setenv("WTC_TEST_REX_STATUS_OWNER", "")
	t.Setenv("WTC_TEST_REX_STATUS", "working")
	if again := openCollection(c, "sample", opt, "wide", true, true); !strings.Contains(strings.Join(again.Actions, " "), "agent live (claude working)") {
		t.Fatalf("a live agent was not left alone: %+v", again)
	}
}

func TestRexTrimClosesIdleToolPanes(t *testing.T) {
	c, bin, _ := rexTestOpen(t, "shell", "browse")
	if err := os.WriteFile(filepath.Join(bin, "nvim"), []byte("#!/bin/sh\necho \"$OPEN_TEST_NVIM_PID 1 0\"\n"), 0755); err != nil {
		t.Fatal(err)
	}
	main := rexTestRead(t).Sessions[0].Windows[0].ID
	shell := rexTestEditBlock(t, "shell", func(b *rexTestBlock) { b.Owner = "client:app" })
	state := rexTestRead(t)
	state.Sessions[0].Active = main
	for i := range state.Sessions[0].Windows {
		state.Sessions[0].Windows[i].Focused = shell
	}
	rexTestWrite(t, state)
	browsePID, nvimPID := rexTestBrowseProcess(t)
	rexTestEditBlock(t, "browse", func(b *rexTestBlock) { b.Fg, b.PID = "taba", browsePID })
	t.Setenv("OPEN_TEST_NVIM_PID", "1")
	trim := openOptions{Session: "test", Multiplexer: "rex", Trim: true}
	item := openTrimCollection(c, "sample", trim)
	if slices.Sort(item.Actions); item.Error != "" || strings.Join(item.Actions, ", ") != "browse kept (Neovim on the socket runs elsewhere), shell kept (focused)" {
		t.Fatalf("trim did not keep a focused shell and someone else's Neovim: %+v", item)
	}
	rexTestEditBlock(t, "shell", func(b *rexTestBlock) { b.Owner = "" })
	t.Setenv("OPEN_TEST_NVIM_PID", strconv.Itoa(nvimPID))
	item = openTrimCollection(c, "sample", trim)
	if slices.Sort(item.Actions); item.Error != "" || strings.Join(item.Actions, ", ") != "browse closed, shell closed" {
		t.Fatalf("idle browse and shell were not trimmed: %+v", item)
	}
	if labels := rexTestLabels(rexTestRead(t)); labels["shell"] != 0 || labels["browse"] != 0 || labels["agent"] != 1 || labels["status"] != 1 {
		t.Fatalf("trim touched the wrong panes: %+v", labels)
	}
}

// rexTestBrowseProcess starts a process that ps shows as "taba browse" with
// a child, as a browse pane's taba and Neovim.
func rexTestBrowseProcess(t *testing.T) (browse, child int) {
	t.Helper()
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child")
	if err := os.WriteFile(filepath.Join(dir, "browse"), []byte("sleep 60 &\necho $! > child\nwait\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cmd := &exec.Cmd{Path: "/bin/sh", Args: []string{"taba", "browse"}, Dir: dir}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	for range 100 {
		if data, err := os.ReadFile(pidFile); err == nil && strings.HasSuffix(string(data), "\n") {
			child, _ = strconv.Atoi(strings.TrimSpace(string(data)))
			t.Cleanup(func() {
				if process, err := os.FindProcess(child); err == nil {
					_ = process.Kill()
				}
			})
			return cmd.Process.Pid, child
		}
		time.Sleep(20 * time.Millisecond) //nolint:forbidigo // Waits for a real child process to start.
	}
	t.Fatal("browse stand-in did not start its child")
	return 0, 0
}

func TestRexMovesKeepTheActiveWindow(t *testing.T) {
	c, _, _ := rexTestOpen(t, "shell")
	state := rexTestRead(t)
	main := state.Sessions[0].Windows[0]
	state.Sessions[0].Windows[0].Focused = rexTestEditBlock(t, "shell", func(*rexTestBlock) {})
	rexTestWrite(t, state)
	opt := openOptions{Session: "test", Multiplexer: "rex", NoAgent: true, NoBrowse: true, NoStatus: true, Tools: []string{"shell"}, LayoutSet: true}
	if switched := openCollection(c, "sample", opt, "narrow", true, true); switched.Error != "" {
		t.Fatalf("switch failed: %+v", switched)
	}
	state = rexTestRead(t)
	if len(state.Sessions[0].Windows) != 2 || state.Sessions[0].Active != main.ID {
		t.Fatalf("moving the focused shell to the tools tab switched the shown tab: %+v", state.Sessions[0])
	}
	if !strings.Contains(rexTestCalls(t), `"session.focus_window"`) {
		t.Fatal("the fixture did not move focus with the shell")
	}
}

func TestRexRetireClosesCollectionSession(t *testing.T) {
	c, _, bin, _, _ := openTestFixture(t)
	rexTestSetup(t, bin)
	host := newTerminalHost("rex", "test")
	if _, _, err := host.CreateWorkspace("sample", t.TempDir(), nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err := host.CreateWorkspace("other", t.TempDir(), nil); err != nil {
		t.Fatal(err)
	}
	closer := retireCloser(host)
	if closed, warning := closer("sample", "session:999"); closed || !strings.Contains(warning, "identity changed") {
		t.Fatalf("a changed session identity was closed: %v %q", closed, warning)
	}
	if closed, warning := closer("sample", ""); !closed || warning != "" {
		t.Fatalf("collection session was not closed: %v %q", closed, warning)
	}
	if state := rexTestRead(t); len(state.Sessions) != 1 || state.Sessions[0].Label != "test/other" {
		t.Fatalf("retire closed the wrong sessions: %+v", state.Sessions)
	}
	if retireCloser(newTerminalHost("herdr", "test")) != nil {
		t.Fatal("herdr retirement must keep closing its own workspace")
	}

	herdrLog := filepath.Join(t.TempDir(), "herdr.log")
	herdr := "#!/bin/sh\necho \"$*\" >> " + shellQuote(herdrLog) + "\ncase \"$*\" in *'workspace list') echo '{\"result\":{\"workspaces\":[{\"label\":\"other\",\"workspace_id\":\"w9\"}]}}' ;; esac\n"
	if err := os.WriteFile(filepath.Join(bin, "herdr"), []byte(herdr), 0755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := newTerminalHost("rex", openSession(c, "")).CreateWorkspace("other", t.TempDir(), nil); err != nil {
		t.Fatal(err)
	}
	if closed, warning := retireEverywhere(c)("other", ""); !closed || warning != "" {
		t.Fatalf("retiring another collection did not close it: %v %q", closed, warning)
	}
	if log, _ := os.ReadFile(herdrLog); !strings.Contains(string(log), "workspace close w9") || len(rexTestRead(t).Sessions) != 1 {
		t.Fatalf("retiring another collection did not close both its herdr workspace and its Rex session:\n%s%+v", log, rexTestRead(t).Sessions)
	}
}

func TestRexSelfRetireHandsOffToCleanupSession(t *testing.T) {
	c, _ := retireHandoffFixture(t, "unused")
	rexTestSetup(t, filepath.Join(filepath.Dir(c.Collection), "bin"))
	t.Setenv("WTC_MULTIPLEXER", "rex")
	host := newTerminalHost("rex", "proj")
	source, pane, err := host.CreateWorkspace("finished", c.Collection, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("REX_BLOCK", pane)
	if err := handoffSelfRetire(c, "finished", false, true); err != nil {
		t.Fatal(err)
	}
	state := rexTestRead(t)
	if !slices.ContainsFunc(state.Sessions, func(s rexTestSession) bool { return s.Label == "proj/--cleanup--" }) {
		t.Fatalf("no cleanup session: %+v", state.Sessions)
	}
	var worker rexTestBlock
	for _, block := range state.Blocks {
		if slices.Contains(block.Cmd, "WTC_RETIRE_SOURCE_WORKSPACE="+source) {
			worker = block
		}
	}
	for _, setting := range []string{"WTC_RETIRE_SESSION=proj", "WTC_RETIRE_SOURCE_PANE=" + pane, "WTC_RETIRE_TARGET=finished", "WTC_RETIRE_MULTIPLEXER=rex"} {
		if !slices.Contains(worker.Cmd, setting) {
			t.Fatalf("cleanup pane lacks %s: %q", setting, worker.Cmd)
		}
	}
	if slices.ContainsFunc(worker.Cmd, func(arg string) bool { return strings.HasPrefix(arg, "HARNESS_HERDR_SESSION=") }) {
		t.Fatalf("a Rex cleanup pane names a herdr session: %q", worker.Cmd)
	}
	if !strings.Contains(worker.Ran, " retire-worker 'finished'") || worker.CWD != c.Workspace {
		t.Fatalf("cleanup pane does not run the worker in the workspace root: %+v", worker)
	}
	if _, err := os.Stat(c.Collection); err != nil {
		t.Fatalf("handoff deleted the collection before the worker ran: %v", err)
	}
}

func TestRexRetireWorkerClosesTheSourceSession(t *testing.T) {
	target, herdrLog := retireWorkerFixture(t)
	rexTestSetup(t, filepath.Join(filepath.Dir(target), "bin"))
	host := newTerminalHost("rex", "fixture")
	source, sourcePane, err := host.CreateWorkspace("finished", target, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, cleanupPane, err := host.CreateWorkspace(cleanupWorkspaceLabel, filepath.Dir(target), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := host.RenamePane(sourcePane, "agent"); err != nil {
		t.Fatal(err)
	}
	rexTestEditBlock(t, "agent", func(b *rexTestBlock) { b.Fg = "claude" })
	t.Setenv("WTC_TEST_REX_STATUS", "done")
	t.Setenv("HERDR_ENV", "")
	t.Setenv("REX_BLOCK", cleanupPane)
	t.Setenv("WTC_RETIRE_MULTIPLEXER", "rex")
	t.Setenv("WTC_RETIRE_SOURCE_WORKSPACE", source)
	t.Setenv("WTC_RETIRE_SOURCE_PANE", sourcePane)
	if err := runRetireWorker("finished", false, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("target remains: %v", err)
	}
	state := rexTestRead(t)
	if len(state.Sessions) != 1 || state.Sessions[0].Label != "fixture/"+cleanupWorkspaceLabel {
		t.Fatalf("the worker did not close exactly the source session: %+v", state.Sessions)
	}
	if log, _ := os.ReadFile(herdrLog); strings.Contains(string(log), "workspace close") {
		t.Fatalf("a Rex retirement closed a herdr workspace: %s", log)
	}
}

func TestRexRetireWaitsWhenItCannotTellWhetherTheSourceAgentRuns(t *testing.T) {
	target, _ := retireWorkerFixture(t)
	rexTestSetup(t, filepath.Join(filepath.Dir(target), "bin"))
	host := newTerminalHost("rex", "fixture")
	source, sourcePane, err := host.CreateWorkspace("finished", target, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, cleanupPane, err := host.CreateWorkspace(cleanupWorkspaceLabel, filepath.Dir(target), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := host.RenamePane(sourcePane, "agent"); err != nil {
		t.Fatal(err)
	}
	rexTestEditBlock(t, "agent", func(b *rexTestBlock) { b.Fg, b.Status = "claude", "working" })
	t.Setenv("HERDR_ENV", "")
	t.Setenv("REX_BLOCK", cleanupPane)
	t.Setenv("WTC_RETIRE_MULTIPLEXER", "rex")
	t.Setenv("WTC_RETIRE_SOURCE_WORKSPACE", source)
	t.Setenv("WTC_RETIRE_SOURCE_PANE", sourcePane)
	quiet := retireQuietScreen
	retireQuietScreen = 0
	t.Cleanup(func() { retireQuietScreen = quiet })
	for _, call := range []string{"process", "program_status"} {
		t.Setenv("WTC_TEST_REX_BROKEN_CALL", call)
		if _, err := host.Panes(source); err == nil {
			t.Fatalf("a failed %s lookup read as a pane without a working agent", call)
		}
		if err := waitRetireSource(host, source, sourcePane); err == nil {
			t.Fatalf("with %s failing, the retire watch finished without knowing whether the agent runs", call)
		}
		if err := runRetireWorker("finished", true, false); err == nil {
			t.Fatalf("with %s failing, the worker retired a collection whose agent it could not see", call)
		}
		if _, err := os.Stat(target); err != nil {
			t.Fatalf("target was removed: %v", err)
		}
		if !slices.ContainsFunc(rexTestRead(t).Sessions, func(s rexTestSession) bool { return s.ID == source }) {
			t.Fatal("the source session was closed")
		}
	}
}

type rexTestScreens map[string]string

func (s rexTestScreens) VisibleText(pane string) (string, error) { return s[pane], nil }

func TestRetireSourceWatchNeedsAQuietScreenForUnreportedAgents(t *testing.T) {
	start := time.Now()
	screens := rexTestScreens{"p": "thinking"}
	watch := &retireSourceWatch{screens: screens}
	unknown := openPaneInfo{ID: "p", Agent: "codex", AgentStatus: "unknown"}
	if watch.finished(unknown, start) || watch.finished(unknown, start.Add(retireQuietScreen/2)) {
		t.Fatal("an agent that reports nothing finished before its screen was quiet long enough")
	}
	screens["p"] = "still thinking"
	if watch.finished(unknown, start.Add(retireQuietScreen)) {
		t.Fatal("a changed screen counted as quiet")
	}
	if !watch.finished(unknown, start.Add(2*retireQuietScreen)) {
		t.Fatal("a quiet screen did not finish the turn")
	}
	blocked := openPaneInfo{ID: "p", Agent: "claude", AgentStatus: "blocked"}
	if watch.finished(blocked, start.Add(time.Hour)) {
		t.Fatal("an agent waiting for an answer counted as finished")
	}
	if (&retireSourceWatch{}).finished(unknown, start.Add(time.Hour)) {
		t.Fatal("herdr's unknown state counted as finished")
	}
	for _, pane := range []openPaneInfo{{ID: "p"}, {ID: "p", Agent: "claude", AgentStatus: "done"}, {ID: "p", Agent: "claude", AgentStatus: "idle"}} {
		if !watch.finished(pane, start) {
			t.Fatalf("%+v did not count as finished", pane)
		}
	}
}

func TestRexVisibleTextLeavesOutScrollback(t *testing.T) {
	_, _, _ = rexTestOpen(t)
	agent := rexTestEditBlock(t, "agent", func(*rexTestBlock) {})
	text, err := (&rexHost{}).VisibleText(agent)
	if err != nil || text != "line one\nline two\nline three" {
		t.Fatalf("visible text = %q, %v", text, err)
	}
}

func TestRexWithoutAServer(t *testing.T) {
	_, _, bin, _, _ := openTestFixture(t)
	rexTestSetup(t, bin)
	t.Setenv("WTC_TEST_REX_DOWN", "1")
	t.Setenv("REX_BLOCK", "block:stale")
	host := newTerminalHost("rex", "test")
	if host.Running() {
		t.Fatal("a missing server counts as running")
	}
	if err := host.EnsureRunning(); err == nil || !strings.Contains(err.Error(), "start Rex first") {
		t.Fatalf("a missing server is not reported: %v", err)
	}
	if _, inPane := host.Here(); inPane {
		t.Fatal("a stale REX_BLOCK counts as a Rex pane, which refuses an in-place retire")
	}
	if strings.Contains(rexTestCalls(t), `"server"`) {
		t.Fatal("taba started a Rex server")
	}
}

func TestRexFocusAndWidth(t *testing.T) {
	_, _, _ = rexTestOpen(t)
	state := rexTestRead(t)
	agent := rexTestEditBlock(t, "agent", func(*rexTestBlock) {})
	host := newTerminalHost("rex", "test")
	t.Setenv("WTC_TEST_REX_NO_REMOTE", "1")
	if err := host.FocusWorkspace(state.Sessions[0].ID); err == nil || !strings.Contains(err.Error(), "Remote Control") {
		t.Fatalf("focus without Remote Control is not explained: %v", err)
	}
	t.Setenv("REX_BLOCK", agent)
	t.Setenv("WTC_TEST_REX_COLUMNS", "56")
	if width := host.SessionWidth(); width != 140 {
		t.Fatalf("a 56-column agent pane, 40%% of its window, gives width %d, want 140", width)
	}
}

func TestMultiplexerSelection(t *testing.T) {
	for _, tc := range []struct {
		setting               string
		rexTerminal, hasHerdr bool
		want                  string
	}{
		{want: "herdr"},
		{hasHerdr: true, want: "herdr"},
		{rexTerminal: true, hasHerdr: true, want: "herdr"},
		{rexTerminal: true, want: "rex"},
		{setting: "rex", hasHerdr: true, want: "rex"},
		{setting: "herdr", rexTerminal: true, want: "herdr"},
		{setting: "tmux", want: "error"},
		{setting: "Rex", rexTerminal: true, want: "error"},
	} {
		got, err := chooseMultiplexer(tc.setting, tc.rexTerminal, tc.hasHerdr)
		if err != nil {
			got = "error"
			if !strings.Contains(err.Error(), "expected herdr or rex") {
				t.Errorf("unclear error: %v", err)
			}
		}
		if got != tc.want {
			t.Errorf("setting %q, Rex terminal %v, herdr installed %v: got %s, want %s", tc.setting, tc.rexTerminal, tc.hasHerdr, got, tc.want)
		}
	}
}

func TestMultiplexerSettingAppliesEverywhere(t *testing.T) {
	c, root, bin, _, _ := openTestFixture(t)
	rexTestSetup(t, bin)
	t.Setenv("PATH", bin)
	if err := os.WriteFile(filepath.Join(bin, "herdr"), []byte("#!/bin/sh\nexit 1\n"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(c.ConfigRoot, 0755); err != nil {
		t.Fatal(err)
	}
	path, err := wtc.MachineEnvFile(c.ConfigRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("WTC_MULTIPLEXER=rex\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if kind, err := openMultiplexer(c); kind != "rex" || err != nil {
		t.Fatalf("the machine setting does not select Rex: %q %v", kind, err)
	}
	if host, err := retireHost(c); err != nil || host.Kind() != "rex" {
		t.Fatalf("retirement ignores the machine setting: %v %v", host, err)
	}
	if host, err := statusNextHost(c, statusNextRunner{}); err != nil || host.Kind() != "rex" {
		t.Fatalf("status next ignores the machine setting: %v %v", host, err)
	}
	if err := os.WriteFile(path, []byte("WTC_MULTIPLEXER=screen\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := browseCallerIsAgent(c); err == nil {
		t.Fatal("an unknown multiplexer setting was ignored")
	}

	outside := t.TempDir()
	prior, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(outside); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(prior) })
	t.Setenv("REX_BLOCK", "block:1")
	if host, err := hostName(); host != "terminal" || err != nil {
		t.Fatalf("taba host in a Rex terminal with herdr installed: %q %v", host, err)
	}
	if err := os.Remove(filepath.Join(bin, "herdr")); err != nil {
		t.Fatal(err)
	}
	if host, err := hostName(); host != "rex" || err != nil {
		t.Fatalf("taba host in a Rex terminal without herdr: %q %v", host, err)
	}
	t.Setenv("WTC_MULTIPLEXER", "herdr")
	if host, _ := hostName(); host != "terminal" {
		t.Fatalf("taba host ignores WTC_MULTIPLEXER=herdr: %q", host)
	}
	_ = root
}

// The suite must not reach a real herdr or Rex server, whatever multiplexer
// the shell that runs it is in.
func TestTestsCannotReachARealMultiplexer(t *testing.T) {
	if report := os.Getenv("TABA_TEST_MULTIPLEXER_REPORT"); report != "" {
		kind, err := openMultiplexer(nil)
		rex, _ := rexCommand()
		line := strings.Join([]string{kind, os.Getenv("REX_BLOCK"), os.Getenv("HERDR_SOCKET_PATH"), rex}, "|")
		if err != nil {
			line = err.Error()
		}
		if err := os.WriteFile(report, []byte(line), 0600); err != nil {
			t.Fatal(err)
		}
		return
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "rex"), []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}
	report := filepath.Join(dir, "report")
	cmd := exec.Command(os.Args[0], "-test.run=^TestTestsCannotReachARealMultiplexer$")
	for _, setting := range os.Environ() {
		if !strings.HasPrefix(setting, "TABA_TEST_ENV=") {
			cmd.Env = append(cmd.Env, setting)
		}
	}
	cmd.Env = append(cmd.Env, "TABA_TEST_MULTIPLEXER_REPORT="+report, "REX_BLOCK=block:real", "REX_BIN_DIR="+dir,
		"HERDR_ENV=1", "HERDR_SESSION=real", "HERDR_SOCKET_PATH="+dir, "WTC_MULTIPLEXER=rex")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("child: %v\n%s", err, out)
	}
	got, err := os.ReadFile(report)
	if err != nil {
		t.Fatal(err)
	}
	if fields := strings.Split(string(got), "|"); len(fields) != 4 || fields[0] != "herdr" || fields[1] != "" || fields[2] != "" || fields[3] == filepath.Join(dir, "rex") {
		t.Fatalf("a test saw the shell's multiplexer (kind|REX_BLOCK|HERDR_SOCKET_PATH|rex): %q", got)
	}
}

func TestAdHocRetiresInPlaceInARexTerminalTabaDidNotOpen(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PATH", "/usr/bin:/bin")
	rexTestSetup(t, t.TempDir())
	t.Setenv("WTC_CONFIG_ROOT", "")
	t.Setenv("WTC_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("WTC_MULTIPLEXER", "rex")
	dir := filepath.Join(root, "task")
	if err := os.Mkdir(dir, 0755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	if err := runWTC(t, "init"); err != nil {
		t.Fatal(err)
	}
	var plain rexCreated
	if err := (&rexHost{}).rexJSON(&plain, "new", "scratch", "-c", dir, "--json"); err != nil {
		t.Fatal(err)
	}
	_, opened, err := newTerminalHost("rex", "proj").CreateWorkspace("task", dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("REX_BLOCK", opened)
	if here, inPane := newTerminalHost("rex", "").Here(); !inPane || here.Session != "proj" {
		t.Fatalf("a pane taba opened is not its pane: %+v %v", here, inPane)
	}
	t.Setenv("REX_BLOCK", plain.block())
	if err := runWTC(t, "retire", "."); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("collection folder remains: %v", err)
	}
}

func TestDoctorChecksTheMultiplexerTheCollectionUses(t *testing.T) {
	c, _, bin, _, _ := openTestFixture(t)
	rexTestSetup(t, bin)
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	t.Setenv("REX_BLOCK", "")
	checks := func() map[string]map[string]any {
		_, rows := doctorToolChecks(c)
		byName := map[string]map[string]any{}
		for _, row := range rows {
			byName[row["name"].(string)] = row
		}
		return byName
	}
	t.Setenv("WTC_MULTIPLEXER", "rex")
	if rows := checks(); rows["rex"]["ok"] != true || rows["rex"]["detail"] != filepath.Join(bin, "rex") || rows["herdr"] != nil || rows["multiplexer"] != nil {
		t.Fatalf("Rex collection: %v", rows)
	}
	away := t.TempDir()
	if err := os.Rename(filepath.Join(bin, "rex"), filepath.Join(away, "rex")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("REX_BIN_DIR", away)
	if rows := checks(); rows["rex"]["ok"] != true || rows["rex"]["detail"] != filepath.Join(away, "rex") {
		t.Fatalf("rex in REX_BIN_DIR, as inside a Rex terminal: %v", rows)
	}
	t.Setenv("REX_BIN_DIR", "")
	if rows := checks(); rows["rex"]["ok"] != false {
		t.Fatalf("a missing rex passed: %v", rows)
	}
	t.Setenv("WTC_MULTIPLEXER", "herdr")
	if rows := checks(); rows["herdr"] == nil || rows["rex"] != nil {
		t.Fatalf("herdr collection: %v", rows)
	}
	t.Setenv("WTC_MULTIPLEXER", "zellij")
	if rows := checks(); rows["multiplexer"]["ok"] != false || !strings.Contains(rows["multiplexer"]["detail"].(string), "zellij") || rows["herdr"] == nil {
		t.Fatalf("an unknown multiplexer setting: %v", rows)
	}
}

func TestRexNewOpensOnlyBesideTheProjectsCollections(t *testing.T) {
	c, _, bin, _, collection := openTestFixture(t)
	rexTestSetup(t, bin)
	t.Setenv("WTC_MULTIPLEXER", "rex")
	t.Setenv("HARNESS_HERDR_SESSION", "test")
	if _, _, err := newTerminalHost("rex", "elsewhere").CreateWorkspace("other", t.TempDir(), nil); err != nil {
		t.Fatal(err)
	}
	if err := openAfterNew(c, collection, false, false); err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(rexTestRead(t).Sessions, func(s rexTestSession) bool { return s.Label == "test/sample" }) {
		t.Fatal("new opened a collection while no collection of its project is in Rex")
	}
	if _, _, err := newTerminalHost("rex", "test").CreateWorkspace("first", t.TempDir(), nil); err != nil {
		t.Fatal(err)
	}
	if err := openAfterNew(c, collection, false, false); err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(rexTestRead(t).Sessions, func(s rexTestSession) bool { return s.Label == "test/sample" }) {
		t.Fatal("new did not open beside the project's collections")
	}
}
