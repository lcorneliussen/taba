package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"taba.sh/taba/internal/wtc"
)

func openReviewTestGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Example", "GIT_AUTHOR_EMAIL=example@example.invalid", "GIT_COMMITTER_NAME=Example", "GIT_COMMITTER_EMAIL=example@example.invalid")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func openReviewTestRepo(t *testing.T, c *wtc.Context) (dir, base, head string) {
	t.Helper()
	dir = filepath.Join(c.Collection, "example")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	openReviewTestGit(t, dir, "init", "-b", "feature")
	openReviewTestGit(t, dir, "remote", "add", "origin", "https://github.com/example/example.git")
	openReviewTestGit(t, dir, "commit", "--allow-empty", "-m", "base")
	base = openReviewTestGit(t, dir, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(dir, "change.txt"), []byte("pinned change\n"), 0644); err != nil {
		t.Fatal(err)
	}
	openReviewTestGit(t, dir, "add", "change.txt")
	openReviewTestGit(t, dir, "commit", "-m", "change")
	head = openReviewTestGit(t, dir, "rev-parse", "HEAD")
	return
}

func openReviewTestBundle(t *testing.T, c *wtc.Context, name, base, head string) string {
	t.Helper()
	return openReviewTestManifest(t, c, name, wtc.ReviewManifest{Repo: "example", Collection: "sample", Forge: "github", Slug: "example/example", HeadBranch: "feature", BaseSHA: base, HeadSHA: head, Round: 1, RepoDir: "/untrusted/ignored"})
}
func openReviewTestManifest(t *testing.T, c *wtc.Context, name string, m wtc.ReviewManifest) string {
	t.Helper()
	dir := filepath.Join(c.Collection, ".wtc-reviews", name)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(m)
	path := filepath.Join(dir, "manifest.json")
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestOpenReviewSelectsLatestApplicablePinnedBundle(t *testing.T) {
	c, _, _, _, _ := openTestFixture(t)
	dir, base, head := openReviewTestRepo(t, c)
	older := openReviewTestBundle(t, c, "older", base, base)
	if err := os.Chtimes(older, time.Unix(1, 0), time.Unix(1, 0)); err != nil {
		t.Fatal(err)
	}
	openReviewTestBundle(t, c, "latest", base, head)
	// The reviewed head stays pinned even after a commit and dirty edits.
	openReviewTestGit(t, dir, "commit", "--allow-empty", "-m", "later")
	os.WriteFile(filepath.Join(dir, "change.txt"), []byte("unreviewed working tree\n"), 0644)
	for _, m := range []wtc.ReviewManifest{
		{Repo: "example", Collection: "foreign", Forge: "github", Slug: "example/example", HeadBranch: "feature", BaseSHA: base, HeadSHA: base},
		{Repo: "example", Collection: "sample", Forge: "github", Slug: "example/example", HeadBranch: "unrelated", BaseSHA: base, HeadSHA: base},
		{Repo: "../outside", Collection: "sample", HeadBranch: "feature", BaseSHA: base, HeadSHA: base},
		{Repo: "example", Collection: "sample", Forge: "github", Slug: "foreign/example", HeadBranch: "feature", BaseSHA: base, HeadSHA: base},
	} {
		openReviewTestManifest(t, c, strings.ReplaceAll(m.Repo+m.Collection+m.Slug+m.HeadBranch, "/", "_"), m)
	}
	malformed := openReviewTestBundle(t, c, "malformed", base, base)
	os.WriteFile(malformed, []byte("not json"), 0644)
	r, found, err := openReviewBundleRange(c)
	if err != nil || !found || r.dir != dir || r.base != base || r.head != head || r.source != "bundle latest" {
		t.Fatalf("wrong bundle: %+v %v %v", r, found, err)
	}
	c.Registry.Repos[0].Remote = "https://github.com/Example/Example.git"
	if _, found, err := openReviewBundleRange(c); err != nil || !found {
		t.Fatalf("canonical bundle rejected for mixed-case remote: %v %v", found, err)
	}
	c.Registry.Repos[0].Remote = "https://github.com/example/example.git"
	// An applicable newest bundle with missing objects fails instead of hiding
	// the problem behind an older bundle or an unrelated local range.
	openReviewTestBundle(t, c, "missing", base, strings.Repeat("a", 40))
	_, found, err = openReviewBundleRange(c)
	if !found || err == nil || !strings.Contains(err.Error(), "unavailable locally") {
		t.Fatalf("missing objects silently ignored: %v %v", found, err)
	}
}

func TestOpenReviewRejectsInvalidPinnedRanges(t *testing.T) {
	c, _, _, _, _ := openTestFixture(t)
	dir, base, head := openReviewTestRepo(t, c)
	blob := openReviewTestGit(t, dir, "rev-parse", head+":change.txt")
	for _, sha := range []string{"HEAD", "--help", "$(touch unsafe)", head[:12], blob} {
		if _, err := openReviewPinned("example", dir, base, sha, "test"); err == nil {
			t.Fatalf("accepted invalid commit %q", sha)
		}
	}
	// Compute the three-dot merge base, rather than comparing diverged tips.
	openReviewTestGit(t, dir, "switch", "-c", "base-moved", base)
	openReviewTestGit(t, dir, "commit", "--allow-empty", "-m", "base moved")
	tip := openReviewTestGit(t, dir, "rev-parse", "HEAD")
	r, err := openReviewPinned("example", dir, tip, head, "test")
	if err != nil || r.base != base {
		t.Fatalf("wrong merge base: %+v %v", r, err)
	}
}

func TestOpenReviewPRFallbackUsesExactRemoteIdentity(t *testing.T) {
	for _, forge := range []string{"github", "bitbucket"} {
		t.Run(forge, func(t *testing.T) {
			c, _, bin, _, _ := openTestFixture(t)
			dir, base, head := openReviewTestRepo(t, c)
			if forge == "bitbucket" {
				c.Registry.Repos[0].Remote = "https://bitbucket.org/example/example.git"
			}
			openReviewTestGit(t, dir, "commit", "--allow-empty", "-m", "unpushed")
			pr := map[string]any{"number": 7, "state": "OPEN", "headRefName": "feature", "headRefOid": head, "baseRefOid": base, "headRepository": map[string]string{"nameWithOwner": "example/example"}}
			if forge == "bitbucket" {
				endpoint := func(sha string) map[string]any {
					return map[string]any{"branch": map[string]string{"name": "feature"}, "commit": map[string]string{"hash": sha}, "repository": map[string]string{"full_name": "example/example"}}
				}
				pr = map[string]any{"id": 7, "state": "OPEN", "source": endpoint(head), "destination": endpoint(base), "description": strings.Repeat("synthetic ", 20000)}
			}
			cli := "gh"
			if forge == "bitbucket" {
				cli = "bb"
			}
			dataPath := filepath.Join(bin, "pr.json")
			write := func(v any) {
				t.Helper()
				if forge == "bitbucket" {
					if list, ok := v.([]any); ok {
						v = map[string]any{"pullRequests": list}
					}
				}
				data, _ := json.Marshal(v)
				if err := os.WriteFile(dataPath, data, 0644); err != nil {
					t.Fatal(err)
				}
			}
			script := "#!/bin/sh\ncat " + shellQuote(dataPath) + "\n"
			os.WriteFile(filepath.Join(bin, cli), []byte(script), 0755)
			write([]any{pr})
			r, err := openReviewPRRange(c)
			if err != nil || r.head != head || r.base != base || r.source != "PR #7" {
				t.Fatalf("wrong PR fallback: %+v %v", r, err)
			}
			// Repository casing does not change forge identity.
			originalRemote := c.Registry.Repos[0].Remote
			c.Registry.Repos[0].Remote = strings.ReplaceAll(originalRemote, "example/example", "Example/Example")
			if r, err := openReviewPRRange(c); err != nil || r.head != head {
				t.Fatalf("canonical PR rejected for mixed-case remote: %+v %v", r, err)
			}
			c.Registry.Repos[0].Remote = originalRemote
			if forge == "bitbucket" {
				for name, payload := range map[string]any{
					"array":        []any{pr},
					"values":       map[string]any{"values": []any{pr}},
					"pullRequests": map[string]any{"pullRequests": []any{pr}},
				} {
					data, _ := json.Marshal(payload)
					if err := os.WriteFile(dataPath, data, 0644); err != nil {
						t.Fatal(err)
					}
					if r, err := openReviewPRRange(c); err != nil || r.head != head {
						t.Fatalf("%s response failed: %+v %v", name, r, err)
					}
				}
				capped := make([]any, 100)
				for i := range capped {
					capped[i] = pr
				}
				write(capped)
				if _, err := openReviewPRRange(c); err == nil || !strings.Contains(err.Error(), "reached its limit") {
					t.Fatalf("incomplete PR list accepted: %v", err)
				}
			}
			// Multiple same-name branches must not select an arbitrary PR.
			write([]any{pr, pr})
			if _, err := openReviewPRRange(c); err == nil || !strings.Contains(err.Error(), "multiple") {
				t.Fatalf("ambiguous PR accepted: %v", err)
			}
			if forge == "github" {
				pr["headRepository"] = map[string]string{"nameWithOwner": "foreign/example"}
			} else {
				pr["source"].(map[string]any)["repository"] = map[string]string{"full_name": "foreign/example"}
			}
			write([]any{pr})
			if _, err := openReviewPRRange(c); err == nil {
				t.Fatal("foreign same-name branch accepted")
			}
			if forge == "github" {
				pr["headRepository"] = map[string]string{"nameWithOwner": "example/example"}
				pr["headRefOid"] = strings.Repeat("a", 40)
			} else {
				pr["source"].(map[string]any)["repository"] = map[string]string{"full_name": "example/example"}
				pr["source"].(map[string]any)["commit"] = map[string]string{"hash": strings.Repeat("a", 40)}
			}
			write([]any{pr})
			if _, err := openReviewPRRange(c); err == nil || !strings.Contains(err.Error(), "unavailable locally") {
				t.Fatalf("missing remote head silently replaced: %v", err)
			}
			if forge == "github" {
				pr["headRefOid"] = head
			} else {
				pr["source"].(map[string]any)["commit"] = map[string]string{"hash": head}
			}
			// Enlistment is checked against the remote branch and number too.
			os.WriteFile(c.PRFile(), []byte("example 7 feature\n"), 0644)
			write(pr)
			if r, err := openReviewPRRange(c); err != nil || r.head != head {
				t.Fatalf("enlisted PR failed: %+v %v", r, err)
			}
			if forge == "github" {
				pr["number"] = 8
			} else {
				pr["id"] = 8
			}
			write(pr)
			if _, err := openReviewPRRange(c); err == nil {
				t.Fatal("mismatched enlisted PR accepted")
			}
		})
	}
}

func TestOpenReviewViewersShowOnlyPinnedChanges(t *testing.T) {
	for _, hunk := range []bool{false, true} {
		t.Run(map[bool]string{false: "less", true: "hunk"}[hunk], func(t *testing.T) {
			c, _, bin, _, _ := openTestFixture(t)
			dir, base, head := openReviewTestRepo(t, c)
			openReviewTestBundle(t, c, "bundle", base, head)
			// Limit PATH to synthetic viewer binaries and the Git binary; this tests
			// availability independently of tools installed on the host.
			git, _ := exec.LookPath("git")
			os.Symlink(git, filepath.Join(bin, "git"))
			os.WriteFile(filepath.Join(bin, "less"), []byte("#!/bin/sh\n/bin/cat\n"), 0755)
			output := filepath.Join(bin, "viewer-args")
			if hunk {
				os.WriteFile(filepath.Join(bin, "hunk"), []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > "+shellQuote(output)+"\n"), 0755)
			}
			t.Setenv("PATH", bin)
			os.WriteFile(filepath.Join(dir, "change.txt"), []byte("dirty secret\n"), 0644)
			hook := filepath.Join(bin, "external-called")
			os.WriteFile(filepath.Join(dir, ".gitattributes"), []byte("*.txt diff=custom\n"), 0644)
			openReviewTestGit(t, dir, "config", "diff.custom.textconv", "touch "+hook)
			t.Setenv("GIT_EXTERNAL_DIFF", "touch "+hook)
			command, detail, err := openReviewCommand(c)
			if err != nil || !strings.Contains(detail, base+".."+head) {
				t.Fatalf("viewer command: %s %s %v", command, detail, err)
			}
			cmd := exec.Command("/bin/sh", "-c", command)
			cmd.Dir = c.Collection
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("viewer failed: %v %s", err, out)
			}
			if hunk {
				args, _ := os.ReadFile(output)
				if string(args) != "diff\n"+base+"\n"+head+"\n" {
					t.Fatalf("wrong hunk endpoints: %s", args)
				}
			} else if !strings.Contains(string(out), "+pinned change") || strings.Contains(string(out), "dirty secret") {
				t.Fatalf("wrong diff: %s", out)
			}
			if _, err := os.Stat(hook); !os.IsNotExist(err) {
				t.Fatal("external diff or textconv executed")
			}
		})
	}
}

func TestOpenReviewPaneLifecycle(t *testing.T) {
	for _, layout := range []string{"wide", "narrow"} {
		t.Run(layout, func(t *testing.T) {
			c, _, bin, log, _ := openTestFixture(t)
			if err := os.WriteFile(filepath.Join(bin, "less"), []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
				t.Fatal(err)
			}
			_, base, head := openReviewTestRepo(t, c)
			openReviewTestBundle(t, c, "bundle", base, head)
			opt := openOptions{Session: "test", NoAgent: true, NoBrowse: true, NoStatus: true, Tools: []string{"review", "shell", "browse"}}
			opened := openCollection(c, "sample", opt, layout, true, true)
			if opened.Error != "" {
				t.Fatalf("open failed: %+v", opened)
			}
			check := func(want string) {
				t.Helper()
				got, panes, err := openLayoutState("test", "ws1")
				if err != nil || got != want || len(panes) != 5 {
					t.Fatalf("wrong layout %s, %v: %+v", got, err, panes)
				}
				state := openTestState(t)
				agent, review, browse, shell, status := openTestPane(t, state, "agent"), openTestPane(t, state, "review"), openTestPane(t, state, "browse"), openTestPane(t, state, "shell"), openTestPane(t, state, "status")
				if want == "wide" {
					if review.X <= agent.X || review.X != browse.X || review.X != status.X || review.Y <= browse.Y || review.Y >= status.Y {
						t.Fatalf("review outside right column: %s", state)
					}
				} else if review.TabLabel != "tools" || review.Y <= browse.Y || review.Y >= shell.Y {
					t.Fatalf("review outside tools stack: %s", state)
				}
			}
			check(layout)
			before, _ := os.ReadFile(log)
			if again := openCollection(c, "sample", opt, layout, true, true); again.Error != "" {
				t.Fatalf("rerun failed: %+v", again)
			}
			after, _ := os.ReadFile(log)
			if strings.Count(string(after), "pane run ") != strings.Count(string(before), "pane run ") || strings.Count(string(after), "pane split ") != strings.Count(string(before), "pane split ") {
				t.Fatal("rerun restarted or duplicated panes")
			}
			opt.LayoutSet = true
			opt.Tools = nil
			for _, desired := range []string{"narrow", "wide", "narrow"} {
				if item := openCollection(c, "sample", opt, desired, true, true); item.Error != "" {
					t.Fatalf("switch failed: %+v", item)
				}
				check(desired)
			}
			after, _ = os.ReadFile(log)
			if strings.Contains(string(after), "pane close ") || strings.Count(string(after), "pane run ") != strings.Count(string(before), "pane run ") {
				t.Fatal("layout switch killed or restarted a viewer")
			}
			// A running viewer is busy; only an unfocused returned prompt is idle.
			trim := openOptions{Session: "test", Trim: true, DryRun: true}
			item := openTrimCollection(c, "sample", trim)
			if !strings.Contains(strings.Join(item.Actions, " "), "review kept (running") {
				t.Fatalf("running viewer trimmed: %+v", item)
			}
			openTestEditPanes(t, func(label string, p map[string]any) { p["focused"] = label == "review" }, map[string][]string{"review": {"-zsh"}})
			item = openTrimCollection(c, "sample", trim)
			if !strings.Contains(strings.Join(item.Actions, " "), "review kept (focused)") {
				t.Fatalf("focused viewer trimmed: %+v", item)
			}
			openTestEditPanes(t, func(_ string, p map[string]any) { p["focused"] = false }, nil)
			item = openTrimCollection(c, "sample", trim)
			if !strings.Contains(strings.Join(item.Actions, " "), "would close review") {
				t.Fatalf("idle review not trimmed: %+v", item)
			}
			trim.DryRun = false
			item = openTrimCollection(c, "sample", trim)
			if item.Error != "" || openTestCount(t, openTestState(t), "review") != 0 || openTestCount(t, openTestState(t), "agent") != 1 || openTestCount(t, openTestState(t), "status") != 1 {
				t.Fatalf("wrong trim: %+v", item)
			}
		})
	}
}

func TestOpenReviewRemainsOptIn(t *testing.T) {
	if got := strings.Join(openWantedTools(true, nil), ","); got != "browse,shell" {
		t.Fatalf("full changed: %s", got)
	}
	if got := openWantedTools(false, nil); len(got) != 0 {
		t.Fatalf("default changed: %v", got)
	}
	if got := strings.Join(openWantedTools(true, []string{"review", "review"}), ","); got != "browse,shell,review" {
		t.Fatalf("wrong requested tools: %s", got)
	}
}

func TestOpenReviewQuotesViewerDirectory(t *testing.T) {
	c, _, bin, _, _ := openTestFixture(t)
	name := "a' $(touch should-not-exist)"
	dir := filepath.Join(c.Collection, name)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(bin, "cwd")
	os.WriteFile(filepath.Join(bin, "hunk"), []byte("#!/bin/sh\npwd > "+shellQuote(output)+"\n"), 0755)
	r := openReviewRange{repo: "example", dir: dir, base: strings.Repeat("a", 40), head: strings.Repeat("b", 40), source: "test"}
	command, _, err := openReviewViewer(c, r)
	if err != nil {
		t.Fatal(err)
	}
	// Running twice verifies that cd does not persist in the pane's shell.
	cmd := exec.Command("/bin/sh", "-c", command+"; "+command)
	cmd.Dir = c.Collection
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("quoted command failed: %v %s", err, out)
	}
	data, _ := os.ReadFile(output)
	if strings.TrimSpace(string(data)) != dir {
		t.Fatalf("wrong directory: %s", data)
	}
	if _, err := os.Stat(filepath.Join(c.Collection, "should-not-exist")); !os.IsNotExist(err) {
		t.Fatal("directory name was executed")
	}
}

func TestOpenReviewRejectsSymlinkedMetadataAndSibling(t *testing.T) {
	c, _, _, _, _ := openTestFixture(t)
	dir, base, head := openReviewTestRepo(t, c)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(c.Collection, ".wtc-reviews")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := openReviewBundleRange(c); err == nil {
		t.Fatal("followed external bundle root")
	}
	os.Remove(filepath.Join(c.Collection, ".wtc-reviews"))
	manifest := openReviewTestBundle(t, c, "bundle", base, head)
	data, _ := os.ReadFile(manifest)
	external := filepath.Join(outside, "manifest.json")
	os.WriteFile(external, data, 0644)
	os.Remove(manifest)
	os.Symlink(external, manifest)
	if _, found, err := openReviewBundleRange(c); found || err != nil {
		t.Fatalf("followed external manifest: %v %v", found, err)
	}
	os.Rename(dir, filepath.Join(outside, "example"))
	os.Symlink(filepath.Join(outside, "example"), dir)
	if _, _, _, err := openReviewRepo(c, "example"); err == nil {
		t.Fatal("followed external sibling")
	}
}

func TestOpenReviewResolvesHarnessAliasAndRejectsMultipleSiblingBundles(t *testing.T) {
	c, _, _, _, _ := openTestFixture(t)
	dir, base, head := openReviewTestRepo(t, c)
	c.Config.Harness.Name = "example"
	// The registered harness name maps to harness/, never a manifest path.
	os.RemoveAll(c.Harness)
	if err := os.Rename(dir, c.Harness); err != nil {
		t.Fatal(err)
	}
	openReviewTestBundle(t, c, "harness", base, head)
	r, found, err := openReviewBundleRange(c)
	if err != nil || !found || r.dir != c.Harness {
		t.Fatalf("harness alias not resolved: %+v %v %v", r, found, err)
	}
	c.Config.Harness.Name = ""
	openReviewTestRepo(t, c)
	openReviewTestBundle(t, c, "app", base, head)
	openReviewTestManifest(t, c, "harness", wtc.ReviewManifest{Repo: "harness", Collection: "sample", Forge: "github", Slug: "example/example", HeadBranch: "feature", BaseSHA: base, HeadSHA: head})
	if _, _, err := openReviewBundleRange(c); err == nil || !strings.Contains(err.Error(), "multiple siblings") {
		t.Fatalf("ambiguous bundles accepted: %v", err)
	}
}

func TestOpenIdleReviewRequiresExplicitRequest(t *testing.T) {
	c, _, _, log, _ := openTestFixture(t)
	if err := openCreateWorkspace(c, "test", "sample"); err != nil {
		t.Fatal(err)
	}
	panes := []openPaneInfo{{Label: "review", ID: "p1"}}
	opt := openOptions{Session: "test", NoAgent: true, NoStatus: true}
	item := openItem{}
	if err := openPlanPanes(&item, panes, opt); err != nil || len(item.Actions) != 0 {
		t.Fatalf("unrequested idle review planned: %+v %v", item, err)
	}
	if err := openStartPanes(&item, c, panes, opt); err != nil {
		t.Fatalf("plain open failed on idle optional review: %v", err)
	}
	if !strings.Contains(strings.Join(item.Actions, " "), "use --add review to restart") {
		t.Fatalf("idle state unexplained: %+v", item)
	}
	calls, _ := os.ReadFile(log)
	if strings.Contains(string(calls), "pane run ") {
		t.Fatalf("unrequested viewer restarted: %s", calls)
	}
	opt.Tools = []string{"review"}
	if err := openStartPanes(&item, c, panes, opt); err == nil || !strings.Contains(err.Error(), "no applicable review bundle or current PR") {
		t.Fatalf("explicit review request lost its error: %v", err)
	}
}

func TestOpenReviewPicksTheNewestBundleAcrossBothRoots(t *testing.T) {
	c, _, _, _, _ := openTestFixture(t)
	_, base, head := openReviewTestRepo(t, c)
	legacy := openReviewTestBundle(t, c, "legacy", base, base)
	if err := os.Chtimes(legacy, time.Unix(1, 0), time.Unix(1, 0)); err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(c.Collection, ".taba-reviews", "alternate")
	if err := os.MkdirAll(filepath.Dir(moved), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Dir(openReviewTestBundle(t, c, "alternate", base, head)), moved); err != nil {
		t.Fatal(err)
	}
	r, found, err := openReviewBundleRange(c)
	if err != nil || !found || !strings.Contains(r.source, "alternate") {
		t.Fatalf("newest bundle not selected: %+v %v %v", r, found, err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(c.Collection, "elsewhere")); err != nil {
		t.Fatal(err)
	}
	os.RemoveAll(filepath.Dir(moved))
	if err := os.Rename(filepath.Join(c.Collection, "elsewhere"), filepath.Dir(moved)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := openReviewBundleRange(c); err == nil {
		t.Fatal("followed an external alternate bundle root")
	}
}
