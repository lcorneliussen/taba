package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"taba.sh/taba/internal/wtc"
)

func TestBrowseSocketMatchesShellContract(t *testing.T) {
	if got := browseChecksum("abc"); got != 1219131554 {
		t.Fatalf("POSIX cksum differs: %d", got)
	}
	if got := browseSocket("/work/example-wtc", "topic"); got != "/tmp/wtc-browse-example-wtc-topic.nvim" {
		t.Fatalf("short socket: %s", got)
	}
	name := strings.Repeat("long-collection-", 7)
	got := browseSocket("/work/example-wtc", name)
	if got != "/tmp/wtc-browse-"+("example-wtc-" + name)[:60]+"-1785337918.nvim" {
		t.Fatalf("long socket differs from shell spelling: %s", got)
	}
}

func TestBrowseHereLoadsBundledViewInCollection(t *testing.T) {
	root := t.TempDir()
	collection := filepath.Join(root, "topic")
	if err := os.Mkdir(collection, 0755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0755); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(root, "nvim.log")
	program := "#!/bin/sh\nif [ \"$1\" = --server ]; then exit 1; fi\nprintf 'cwd=%s\\n' \"$PWD\" > \"$BROWSE_TEST_LOG\"\nprintf 'arg=%s\\n' \"$@\" >> \"$BROWSE_TEST_LOG\"\n"
	if err := os.WriteFile(filepath.Join(bin, "nvim"), []byte(program), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("BROWSE_TEST_LOG", log)
	c := &wtc.Context{Collection: collection, Workspace: root}
	if err := browseHere(c); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, want := range []string{"cwd=" + collection, "arg=--listen", "arg=" + browseSocket(root, "topic"), "vim.g.wtc_browse_root", "dofile("} {
		if !strings.Contains(text, want) {
			t.Fatalf("Neovim launch missing %q: %s", want, text)
		}
	}
}

func TestBrowseViewAllowsHarnessOverlay(t *testing.T) {
	root := t.TempDir()
	harness := filepath.Join(root, "harness")
	path := filepath.Join(harness, "overlays", "browse", "wtc-browse.lua")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("vim.g.custom_browse = true\n"), 0644); err != nil {
		t.Fatal(err)
	}
	body, err := browseView(&wtc.Context{Harness: harness})
	if err != nil || string(body) != "vim.g.custom_browse = true\n" {
		t.Fatalf("harness browse overlay ignored: %q, %v", body, err)
	}
}

func TestBrowseFromAgentPaneDoesNotTakeItOver(t *testing.T) {
	root := t.TempDir()
	collection := filepath.Join(root, "target")
	if err := os.MkdirAll(filepath.Join(collection, "harness"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(collection, "harness", ".harness-repos.yml"), []byte("repos:\n  - name: example\n    remote: https://github.com/example/example.git\n    default_ref: origin/main\n"), 0644); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0755); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(root, "calls.log")
	herdr := `#!/bin/sh
printf 'herdr %s\n' "$*" >> "$BROWSE_TEST_LOG"
case "$*" in
  *'agent list') echo '{"result":{"agents":[{"pane_id":"caller-agent"}]}}' ;;
  *) exit 2 ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "herdr"), []byte(herdr), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "nvim"), []byte("#!/bin/sh\nprintf 'nvim %s\\n' \"$*\" >> \"$BROWSE_TEST_LOG\"\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("BROWSE_TEST_LOG", log)
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_SESSION", "caller-session")
	t.Setenv("HERDR_PANE_ID", "caller-agent")
	if agent, err := browseCallerIsAgent(); err != nil || !agent {
		t.Fatalf("registered herdr caller was not recognized as an agent: %v", err)
	}
	t.Chdir(collection)
	cli := &cobra.Command{Use: "wtc"}
	asJSON := false
	addBrowseCommand(cli, &asJSON)
	cli.SetArgs([]string{"browse"})
	cli.SetOut(io.Discard)
	cli.SetErr(io.Discard)
	err := cli.Execute()
	if err == nil || !strings.Contains(err.Error(), "wtc open target --session caller-session --add browse") {
		t.Fatalf("agent pane was not pointed at wtc open --add browse: %v", err)
	}
	body, _ := os.ReadFile(log)
	if strings.Contains(string(body), "nvim ") || strings.Contains(string(body), "pane ") {
		t.Fatalf("browse took over or routed from the agent pane: %s", body)
	}
}
