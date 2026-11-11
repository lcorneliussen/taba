package wtc

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
	f.put(t, "token", "synthetic-session-a")
	t.Setenv("RENAME_FIXTURE", f.dir)
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_CONFIG_DIR", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	for _, name := range []string{"BB_USERNAME", "BB_API_TOKEN", "ATLASSIAN_ACCOUNT_EMAIL", "ATLASSIAN_API_TOKEN"} {
		t.Setenv(name, "")
	}
	bin := t.TempDir()
	fixtureFile(t, filepath.Join(bin, "gh"), `#!/bin/sh
printf '%s\n' "$*" >> "$RENAME_FIXTURE/calls"
case "$1 $2" in
 'auth token')
   [ ! -f "$RENAME_FIXTURE/fail-auth" ] || exit 1
   if [ -f "$RENAME_FIXTURE/rotate-after-first-repair" ]; then
     file=$(cat "$RENAME_FIXTURE/rotate-after-first-repair")
     if grep -q 'new-name/pull/42' "$file"; then printf 'synthetic-session-b\n'; exit 0; fi
   fi
   if [ -n "$GH_TOKEN" ]; then printf '%s\n' "$GH_TOKEN"; else cat "$RENAME_FIXTURE/token"; fi
   exit 0 ;;
 'api repos/example/old-name') file=old ;;
 'api repos/example/new-name') file=new ;;
 'api repos/example/new-name/pulls/42') file=base ;;
 'api repos/example/new-name/pulls/43') file=base-43 ;;
 'api repos/example/new-name/pulls/44') file=base-44 ;;
 'pr list') printf '[]\n'; exit 0 ;;
 'pr view') cat "$RENAME_FIXTURE/detail"; exit 0 ;;
 *) exit 2 ;;
esac
if [ -f "$RENAME_FIXTURE/fail" ] || [ -f "$RENAME_FIXTURE/fail-$file" ]; then printf 'offline/auth/HTTP 404 synthetic-secret\n' >&2; exit 1; fi
if [ -f "$RENAME_FIXTURE/expect-api-token" ] && [ "$GH_TOKEN" != "$(cat "$RENAME_FIXTURE/expect-api-token")" ]; then exit 61; fi
if [ "$file" = base ] && [ -f "$RENAME_FIXTURE/block" ]; then
 touch "$RENAME_FIXTURE/entered"
 while [ ! -f "$RENAME_FIXTURE/continue" ]; do sleep 0.01; done
fi
if [ "$file" = base-43 ] && [ -f "$RENAME_FIXTURE/block-43" ]; then
 touch "$RENAME_FIXTURE/entered-43"
 while [ ! -f "$RENAME_FIXTURE/continue-43" ]; do sleep 0.01; done
fi
cat "$RENAME_FIXTURE/$file"
`, 0755)
	fixtureFile(t, filepath.Join(bin, "bb"), "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$RENAME_FIXTURE/calls\"\ncat \"$RENAME_FIXTURE/detail\"\n", 0755)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if forge == "bitbucket.org" {
		t.Setenv("BB_API_USER", "fixture")
		t.Setenv("BB_API_PASS", "synthetic")
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			log, _ := os.OpenFile(filepath.Join(f.dir, "calls"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
			if log != nil {
				fmt.Fprintln(log, "api "+r.URL.Path)
				log.Close()
			}
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
			_, failAll := os.Stat(filepath.Join(f.dir, "fail"))
			_, failEndpoint := os.Stat(filepath.Join(f.dir, "fail-"+file))
			if failAll == nil || failEndpoint == nil {
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
				p := prIdentityProof{}
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

func TestStatusRenameFailedRepositoryIsQueriedOncePerRefresh(t *testing.T) {
	c := newWorkspaceFixture(t)
	f := newRenameProviderFixture(t, "github.com")
	c.Registry.Repos = append(c.Registry.Repos, Repo{Name: "reference", Remote: "https://github.com/example/new-name.git"})
	f.put(t, "fail-old", "")
	before := "reference 42 topic https://github.com/example/old-name/pull/42 One\n" +
		"reference 43 topic https://github.com/example/old-name/pull/43 Two\n" +
		"reference 44 topic https://github.com/example/old-name/pull/44 Three\n" +
		"# merged-pr reference 43 2026-10-29T12:00:00Z SUCCESS\n"
	fixtureFile(t, c.PRFile(), before, 0600)
	var progress []string
	c.StatusProgress = func(message string) { progress = append(progress, message) }
	for refresh := 1; refresh <= 2; refresh++ {
		if _, err := c.statusForgePreview(false, true); err != nil {
			t.Fatal(err)
		}
		calls, _ := os.ReadFile(filepath.Join(f.dir, "calls"))
		if got := strings.Count(string(calls), "api repos/example/old-name "); got != refresh {
			t.Fatalf("failed shared endpoint repeated: %d old-name calls after %d refreshes; want one per refresh", got, refresh)
		}
		if raw, _ := os.ReadFile(c.PRFile()); string(raw) != before {
			t.Fatal("unavailable identity changed owner metadata")
		}
	}
	if strings.Contains(strings.Join(progress, "\n"), "synthetic-secret") {
		t.Fatal("cached failure leaked provider output")
	}
}

func refillRenameBudget(t *testing.T, forge string) {
	t.Helper()
	// Model a real 60-second refill without sleeping or changing the configured
	// interval, token cap, or production budget implementation.
	fixtureFile(t, statusForgeStatePath("budget", forge), fmt.Sprintf("0 %d\n", time.Now().Add(-61*time.Second).UnixNano()), 0600)
}

func TestStatusRenameProgressesAcrossPositiveBudgetRefills(t *testing.T) {
	c := newWorkspaceFixture(t)
	f := newRenameProviderFixture(t, "github.com")
	t.Setenv("WTC_CONFIG_ROOT", c.ConfigRoot)
	t.Setenv("WTC_FORGE_MIN_INTERVAL", "60")
	registry := filepath.Join(c.Harness, ".harness-repos.yml")
	data, _ := os.ReadFile(registry)
	fixtureFile(t, registry, string(data)+"  - name: reference\n    remote: https://github.com/example/new-name.git\n    default_ref: origin/main\n", 0644)
	before := "reference 42 topic https://github.com/example/old-name/pull/42 Synthetic rate-limited rename\n"
	fixtureFile(t, c.PRFile(), before, 0600)
	for refresh := 0; refresh < 3; refresh++ {
		if refresh > 0 {
			refillRenameBudget(t, "github.com")
		}
		fresh, err := OpenCollection(c.Collection)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fresh.statusForgePreview(false, true); err != nil {
			t.Fatal(err)
		}
		calls, _ := os.ReadFile(filepath.Join(f.dir, "calls"))
		if strings.Count(string(calls), "api ") != refresh+1 {
			t.Fatalf("refresh bypassed one-call interval: %s", calls)
		}
	}
	after, _ := os.ReadFile(c.PRFile())
	if string(after) != strings.Replace(before, "old-name", "new-name", 1) {
		calls, _ := os.ReadFile(filepath.Join(f.dir, "calls"))
		t.Fatalf("valid rename never progressed across refills; metadata=%q, provider calls:\n%s", after, calls)
	}
}

func stagedRenameFixture(t *testing.T, forge string) (*Context, renameProviderFixture, string) {
	t.Helper()
	c := newWorkspaceFixture(t)
	f := newRenameProviderFixture(t, forge)
	t.Setenv("WTC_CONFIG_ROOT", c.ConfigRoot)
	t.Setenv("WTC_FORGE_MIN_INTERVAL", "60")
	registry := filepath.Join(c.Harness, ".harness-repos.yml")
	raw, _ := os.ReadFile(registry)
	fixtureFile(t, registry, string(raw)+"  - name: reference\n    remote: https://"+forge+"/example/new-name.git\n    default_ref: origin/main\n", 0644)
	before := "reference 42 topic " + prURLForStatus(forge, "example/old-name", "42") + " Synthetic rename\n"
	fixtureFile(t, c.PRFile(), before, 0600)
	return c, f, before
}

func stagedRenameRefresh(t *testing.T, c *Context) StatusSnapshot {
	t.Helper()
	fresh, err := OpenCollection(c.Collection)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := fresh.statusForgePreview(false, true)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func stagedRenamePath(t *testing.T) string {
	t.Helper()
	paths, _ := filepath.Glob(filepath.Join(statusForgeCacheDir(), "identity-attempt-v1-*.json"))
	if len(paths) != 1 {
		t.Fatalf("want one pending attempt, got %d", len(paths))
	}
	return paths[0]
}

func TestStatusRenameStagedProofInvalidation(t *testing.T) {
	for _, forge := range []string{"github.com", "bitbucket.org"} {
		for _, mode := range []string{"old-name-reused", "canonical-reused", "base-mismatch", "expired", "future", "malformed", "credential-change", "context-change", "offline", "auth-unavailable", "permissions", "nonregular", "cooldown"} {
			t.Run(forge+"/"+mode, func(t *testing.T) {
				c, f, before := stagedRenameFixture(t, forge)
				stagedRenameRefresh(t, c)
				refillRenameBudget(t, forge)
				stagedRenameRefresh(t, c)
				path := stagedRenamePath(t)
				raw, _ := os.ReadFile(path)
				if strings.Contains(string(raw), "synthetic") || strings.Contains(string(raw), "secret") {
					t.Fatal("pending proof persisted credentials or provider error output")
				}
				switch mode {
				case "old-name-reused":
					f.put(t, "old", renameRepositoryJSON(forge, "example/old-name", true))
				case "canonical-reused":
					f.put(t, "old", renameRepositoryJSON(forge, "example/elsewhere", false))
					f.put(t, "new", renameRepositoryJSON(forge, "example/new-name", true))
				case "base-mismatch":
					// Invalidate the pending base observation, never treat a different
					// immutable base as another stage of the same proof.
					var a prIdentityAttempt
					json.Unmarshal(raw, &a)
					a.Base.ID = "different"
					raw, _ = json.Marshal(a)
					fixtureFile(t, path, string(raw), 0600)
				case "expired", "future":
					t.Setenv("WTC_FORGE_CACHE_AGE", "86400")
					var a prIdentityAttempt
					json.Unmarshal(raw, &a)
					a.Started = time.Now().Add(-6 * time.Minute)
					if mode == "future" {
						a.Started = time.Now().Add(time.Minute)
					}
					a.BaseObserved = a.Started.Add(time.Second)
					raw, _ = json.Marshal(a)
					fixtureFile(t, path, string(raw), 0600)
				case "malformed":
					fixtureFile(t, path, "not-json", 0600)
				case "credential-change":
					if forge == "github.com" {
						f.put(t, "token", "synthetic-session-b")
					} else {
						t.Setenv("BB_API_PASS", "synthetic-session-b")
					}
				case "context-change":
					t.Setenv("WTC_CONFIG_ROOT", t.TempDir())
				case "offline":
					f.put(t, "fail-old", "")
				case "auth-unavailable":
					if forge == "github.com" {
						f.put(t, "fail-auth", "")
					} else {
						t.Setenv("BB_API_PASS", "")
					}
				case "permissions":
					if err := os.Chmod(path, 0644); err != nil {
						t.Fatal(err)
					}
				case "nonregular":
					os.Remove(path)
					if err := os.Mkdir(path, 0700); err != nil {
						t.Fatal(err)
					}
				case "cooldown":
					statusForgeCool(forge, time.Minute)
				}
				refillRenameBudget(t, forge)
				var progress []string
				fresh, _ := OpenCollection(c.Collection)
				fresh.StatusProgress = func(message string) { progress = append(progress, message) }
				if _, err := fresh.statusForgePreview(false, true); err != nil {
					t.Fatal(err)
				}
				if after, _ := os.ReadFile(c.PRFile()); string(after) != before {
					t.Fatalf("%s authorized stale/inconsistent repair: %q", mode, after)
				}
				if strings.Contains(strings.Join(progress, "\n"), "synthetic-secret") {
					t.Fatal("provider error leaked")
				}
				if mode == "old-name-reused" || mode == "canonical-reused" || mode == "offline" || mode == "auth-unavailable" {
					if _, err := os.Stat(path); !os.IsNotExist(err) {
						t.Fatal("failed identity retained pending evidence")
					}
				}
				if mode == "cooldown" {
					fixtureFile(t, statusForgeStatePath("cool", forge), fmt.Sprint(time.Now().Add(-time.Second).UnixNano()), 0600)
					stagedRenameRefresh(t, c)
					after, _ := os.ReadFile(c.PRFile())
					if string(after) != strings.Replace(before, "old-name", "new-name", 1) {
						t.Fatal("fresh completing lookup did not resume after cooldown")
					}
				}
			})
		}
	}
}

func TestStatusRenameActiveRowsRecoverBeforeHistoricalBackfill(t *testing.T) {
	c, f, _ := stagedRenameFixture(t, "github.com")
	var history strings.Builder
	for n := 100; n < 120; n++ {
		fmt.Fprintf(&history, "reference %d old-topic https://github.com/example/old-name/pull/%d Historical\n# merged-pr reference %d 2026-10-29T12:00:00Z SUCCESS\n", n, n, n)
	}
	active := "reference 42 topic https://github.com/example/old-name/pull/42 Active\n"
	before := history.String() + active
	fixtureFile(t, c.PRFile(), before, 0600)
	for refresh := 0; refresh < 5; refresh++ {
		if refresh > 0 {
			refillRenameBudget(t, "github.com")
		}
		snapshot := stagedRenameRefresh(t, c)
		calls, _ := os.ReadFile(filepath.Join(f.dir, "calls"))
		if strings.Contains(string(calls), "/pulls/100") {
			t.Fatal("historical proof consumed active recovery/enrichment allowance")
		}
		if refresh >= 2 {
			want := history.String() + strings.Replace(active, "old-name", "new-name", 1)
			if after, _ := os.ReadFile(c.PRFile()); string(after) != want {
				t.Fatalf("active URL did not recover ahead of history: active=%q, settled bytes preserved=%t", strings.TrimPrefix(string(after), history.String()), strings.HasPrefix(string(after), history.String()))
			}
		}
		if refresh == 4 && snapshot.PRs[len(snapshot.PRs)-1].State != "OPEN" {
			t.Fatalf("active URL healed but its status still starved: %+v", snapshot.PRs[len(snapshot.PRs)-1])
		}
	}
}

func TestPRIdentityConcurrentAttemptsRespectOneTokenAndComplete(t *testing.T) {
	for _, forge := range []string{"github.com", "bitbucket.org"} {
		t.Run(forge, func(t *testing.T) {
			f := newRenameProviderFixture(t, forge)
			t.Setenv("WTC_FORGE_MIN_INTERVAL", "60")
			m := &prIdentityMismatch{old: "example/old-name", current: "example/new-name", forge: forge}
			for refill := 0; refill < 3; refill++ {
				if refill > 0 {
					refillRenameBudget(t, forge)
				}
				var wg sync.WaitGroup
				out := make(chan string, 2)
				for worker := 0; worker < 2; worker++ {
					wg.Add(1)
					go func() {
						defer wg.Done()
						p := prIdentityProof{}
						url, err := p.verify(m, "42")
						if err != nil && !errors.Is(err, errForgeBudget) {
							t.Errorf("unexpected concurrent proof error: %v", err)
						}
						out <- url
					}()
				}
				wg.Wait()
				close(out)
				completed := 0
				for url := range out {
					if url != "" {
						completed++
					}
				}
				if (refill == 2 && completed != 1) || (refill < 2 && completed != 0) {
					t.Fatalf("completion count at refill %d: %d", refill, completed)
				}
				calls, _ := os.ReadFile(filepath.Join(f.dir, "calls"))
				if strings.Count(string(calls), "api ") != refill+1 {
					t.Fatalf("concurrent requests bypassed interval: %s", calls)
				}
			}
		})
	}
}

func TestStatusRenameSameRefreshRotationStopsLaterRows(t *testing.T) {
	for _, mode := range []string{"token", "context"} {
		t.Run(mode, func(t *testing.T) {
			c := newWorkspaceFixture(t)
			f := newRenameProviderFixture(t, "github.com")
			c.Registry.Repos = append(c.Registry.Repos, Repo{Name: "reference", Remote: "https://github.com/example/new-name.git"})
			for _, number := range []int{43, 44} {
				f.put(t, fmt.Sprintf("base-%d", number), renameBaseJSON("github.com", renameRepositoryJSON("github.com", "example/new-name", false), number))
			}
			f.put(t, "expect-api-token", "synthetic-session-a")
			before := "reference 42 topic https://github.com/example/old-name/pull/42 One\n" +
				"reference 43 topic https://github.com/example/old-name/pull/43 Two\n" +
				"reference 44 topic https://github.com/example/old-name/pull/44 Three\n"
			fixtureFile(t, c.PRFile(), before, 0600)
			if mode == "token" {
				f.put(t, "rotate-after-first-repair", c.PRFile())
			} else {
				f.put(t, "block-43", "")
			}
			done := make(chan error, 1)
			go func() { _, err := c.statusForgePreview(false, true); done <- err }()
			if mode == "context" {
				deadline := time.Now().Add(5 * time.Second)
				for {
					if _, err := os.Stat(filepath.Join(f.dir, "entered-43")); err == nil {
						break
					}
					if time.Now().After(deadline) {
						f.put(t, "continue-43", "")
						t.Fatal("second row did not reach the rotation barrier")
					}
					time.Sleep(10 * time.Millisecond)
				}
				t.Setenv("GH_CONFIG_DIR", t.TempDir())
				f.put(t, "continue-43", "")
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if after, _ := os.ReadFile(c.PRFile()); string(after) != strings.Replace(before, "old-name", "new-name", 1) {
				t.Fatalf("rotation allowed a later-row URL CAS: %q", after)
			}
			calls, _ := os.ReadFile(filepath.Join(f.dir, "calls"))
			if strings.Contains(string(calls), "/pulls/44") {
				t.Fatal("same refresh continued proof after credential/context rotation")
			}
			paths, _ := filepath.Glob(filepath.Join(statusForgeCacheDir(), "identity-attempt-v1-*.json"))
			if len(paths) != 0 {
				t.Fatal("rotation persisted mixed-session observations")
			}
		})
	}
}

func TestStatusRenameStagedProofPreservesWritersAndSweep(t *testing.T) {
	for _, mode := range []string{"edit", "removed", "sweep"} {
		t.Run(mode, func(t *testing.T) {
			c, f, before := stagedRenameFixture(t, "github.com")
			stagedRenameRefresh(t, c)
			refillRenameBudget(t, "github.com")
			stagedRenameRefresh(t, c)
			if mode == "edit" {
				_, err := c.EnlistPR(PRRecord{Repo: "reference", Number: "42", Branch: "owner-topic", Title: "Owner edit", URL: "https://github.com/example/old-name/pull/42"})
				if err != nil {
					t.Fatal(err)
				}
				raw, _ := os.ReadFile(c.PRFile())
				before = string(raw)
			} else if mode == "removed" {
				if err := c.UnlistPR("reference", "42"); err != nil {
					t.Fatal(err)
				}
				raw, _ := os.ReadFile(c.PRFile())
				before = string(raw)
			} else {
				path := stagedRenamePath(t)
				pending, _ := os.ReadFile(path)
				calls, _ := os.ReadFile(filepath.Join(f.dir, "calls"))
				if _, _, err := c.StatusLiveSnapshot(true, true); err != nil {
					t.Fatal(err)
				}
				if after, _ := os.ReadFile(path); string(after) != string(pending) {
					t.Fatal("sweep advanced or invalidated pending proof")
				}
				if after, _ := os.ReadFile(filepath.Join(f.dir, "calls")); string(after) != string(calls) {
					t.Fatal("sweep spent a pending identity request")
				}
				if after, _ := os.ReadFile(c.PRFile()); string(after) != before {
					t.Fatal("sweep wrote owner metadata")
				}
			}
			refillRenameBudget(t, "github.com")
			stagedRenameRefresh(t, c)
			want := before
			if mode != "removed" {
				want = strings.Replace(before, "old-name", "new-name", 1)
			}
			if after, _ := os.ReadFile(c.PRFile()); string(after) != want {
				t.Fatalf("pending proof overwrote or resurrected owner state: %q", after)
			}
		})
	}
}
