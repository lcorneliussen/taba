package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"taba.sh/taba/internal/wtc"
)

// A pane in an unknown credential context keeps no shared observations: here
// gh's hosts file names a second host, so the keychain credential in use is
// unresolved. A background pass that the forge refuses must not turn the rows
// the pane already showed back into never-observed ones.
func TestStatusPane562UnknownContextPaneKeepsObservedPRRows(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("TMPDIR", t.TempDir())
	for _, name := range []string{"WTC_CONFIG_ROOT", "TABA_CONFIG_ROOT", "WTC_FORGE_MIN_INTERVAL", "TABA_FORGE_MIN_INTERVAL",
		"GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN", "GH_HOST", "GH_REPO", "XDG_CONFIG_HOME"} {
		t.Setenv(name, "")
	}
	// gh's default storage (account in hosts.yml, token in the keychain), with
	// a second host configured.
	ghConfig := filepath.Join(home, "gh")
	pane562Write(t, filepath.Join(ghConfig, "hosts.yml"), "github.com:\n    git_protocol: https\n    users:\n        synthetic-user:\n    user: synthetic-user\nghe.example.com:\n    user: synthetic-enterprise\n", 0600)
	t.Setenv("GH_CONFIG_DIR", ghConfig)

	root := t.TempDir()
	pane562Write(t, filepath.Join(root, wtc.AdHocConfigDir, "repos.yml"), "repos:\n  - name: widget\n    remote: https://github.com/example/widget.git\n    default_ref: origin/main\n", 0600)
	c, err := wtc.OpenCollection(root)
	if err != nil {
		t.Fatal(err)
	}
	numbers := []string{"7", "8", "9"}
	var records, list []string
	for _, n := range numbers {
		records = append(records, "widget "+n+" - https://github.com/example/widget/pull/"+n+" Synthetic change "+n+"\n")
		list = append(list, `{"number":`+n+`,"title":"Synthetic change `+n+`","state":"OPEN","isDraft":false,"headRefName":"change-`+n+`","baseRefName":"main"}`)
	}
	pane562Write(t, c.PRFile(), strings.Join(records, ""), 0600)

	bin := t.TempDir()
	mode := filepath.Join(bin, "mode")
	pane562Write(t, mode, "live", 0600)
	pane562Write(t, filepath.Join(bin, "gh"), `#!/bin/sh
if [ "$(cat '`+mode+`')" = limited ]; then
  echo 'HTTP 429: API rate limit exceeded' >&2
  exit 1
fi
case "$1 $2" in
"pr list") printf '%s' '[`+strings.Join(list, ",")+`]' ;;
"pr view") printf '{"number":%s,"state":"OPEN","title":"Synthetic change %s","isDraft":false,"baseRefName":"main"}' "$3" "$3" ;;
*) printf '{}' ;;
esac
`, 0700)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	model := statusTUIModel{width: 120, height: 40, focused: true, snapshot: wtc.StatusSnapshot{Collection: filepath.Base(root)}}
	pass := func() {
		t.Helper()
		snapshot, _, err := c.StatusLiveSnapshotWithFetchAge(false, true, 0)
		if err != nil {
			t.Fatal(err)
		}
		updated, _ := model.Update(statusLoadedMsg{snapshot: snapshot, at: time.Now()})
		model = updated.(statusTUIModel)
	}

	pass()
	if len(model.snapshot.PRs) != len(numbers) {
		t.Fatalf("first pass rows: %+v", model.snapshot.PRs)
	}
	seen := map[string]string{}
	for _, row := range model.snapshot.PRs {
		if row.State != "OPEN" || row.ObservedAt == "" || pane562Unavailable(row.Freshness) {
			t.Fatalf("first pass did not observe #%s: %+v", row.Number, row)
		}
		seen[row.Number] = row.ObservedAt
	}

	pane562Write(t, mode, "limited", 0600)
	pass()
	plain := ansi.Strip(model.View().Content)
	aged := regexp.MustCompile(`\s[dus]:\d+[smhd]\s`)
	for _, n := range numbers {
		var line string
		for _, l := range strings.Split(plain, "\n") {
			if strings.HasPrefix(strings.TrimSpace(l), "#"+n+" ") {
				line = l
			}
		}
		if !strings.Contains(line, "Synthetic change "+n) || !aged.MatchString(line) {
			t.Fatalf("#%s is not shown with its earlier observation and age:\n%s", n, plain)
		}
	}
	for _, row := range model.snapshot.PRs {
		if row.State != "OPEN" || row.ObservedAt != seen[row.Number] || !pane562Unavailable(row.Freshness) {
			t.Fatalf("pane lost what it saw of #%s: %+v", row.Number, row)
		}
	}
}

func pane562Unavailable(freshness string) bool {
	return freshness == "deferred" || freshness == "unavailable" || freshness == "stale"
}

func pane562Write(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}
