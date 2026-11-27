package wtc

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestStatusBitbucketPredictsExactPRCommits(t *testing.T) {
	for _, tc := range []struct {
		name, want                                           string
		conflict, missing, discovered, short, failedPipeline bool
	}{
		{name: "conflicting", want: "CONFLICTING", conflict: true},
		{name: "clean behind", want: "BEHIND"},
		{name: "missing source object", want: "UNKNOWN", missing: true},
		{name: "discovered conflict", want: "CONFLICTING", conflict: true, discovered: true},
		{name: "abbreviated conflict", want: "CONFLICTING", conflict: true, short: true},
		{name: "abbreviated clean", want: "BEHIND", short: true},
		{name: "PR failure beside stale local head", want: "BEHIND", failedPipeline: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("TMPDIR", t.TempDir())
			t.Setenv("HOME", t.TempDir())
			t.Setenv("BB_USERNAME", "fixture")
			t.Setenv("BB_API_TOKEN", "fixture-token")
			c := newWorkspaceFixture(t)
			dir := c.Harness
			root := fixtureGit(t, "-C", dir, "rev-parse", "HEAD")
			commit := func(name, body string) string {
				t.Helper()
				fixtureFile(t, filepath.Join(dir, name), body, 0644)
				fixtureGit(t, "-C", dir, "add", name)
				fixtureGit(t, "-C", dir, "-c", "user.name=T", "-c", "user.email=t@example.invalid", "commit", "-qm", "fixture change")
				return fixtureGit(t, "-C", dir, "rev-parse", "HEAD")
			}
			fixtureGit(t, "-C", dir, "switch", "-qc", "topic")
			head := commit("README.md", "source\n")
			fixtureGit(t, "-C", dir, "switch", "--detach", root)
			name := "separate.txt"
			if tc.conflict {
				name = "README.md"
			}
			base := commit(name, "destination\n")
			fixtureGit(t, "-C", dir, "update-ref", "refs/remotes/origin/main", base)
			fixtureGit(t, "-C", dir, "switch", "topic")
			// Reset the checkout to the destination commit. The PR prediction
			// must still use its reported source commit, not local HEAD.
			fixtureGit(t, "-C", dir, "reset", "--hard", base)
			fixtureFile(t, filepath.Join(dir, "untracked.txt"), "keep me\n", 0644)
			fixtureGit(t, "-C", dir, "remote", "set-url", "origin", "https://bitbucket.org/example/harness.git")
			c.Registry.Repos[0].Remote = "https://bitbucket.org/example/harness.git"
			if !tc.discovered {
				if _, err := c.EnlistPR(PRRecord{Repo: "agent-harness", Number: "7", Branch: "topic"}); err != nil {
					t.Fatal(err)
				}
			}
			if tc.missing {
				head = strings.Repeat("1", 40)
			}
			reportedHead, reportedBase := head, base
			if tc.short {
				reportedHead, reportedBase = head[:12], base[:12]
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.failedPipeline && strings.HasSuffix(r.URL.Path, "/pipelines/") {
					if r.URL.Query().Get("target.commit.hash") != head {
						t.Error("pipeline request did not use the PR head")
					}
					fmt.Fprintf(w, `{"values":[
 {"state":{"name":"COMPLETED","result":{"name":"SUCCESSFUL"}},"target":{"commit":{"hash":%q},"selector":{"type":"branches"}}},
 {"state":{"name":"COMPLETED","result":{"name":"FAILED"}},"target":{"commit":{"hash":%q},"selector":{"type":"pullrequests","pattern":"**"}}}]}`, base, head)
					return
				}
				if strings.HasSuffix(r.URL.Path, "/statuses") || strings.HasSuffix(r.URL.Path, "/pipelines/") {
					fmt.Fprint(w, `{"values":[]}`)
					return
				}
				if strings.Contains(r.URL.Path, "/commit/") {
					fmt.Fprintf(w, `{"hash":%q}`, head)
					return
				}
				fields := r.URL.Query().Get("fields")
				if !strings.Contains(fields, "source.commit.hash") || !strings.Contains(fields, "destination.commit.hash") {
					t.Errorf("list omitted exact commit identities: %s", fields)
				}
				fmt.Fprintf(w, `{"values":[{"id":7,"state":"OPEN","title":"Synthetic change","source":{"branch":{"name":"topic"},"commit":{"hash":%q}},"destination":{"branch":{"name":"main"},"commit":{"hash":%q}}}]}`, reportedHead, reportedBase)
			}))
			defer server.Close()
			old := statusBBAPI
			statusBBAPI = server.URL
			t.Cleanup(func() { statusBBAPI = old })
			before := fixtureGit(t, "-C", dir, "status", "--porcelain=v1")
			beforeRefs := fixtureGit(t, "-C", dir, "show-ref")
			index := fixtureGit(t, "-C", dir, "rev-parse", "--git-path", "index")
			if !filepath.IsAbs(index) {
				index = filepath.Join(dir, index)
			}
			beforeIndex, err := os.ReadFile(index)
			if err != nil {
				t.Fatal(err)
			}
			snapshot, err := c.statusForgePreview(false, false)
			if err != nil {
				t.Fatal(err)
			}
			var facts *StatusPRFacts
			for _, row := range snapshot.Repos {
				if row.Dir == "harness" {
					facts = row.PR
					if row.Next == nil || row.Next.Step != "commit" || row.Next.By != "agent" {
						t.Errorf("dirty checkout must offer an author commit handoff: %+v", row.Next)
					}
				}
			}
			if facts == nil || facts.Merge != tc.want {
				t.Fatalf("PR facts: %+v, want %s", facts, tc.want)
			}
			if tc.failedPipeline && (facts.Checks != "FAILURE" || facts.Head != head) {
				t.Fatalf("pipeline failure or PR head lost beside the stale dirty checkout: %+v", facts)
			}
			if !tc.discovered && (len(snapshot.PRs) != 1 || snapshot.PRs[0].Merge == nil || *snapshot.PRs[0].Merge != tc.want) {
				t.Fatalf("enlisted PR observation: %+v", snapshot.PRs)
			}
			if tc.missing {
				if facts.MergeSource != "" || facts.MergeHead != "" || len(facts.MergeConflicts) != 0 {
					t.Fatalf("unknown prediction claims evidence: %+v", facts)
				}
			} else if facts.MergeSource != "local" || facts.MergeHead != head || facts.MergeBase != base {
				t.Fatalf("missing prediction provenance: %+v", facts)
			}
			if tc.conflict {
				if !reflect.DeepEqual(facts.MergeConflicts, []string{"README.md"}) || !strings.Contains(snapshot.Markdown(), "✗ conflict") {
					t.Fatalf("conflict hidden: %+v\n%s", facts, snapshot.Markdown())
				}
				data, _ := json.Marshal(snapshot)
				if !strings.Contains(string(data), `"merge_conflicts":["README.md"]`) {
					t.Fatalf("JSON omitted conflicts: %s", data)
				}
			}
			if after := fixtureGit(t, "-C", dir, "status", "--porcelain=v1"); after != before {
				t.Fatalf("worktree changed: before %s, after %s", before, after)
			}
			if after := fixtureGit(t, "-C", dir, "show-ref"); after != beforeRefs {
				t.Fatal("prediction changed refs")
			}
			afterIndex, err := os.ReadFile(index)
			if err != nil || !reflect.DeepEqual(beforeIndex, afterIndex) {
				t.Fatalf("prediction changed index: %v", err)
			}
			if tc.conflict {
				if err := os.Remove(filepath.Join(dir, "untracked.txt")); err != nil {
					t.Fatal(err)
				}
				clean, err := c.statusForgePreview(false, false)
				if err != nil {
					t.Fatal(err)
				}
				for _, row := range clean.Repos {
					if row.Dir == "harness" && (row.Next == nil || row.Next.Step != "merge") {
						t.Errorf("conflict absent from clean-checkout next action: %+v", row.Next)
					}
				}
			}
		})
	}
}

func TestStatusBitbucketPredictionUnknownAndSettledCases(t *testing.T) {
	// A syntactically valid but unavailable pair must never fall back to HEAD.
	dir := t.TempDir()
	fixtureGit(t, "-C", dir, "init", "-q")
	fixtureFile(t, filepath.Join(dir, "a"), "base\n", 0644)
	fixtureGit(t, "-C", dir, "add", ".")
	fixtureGit(t, "-C", dir, "-c", "user.name=T", "-c", "user.email=t@example.invalid", "commit", "-qm", "base")
	sha := fixtureGit(t, "-C", dir, "rev-parse", "HEAD")
	blob := fixtureGit(t, "-C", dir, "rev-parse", "HEAD:a")
	fixtureGit(t, "-C", dir, "branch", "abcdef123456", "HEAD")
	rows := []StatusRepo{{Forge: "bitbucket.org", Slug: "example/widget", Worktree: dir}}
	for _, tc := range []struct{ name, head, base, state, forge, slug, want string }{
		{"current", sha, sha, "OPEN", "bitbucket.org", "example/widget", "CLEAN"},
		{"draft", sha, sha, "DRAFT", "bitbucket.org", "example/widget", "CLEAN"},
		{"short commits", sha[:11], sha[:12], "OPEN", "bitbucket.org", "example/widget", "CLEAN"},
		{"missing head", "", sha, "OPEN", "bitbucket.org", "example/widget", "UNKNOWN"},
		{"missing base", sha, "", "OPEN", "bitbucket.org", "example/widget", "UNKNOWN"},
		{"unavailable base", sha, strings.Repeat("1", 40), "OPEN", "bitbucket.org", "example/widget", "UNKNOWN"},
		{"ref name", sha, "HEAD", "OPEN", "bitbucket.org", "example/widget", "UNKNOWN"},
		{"hex ref name", sha, "abcdef123456", "OPEN", "bitbucket.org", "example/widget", "UNKNOWN"},
		{"blob prefix", sha, blob[:12], "OPEN", "bitbucket.org", "example/widget", "UNKNOWN"},
		{"closed", sha, sha, "DECLINED", "bitbucket.org", "example/widget", "UNKNOWN"},
		{"different repository", sha, sha, "OPEN", "bitbucket.org", "example/other", "UNKNOWN"},
		{"other forge", sha, sha, "OPEN", "github.com", "example/widget", "UNKNOWN"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := statusPredictBBMerge(statusPRDetail{State: tc.state, Merge: "UNKNOWN", HeadCommit: tc.head, BaseCommit: tc.base}, tc.forge, tc.slug, rows)
			if got.Merge != tc.want {
				t.Fatalf("got %+v, want %s", got, tc.want)
			}
		})
	}
}

func TestStatusBBFingerprintIncludesCommitPair(t *testing.T) {
	var item statusBBListItem
	if err := json.Unmarshal([]byte(`{"id":7,"source":{"commit":{"hash":"source"}},"destination":{"commit":{"hash":"base"}}}`), &item); err != nil {
		t.Fatal(err)
	}
	before := item.fingerprint()
	item.Source.Commit.Hash = "new-source"
	if item.fingerprint() == before {
		t.Fatal("source change reused cached detail")
	}
	before = item.fingerprint()
	item.Destination.Commit.Hash = "new-base"
	if item.fingerprint() == before {
		t.Fatal("destination change reused cached detail")
	}
}
