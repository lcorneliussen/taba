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
	"time"
)

type renameProviderFixture struct {
	dir, forge string
}

func renameRepositoryJSON(forge, slug string, different bool) string {
	if forge == "github.com" {
		id := 17
		if different {
			id = 18
		}
		return fmt.Sprintf(`{"id":%d,"full_name":%q}`, id, slug)
	}
	uuid := "{11111111-1111-4111-8111-111111111111}"
	if different {
		uuid = "{22222222-2222-4222-8222-222222222222}"
	}
	return fmt.Sprintf(`{"uuid":%q,"full_name":%q}`, uuid, slug)
}

func renameBaseJSON(forge, repository string, number int) string {
	if forge == "github.com" {
		return fmt.Sprintf(`{"number":%d,"base":{"repo":%s},"head":{"repo":%s}}`, number, repository, renameRepositoryJSON(forge, "example/new-name", false))
	}
	return fmt.Sprintf(`{"id":%d,"destination":{"repository":%s},"source":{"repository":%s}}`, number, repository, renameRepositoryJSON(forge, "example/new-name", false))
}

func newRenameProviderFixture(t *testing.T, forge string) renameProviderFixture {
	t.Helper()
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("WTC_FORGE_MIN_INTERVAL", "0")
	f := renameProviderFixture{dir: t.TempDir(), forge: forge}
	repo := renameRepositoryJSON(forge, "example/new-name", false)
	f.put(t, "old", repo)
	f.put(t, "new", repo)
	f.put(t, "base", renameBaseJSON(forge, repo, 42))
	f.put(t, "detail", `{"number":42,"id":42,"state":"OPEN","title":"Synthetic rename","mergeStateStatus":"CONFLICTING","mergeable":"CONFLICTING"}`)
	t.Setenv("RENAME_FIXTURE", f.dir)
	bin := t.TempDir()
	fixtureFile(t, filepath.Join(bin, "gh"), `#!/bin/sh
printf '%s\n' "$*" >> "$RENAME_FIXTURE/calls"
case "$1 $2" in
 'api repos/example/old-name') file=old ;;
 'api repos/example/new-name') file=new ;;
 'api repos/example/new-name/pulls/42') file=base ;;
 'pr list') printf '[]\n'; exit 0 ;;
 'pr view') cat "$RENAME_FIXTURE/detail"; exit 0 ;;
 *) exit 2 ;;
esac
if [ -f "$RENAME_FIXTURE/fail" ]; then printf 'offline/auth/HTTP 404 synthetic-secret\n' >&2; exit 1; fi
if [ "$file" = base ] && [ -f "$RENAME_FIXTURE/block" ]; then
 touch "$RENAME_FIXTURE/entered"
 while [ ! -f "$RENAME_FIXTURE/continue" ]; do sleep 0.01; done
fi
cat "$RENAME_FIXTURE/$file"
`, 0755)
	fixtureFile(t, filepath.Join(bin, "bb"), "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$RENAME_FIXTURE/calls\"\ncat \"$RENAME_FIXTURE/detail\"\n", 0755)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if forge == "bitbucket.org" {
		t.Setenv("BB_API_USER", "fixture")
		t.Setenv("BB_API_PASS", "synthetic")
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			file := ""
			switch r.URL.Path {
			case "/repositories/example/old-name":
				file = "old"
			case "/repositories/example/new-name":
				file = "new"
			case "/repositories/example/new-name/pullrequests/42":
				file = "base"
			default:
				fmt.Fprint(w, `{"values":[]}`)
				return
			}
			if _, err := os.Stat(filepath.Join(f.dir, "fail")); err == nil {
				http.Error(w, "synthetic-secret", http.StatusUnauthorized)
				return
			}
			raw, _ := os.ReadFile(filepath.Join(f.dir, file))
			w.Write(raw)
		}))
		old := statusBBAPI
		statusBBAPI = server.URL
		t.Cleanup(func() { statusBBAPI = old; server.Close() })
	}
	return f
}

func (f renameProviderFixture) put(t *testing.T, name, body string) {
	t.Helper()
	fixtureFile(t, filepath.Join(f.dir, name), body, 0600)
}

func TestPRRenameProofRequiresSameImmutableBaseRepository(t *testing.T) {
	for _, forge := range []string{"github.com", "bitbucket.org"} {
		for _, mode := range []string{"rename", "reused-old-name", "different-base", "wrong-number", "missing-base", "missing-id", "different-canonical-name", "lookup-unavailable", "cooldown", "spent-budget"} {
			t.Run(forge+"/"+mode, func(t *testing.T) {
				f := newRenameProviderFixture(t, forge)
				switch mode {
				case "reused-old-name":
					f.put(t, "old", renameRepositoryJSON(forge, "example/old-name", true))
				case "different-base":
					f.put(t, "base", renameBaseJSON(forge, renameRepositoryJSON(forge, "example/new-name", true), 42))
				case "wrong-number":
					f.put(t, "base", renameBaseJSON(forge, renameRepositoryJSON(forge, "example/new-name", false), 43))
				case "missing-base":
					f.put(t, "base", `{"number":42,"id":42,"head":{"repo":{"id":17,"full_name":"example/new-name"}},"source":{"repository":{"uuid":"{11111111-1111-4111-8111-111111111111}","full_name":"example/new-name"}}}`)
				case "missing-id":
					f.put(t, "old", `{"full_name":"example/new-name"}`)
				case "different-canonical-name":
					f.put(t, "old", renameRepositoryJSON(forge, "other/new-name", false))
				case "lookup-unavailable":
					f.put(t, "fail", "")
				case "cooldown":
					statusForgeCool(forge, time.Minute)
				case "spent-budget":
					t.Setenv("WTC_FORGE_MIN_INTERVAL", "3600")
					if !statusTakeForgeToken(forge) {
						t.Fatal("could not spend initial token")
					}
				}
				p := prIdentityProof{repositories: map[string]prProviderRepository{}}
				url, err := p.verify(&prIdentityMismatch{old: "example/old-name", current: "example/new-name", forge: forge}, "42")
				if mode == "rename" {
					if err != nil || url != prURLForStatus(forge, "example/new-name", "42") {
						t.Fatalf("verified rename: %q %v", url, err)
					}
				} else if err == nil || url != "" || strings.Contains(err.Error(), "synthetic-secret") {
					t.Fatalf("unsafe evidence accepted/leaked: %q %v", url, err)
				}
			})
		}
	}
}

func TestStatusRecoversRenamePreservingLocalState(t *testing.T) {
	for _, forge := range []string{"github.com", "bitbucket.org"} {
		for _, filename := range []string{".wtc-prs", ".taba-prs"} {
			t.Run(forge+"/"+filename, func(t *testing.T) {
				c := newWorkspaceFixture(t)
				f := newRenameProviderFixture(t, forge)
				c.Registry.Repos = append(c.Registry.Repos, Repo{Name: "reference", Remote: "https://" + forge + "/example/new-name.git"})
				oldURL := prURLForStatus(forge, "example/old-name", "42")
				newURL := prURLForStatus(forge, "example/new-name", "42")
				before := "# User comment\r\nreference\t42  topic\t" + oldURL + "  User title\r\nother 9 - - Unrelated\n# merged-pr reference 42 2026-10-29T12:00:00Z SUCCESS\n"
				// Start with a settled merge to prove recovery also preserves its facts.
				fixtureFile(t, filepath.Join(c.Collection, filename), before, 0600)
				fixtureGit(t, "-C", c.Harness, "switch", "-qc", "topic")
				fixtureFile(t, filepath.Join(c.Harness, "dirty.txt"), "user work", 0600)
				head := fixtureGit(t, "-C", c.Harness, "rev-parse", "HEAD")
				remote := fixtureGit(t, "-C", c.Harness, "remote", "get-url", "origin")
				dirty := fixtureGit(t, "-C", c.Harness, "status", "--porcelain")
				for run := 0; run < 2; run++ {
					snapshot, err := c.statusForgePreview(false, true)
					if err != nil || len(snapshot.PRs) != 2 || snapshot.PRs[0].State != "MERGED" || snapshot.PRs[0].URL == nil || *snapshot.PRs[0].URL != newURL {
						t.Fatalf("status recovery: %+v %v", snapshot.PRs, err)
					}
					data, _ := os.ReadFile(c.PRFile())
					if string(data) != strings.Replace(before, oldURL, newURL, 1) {
						t.Fatalf("unrelated bytes changed: %q", data)
					}
					if fixtureGit(t, "-C", c.Harness, "rev-parse", "HEAD") != head || fixtureGit(t, "-C", c.Harness, "branch", "--show-current") != "topic" || fixtureGit(t, "-C", c.Harness, "remote", "get-url", "origin") != remote || fixtureGit(t, "-C", c.Harness, "status", "--porcelain") != dirty {
						t.Fatal("status recovery mutated checkout")
					}
				}
				// Proof is no longer needed on the next refresh, even offline.
				f.put(t, "fail", "")
				if _, err := c.statusForgePreview(false, false); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestStatusRenameOpenAndUnavailableDiagnostics(t *testing.T) {
	for _, unavailable := range []bool{false, true} {
		t.Run(fmt.Sprint(unavailable), func(t *testing.T) {
			c := newWorkspaceFixture(t)
			f := newRenameProviderFixture(t, "github.com")
			c.Registry.Repos = append(c.Registry.Repos, Repo{Name: "reference", Remote: "https://github.com/example/new-name.git"})
			before := "reference 42 topic https://github.com/example/old-name/pull/42 Synthetic rename\n"
			fixtureFile(t, c.PRFile(), before, 0600)
			var progress []string
			c.StatusProgress = func(message string) { progress = append(progress, message) }
			if unavailable {
				f.put(t, "fail", "")
			}
			s, err := c.statusForgePreview(false, true)
			if err != nil || len(s.PRs) != 1 {
				t.Fatalf("status: %+v %v", s.PRs, err)
			}
			if unavailable {
				raw, _ := os.ReadFile(c.PRFile())
				if string(raw) != before || s.PRs[0].State != "UNKNOWN" || s.PRs[0].URL != nil || !strings.Contains(strings.Join(progress, "\n"), "verify forge login") || strings.Contains(strings.Join(progress, "\n"), "synthetic-secret") {
					t.Fatalf("unavailable evidence mutated state or lacked safe action: %+v %q", s.PRs, progress)
				}
			} else if s.PRs[0].State != "OPEN" || s.PRs[0].Slug != "example/new-name" || s.PRs[0].Merge == nil || *s.PRs[0].Merge != "CONFLICTING" {
				t.Fatalf("real PR facts not recovered: %+v", s.PRs)
			}
		})
	}
}

func TestPRURLRepairPreservesConcurrentEditsAndDoesNotResurrect(t *testing.T) {
	for _, mode := range []string{"branch-title", "replaced-url", "removed", "duplicate"} {
		t.Run(mode, func(t *testing.T) {
			c := newWorkspaceFixture(t)
			before := PRRecord{Repo: "reference", Number: "42", Branch: "old-topic", URL: "https://github.com/example/old-name/pull/42", Title: "old title"}
			latest := before
			latest.Branch, latest.Title = "new-topic", "New title"
			if mode == "replaced-url" {
				latest.URL = "https://github.com/other/widget/pull/42"
			}
			data := "# retain\n" + formatPR(latest) + "\n# merged-pr reference 42 2026-10-29T12:00:00Z SUCCESS\n"
			if mode == "removed" {
				data = "# retain\n"
			} else if mode == "duplicate" {
				data += formatPR(latest) + "\n"
			}
			fixtureFile(t, c.PRFile(), data, 0600)
			canonical := "https://github.com/example/new-name/pull/42"
			got, changed, err := c.reconcilePRURL(before, canonical)
			if err != nil || changed != (mode == "branch-title") {
				t.Fatalf("CAS result: %+v %t %v", got, changed, err)
			}
			raw, _ := os.ReadFile(c.PRFile())
			want := data
			if changed {
				want = strings.Replace(data, before.URL, canonical, 1)
				if got.Branch != latest.Branch || got.Title != latest.Title || got.MergedOn == "" || got.FinalChecks != "SUCCESS" {
					t.Fatalf("lost concurrent/final facts: %+v", got)
				}
			}
			if string(raw) != want {
				t.Fatalf("CAS changed unrelated data: %q", raw)
			}
		})
	}
}

func TestStatusRenameConcurrentWriterDuringProviderLookup(t *testing.T) {
	c := newWorkspaceFixture(t)
	f := newRenameProviderFixture(t, "github.com")
	c.Registry.Repos = append(c.Registry.Repos, Repo{Name: "reference", Remote: "https://github.com/example/new-name.git"})
	record := PRRecord{Repo: "reference", Number: "42", Branch: "old-topic", URL: "https://github.com/example/old-name/pull/42", Title: "old title"}
	if _, err := c.EnlistPR(record); err != nil {
		t.Fatal(err)
	}
	f.put(t, "block", "")
	finished := make(chan error, 1)
	go func() { _, err := c.statusForgePreview(false, true); finished <- err }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(f.dir, "entered")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			f.put(t, "continue", "")
			t.Fatal("provider lookup did not reach deterministic barrier")
		}
		time.Sleep(10 * time.Millisecond)
	}
	record.Branch, record.Title = "new-topic", "New title"
	_, editErr := c.EnlistPR(record)
	f.put(t, "continue", "")
	if err := <-finished; err != nil || editErr != nil {
		t.Fatalf("status/edit failed: %v %v", err, editErr)
	}
	records, err := c.ListPRs()
	if err != nil || len(records) != 1 || records[0].Branch != "new-topic" || records[0].Title != "New title" || records[0].URL != "https://github.com/example/new-name/pull/42" {
		encoded, _ := json.Marshal(records)
		t.Fatalf("concurrent edit lost: %s %v", encoded, err)
	}
}

func TestStatusWorkspaceSweepDoesNotRepairSiblingPRMetadata(t *testing.T) {
	c := newWorkspaceFixture(t)
	created, err := c.NewCollection(NewOptions{Slug: "other"})
	if err != nil {
		t.Fatal(err)
	}
	newRenameProviderFixture(t, "github.com")
	other, err := OpenCollection(created.Collection)
	if err != nil {
		t.Fatal(err)
	}
	registryPath := filepath.Join(other.Harness, ".harness-repos.yml")
	registry, _ := os.ReadFile(registryPath)
	fixtureFile(t, registryPath, string(registry)+"  - name: reference\n    remote: https://github.com/example/new-name.git\n    default_ref: origin/main\n", 0644)
	before := "# Owner state\nreference 42 topic https://github.com/example/old-name/pull/42 Sibling reference\n"
	fixtureFile(t, other.PRFile(), before, 0600)
	if _, _, err := c.StatusLiveSnapshot(true, true); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(other.PRFile()); string(data) != before {
		t.Fatal("workspace sweep repaired a sibling registry")
	}
	if _, err := os.Stat(filepath.Join(other.Collection, ".wtc-prs.lock")); !os.IsNotExist(err) {
		t.Fatal("workspace sweep acquired sibling registry write authority")
	}
	// The sibling's own scoped status refresh has write authority.
	other, err = OpenCollection(created.Collection)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.StatusForgePreview(); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(other.PRFile()); string(data) != strings.Replace(before, "old-name", "new-name", 1) {
		t.Fatal("own scoped refresh did not repair the verified reference")
	}
}
