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
 'api graphql')
   printf '%s\n' "$*" >> "$RENAME_FIXTURE/provider-calls"
   number=42
   for arg in "$@"; do case "$arg" in number=*) number=${arg#number=} ;; esac; done
   printf 'repair %s\n' "$number" >> "$RENAME_FIXTURE/dispatches"
   [ ! -f "$RENAME_FIXTURE/fail-old" ] && [ ! -f "$RENAME_FIXTURE/fail-new" ] && [ ! -f "$RENAME_FIXTURE/fail-base" ] || exit 1
   file=graphql-$number ;;
 'api repos/example/old-name') file=old ;;
 'api repos/example/new-name') file=new ;;
 'api repos/example/new-name/pulls/42') file=base ;;
 'api repos/example/new-name/pulls/43') file=base-43 ;;
 'api repos/example/new-name/pulls/44') file=base-44 ;;
 'pr list')
   printf 'list\n' >> "$RENAME_FIXTURE/dispatches"
   printf '%s\n' "$*" >> "$RENAME_FIXTURE/provider-calls"
   if [ -f "$RENAME_FIXTURE/list" ]; then cat "$RENAME_FIXTURE/list"; else printf '[]\n'; fi; exit 0 ;;
 'pr view')
   printf 'detail %s\n' "$3" >> "$RENAME_FIXTURE/dispatches"
   printf '%s\n' "$*" >> "$RENAME_FIXTURE/provider-calls"
   [ ! -f "$RENAME_FIXTURE/fail-view-$3" ] || exit 1
   if [ -f "$RENAME_FIXTURE/detail-$3" ]; then cat "$RENAME_FIXTURE/detail-$3"; else cat "$RENAME_FIXTURE/detail"; fi
   exit 0 ;;
 *) exit 2 ;;
esac
if [ -f "$RENAME_FIXTURE/fail" ] || [ -f "$RENAME_FIXTURE/fail-$file" ]; then printf 'offline/auth/HTTP 404 synthetic-secret\n' >&2; exit 1; fi
if [ -f "$RENAME_FIXTURE/expect-api-token" ] && [ "$GH_TOKEN" != "$(cat "$RENAME_FIXTURE/expect-api-token")" ]; then exit 61; fi
if [ "$file" = graphql-42 ] && [ -f "$RENAME_FIXTURE/block" ]; then
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
	if strings.HasPrefix(name, "graphql-") {
		return
	}
	var old, current map[string]any
	raw, _ := os.ReadFile(filepath.Join(f.dir, "old"))
	_ = json.Unmarshal(raw, &old)
	raw, _ = os.ReadFile(filepath.Join(f.dir, "new"))
	_ = json.Unmarshal(raw, &current)
	convert := func(r map[string]any) map[string]any {
		id := ""
		if r["id"] != nil {
			id = "R_fixture_" + fmt.Sprint(r["id"])
		}
		return map[string]any{"id": id, "nameWithOwner": r["full_name"]}
	}
	files, _ := filepath.Glob(filepath.Join(f.dir, "base*"))
	for _, path := range files {
		if strings.Contains(filepath.Base(path), "fail") {
			continue
		}
		n := "42"
		if strings.HasPrefix(filepath.Base(path), "base-") {
			n = strings.TrimPrefix(filepath.Base(path), "base-")
		}
		raw, _ = os.ReadFile(path)
		var pr struct {
			Number int `json:"number"`
			Base   struct {
				Repo map[string]any `json:"repo"`
			} `json:"base"`
		}
		_ = json.Unmarshal(raw, &pr)
		repository := convert(old)
		repository["pullRequest"] = map[string]any{"number": pr.Number, "url": fmt.Sprintf("https://github.com/%v/pull/%d", current["full_name"], pr.Number), "baseRepository": convert(pr.Base.Repo)}
		response, _ := json.Marshal(map[string]any{"data": map[string]any{"old": repository, "current": convert(current)}})
		fixtureFile(t, filepath.Join(f.dir, "graphql-"+n), string(response), 0600)
	}
}

func TestPRRenameProofRequiresSameImmutableBaseRepository(t *testing.T) {
	for _, forge := range []string{"github.com"} {
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
				url, err := prIdentityGitHub(&prIdentityMismatch{old: "example/old-name", current: "example/new-name", forge: forge}, "42", nil)
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
	for _, forge := range []string{"github.com"} {
		for _, filename := range []string{".wtc-prs", ".taba-prs"} {
			t.Run(forge+"/"+filename, func(t *testing.T) {
				c := newWorkspaceFixture(t)
				f := newRenameProviderFixture(t, forge)
				fixtureRenameRegistry(t, c, "https://"+forge+"/example/new-name.git")
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
			fixtureRenameRegistry(t, c, "https://github.com/example/new-name.git")
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
	fixtureRenameRegistry(t, c, "https://github.com/example/new-name.git")
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

func TestStatusRenameFailureIsBoundedToOneRequestPerRefresh(t *testing.T) {
	c := newWorkspaceFixture(t)
	f := newRenameProviderFixture(t, "github.com")
	fixtureRenameRegistry(t, c, "https://github.com/example/new-name.git")
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
		if got := strings.Count(string(calls), "api graphql "); got != refresh {
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

func fixtureRenameRegistry(t *testing.T, c *Context, remote string) {
	t.Helper()
	t.Setenv("WTC_CONFIG_ROOT", c.ConfigRoot)
	raw, err := os.ReadFile(c.registryPath())
	if err != nil {
		t.Fatal(err)
	}
	fixtureFile(t, c.registryPath(), string(raw)+"  - name: reference\n    remote: "+remote+"\n    default_ref: origin/main\n", 0644)
	c.Registry.Repos = append(c.Registry.Repos, Repo{Name: "reference", Remote: remote})
}

func TestStatusOmittedCacheHitDoesNotCertifyFullRequest(t *testing.T) {
	t.Setenv("WTC_FORGE_FULL_REFRESH", "600")
	f := newRenameProviderFixture(t, "github.com")
	record := PRRecord{Repo: "reference", Number: "42", URL: "https://github.com/example/new-name/pull/42"}
	raw, _ := os.ReadFile(filepath.Join(f.dir, "detail"))
	statusWriteForgeCache("github.com", "example/new-name", "42", raw)
	listed, _ := statusEncodeOpenPRs(nil, false, time.Now())
	statusWriteForgeEntry("openprs", "github.com", "example/new-name", "open", listed)
	statusWriteForgeEntry("omit", "github.com", "example/new-name", "42", []byte("ok"))
	t.Setenv("WTC_FORGE_MIN_INTERVAL", "60")
	if !statusTakeForgeToken("github.com") {
		t.Fatal("budget setup")
	}
	if detail := statusEnrichRecordMetadata(record, "example/new-name", "github.com"); detail.State != "OPEN" {
		t.Fatal("cached facts unavailable")
	}
	if !statusFullRefreshDue("github.com", "example/new-name", "42") {
		t.Fatal("cache-only omitted-list result certified a full request that never happened")
	}
	calls, _ := os.ReadFile(filepath.Join(f.dir, "calls"))
	if strings.Contains(string(calls), "pr view ") {
		t.Fatal("fixture unexpectedly dispatched detail")
	}
}

func TestAtomicRenameRejectsPartialErrorsAndWrongURL(t *testing.T) {
	for _, mode := range []string{"errors", "old-null", "current-null", "base-null", "wrong-url", "different-current", "fork-base"} {
		t.Run(mode, func(t *testing.T) {
			f := newRenameProviderFixture(t, "github.com")
			raw, _ := os.ReadFile(filepath.Join(f.dir, "graphql-42"))
			var response map[string]any
			json.Unmarshal(raw, &response)
			data := response["data"].(map[string]any)
			old := data["old"].(map[string]any)
			pr := old["pullRequest"].(map[string]any)
			switch mode {
			case "errors":
				response["errors"] = []any{map[string]any{"message": "synthetic-secret"}}
			case "old-null":
				data["old"] = nil
			case "current-null":
				data["current"] = nil
			case "base-null":
				pr["baseRepository"] = nil
			case "wrong-url":
				pr["url"] = "https://github.com/example/old-name/pull/42"
			case "different-current":
				data["current"].(map[string]any)["id"] = "R_different"
			case "fork-base":
				pr["baseRepository"].(map[string]any)["nameWithOwner"] = "other/fork"
			}
			raw, _ = json.Marshal(response)
			f.put(t, "graphql-42", string(raw))
			canonical, err := prIdentityGitHub(&prIdentityMismatch{old: "example/old-name", current: "example/new-name", forge: "github.com"}, "42", nil)
			if err == nil || canonical != "" || strings.Contains(err.Error(), "synthetic-secret") {
				t.Fatalf("unsafe atomic result: %q %v", canonical, err)
			}
		})
	}
}

func TestAtomicRenameUsesIndependentAliasesAndOpaqueIDs(t *testing.T) {
	f := newRenameProviderFixture(t, "github.com")
	canonical, err := prIdentityGitHub(&prIdentityMismatch{old: "example/old-name", current: "example/new-name", forge: "github.com"}, "42", nil)
	if err != nil || canonical != "https://github.com/example/new-name/pull/42" {
		t.Fatalf("atomic proof: %q %v", canonical, err)
	}
	raw, _ := os.ReadFile(filepath.Join(f.dir, "provider-calls"))
	calls := string(raw)
	for _, want := range []string{"old:repository(owner:$oldOwner,name:$oldName)", "current:repository(owner:$currentOwner,name:$currentName)", "baseRepository{id nameWithOwner}", "-f oldOwner=example -f oldName=old-name -f currentOwner=example -f currentName=new-name -F number=42"} {
		if !strings.Contains(calls, want) {
			t.Fatalf("independent query/variables missing %q", want)
		}
	}
	if strings.Count(calls, "api graphql ") != 1 {
		t.Fatal("atomic proof used multiple operations")
	}
}

func TestBitbucketRenameRemainsFailClosed(t *testing.T) {
	c := newWorkspaceFixture(t)
	f := newRenameProviderFixture(t, "bitbucket.org")
	fixtureRenameRegistry(t, c, "https://bitbucket.org/example/new-name.git")
	before := "reference 42 topic https://bitbucket.org/example/old-name/pull-requests/42 Reference\n# merged-pr reference 42 2026-10-29T12:00:00Z SUCCESS\n"
	fixtureFile(t, c.PRFile(), before, 0600)
	if _, err := c.statusForgePreview(false, true); err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(c.PRFile()); string(raw) != before {
		t.Fatal("unsupported forge metadata changed")
	}
	if raw, _ := os.ReadFile(filepath.Join(f.dir, "calls")); len(raw) != 0 {
		t.Fatalf("unsupported rename made proof calls: %s", raw)
	}
}

func advanceRenameMinute(t *testing.T) {
	t.Helper()
	// Model elapsed minutes in existing cache mtimes, preserving their data and
	// default TTL90. The real allowance remains one request per60seconds.
	paths, _ := filepath.Glob(filepath.Join(statusForgeCacheDir(), "*.json"))
	for _, path := range paths {
		if strings.HasPrefix(filepath.Base(path), "identity-schedule-") {
			continue
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		old := info.ModTime().Add(-time.Minute)
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}
	refillRenameBudget(t, "github.com")
}

func fairAtomicFixture(t *testing.T, listMode string) (*Context, renameProviderFixture, string) {
	t.Helper()
	t.Setenv("WTC_FORGE_FULL_REFRESH", "600")
	c, f, _ := stagedRenameFixture(t, "github.com")
	for _, n := range []int{43, 44, 45, 46} {
		f.put(t, fmt.Sprintf("base-%d", n), renameBaseJSON("github.com", renameRepositoryJSON("github.com", "example/new-name", false), n))
		f.put(t, fmt.Sprintf("detail-%d", n), fmt.Sprintf(`{"number":%d,"state":"OPEN","title":"Canonical detail %d"}`, n, n))
	}
	f.put(t, "fail-graphql-42", "")
	f.put(t, "fail-view-44", "")
	var list []map[string]any
	if listMode == "listed" {
		list = append(list, map[string]any{"number": 45, "state": "OPEN", "title": "Listed canonical"})
	}
	if listMode == "capped" {
		for n := 100; n < 200; n++ {
			list = append(list, map[string]any{"number": n, "state": "OPEN"})
		}
	}
	raw, _ := json.Marshal(list)
	f.put(t, "list", string(raw))
	t.Setenv("WTC_FORGE_MIN_INTERVAL", "0")
	if _, _, err := statusOpenPRs("github.com", "example/new-name"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WTC_FORGE_MIN_INTERVAL", "60")
	before := "reference 43 old-topic https://github.com/example/old-name/pull/43 Settled\n# merged-pr reference 43 2026-10-29T12:00:00Z SUCCESS\n" +
		"reference 42 topic https://github.com/example/old-name/pull/42 Unavailable\n" +
		"reference 44 bad https://github.com/example/new-name/pull/44 Bad ordinary\n" +
		"reference 45 first https://github.com/example/new-name/pull/45 First healthy\n" +
		"reference 46 second https://github.com/example/new-name/pull/46 Second healthy\n"
	fixtureFile(t, c.PRFile(), before, 0600)
	return c, f, before
}

func TestStatusAtomicRecoveryFairStableQueue(t *testing.T) {
	for _, mode := range []string{"listed", "omitted", "capped"} {
		t.Run(mode, func(t *testing.T) {
			c, f, before := fairAtomicFixture(t, mode)
			if statusForgeCacheAge() != 90*time.Second {
				t.Fatal("fixture needs defaultTTL90")
			}
			for turn := 0; turn < 8; turn++ {
				if turn > 0 {
					advanceRenameMinute(t)
				}
				stagedRenameRefresh(t, c)
			}
			raw, _ := os.ReadFile(c.PRFile())
			want := strings.Replace(before, "old-name/pull/43", "new-name/pull/43", 1)
			if string(raw) != want {
				t.Fatalf("later valid repair starved or file order/bytes changed: %q", raw)
			}
			raw, _ = os.ReadFile(filepath.Join(f.dir, "provider-calls"))
			calls := string(raw)
			for _, n := range []string{"45", "46"} {
				if !strings.Contains(calls, "pr view "+n+" ") {
					t.Fatalf("healthy canonical PR%s starved: %s", n, calls)
				}
			}
			if !strings.Contains(calls, "pr view 44 ") || !strings.Contains(calls, "number=42") || !strings.Contains(calls, "number=43") {
				t.Fatal("failed jobs or later repair not attempted")
			}
			// One warm-up list plus exactly one dispatched operation per refill.
			count := strings.Count(calls, "api graphql ") + strings.Count(calls, "pr view ") + strings.Count(calls, "pr list ")
			if count != 9 {
				t.Fatalf("one-token allowance changed: got%d operations", count)
			}
			receipts, _ := os.ReadFile(filepath.Join(f.dir, "dispatches"))
			if string(receipts) != "list\ndetail 44\nrepair 42\ndetail 45\nrepair 43\ndetail 46\nrepair 42\ndetail 44\nrepair 42\n" {
				t.Fatalf("priority cursor did not rotate actual failed/successful requests: %s", receipts)
			}
		})
	}
}

func TestStatusAtomicNonissuanceKeepsCursor(t *testing.T) {
	for _, mode := range []string{"denial", "cooling", "preflight"} {
		t.Run(mode, func(t *testing.T) {
			c, f, before := stagedRenameFixture(t, "github.com")
			f.put(t, "fail-graphql-42", "")
			stagedRenameRefresh(t, c)
			path := c.prIdentitySchedulePath("github.com")
			saved, _ := os.ReadFile(path)
			calls, _ := os.ReadFile(filepath.Join(f.dir, "provider-calls"))
			if mode == "cooling" {
				statusForgeCool("github.com", time.Minute)
				refillRenameBudget(t, "github.com")
			}
			if mode == "preflight" {
				f.put(t, "fail-auth", "")
				refillRenameBudget(t, "github.com")
			}
			for n := 0; n < 3; n++ {
				stagedRenameRefresh(t, c)
			}
			if raw, _ := os.ReadFile(path); string(raw) != string(saved) {
				t.Fatal("unissued work advanced scheduling cursor")
			}
			if raw, _ := os.ReadFile(filepath.Join(f.dir, "provider-calls")); string(raw) != string(calls) {
				t.Fatal("denied/cooling/preflight made provider calls")
			}
			if raw, _ := os.ReadFile(c.PRFile()); string(raw) != before {
				t.Fatal("failed proof changed registry")
			}
		})
	}
}

func TestAtomicRenameInputContextChangesRefuseCAS(t *testing.T) {
	for _, mode := range []string{"registry", "selection", "registry-alias"} {
		t.Run(mode, func(t *testing.T) {
			c, f, before := stagedRenameFixture(t, "github.com")
			f.put(t, "block", "")
			fresh, err := OpenCollection(c.Collection)
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { _, err := fresh.statusForgePreview(false, true); done <- err }()
			deadline := time.Now().Add(5 * time.Second)
			for {
				if _, err := os.Stat(filepath.Join(f.dir, "entered")); err == nil {
					break
				}
				if time.Now().After(deadline) {
					f.put(t, "continue", "")
					<-done
					t.Fatal("query barrier missing")
				}
				time.Sleep(10 * time.Millisecond)
			}
			registry := c.registryPath()
			raw, _ := os.ReadFile(registry)
			switch mode {
			case "registry":
				fixtureFile(t, registry, strings.Replace(string(raw), "example/new-name", "example/owner-choice", 1), 0644)
			case "selection":
				fixtureFile(t, filepath.Join(c.Collection, ".wtc", "config", "repos.yml"), "repos: []\n", 0600)
			case "registry-alias":
				if err := os.Rename(registry, registry+".owner"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Base(registry)+".owner", registry); err != nil {
					t.Fatal(err)
				}
			}
			f.put(t, "continue", "")
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if raw, _ := os.ReadFile(c.PRFile()); string(raw) != before {
				t.Fatal("changed selected inputs authorized URL CAS")
			}
		})
	}
}

func TestAtomicScheduleUnsafeFilesFailClosed(t *testing.T) {
	for _, mode := range []string{"permissions", "symlink", "directory"} {
		t.Run(mode, func(t *testing.T) {
			c, f, before := stagedRenameFixture(t, "github.com")
			path := c.prIdentitySchedulePath("github.com")
			if !statusSecureCacheDir(filepath.Dir(path)) {
				t.Fatal("cache setup")
			}
			switch mode {
			case "permissions":
				fixtureFile(t, path, `{"Version":1,"Repair":true}`, 0644)
				if err := os.Chmod(path, 0644); err != nil {
					t.Fatal(err)
				}
				info, err := os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
				if got := info.Mode().Perm(); got != 0644 {
					t.Fatalf("unsafe scheduling fixture mode = %04o, want 0644", got)
				}
			case "symlink":
				target := filepath.Join(t.TempDir(), "owner")
				fixtureFile(t, target, "owner bytes", 0600)
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			}
			stagedRenameRefresh(t, c)
			if raw, _ := os.ReadFile(c.PRFile()); string(raw) != before {
				t.Fatal("unsafe scheduling file authorized repair")
			}
			if raw, _ := os.ReadFile(filepath.Join(f.dir, "provider-calls")); len(raw) != 0 {
				t.Fatal("unsafe scheduling cache dispatched repair")
			}
		})
	}
}

func TestRejectedGitHubRepairDoesNotConsumeBitbucketAllowance(t *testing.T) {
	c := newWorkspaceFixture(t)
	newRenameProviderFixture(t, "bitbucket.org")
	gh := newRenameProviderFixture(t, "github.com")
	fixtureRenameRegistry(t, c, "https://github.com/example/new-name.git")
	registry, _ := os.ReadFile(c.registryPath())
	fixtureFile(t, c.registryPath(), string(registry)+"  - name: bb-reference\n    remote: https://bitbucket.org/example/new-name.git\n    default_ref: origin/main\n", 0644)
	gh.put(t, "fail-graphql-42", "")
	gh.put(t, "detail", `{"id":43,"number":43,"state":"OPEN","title":"Independent forge"}`)
	t.Setenv("WTC_FORGE_MIN_INTERVAL", "60")
	before := "reference 42 topic https://github.com/example/old-name/pull/42 Rejected\nbb-reference 43 topic https://bitbucket.org/example/new-name/pull-requests/43 Healthy\nbb-reference 44 old https://bitbucket.org/example/old-name/pull-requests/44 Settled\n# merged-pr bb-reference 44 2026-10-29T12:00:00Z SUCCESS\n"
	fixtureFile(t, c.PRFile(), before, 0600)
	for n := 0; n < 2; n++ {
		if n > 0 {
			refillRenameBudget(t, "github.com")
			refillRenameBudget(t, "bitbucket.org")
		}
		snapshot := stagedRenameRefresh(t, c)
		if n == 1 && snapshot.PRs[1].State != "OPEN" {
			t.Fatalf("GitHub failure blocked independent Bitbucket detail: %+v", snapshot.PRs[1])
		}
	}
	if raw, _ := os.ReadFile(c.PRFile()); string(raw) != before {
		t.Fatal("unsupported Bitbucket rename or rejected GitHub metadata changed")
	}
}

func TestPRURLRepairPreservesUnicodeWhitespaceAndTitle(t *testing.T) {
	for _, space := range []string{"\u00a0", "\u2003", "\u202f", "\u0085", "\t"} {
		for _, mixed := range []bool{true, false} {
			t.Run(fmt.Sprintf("%x/mixed-%t", []rune(space)[0], mixed), func(t *testing.T) {
				c := newWorkspaceFixture(t)
				oldURL := "https://github.com/example/old-name/pull/42"
				newURL := "https://github.com/example/new-name/pull/42"
				before := "# header\r\n" + space + "reference" + space + "42" + space + "topic" + space + oldURL + space + "Owner " + oldURL + " title\r\n# tail\n"
				if mixed {
					before = "# header\r\nreference" + space + "42 topic " + oldURL + " Owner " + oldURL + " title\r\n# tail\n"
				}
				fixtureFile(t, c.PRFile(), before, 0600)
				record, ok := parsePR(strings.Split(before, "\n")[1])
				if !ok {
					t.Fatal("accepted row fixture rejected")
				}
				if _, changed, err := c.reconcilePRURL(record, newURL); err != nil || !changed {
					t.Fatalf("URL repair: %t %v", changed, err)
				}
				if raw, _ := os.ReadFile(c.PRFile()); string(raw) != strings.Replace(before, oldURL, newURL, 1) {
					t.Fatalf("URL-only byte preservation failed: %q", raw)
				}
			})
		}
	}
}

func TestAtomicOutOfRangeRowDoesNotBlockValidRepair(t *testing.T) {
	c, f, valid := stagedRenameFixture(t, "github.com")
	bad := "reference 2147483648 topic https://github.com/example/old-name/pull/2147483648 Out of range\n"
	fixtureFile(t, c.PRFile(), bad+valid, 0600)
	stagedRenameRefresh(t, c)
	if raw, _ := os.ReadFile(c.PRFile()); string(raw) != bad+strings.Replace(valid, "old-name", "new-name", 1) {
		t.Fatal("locally unrepairable row blocked a later valid repair")
	}
	if raw, _ := os.ReadFile(filepath.Join(f.dir, "dispatches")); string(raw) != "repair 42\n" {
		t.Fatalf("range preflight consumed network service: %s", raw)
	}
}

func TestProtectedDetailRetainsMergedHead(t *testing.T) {
	_, target, merge := squashFixture(t, true)
	head := fixtureGit(t, "-C", target.path, "rev-parse", "HEAD")
	c, f, _ := stagedRenameFixture(t, "github.com")
	f.put(t, "fail-graphql-42", "")
	fixtureFile(t, c.PRFile(), "reference 42 old https://github.com/example/old-name/pull/42 Bad repair\nreference 43 topic https://github.com/example/new-name/pull/43 Merged detail\n", 0600)
	providerGH := filepath.Join(strings.Split(os.Getenv("PATH"), string(os.PathListSeparator))[0], "gh")
	t.Setenv("HEAD_PROVIDER_GH", providerGH)
	t.Setenv("MERGED_FIXTURE_HEAD", head)
	t.Setenv("MERGED_FIXTURE_COMMIT", merge)
	bin := t.TempDir()
	fixtureFile(t, filepath.Join(bin, "gh"), `#!/bin/sh
if [ "$1 $2" != 'pr view' ]; then exec "$HEAD_PROVIDER_GH" "$@"; fi
head=''
case "$*" in *headRefOid*) head=",\"headRefOid\":\"$MERGED_FIXTURE_HEAD\"" ;; esac
printf '{"number":43,"state":"MERGED","mergedAt":"2026-10-29T12:00:00Z","mergeCommit":{"oid":"%s"},"statusCheckRollup":[{"status":"COMPLETED","conclusion":"SUCCESS"}]%s}\n' "$MERGED_FIXTURE_COMMIT" "$head"
`, 0755)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	stagedRenameRefresh(t, c)
	records, err := c.ListPRs()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 {
		t.Fatalf("unexpected records: %+v", records)
	}
	if records[1].MergeHead != head {
		t.Errorf("first protected settled observation lost merged head: %+v", records[1])
	}
	fixtureFile(t, filepath.Join(target.path, "followup.txt"), "later owner work\n", 0644)
	fixtureGit(t, "-C", target.path, "add", "followup.txt")
	fixtureGit(t, "-C", target.path, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "follow-up")
	details, err := c.statusEnrichRecords(records, nil)
	if err != nil {
		t.Fatal(err)
	}
	row, err := statusLocalRepo(target)
	if err != nil {
		t.Fatal(err)
	}
	if landing := statusMergedLanding(row, details[1]); landing.State != "follow-up" {
		t.Fatalf("recorded first observation cannot classify later follow-up: %+v", landing)
	}
}

func TestAtomicAmbiguousRepairsAndDuplicateOrdinaryJobsDoNotStarve(t *testing.T) {
	for _, lane := range []string{"repair", "ordinary"} {
		t.Run(lane, func(t *testing.T) {
			c, f, _ := stagedRenameFixture(t, "github.com")
			f.put(t, "base-43", renameBaseJSON("github.com", renameRepositoryJSON("github.com", "example/new-name", false), 43))
			f.put(t, "detail-43", `{"number":43,"state":"OPEN","title":"Healthy detail"}`)
			var before string
			if lane == "repair" {
				duplicate := "reference 42 old https://github.com/example/old-name/pull/42 Duplicate\n"
				before = duplicate + duplicate + "reference 43 old https://github.com/example/old-name/pull/43 Valid\n"
			} else {
				duplicate := "reference 42 old https://github.com/example/new-name/pull/42 Duplicate\n"
				before = duplicate + duplicate + "reference 43 live https://github.com/example/new-name/pull/43 Healthy\nreference 45 old https://github.com/example/old-name/pull/45 Unavailable\n"
				f.put(t, "fail-view-42", "")
				f.put(t, "fail-graphql-45", "")
			}
			fixtureFile(t, c.PRFile(), before, 0600)
			for cycle := 0; cycle < 4; cycle++ {
				if cycle > 0 {
					advanceRenameMinute(t)
				}
				stagedRenameRefresh(t, c)
			}
			after, _ := os.ReadFile(c.PRFile())
			calls, _ := os.ReadFile(filepath.Join(f.dir, "dispatches"))
			if lane == "repair" {
				if string(after) != strings.Replace(before, "old-name/pull/43", "new-name/pull/43", 1) {
					t.Fatalf("ambiguous duplicate repairs starved valid row: %s", calls)
				}
				if strings.Contains(string(calls), "repair 42\n") {
					t.Fatal("CAS-ambiguous repair was issued")
				}
			} else {
				if !strings.Contains(string(calls), "detail 43\n") {
					t.Fatalf("duplicate ordinary key starved healthy detail: %s", calls)
				}
				if string(after) != before {
					t.Fatal("scheduling deduplicated owner file")
				}
			}
		})
	}
}
