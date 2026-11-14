package wtc

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStatusDiscoveredPRInventory(t *testing.T) {
	for _, forge := range []string{"github.com", "bitbucket.org"} {
		for _, draft := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/draft=%v", forge, draft), func(t *testing.T) {
				t.Setenv("TMPDIR", t.TempDir())
				c := newWorkspaceFixture(t)
				fixtureGit(t, "-C", c.Harness, "switch", "-qc", "topic")
				c.Registry.Repos[0].Remote = "https://" + forge + "/example/agent-harness.git"
				bin := t.TempDir()
				calls := filepath.Join(t.TempDir(), "calls")
				t.Setenv("DISCOVERY_CALLS", calls)
				payload := fmt.Sprintf(`[{"number":7,"headRefName":"topic","state":"OPEN","isDraft":%v,"title":"Synthetic change","baseRefName":"main","statusCheckRollup":[]}]`, draft)
				fixtureFile(t, filepath.Join(bin, "gh"), "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$DISCOVERY_CALLS\"\n[ \"$2\" = list ] || exit 3\nprintf '%s\\n' '"+payload+"'\n", 0755)
				fixtureFile(t, filepath.Join(bin, "bb"), "#!/bin/sh\nexit 3\n", 0755)
				t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
				if forge == "bitbucket.org" {
					t.Setenv("BB_USERNAME", "fixture")
					t.Setenv("BB_API_TOKEN", "synthetic")
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						fmt.Fprintf(w, `{"values":[{"id":7,"state":"OPEN","draft":%v,"title":"Synthetic change","source":{"branch":{"name":"topic"}},"destination":{"branch":{"name":"main"}}}]}`, draft)
					}))
					old := statusBBAPI
					statusBBAPI = server.URL
					t.Cleanup(func() { statusBBAPI = old; server.Close() })
				}
				before := fixtureGit(t, "-C", c.Harness, "show-ref")
				snapshot, err := c.StatusForgePreview()
				if err != nil {
					t.Fatal(err)
				}
				var inline *StatusPRFacts
				for _, r := range snapshot.Repos {
					if r.Dir == "harness" {
						inline = r.PR
					}
				}
				if inline == nil || inline.Number != "7" {
					t.Fatalf("branch discovery missing: %+v", inline)
				}
				if len(snapshot.PRs) != 1 {
					t.Fatalf("branch PR #%s absent from inventory: %+v", inline.Number, snapshot.PRs)
				}
				row := snapshot.PRs[0]
				raw, _ := json.Marshal(row)
				var fields map[string]any
				if err := json.Unmarshal(raw, &fields); err != nil {
					t.Fatal(err)
				}
				if fields["not_enlisted"] != true || fields["enlist_command"] == "" {
					t.Fatalf("missing provenance/action: %s", raw)
				}
				if row.Draft != draft || row.Freshness != inline.Freshness || row.ObservedAt != inline.ObservedAt || row.OnBranch || row.Landing != nil || row.Archived || row.Follow != nil {
					t.Fatalf("discovery changed facts/authority: %+v", row)
				}
				if !strings.Contains(row.DisplayTitle, "not enlisted") || !strings.Contains(row.DisplayTitle, "pr enlist") {
					t.Fatalf("TUI title lacks provenance/action: %s", row.DisplayTitle)
				}
				if !strings.Contains(snapshot.Markdown(), "not enlisted") || !strings.Contains(snapshot.Markdown(), "pr enlist") {
					t.Fatal("markdown lacks provenance/action")
				}
				if _, err := os.Stat(c.PRFile()); !os.IsNotExist(err) {
					t.Fatalf("discovery wrote enlistment: %v", err)
				}
				if got := fixtureGit(t, "-C", c.Harness, "show-ref"); got != before {
					t.Fatal("discovery changed refs")
				}
				if forge == "github.com" {
					raw, _ := os.ReadFile(calls)
					if strings.Count(string(raw), "pr list") != 1 {
						t.Fatalf("extra forge calls: %s", raw)
					}
				}
			})
		}
	}
}

func TestStatusDiscoveredPRIdentityAndEnlistment(t *testing.T) {
	for _, enlisted := range []string{"none", "branch", "no branch", "URL alias", "other owner", "other forge", "hostile URL", "ambiguous"} {
		t.Run(enlisted, func(t *testing.T) {
			t.Setenv("TMPDIR", t.TempDir())
			c := newWorkspaceFixture(t)
			c.Registry.Repos[0].Remote = "https://github.com/example/agent-harness.git"
			c.Registry.Repos[1].Remote = "https://github.com/example/widget.git"
			fixtureGit(t, "--git-dir="+filepath.Join(c.Workspace, ".bare", "widget.git"), "worktree", "add", "-q", "--detach", filepath.Join(c.Collection, "widget"), "origin/main")
			for _, dir := range []string{c.Harness, filepath.Join(c.Collection, "widget")} {
				fixtureGit(t, "-C", dir, "switch", "-qc", "topic")
			}
			if enlisted == "ambiguous" {
				c.Registry.Repos = append(c.Registry.Repos, Repo{Name: "agent-harness", Remote: "https://github.com/another/agent-harness.git"})
			} else if enlisted != "none" {
				record := PRRecord{Repo: "agent-harness", Number: "7", Branch: "topic"}
				switch enlisted {
				case "no branch":
					record.Branch = ""
				case "URL alias":
					record = PRRecord{Repo: "reference", Number: "7", URL: "https://github.com/EXAMPLE/AGENT-HARNESS/pull/7"}
				case "other owner":
					record = PRRecord{Repo: "reference", Number: "7", URL: "https://github.com/another/agent-harness/pull/7"}
				case "other forge":
					record = PRRecord{Repo: "reference", Number: "7", URL: "https://bitbucket.org/example/agent-harness/pull-requests/7"}
				case "hostile URL":
					record.URL = "https://github.com.evil.invalid/example/agent-harness/pull/7"
				}
				if _, err := c.EnlistPR(record); err != nil {
					t.Fatal(err)
				}
			}
			bin := t.TempDir()
			fixtureFile(t, filepath.Join(bin, "gh"), `#!/bin/sh
case "$2" in
list) printf '[{"number":7,"headRefName":"topic","state":"OPEN","title":"Synthetic change"}]\n' ;;
view) printf '{"number":7,"state":"OPEN","title":"Synthetic change"}\n' ;;
*) exit 3 ;;
esac
`, 0755)
			fixtureFile(t, filepath.Join(bin, "bb"), "#!/bin/sh\nexit 3\n", 0755)
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("BB_USERNAME", "fixture")
			t.Setenv("BB_API_TOKEN", "synthetic")
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, `{"values":[{"id":7,"state":"OPEN","title":"Synthetic reference"}]}`)
			}))
			old := statusBBAPI
			statusBBAPI = server.URL
			t.Cleanup(func() { statusBBAPI = old; server.Close() })
			before, _ := os.ReadFile(c.PRFile())
			snapshot, err := c.StatusForgePreview()
			if err != nil {
				t.Fatal(err)
			}
			after, _ := os.ReadFile(c.PRFile())
			if string(after) != string(before) {
				t.Fatalf("status rewrote enlistment: before %s after %s", before, after)
			}
			seen := map[string]bool{}
			discovered := 0
			for _, pr := range snapshot.PRs {
				if pr.URL != nil {
					if seen[*pr.URL] {
						t.Fatalf("duplicate full PR identity: %+v", snapshot.PRs)
					}
					seen[*pr.URL] = true
				}
				if pr.NotEnlisted {
					discovered++
				}
			}
			want := 2
			if enlisted == "branch" || enlisted == "no branch" || enlisted == "URL alias" || enlisted == "hostile URL" || enlisted == "ambiguous" {
				want = 1
			}
			if discovered != want {
				t.Fatalf("discovered %d want %d: %+v", discovered, want, snapshot.PRs)
			}
			if !seen["https://github.com/example/widget/pull/7"] {
				t.Fatal("same PR number in second repository disappeared")
			}
		})
	}
}

func TestStatusDiscoveryDoesNotInventOpenOrPersistentMerge(t *testing.T) {
	for _, state := range []string{"MERGED", "CLOSED", "error", "invalid", "empty"} {
		t.Run(state, func(t *testing.T) {
			t.Setenv("TMPDIR", t.TempDir())
			c := newWorkspaceFixture(t)
			c.Registry.Repos[0].Remote = "https://github.com/example/agent-harness.git"
			fixtureGit(t, "-C", c.Harness, "switch", "-qc", "topic")
			bin := t.TempDir()
			// Discovery supplies the branch identity, while detail is absent or terminal.
			payload := `[{"number":7,"headRefName":"topic","state":"` + state + `"}]`
			if state == "empty" {
				payload = `[]`
			}
			script := "#!/bin/sh\n"
			if state == "error" {
				script += "exit 1\n"
			} else if state == "invalid" {
				script += "printf 'invalid'\n"
			} else {
				script += "printf '%s\\n' '" + payload + "'\n"
			}
			fixtureFile(t, filepath.Join(bin, "gh"), script, 0755)
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			refs := fixtureGit(t, "-C", c.Harness, "show-ref")
			snapshot, err := c.StatusForgePreview()
			if err != nil {
				t.Fatal(err)
			}
			if len(snapshot.PRs) != 0 {
				t.Fatalf("invented open discovery from %s: %+v", state, snapshot.PRs)
			}
			if _, err := os.Stat(c.PRFile()); !os.IsNotExist(err) {
				t.Fatalf("discovery recorded state: %v", err)
			}
			if fixtureGit(t, "-C", c.Harness, "show-ref") != refs {
				t.Fatal("terminal discovery moved refs")
			}
		})
	}
}
