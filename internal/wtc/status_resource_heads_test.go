package wtc

import (
	"context"
	"crypto/sha1"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func publicationSnapshot(t *testing.T, c *Context) StatusSnapshot {
	t.Helper()
	row, err := statusResourceLocalReference(context.Background(), catchUpTarget{
		path: c.Harness, collection: filepath.Base(c.Collection), repo: "agent-harness", ref: "origin/main",
	})
	if err != nil {
		// An unborn HEAD has no counts. Keep the partial unknown input used by
		// the independent reader instead of assigning an invented commit.
		if _, headErr := statusGit(c.Harness, "rev-parse", "--verify", "HEAD"); headErr == nil {
			t.Fatal(err)
		}
		row = StatusRepo{Collection: filepath.Base(c.Collection), Dir: "harness", Repo: "agent-harness",
			Worktree: c.Harness, BranchKind: "branch", Branch: "unborn", BranchDisplay: "unborn", Tree: "clean", baseRef: "origin/main"}
	}
	return StatusSnapshot{Schema: 1, Collection: filepath.Base(c.Collection), GeneratedAt: "2000-01-01T00:00:00Z", Repos: []StatusRepo{row}}
}

func publicationBytes(t *testing.T, c *Context) map[string]string {
	t.Helper()
	files := map[string]string{}
	for _, name := range []string{".wtc-status.json", ".wtc-status.md", ".last-wtc-status.yml"} {
		raw, err := os.ReadFile(filepath.Join(c.Collection, name))
		if err != nil {
			t.Fatal(err)
		}
		files[name] = string(raw)
	}
	return files
}

func comparePublication(t *testing.T, c *Context, input StatusSnapshot) StatusSnapshot {
	t.Helper()
	want := input
	want.Repos = append([]StatusRepo(nil), input.Repos...)
	statusNextContext(context.Background(), &want)
	if err := c.WriteStatusSnapshotContext(context.Background(), want); err != nil {
		t.Fatal(err)
	}
	bytes := publicationBytes(t, c)
	got := input
	got.Repos = append([]StatusRepo(nil), input.Repos...)
	if err := c.statusFinalizePublication(context.Background(), &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(want, got) || !reflect.DeepEqual(bytes, publicationBytes(t, c)) {
		t.Fatalf("same-call publication changed snapshot or format bytes: want=%+v got=%+v", want, got)
	}
	return got
}

func TestStatusResourcePublicationWholeFormats(t *testing.T) {
	for _, state := range []string{"clean", "untracked", "staged", "dirty", "rename", "detached", "named-detached", "unpushed", "conflict", "unborn"} {
		t.Run(state, func(t *testing.T) {
			c := newWorkspaceFixture(t)
			git := func(args ...string) string { return fixtureGit(t, append([]string{"-C", c.Harness}, args...)...) }
			git("switch", "-qc", "topic")
			switch state {
			case "untracked", "staged":
				fixtureFile(t, filepath.Join(c.Harness, "new"), "Synthetic change\n", 0600)
				if state == "staged" {
					git("add", "new")
				}
			case "dirty":
				fixtureFile(t, filepath.Join(c.Harness, "README.md"), "Synthetic edit\n", 0600)
			case "rename":
				git("mv", "README.md", "renamed")
			case "detached":
				git("checkout", "-q", "--detach", "HEAD")
			case "named-detached":
				git("switch", "-qc", "(detached)")
			case "unpushed":
				git("-c", "user.name=Synthetic", "-c", "user.email=synthetic@example.invalid", "commit", "--allow-empty", "-qm", "Synthetic follow-up")
			case "conflict":
				git("switch", "-qc", "other")
				fixtureFile(t, filepath.Join(c.Harness, "README.md"), "other\n", 0600)
				git("add", ".")
				git("-c", "user.name=Synthetic", "-c", "user.email=synthetic@example.invalid", "commit", "-qm", "Synthetic side")
				git("switch", "-q", "topic")
				fixtureFile(t, filepath.Join(c.Harness, "README.md"), "topic\n", 0600)
				git("add", ".")
				git("-c", "user.name=Synthetic", "-c", "user.email=synthetic@example.invalid", "commit", "-qm", "Synthetic topic")
				if _, err := statusGit(c.Harness, "-c", "user.name=Synthetic", "-c", "user.email=synthetic@example.invalid", "merge", "other"); err == nil {
					t.Fatal("conflict fixture did not conflict")
				}
				if git("ls-files", "-u") == "" {
					t.Fatal("missing real unmerged entries")
				}
			case "unborn":
				git("switch", "--orphan", "unborn")
			}
			for _, abbrev := range []string{"4", "12", "auto", "no"} {
				t.Run(abbrev, func(t *testing.T) {
					git("config", "core.abbrev", abbrev)
					input := publicationSnapshot(t, c)
					comparePublication(t, c, input)
					input.Repos[0].baseRef = "origin/missing"
					comparePublication(t, c, input)
				})
			}
		})
	}
}

func TestStatusResourcePublicationAbbreviationCollision(t *testing.T) {
	c := newWorkspaceFixture(t)
	head := fixtureGit(t, "-C", c.Harness, "rev-parse", "HEAD")
	fixtureGit(t, "-C", c.Harness, "config", "core.abbrev", "4")
	found := ""
	for n := 0; n < 1000000; n++ {
		blob := fmt.Sprintf("Synthetic collision %d", n)
		digest := fmt.Sprintf("%x", sha1.Sum([]byte(fmt.Sprintf("blob %d\x00%s", len(blob), blob))))
		if digest[:4] == head[:4] && digest != head {
			found = blob
			break
		}
	}
	if found == "" {
		t.Fatal("bounded collision fixture exhausted")
	}
	cmd := exec.Command("git", "-C", c.Harness, "hash-object", "-w", "--stdin")
	cmd.Stdin = strings.NewReader(found)
	if raw, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("store collision: %v %s", err, raw)
	}
	row := publicationSnapshot(t, c).Repos[0]
	heads, err := statusReadPublicationHeads(context.Background(), row)
	if err != nil || len(heads.short) <= 4 || heads.short != fixtureGit(t, "-C", c.Harness, "rev-parse", "--short", "HEAD") {
		t.Fatalf("unique abbreviation changed: %+v %v", heads, err)
	}
	comparePublication(t, c, publicationSnapshot(t, c))
}

func TestStatusResourcePublicationParser(t *testing.T) {
	head, base := strings.Repeat("a", 40), strings.Repeat("b", 40)
	for _, raw := range []string{"", head, head + "\n" + base, head + "\n" + base + "\naaa", head + "\n" + base + "\nbbbb", head + "\n" + base + "\naaaa\nextra", "HEAD\n" + base + "\naaaa", strings.ToUpper(head) + "\n" + base + "\naaaa"} {
		if _, ok := statusParsePublicationHeads(raw); ok {
			t.Fatalf("accepted malformed batch %q", raw)
		}
	}
	if got, ok := statusParsePublicationHeads(head + "\n" + base + "\naaaa"); !ok || got.head != head || got.base != base || got.short != "aaaa" {
		t.Fatal("valid association lost")
	}
}

func TestStatusResourcePublicationCancelBeforeAndBetweenWrite(t *testing.T) {
	for _, between := range []bool{false, true} {
		t.Run(fmt.Sprint(between), func(t *testing.T) {
			c := newWorkspaceFixture(t)
			input := publicationSnapshot(t, c)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if between {
				c.StatusProgress = func(step string) {
					if step == "Writing status snapshot" {
						cancel()
					}
				}
			} else {
				cancel()
			}
			calls := resourceCountGit(t)
			if err := c.statusFinalizePublication(ctx, &input); !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation lost: %v", err)
			}
			count := resourceCalls(calls)
			if !between && count != 0 || between && count != 1 {
				t.Fatalf("unexpected dispatch count: %d", count)
			}
			time.Sleep(20 * time.Millisecond)
			if resourceCalls(calls) != count {
				t.Fatal("late dispatch after cancellation")
			}
			for _, name := range []string{".wtc-status.json", ".wtc-status.md", ".last-wtc-status.yml"} {
				if _, err := os.Stat(filepath.Join(c.Collection, name)); !os.IsNotExist(err) {
					t.Fatal("canceled observation published", name, err)
				}
			}
		})
	}
}

func publicationGitShim(t *testing.T, body string) string {
	t.Helper()
	real, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	calls := filepath.Join(bin, "calls")
	t.Setenv("PUBLICATION_REAL_GIT", real)
	t.Setenv("PUBLICATION_CALLS", calls)
	fixtureFile(t, filepath.Join(bin, "git"), "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$PUBLICATION_CALLS\"\n"+body+"\nexec \"$PUBLICATION_REAL_GIT\" \"$@\"\n", 0700)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return calls
}

func TestStatusResourcePublicationMalformedAndUnsupportedFallback(t *testing.T) {
	for _, mode := range []string{"unsupported", "empty", "malformed", "wrong-short"} {
		t.Run(mode, func(t *testing.T) {
			c := newWorkspaceFixture(t)
			input := publicationSnapshot(t, c)
			body := "exit 2"
			switch mode {
			case "empty":
				body = "exit 0"
			case "malformed":
				body = "printf 'malformed\\n'; exit 0"
			case "wrong-short":
				body = "printf '" + strings.Repeat("a", 40) + "\\n" + strings.Repeat("b", 40) + "\\nbbbb\\n'; exit 0"
			}
			calls := publicationGitShim(t, "if [ \"$#\" = 7 ] && [ \"$3 $4 $6\" = 'rev-parse HEAD --short' ]; then "+body+"; fi")
			comparePublication(t, c, input)
			raw, _ := os.ReadFile(calls)
			if strings.Count(string(raw), "origin/main --short HEAD") != 1 {
				t.Fatal("batch/fallback control did not exercise one probe")
			}
		})
	}
}

func TestStatusResourcePublicationHeldReadCancellation(t *testing.T) {
	for _, held := range []string{"probe", "fallback"} {
		for _, deadline := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/deadline=%t", held, deadline), func(t *testing.T) {
				c := newWorkspaceFixture(t)
				input := publicationSnapshot(t, c)
				entered := filepath.Join(t.TempDir(), "entered")
				t.Setenv("PUBLICATION_ENTERED", entered)
				body := `if [ "$#" = 7 ] && [ "$3 $4 $6" = 'rev-parse HEAD --short' ]; then
`
				if held == "fallback" {
					body += "exit 2\nfi\nif [ \"$3 $4\" = 'rev-parse HEAD' ]; then\n"
				}
				body += "printf entered > \"$PUBLICATION_ENTERED\"\nexec /bin/sleep 20\nfi\n"
				calls := publicationGitShim(t, body)
				ctx, cancel := context.WithCancel(context.Background())
				if deadline {
					cancel()
					ctx, cancel = context.WithTimeout(context.Background(), time.Second)
				}
				defer cancel()
				done := make(chan error, 1)
				go func() { done <- c.statusFinalizePublication(ctx, &input) }()
				until := time.Now().Add(3 * time.Second)
				for {
					if _, err := os.Stat(entered); err == nil {
						break
					}
					if time.Now().After(until) {
						t.Fatal("held child was not admitted")
					}
					time.Sleep(5 * time.Millisecond)
				}
				if !deadline {
					cancel()
				}
				select {
				case err := <-done:
					want := context.Canceled
					if deadline {
						want = context.DeadlineExceeded
					}
					if !errors.Is(err, want) {
						t.Fatalf("cancellation outcome: %v", err)
					}
				case <-time.After(2 * time.Second):
					t.Fatal("held read ignored caller bound")
				}
				raw, _ := os.ReadFile(calls)
				want := 1
				if held == "fallback" {
					want = 2
				}
				if strings.Count(string(raw), "\n") != want {
					t.Fatalf("post-timeout/cancel fallback dispatched: %s", raw)
				}
				time.Sleep(25 * time.Millisecond)
				after, _ := os.ReadFile(calls)
				if string(raw) != string(after) {
					t.Fatal("late publication read")
				}
				for _, name := range []string{".wtc-status.json", ".wtc-status.md", ".last-wtc-status.yml"} {
					if _, err := os.Stat(filepath.Join(c.Collection, name)); !os.IsNotExist(err) {
						t.Fatal("held canceled read published", name, err)
					}
				}
			})
		}
	}
}

func TestStatusResourcePublicationAssociationAndGuards(t *testing.T) {
	for _, mode := range []string{"scope", "retired", "branch", "kind", "dir", "worktree", "base", "repo", "collection", "late-scope", "late-retired"} {
		t.Run(mode, func(t *testing.T) {
			c := newWorkspaceFixture(t)
			input := publicationSnapshot(t, c)
			if mode == "scope" {
				input.Collection = "different"
			} else if mode == "retired" {
				fixtureFile(t, filepath.Join(c.Collection, ".wtc-retired"), "{}", 0600)
			} else {
				c.StatusProgress = func(step string) {
					if step != "Writing status snapshot" {
						return
					}
					switch mode {
					case "branch":
						input.Repos[0].Branch = "different"
					case "kind":
						if input.Repos[0].BranchKind == "detached" {
							input.Repos[0].BranchKind = "branch"
						} else {
							input.Repos[0].BranchKind = "detached"
						}
					case "dir":
						input.Repos[0].Dir = "different"
					case "worktree":
						input.Repos[0].Worktree = t.TempDir()
					case "base":
						input.Repos[0].baseRef = "origin/different"
					case "repo":
						input.Repos[0].Repo = "different"
					case "collection":
						input.Repos[0].Collection = "different"
					case "late-scope":
						input.Collection = "different"
					case "late-retired":
						fixtureFile(t, filepath.Join(c.Collection, ".wtc-retired"), "{}", 0600)
					}
				}
			}
			calls := resourceCountGit(t)
			if err := c.statusFinalizePublication(context.Background(), &input); err == nil {
				t.Fatal("changed association or guard published")
			}
			want := 1
			if mode == "scope" || mode == "retired" {
				want = 0
			}
			if resourceCalls(calls) != want {
				t.Fatal("rejected association dispatched extra reads")
			}
			for _, name := range []string{".wtc-status.json", ".wtc-status.md", ".last-wtc-status.yml"} {
				if _, err := os.Stat(filepath.Join(c.Collection, name)); !os.IsNotExist(err) {
					t.Fatal("rejected publication wrote", name, err)
				}
			}
		})
	}
}

func TestStatusResourcePublicationSHA256(t *testing.T) {
	t.Setenv("PATH", fixtureSystemPath(t))
	collection := filepath.Join(t.TempDir(), "fixture")
	repo := filepath.Join(collection, "harness")
	if raw, err := exec.Command("git", "init", "-q", "--object-format=sha256", "-b", "main", repo).CombinedOutput(); err != nil {
		if strings.Contains(string(raw), "unknown option") || strings.Contains(string(raw), "unknown hash algorithm") || strings.Contains(string(raw), "not supported") {
			t.Skipf("Git SHA256 repositories unavailable: %v %s", err, raw)
		}
		t.Fatalf("SHA256 fixture setup: %v %s", err, raw)
	}
	fixtureFile(t, filepath.Join(repo, ".harness-repos.yml"), "repos:\n  - name: tools\n    remote: https://example.invalid/tools.git\n    default_ref: origin/main\n", 0600)
	fixtureGit(t, "-C", repo, "remote", "add", "origin", "https://example.invalid/tools.git")
	fixtureGit(t, "-C", repo, "add", ".")
	fixtureGit(t, "-C", repo, "-c", "user.name=Synthetic", "-c", "user.email=synthetic@example.invalid", "commit", "-qm", "Synthetic SHA256 registry")
	fixtureGit(t, "-C", repo, "update-ref", "refs/remotes/origin/main", "HEAD")
	c, err := OpenCollection(collection)
	if err != nil {
		t.Fatal(err)
	}
	for _, abbrev := range []string{"4", "12", "no"} {
		fixtureGit(t, "-C", repo, "config", "core.abbrev", abbrev)
		input, err := c.StatusLocalSnapshot(false)
		if err != nil || len(input.Repos) != 1 {
			t.Fatalf("SHA256 local input: %+v %v", input, err)
		}
		heads, err := statusReadPublicationHeads(context.Background(), input.Repos[0])
		if err != nil || len(heads.head) != 64 || len(heads.base) != 64 || heads.short != fixtureGit(t, "-C", repo, "rev-parse", "--short", "HEAD") {
			t.Fatalf("SHA256 association/abbreviation: %+v %v", heads, err)
		}
		comparePublication(t, c, input)
	}
}

func TestStatusResourcePublicationIneligibleRowsKeepLegacyReads(t *testing.T) {
	for _, mode := range []string{"review-only", "unconfirmed", "unavailable"} {
		t.Run(mode, func(t *testing.T) {
			c := newWorkspaceFixture(t)
			input := publicationSnapshot(t, c)
			switch mode {
			case "review-only":
				input.Repos[0].ReviewOnly = true
			case "unconfirmed":
				input.Repos[0].PRUnconfirmed = true
			case "unavailable":
				input.Repos[0].PR = &StatusPRFacts{Number: "7", Checks: "FAILURE", Freshness: "unavailable"}
			}
			got := comparePublication(t, c, input)
			if got.Repos[0].Next != nil {
				t.Fatal("ineligible row granted an action")
			}
		})
	}
}

func TestStatusResourcePublicationDoesNotCarryObservationsAcrossCalls(t *testing.T) {
	c := newWorkspaceFixture(t).WithStatusContext(context.Background())
	fixtureGit(t, "-C", c.Harness, "switch", "-qc", "topic")
	fixtureGit(t, "-C", c.Harness, "-c", "user.name=Synthetic", "-c", "user.email=synthetic@example.invalid", "commit", "--allow-empty", "-qm", "Synthetic unpublished work")
	input := publicationSnapshot(t, c)
	oldShort := fixtureGit(t, "-C", c.Harness, "rev-parse", "--short", "HEAD")
	changed := false
	c.StatusProgress = func(step string) {
		if step == "Writing status snapshot" {
			fixtureGit(t, "-C", c.Harness, "-c", "user.name=Synthetic", "-c", "user.email=synthetic@example.invalid", "commit", "--allow-empty", "-qm", "Synthetic concurrent work")
			changed = true
		}
	}
	if err := c.statusFinalizePublication(context.Background(), &input); err != nil {
		t.Fatal(err)
	}
	if !changed || !strings.Contains(publicationBytes(t, c)[".last-wtc-status.yml"], "head: '"+oldShort+"'") {
		t.Fatal("same-call legacy bytes lost their final action-head association")
	}
	oldChoices := input.Repos[0].Choices
	newShort := fixtureGit(t, "-C", c.Harness, "rev-parse", "--short", "HEAD")
	c.StatusProgress = nil
	if err := c.WriteStatusSnapshotContext(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(publicationBytes(t, c)[".last-wtc-status.yml"], "head: '"+newShort+"'") {
		t.Fatal("independent writer reused a prior observation")
	}
	if err := c.statusFinalizePublication(context.Background(), &input); err != nil {
		t.Fatal(err)
	}
	if reflect.DeepEqual(oldChoices, input.Repos[0].Choices) || !strings.Contains(publicationBytes(t, c)[".last-wtc-status.yml"], "head: '"+newShort+"'") {
		t.Fatal("later finalization retained prior mutable facts")
	}
}

func TestStatusResourcePublicationFreshAfterHeldProvider(t *testing.T) {
	c := newWorkspaceFixture(t)
	fixtureGit(t, "-C", c.Harness, "switch", "-qc", "topic")
	entered, release := filepath.Join(t.TempDir(), "entered"), filepath.Join(t.TempDir(), "release")
	t.Setenv("PUBLICATION_HOOK_ENTERED", entered)
	t.Setenv("PUBLICATION_HOOK_RELEASE", release)
	fixtureFile(t, filepath.Join(c.Harness, "hooks", "wtc", "status.build.sh"), `#!/bin/sh
cat >/dev/null
printf entered > "$PUBLICATION_HOOK_ENTERED"
while [ ! -f "$PUBLICATION_HOOK_RELEASE" ]; do sleep .01; done
printf '{"checks":"SUCCESS"}\n'
`, 0700)
	fixtureGit(t, "-C", c.Harness, "add", ".")
	fixtureGit(t, "-C", c.Harness, "-c", "user.name=Synthetic", "-c", "user.email=synthetic@example.invalid", "commit", "-qm", "Synthetic provider")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	t.Cleanup(func() { _ = os.WriteFile(release, nil, 0600) })
	type outcome struct {
		snapshot StatusSnapshot
		err      error
	}
	done := make(chan outcome, 1)
	go func() {
		snapshot, _, err := c.WithStatusContext(ctx).StatusLiveSnapshot(false, true)
		done <- outcome{snapshot, err}
	}()
	until := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(entered); err == nil {
			break
		}
		if time.Now().After(until) {
			t.Fatal("provider phase did not enter")
		}
		time.Sleep(5 * time.Millisecond)
	}
	fixtureGit(t, "-C", c.Harness, "-c", "user.name=Synthetic", "-c", "user.email=synthetic@example.invalid", "commit", "--allow-empty", "-qm", "Synthetic head during provider read")
	short := fixtureGit(t, "-C", c.Harness, "rev-parse", "--short", "HEAD")
	fixtureFile(t, release, "", 0600)
	select {
	case got := <-done:
		if got.err != nil || len(got.snapshot.Repos) != 1 {
			t.Fatalf("provider completion: %+v %v", got.snapshot, got.err)
		}
		reference := got.snapshot
		reference.Repos = append([]StatusRepo(nil), got.snapshot.Repos...)
		statusNextContext(context.Background(), &reference)
		if !reflect.DeepEqual(reference.Repos[0].Choices, got.snapshot.Repos[0].Choices) {
			t.Fatal("final choices used the head before provider completion")
		}
		if !strings.Contains(publicationBytes(t, c)[".last-wtc-status.yml"], "head: '"+short+"'") {
			t.Fatal("legacy head did not come from the final observation")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("released provider did not complete")
	}
}

func TestStatusResourcePublicationCancellationRetainsCommittedMerge(t *testing.T) {
	resourceCancellationFixture(t)
	c := newWorkspaceFixture(t)
	input := publicationSnapshot(t, c)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	confirmed := []byte(`{"number":7,"state":"MERGED","title":"Confirmed"}`)
	if !statusRetainMergeObservation("github.com", "example/widget", "7", confirmed, time.Now()) {
		t.Fatal("confirmed merge not committed")
	}
	c.StatusProgress = func(step string) {
		if step == "Writing status snapshot" {
			cancel()
		}
	}
	if err := c.statusFinalizePublication(ctx, &input); !errors.Is(err, context.Canceled) {
		t.Fatalf("publication outcome: %v", err)
	}
	got, known := statusReadObservedDetail(PRRecord{Number: "7"}, "example/widget", "github.com", time.Hour)
	if !known || got.State != "MERGED" || got.Title != "Confirmed" {
		t.Fatalf("committed terminal observation lost: %+v", got)
	}
	if statusWriteForgeEntrySince("pr", "github.com", "example/widget", "7", []byte(`{"number":7,"state":"OPEN"}`), time.Now()) {
		t.Fatal("compatibility consumer reopened retained merge")
	}
}
