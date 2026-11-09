package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func openTestState(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(os.Getenv("OPEN_TEST_STATE"))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// openTestEditPanes changes the fixture's panes in place.
func openTestEditPanes(t *testing.T, edit func(label string, pane map[string]any), running map[string][]string) {
	t.Helper()
	var state map[string]any
	if err := json.Unmarshal(openTestState(t), &state); err != nil {
		t.Fatal(err)
	}
	for _, raw := range state["panes"].([]any) {
		pane := raw.(map[string]any)
		label, _ := pane["label"].(string)
		edit(label, pane)
		if argv, ok := running[label]; ok {
			if state["running"] == nil {
				state["running"] = map[string]any{}
			}
			state["running"].(map[string]any)[pane["pane_id"].(string)] = argv
		}
	}
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(os.Getenv("OPEN_TEST_STATE"), data, 0644); err != nil {
		t.Fatal(err)
	}
}

func openTestCount(t *testing.T, state []byte, label string) int {
	t.Helper()
	var parsed struct {
		Panes []struct {
			Label string `json:"label"`
		} `json:"panes"`
		Tabs []struct {
			Label string `json:"label"`
		} `json:"tabs"`
	}
	if err := json.Unmarshal(state, &parsed); err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, pane := range parsed.Panes {
		if pane.Label == label {
			count++
		}
	}
	for _, tab := range parsed.Tabs {
		if "tab:"+tab.Label == label {
			count++
		}
	}
	return count
}

func TestOpenStartsNarrowWithAgentAndStatusAndAddsToolPanes(t *testing.T) {
	c, _, _, log, _ := openTestFixture(t)
	opt := openOptions{Session: "test", NoAgent: true, NoStatus: true}
	created := openCollection(c, "sample", opt, "narrow", true, true)
	state := openTestState(t)
	if created.Error != "" || created.Layout != "narrow" || openTestCount(t, state, "browse")+openTestCount(t, state, "shell")+openTestCount(t, state, "tab:tools") != 0 {
		t.Fatalf("default open did more than agent and status: %+v\n%s", created, state)
	}
	if agent, status := openTestPane(t, state, "agent"), openTestPane(t, state, "status"); agent.TabLabel != "main" || !status.at("main", 0, 1) {
		t.Fatalf("status is not stacked under the agent: %s", state)
	}
	listed := openCollection(c, "sample", openOptions{Session: "test", List: true}, "wide", true, true)
	actions := strings.Join(listed.Actions, ", ")
	if listed.Error != "" || listed.Layout != "narrow" || !strings.Contains(actions, "narrow (2 panes)") || !strings.Contains(actions, "browse: no pane") || !strings.Contains(actions, "shell: no pane") {
		t.Fatalf("missing tool panes counted as damage: %+v", listed)
	}
	before, _ := os.ReadFile(log)
	if again := openCollection(c, "sample", opt, "narrow", true, true); again.Error != "" || strings.Contains(strings.Join(again.Actions, " "), "layout") {
		t.Fatalf("rerun changed a minimal workspace: %+v", again)
	}
	if after, _ := os.ReadFile(log); strings.Count(string(after), "pane split ") != strings.Count(string(before), "pane split ") || strings.Contains(string(after), "tab create") {
		t.Fatalf("rerun added panes:\n%s", after)
	}
	withShell := opt
	withShell.Tools = []string{"shell"}
	if planned := openCollection(c, "sample", openOptions{Session: "test", DryRun: true, Tools: []string{"shell"}}, "narrow", true, true); !strings.Contains(strings.Join(planned.Actions, " "), "would add shell pane") {
		t.Fatalf("dry run did not plan the shell pane: %+v", planned)
	}
	if added := openCollection(c, "sample", withShell, "narrow", true, true); added.Error != "" || !strings.Contains(strings.Join(added.Actions, " "), "1 tool pane(s) added") {
		t.Fatalf("shell pane was not added: %+v", added)
	}
	if shell := openTestPane(t, openTestState(t), "shell"); shell.TabLabel != "tools" {
		t.Fatalf("shell is not on the tools tab: %s", openTestState(t))
	}
	withBrowse := opt
	withBrowse.Tools = []string{"browse"}
	if added := openCollection(c, "sample", withBrowse, "narrow", true, true); added.Error != "" {
		t.Fatalf("browse pane was not added: %+v", added)
	}
	state = openTestState(t)
	browse, shell := openTestPane(t, state, "browse"), openTestPane(t, state, "shell")
	if browse.TabLabel != "tools" || shell.TabLabel != "tools" || browse.X != shell.X || shell.Y != browse.Y+1 || strings.Contains(string(state), "restack-tmp") {
		t.Fatalf("browse was not added above the shell: %s", state)
	}
	if listed := openCollection(c, "sample", openOptions{Session: "test", List: true}, "wide", true, true); listed.Layout != "narrow" || !strings.Contains(strings.Join(listed.Actions, " "), "narrow (4 panes)") {
		t.Fatalf("on-demand panes broke the narrow layout: %+v", listed)
	}
}

func TestOpenWideMinimalAddsToolPanesAndSwitchesLayouts(t *testing.T) {
	c, _, _, _, _ := openTestFixture(t)
	opt := openOptions{Session: "test", NoAgent: true, NoStatus: true}
	if created := openCollection(c, "sample", opt, "wide", true, true); created.Error != "" || created.Layout != "wide" {
		t.Fatalf("wide minimal open failed: %+v", created)
	}
	state := openTestState(t)
	agent, status := openTestPane(t, state, "agent"), openTestPane(t, state, "status")
	if status.TabLabel != agent.TabLabel || status.X <= agent.X || status.Y != agent.Y || openTestCount(t, state, "browse")+openTestCount(t, state, "shell") != 0 {
		t.Fatalf("wide minimal is not agent beside status: %s", state)
	}
	if listed := openCollection(c, "sample", openOptions{Session: "test", List: true}, "narrow", true, true); listed.Layout != "wide" || !strings.Contains(strings.Join(listed.Actions, " "), "wide (2 panes)") {
		t.Fatalf("wide minimal is not reported as wide: %+v", listed)
	}
	full := opt
	full.Tools = []string{"browse", "shell"}
	if added := openCollection(c, "sample", full, "narrow", true, true); added.Error != "" || added.Layout != "wide" || !strings.Contains(strings.Join(added.Actions, " "), "2 tool pane(s) added") {
		t.Fatalf("tool panes were not added in the current layout: %+v", added)
	}
	state = openTestState(t)
	agent, status = openTestPane(t, state, "agent"), openTestPane(t, state, "status")
	browse, shell := openTestPane(t, state, "browse"), openTestPane(t, state, "shell")
	if browse.TabLabel != agent.TabLabel || browse.X <= agent.X || status.X != browse.X || status.Y != browse.Y+1 || !shell.at(agent.TabLabel, agent.X, agent.Y+1) || strings.Contains(string(state), "restack-tmp") {
		t.Fatalf("wide tool panes are misplaced: %s", state)
	}
	if listed := openCollection(c, "sample", openOptions{Session: "test", List: true}, "narrow", true, true); listed.Layout != "wide" {
		t.Fatalf("wide with tool panes is not reported as wide: %+v", listed)
	}
	if err := os.Remove(os.Getenv("OPEN_TEST_STATE")); err != nil {
		t.Fatal(err)
	}
	openCollection(c, "sample", opt, "wide", true, true)
	narrowOpt := opt
	narrowOpt.LayoutSet = true
	switched := openCollection(c, "sample", narrowOpt, "narrow", true, true)
	state = openTestState(t)
	if switched.Error != "" || !openTestPane(t, state, "status").at(openTestPane(t, state, "agent").TabLabel, 0, 1) || openTestCount(t, state, "tab:tools")+openTestCount(t, state, "browse")+openTestCount(t, state, "shell") != 0 {
		t.Fatalf("narrow switch did not stack status without adding tool panes: %+v\n%s", switched, state)
	}
	wideOpt := opt
	wideOpt.LayoutSet = true
	widened := openCollection(c, "sample", wideOpt, "wide", true, true)
	state = openTestState(t)
	if widened.Error != "" || openTestPane(t, state, "status").X <= 0 || openTestCount(t, state, "browse")+openTestCount(t, state, "shell") != 0 {
		t.Fatalf("wide switch did not put status beside the agent: %+v\n%s", widened, state)
	}
}

func TestOpenTrimClosesOnlyIdleToolPanes(t *testing.T) {
	c, _, bin, log, _ := openTestFixture(t)
	nvim := "#!/bin/sh\ncase \"$*\" in *--remote-expr*) [ -n \"$OPEN_TEST_MODIFIED\" ] || exit 1; echo \"${OPEN_TEST_NVIM_PID:-7} ${OPEN_TEST_NVIM_PARENT:-1} $OPEN_TEST_MODIFIED\" ;; esac\n"
	if err := os.WriteFile(filepath.Join(bin, "nvim"), []byte(nvim), 0755); err != nil {
		t.Fatal(err)
	}
	opened := openCollection(c, "sample", openOptions{Session: "test", NoAgent: true, NoBrowse: true, NoStatus: true, Tools: []string{"browse", "shell"}}, "narrow", true, true)
	if opened.Error != "" {
		t.Fatalf("full open failed: %+v", opened)
	}
	trim := openOptions{Session: "test", Trim: true}
	keep := func(want ...string) {
		t.Helper()
		item := openTrimCollection(c, "sample", trim)
		actions := strings.Join(item.Actions, ", ")
		for _, w := range want {
			if !strings.Contains(actions, w) {
				t.Fatalf("trim actions %q lack %q", actions, w)
			}
		}
		if calls, _ := os.ReadFile(log); strings.Contains(string(calls), "pane close ") {
			t.Fatalf("trim closed a pane it should keep: %s", actions)
		}
	}
	openTestEditPanes(t, func(label string, pane map[string]any) { pane["focused"] = label == "shell" }, map[string][]string{"browse": {"nvim"}, "agent": {"-zsh"}, "status": {"-zsh"}})
	t.Setenv("OPEN_TEST_MODIFIED", "2")
	keep("browse kept (unsaved buffers)", "shell kept (focused)")
	t.Setenv("OPEN_TEST_MODIFIED", "")
	openTestEditPanes(t, func(label string, pane map[string]any) { pane["focused"] = false }, map[string][]string{"shell": {"make", "test"}})
	keep("browse kept (Neovim did not answer)", "shell kept (running make)")
	t.Setenv("OPEN_TEST_MODIFIED", "0")
	t.Setenv("OPEN_TEST_NVIM_PARENT", "4242")
	keep("browse kept (Neovim on the socket runs elsewhere)")
	t.Setenv("OPEN_TEST_NVIM_PARENT", "")
	openTestEditPanes(t, func(string, map[string]any) {}, map[string][]string{"shell": {"-zsh"}, "browse": {"wtc", "browse", "--here"}})
	dry := trim
	dry.DryRun = true
	if planned := openTrimCollection(c, "sample", dry); !strings.Contains(strings.Join(planned.Actions, " "), "would close browse") || !strings.Contains(strings.Join(planned.Actions, " "), "would close shell") {
		t.Fatalf("dry run did not plan closing idle panes: %+v", planned)
	}
	if calls, _ := os.ReadFile(log); strings.Contains(string(calls), "pane close ") {
		t.Fatalf("dry run closed panes:\n%s", calls)
	}
	trimmed := openTrimCollection(c, "sample", trim)
	state := openTestState(t)
	if trimmed.Error != "" || !strings.Contains(strings.Join(trimmed.Actions, " "), "browse closed") || !strings.Contains(strings.Join(trimmed.Actions, " "), "shell closed") {
		t.Fatalf("idle tool panes were not closed: %+v", trimmed)
	}
	if openTestCount(t, state, "agent") != 1 || openTestCount(t, state, "status") != 1 || openTestCount(t, state, "browse")+openTestCount(t, state, "shell") != 0 {
		t.Fatalf("trim touched agent or status: %s", state)
	}
	if listed := openCollection(c, "sample", openOptions{Session: "test", List: true}, "wide", true, true); listed.Layout != "narrow" || listed.Error != "" {
		t.Fatalf("trimmed workspace is not a narrow workspace: %+v", listed)
	}
	if again := openTrimCollection(c, "sample", trim); strings.Join(again.Actions, " ") != "nothing to trim" {
		t.Fatalf("second trim found work: %+v", again)
	}
}

func TestOpenStatusAboveAgentIsPartial(t *testing.T) {
	c, _, _, _, _ := openTestFixture(t)
	if created := openCollection(c, "sample", openOptions{Session: "test", NoAgent: true, NoStatus: true}, "wide", true, true); created.Error != "" {
		t.Fatalf("wide minimal open failed: %+v", created)
	}
	openTestEditPanes(t, func(label string, pane map[string]any) {
		switch label {
		case "agent":
			pane["x"], pane["y"] = 0, 1
		case "status":
			pane["x"], pane["y"] = 0, 0
		}
	}, nil)
	if listed := openCollection(c, "sample", openOptions{Session: "test", List: true}, "wide", true, true); listed.Layout != "partial" {
		t.Fatalf("status above agent is not reported as partial: %+v", listed)
	}
}

func TestOpenPartialLayoutNamesMissingPanes(t *testing.T) {
	panes := []openPaneInfo{{Label: "agent", ID: "p1"}}
	if got := openLayoutDescription("partial", panes); got != "partial (1 panes; missing status)" {
		t.Fatalf("partial layout does not explain itself: %s", got)
	}
}

func TestOpenPtyReportNamesHoldersWhenLow(t *testing.T) {
	bin := t.TempDir()
	herdr := `#!/bin/sh
case "$*" in
  'session list') printf 'Name status directory socket\nbusy running /x /x.sock\nquiet running /y /y.sock\nold stopped /z /z.sock\n' ;;
  '--session busy pane list') echo '{"result":{"panes":[{"label":"agent"},{"label":"browse"},{"label":"browse"},{"label":"shell"},{"label":""}]}}' ;;
  '--session quiet pane list') echo '{"result":{"panes":[{"label":"agent"}]}}' ;;
  *) echo "unexpected: $*" >&2; exit 2 ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "herdr"), []byte(herdr), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	counts := openPtyCounts
	t.Cleanup(func() { openPtyCounts = counts })
	openPtyCounts = func() (int, int) { return 100, 511 }
	if report, low := openPtyReport(); low || report != "100 of 511 pseudo-terminals in use (kern.tty.ptmx_max)" {
		t.Fatalf("roomy budget reported as low: %q", report)
	}
	openPtyCounts = func() (int, int) { return 470, 511 }
	report, low := openPtyReport()
	if !low || !strings.Contains(report, "most panes: busy 5 (agent 1, browse 2, shell 1, other 1), quiet 1 (agent 1)") || !strings.Contains(report, "wtc open --trim --all") {
		t.Fatalf("low budget does not name holders: %q", report)
	}
	openPtyCounts = func() (int, int) { return 0, 0 }
	if report, low := openPtyReport(); low || report != "" {
		t.Fatalf("unknown budget reported: %q", report)
	}
}
