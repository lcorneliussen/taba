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
			// Terminal list items identify the branch; failed or empty lookups identify none.
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

func TestStatusDiscoveredPRDedupDoesNotCollapseEmptyAliasIdentities(t *testing.T) {
	for _, mode := range []string{"unresolved enlistment", "distinct dot repositories", "enlisted dot repository", "case normalized dot alias"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("TMPDIR", t.TempDir())
			c := newWorkspaceFixture(t)
			fixtureGit(t, "--git-dir="+filepath.Join(c.Workspace, ".bare", "widget.git"), "worktree", "add", "-q", "--detach", filepath.Join(c.Collection, "widget"), "origin/main")
			for i, dir := range []string{c.Harness, filepath.Join(c.Collection, "widget")} {
				remote := "https://github.com/example/.github.git"
				if i == 1 {
					remote = "https://github.com/another/.github.git"
				}
				c.Registry.Repos[i].Remote = remote
				fixtureGit(t, "-C", dir, "remote", "set-url", "origin", remote)
				fixtureGit(t, "-C", dir, "switch", "-qc", "topic")
			}
			var record PRRecord
			switch mode {
			case "unresolved enlistment":
				record = PRRecord{Repo: "missing", Number: "7"}
			case "enlisted dot repository":
				record = PRRecord{Repo: "agent-harness", Number: "7"}
			case "case normalized dot alias":
				record = PRRecord{Repo: "reference", Number: "7", URL: "https://github.com/EXAMPLE/.GITHUB/pull/7"}
			}
			if record.Repo != "" {
				if _, err := c.EnlistPR(record); err != nil {
					t.Fatal(err)
				}
			}
			repos := c.prRepositories()
			for _, name := range []string{"agent-harness", "widget"} {
				slug, forge := repos.forge(name)
				if forge != "github.com" || !strings.HasSuffix(slug, "/.github") {
					t.Fatalf("fixture did not retain forge lookup identity: %s %s", forge, slug)
				}
				if prForgeIdentity("https://"+forge+"/"+slug) != "" {
					t.Fatal("fixture must exercise an identity rejected only by the local-alias helper")
				}
			}
			bin := t.TempDir()
			fixtureFile(t, filepath.Join(bin, "gh"), `#!/bin/sh
[ "$2" = list ] || exit 3
printf '[{"number":7,"state":"OPEN","headRefName":"topic","title":"Synthetic dot repository"}]\n'
`, 0755)
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			before, _ := os.ReadFile(c.PRFile())
			refs := []string{fixtureGit(t, "-C", c.Harness, "show-ref"), fixtureGit(t, "-C", filepath.Join(c.Collection, "widget"), "show-ref")}
			snapshot, err := c.StatusForgePreview()
			if err != nil {
				t.Fatal(err)
			}
			inline := 0
			for _, row := range snapshot.Repos {
				if row.PR != nil && row.PR.Number == "7" {
					inline++
				}
			}
			if inline != 2 {
				t.Fatalf("fixture lost inline branch facts: %+v", snapshot.Repos)
			}
			want := 2
			if mode == "unresolved enlistment" {
				want = 3
			}
			if len(snapshot.PRs) != want {
				t.Fatalf("distinct forge identities collapsed: got %d rows want %d: %+v", len(snapshot.PRs), want, snapshot.PRs)
			}
			urls := map[string]bool{}
			for _, row := range snapshot.PRs {
				if row.URL == nil {
					if row.Repo != "missing" || row.State != "UNKNOWN" || row.NotEnlisted {
						t.Fatalf("unresolved record gained facts: %+v", row)
					}
					continue
				}
				url := strings.ToLower(*row.URL)
				if urls[url] {
					t.Fatalf("duplicate verified forge identity: %s", url)
				}
				urls[url] = true
			}
			for _, url := range []string{"https://github.com/example/.github/pull/7", "https://github.com/another/.github/pull/7"} {
				if !urls[url] {
					t.Fatalf("missing discovered identity %s", url)
				}
			}
			after, _ := os.ReadFile(c.PRFile())
			if string(before) != string(after) {
				t.Fatal("dedup changed enlistment")
			}
			if refs[0] != fixtureGit(t, "-C", c.Harness, "show-ref") || refs[1] != fixtureGit(t, "-C", filepath.Join(c.Collection, "widget"), "show-ref") {
				t.Fatal("dedup changed refs")
			}
		})
	}
}

func TestStatusPRInventoryKeyUsesForgePathIdentity(t *testing.T) {
	for _, tc := range []struct{ forge, slug, number, want string }{
		{"github.com", "Example/.GITHUB", "7", "github.com/example/.github#7"},
		{"bitbucket.org", "Example/.config", "7", "bitbucket.org/Example/.config#7"},
		{"github.com", "example/widget", "8", "github.com/example/widget#8"},
		{"", "", "7", ""},
		{"github.com", "", "7", ""},
		{"github.com.evil.invalid", "example/widget", "7", ""},
		{"github.com", "example/../widget", "7", ""},
		{"github.com", "example/..", "7", ""},
		{"github.com", "example/.", "7", ""},
		{"github.com", "example/%2Egithub", "7", ""},
		{"github.com", "example/widget#7", "7", ""},
		{"github.com", "example/widget?x", "7", ""},
		{"github.com", "user@example/widget", "7", ""},
		{"github.com", "example/widget", "", ""},
		{"github.com", "example/widget", "0", ""},
		{"github.com", "example/widget", "07", ""},
		{"github.com", "example/widget", "-7", ""},
	} {
		t.Run(tc.forge+"/"+tc.slug+"/"+tc.number, func(t *testing.T) {
			key, valid := statusPRInventoryKey(tc.forge, tc.slug, tc.number)
			if key != tc.want || valid != (tc.want != "") {
				t.Fatalf("inventory key=%q valid=%v want %q", key, valid, tc.want)
			}
		})
	}
}
