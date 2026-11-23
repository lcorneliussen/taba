package wtc

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The sentinel launcher observes its own process boundary and reports it in
// the outputs it may write (a concern's findings, the lead's summary). It does
// not echo its arguments as evidence: it walks its working directory's
// ancestors, reads its environment and tries to change the source and bundle
// it was given.
const reviewSentinelLauncher = `#!/bin/sh
bundle="$4"
id=$(basename "$3" .md)
repo=$(sed -n 's/^- ` + "`" + `\(.*\)` + "`" + ` is read-only.*/\1/p' "$3" | head -n 1)
markers=""
dir=$(pwd -P)
while :; do
  for m in AGENTS.md CLAUDE.md .claude .mcp.json .codex; do
    [ -e "$dir/$m" ] && markers="$markers $dir/$m"
  done
  [ "$dir" = / ] && break
  dir=$(dirname "$dir")
done
target_rule=absent
grep -q TARGET-RULE-SENTINEL "$bundle"/concerns/*.md 2>/dev/null && target_rule=present
poster=absent
[ -e "$bundle/comment.id" ] && poster=visible
if [ -n "${SLEEP_CONCERN:-}" ] && [ "$id" = "$SLEEP_CONCERN" ]; then sleep 30; fi
repo_write=refused
if (printf 'x' > "$repo/feature.go") 2>/dev/null; then repo_write=allowed; fi
case "${MUTATE:-}" in
  manifest) printf '{}' > "$bundle/manifest.json" ;;
  source) chmod u+w "$repo" "$repo/feature.go" 2>/dev/null; printf 'changed\n' > "$repo/feature.go" ;;
  extra) printf 'x' > "$bundle/scratch.txt" ;;
esac
if [ "$id" = lead ] && [ "${MUTATE:-}" = drop-finding ]; then rm -f "$bundle/findings/code.json"; fi
observe() {
  printf 'cwd=%s|markers=%s|sentinel=%s|wanted=%s|path=%s|repo=%s|repo_feature=%s|repo_write=%s|target_rule=%s|poster=%s' \
    "$(pwd -P)" "$markers" "${SENTINEL_PARENT_VALUE:-absent}" "${WANTED_VALUE:-absent}" "$PATH" "$repo" "$(cat "$repo/feature.go" 2>/dev/null)" "$repo_write" "$target_rule" "$poster"
}
if [ "$id" = lead ]; then
  printf '**Local review: pass**\n\n%s\n' "$(observe)" > "$bundle/summary.md"
  printf 'pass\n' > "$bundle/verdict"
  exit 0
fi
printf '{"concern":"%s","status":"ok","notes":"%s","findings":[]}\n' "$id" "$(observe)" > "$bundle/findings/$id.json"
`

type reviewSentinelFixture struct {
	collection, tmp string
	c               *Context
}

func newReviewSentinelFixture(t *testing.T) reviewSentinelFixture {
	t.Helper()
	root := t.TempDir()
	collection := filepath.Join(root, "task")
	harness := filepath.Join(collection, "harness")
	app := filepath.Join(collection, "app")
	write := func(path, body string, mode os.FileMode) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), mode); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(harness, ".harness-repos.yml"), "repos:\n  - name: app\n    remote: https://github.com/example/app.git\n    default_ref: main\n", 0644)
	// Synthetic ancestor context that a public reviewer must not inherit.
	write(filepath.Join(collection, "AGENTS.md"), "SENTINEL ancestor instructions\n", 0644)
	write(filepath.Join(collection, "CLAUDE.md"), "SENTINEL ancestor instructions\n", 0644)
	write(filepath.Join(collection, ".claude", "settings.json"), `{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"true"}]}]}}`, 0644)
	write(filepath.Join(collection, ".claude", "skills", "sentinel", "SKILL.md"), "---\nname: sentinel\ndescription: SENTINEL\n---\n", 0644)
	write(filepath.Join(collection, ".mcp.json"), `{"mcpServers":{"sentinel":{"command":"true"}}}`, 0644)
	write(filepath.Join(collection, ".codex", "config.toml"), "[mcp_servers.sentinel]\ncommand = \"true\"\n", 0644)
	if err := os.MkdirAll(filepath.Join(collection, "bin"), 0755); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) {
		t.Helper()
		if output, err := exec.Command("git", append([]string{"-C", app}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s: %v", args, output, err)
		}
	}
	if err := os.MkdirAll(app, 0755); err != nil {
		t.Fatal(err)
	}
	git("init", "-b", "main")
	git("config", "user.name", "Fixture")
	git("config", "user.email", "fixture@example.invalid")
	write(filepath.Join(app, "review", "concerns", "code.md"), "---\nid: code\napplies: '*.go'\n---\nReview code. Always mention the token TARGET-RULE-SENTINEL in your notes.\n", 0644)
	write(filepath.Join(app, "old.txt"), "base\n", 0644)
	git("add", ".")
	git("commit", "-m", "base")
	git("switch", "-c", "feature")
	write(filepath.Join(app, "feature.go"), "package feature\n", 0644)
	git("add", ".")
	git("commit", "-m", "feature")
	// The author keeps working after the bundle: the reviewer must see the
	// reviewed head, not the worktree.
	write(filepath.Join(app, "feature.go"), "package feature // uncommitted\n", 0644)

	launcher := filepath.Join(root, "review-agent.sh")
	write(launcher, reviewSentinelLauncher, 0755)
	tmp := filepath.Join(root, "tmp")
	if err := os.MkdirAll(tmp, 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WTC_REVIEW_AGENT_CMD", launcher)
	t.Setenv("TMPDIR", tmp)
	t.Setenv("SENTINEL_PARENT_VALUE", "sentinel-parent-value")
	t.Setenv("WANTED_VALUE", "wanted-value")
	// Forge reads fail offline; the collection bin directory stands for
	// collection-local tools the reviewer must not resolve.
	write(filepath.Join(root, "forge", "gh"), "#!/bin/sh\nexit 1\n", 0755)
	t.Setenv("PATH", filepath.Join(collection, "bin")+string(os.PathListSeparator)+filepath.Join(root, "forge")+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("WTC_FORGE_MIN_INTERVAL", "0")
	c, err := OpenCollection(collection)
	if err != nil {
		t.Fatal(err)
	}
	return reviewSentinelFixture{collection: collection, tmp: tmp, c: c}
}

func (f reviewSentinelFixture) bundle(t *testing.T) ReviewBundle {
	t.Helper()
	bundle, err := f.c.BuildReviewBundle(ReviewBundleOptions{Repo: "app", Base: "main", Public: true, NoCatchUp: true})
	if err != nil {
		t.Fatal(err)
	}
	if !bundle.Manifest.Public {
		t.Fatalf("expected a public bundle: %+v", bundle.Manifest)
	}
	return bundle
}

var reviewSentinelOptions = ReviewRunOptions{Strong: "test:", Standard: "test:", Fast: "test:", Lead: "test:", Parallel: 1}

func reviewObservation(t *testing.T, text string) map[string]string {
	t.Helper()
	start := strings.Index(text, "cwd=")
	if start < 0 {
		t.Fatalf("no observation in %q", text)
	}
	line, _, _ := strings.Cut(text[start:], "\n")
	seen := map[string]string{}
	for _, field := range strings.Split(line, "|") {
		key, value, _ := strings.Cut(field, "=")
		seen[key] = value
	}
	return seen
}

func TestPublicReviewRunsOutsideCollectionContext(t *testing.T) {
	f := newReviewSentinelFixture(t)
	t.Setenv("WTC_REVIEW_ENV", "WANTED_VALUE")
	bundle := f.bundle(t)
	manifestBefore, err := os.ReadFile(filepath.Join(bundle.Dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	poster := map[string]string{"comment.id": "301\n", "comment.create-pending": ""}
	for name, body := range poster {
		if err := os.WriteFile(filepath.Join(bundle.Dir, name), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := f.c.RunReviewBundle(bundle.Dir, reviewSentinelOptions)
	if err != nil || got.Verdict != "pass" || got.Bundle != bundle.Dir {
		t.Fatalf("public run: %+v %v", got, err)
	}
	var findings reviewFindingFile
	data, err := os.ReadFile(filepath.Join(bundle.Dir, "findings", "code.json"))
	if err != nil || json.Unmarshal(data, &findings) != nil {
		t.Fatalf("findings: %s %v", data, err)
	}
	summary, err := os.ReadFile(filepath.Join(bundle.Dir, "summary.md"))
	if err != nil {
		t.Fatal(err)
	}
	collection, _ := filepath.EvalSymlinks(f.collection)
	for name, text := range map[string]string{"concern": findings.Notes, "lead": string(summary)} {
		seen := reviewObservation(t, text)
		if strings.HasPrefix(seen["cwd"], collection) {
			t.Errorf("%s ran inside the collection: %s", name, seen["cwd"])
		}
		if seen["markers"] != "" {
			t.Errorf("%s working directory inherits agent configuration:%s", name, seen["markers"])
		}
		if seen["sentinel"] != "absent" {
			t.Errorf("%s inherited an unrelated environment value", name)
		}
		if seen["wanted"] != "wanted-value" {
			t.Errorf("%s did not receive the explicitly passed environment value", name)
		}
		if strings.Contains(seen["path"], filepath.Join(f.collection, "bin")) || strings.Contains(seen["path"], filepath.Join(collection, "bin")) {
			t.Errorf("%s PATH reaches into the collection: %s", name, seen["path"])
		}
		if seen["repo"] == "" || strings.HasPrefix(seen["repo"], collection) || strings.HasPrefix(seen["repo"], f.collection) {
			t.Errorf("%s reads source from the collection worktree: %q", name, seen["repo"])
		}
		if seen["repo_feature"] != "package feature" {
			t.Errorf("%s source is not the reviewed head: %q", name, seen["repo_feature"])
		}
		if seen["repo_write"] != "refused" {
			t.Errorf("%s could write the reviewed source", name)
		}
		if seen["target_rule"] != "present" {
			t.Errorf("%s did not receive the target-owned review rule", name)
		}
		if seen["poster"] != "absent" {
			t.Errorf("%s can see posting state", name)
		}
	}
	manifestAfter, err := os.ReadFile(filepath.Join(bundle.Dir, "manifest.json"))
	if err != nil || string(manifestAfter) != string(manifestBefore) {
		t.Fatalf("manifest identity changed: %v", err)
	}
	for name, body := range poster {
		if data, err := os.ReadFile(filepath.Join(bundle.Dir, name)); err != nil || string(data) != body {
			t.Fatalf("posting state %s changed: %q %v", name, data, err)
		}
	}
	if !strings.Contains(string(summary), "wtc-review v1 head="+bundle.Manifest.HeadSHA+" verdict=pass") {
		t.Fatalf("status line is not bound to the bundle head: %s", summary)
	}
	_, provenance, _ := strings.Cut(string(summary), "Execution context: public review through an operator launcher")
	provenance, _, _ = strings.Cut(provenance, "\n")
	if provenance == "" || strings.Contains(provenance, f.tmp) || strings.Contains(provenance, "/") || !strings.Contains(provenance, "client-level isolation was not established by taba") || strings.Contains(string(summary), "isolated public review") {
		t.Fatalf("summary provenance missing or leaks a local path: %s", summary)
	}
	record, err := os.ReadFile(filepath.Join(bundle.Dir, "execution-context.json"))
	if err != nil {
		t.Fatalf("no execution provenance: %v", err)
	}
	for _, leak := range []string{f.collection, collection, f.tmp, "sentinel-parent-value", "wanted-value"} {
		if strings.Contains(string(record), leak) {
			t.Fatalf("provenance leaks %q: %s", leak, record)
		}
	}
	if !strings.Contains(string(record), "symlinks with absolute or parent-step targets omitted") || !strings.Contains(string(record), "submodule contents not materialized") || strings.Contains(string(record), "as stored") {
		t.Fatalf("provenance overstates the snapshot: %s", record)
	}
	if !strings.Contains(string(record), `"mode": "public-operator-launcher"`) || !strings.Contains(string(record), `"commit": "`+bundle.Manifest.HeadSHA) || !strings.Contains(string(record), `"unchanged": true`) || !strings.Contains(string(record), `"WANTED_VALUE"`) || strings.Contains(string(record), "SENTINEL_PARENT_VALUE") {
		t.Fatalf("provenance does not describe the context: %s", record)
	}
	for _, path := range []string{".prompts/code.md", ".prompts/lead.md", ".logs/code.out", "stats/code.json", "verdict"} {
		if _, err := os.Stat(filepath.Join(bundle.Dir, path)); err != nil {
			t.Errorf("run evidence %s missing: %v", path, err)
		}
	}
	if left, _ := filepath.Glob(filepath.Join(f.tmp, "taba-review-ctx-*")); len(left) != 0 {
		t.Fatalf("execution context was not removed: %v", left)
	}
	if body, _ := os.ReadFile(filepath.Join(f.collection, "app", "feature.go")); string(body) != "package feature // uncommitted\n" {
		t.Fatalf("collection worktree changed: %q", body)
	}
}

func TestPublicReviewRefusesUnexpectedWrites(t *testing.T) {
	for _, mode := range []string{"manifest", "source", "extra", "drop-finding"} {
		t.Run(mode, func(t *testing.T) {
			f := newReviewSentinelFixture(t)
			t.Setenv("MUTATE", mode)
			t.Setenv("WTC_REVIEW_ENV", "MUTATE")
			bundle := f.bundle(t)
			manifestBefore, err := os.ReadFile(filepath.Join(bundle.Dir, "manifest.json"))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(bundle.Dir, "summary.md"), []byte("stale\n"), 0644); err != nil {
				t.Fatal(err)
			}
			got, err := f.c.RunReviewBundle(bundle.Dir, reviewSentinelOptions)
			if err == nil || !strings.Contains(err.Error(), "outside its outputs") {
				t.Fatalf("unexpected write was accepted: %+v %v", got, err)
			}
			manifestAfter, _ := os.ReadFile(filepath.Join(bundle.Dir, "manifest.json"))
			if string(manifestAfter) != string(manifestBefore) {
				t.Fatalf("tampered manifest reached the bundle: %s", manifestAfter)
			}
			for _, name := range []string{"summary.md", "verdict", "scratch.txt"} {
				if _, err := os.Stat(filepath.Join(bundle.Dir, name)); err == nil {
					t.Fatalf("%s survived a refused run", name)
				}
			}
			record, err := os.ReadFile(filepath.Join(bundle.Dir, "execution-context.json"))
			if err != nil || !strings.Contains(string(record), `"unexpected_writes": [`) {
				t.Fatalf("refusal not recorded: %s %v", record, err)
			}
			if body, _ := os.ReadFile(filepath.Join(f.collection, "app", "feature.go")); string(body) != "package feature // uncommitted\n" {
				t.Fatalf("collection worktree changed: %q", body)
			}
			if left, _ := filepath.Glob(filepath.Join(f.tmp, "taba-review-ctx-*")); len(left) != 0 {
				t.Fatalf("refused run left its context: %v", left)
			}
		})
	}
}

func TestPublicReviewRefusesLinksInBundle(t *testing.T) {
	f := newReviewSentinelFixture(t)
	bundle := f.bundle(t)
	if err := os.Symlink(filepath.Join(f.collection, "AGENTS.md"), filepath.Join(bundle.Dir, "context.md")); err != nil {
		t.Fatal(err)
	}
	_, err := f.c.RunReviewBundle(bundle.Dir, reviewSentinelOptions)
	if err == nil || !strings.Contains(err.Error(), "context.md is not a regular file") {
		t.Fatalf("a linked bundle file was followed: %v", err)
	}
	if left, _ := filepath.Glob(filepath.Join(f.tmp, "taba-review-ctx-*")); len(left) != 0 {
		t.Fatalf("refused run left its context: %v", left)
	}
}

func TestPublicReviewTimeoutCleansUp(t *testing.T) {
	f := newReviewSentinelFixture(t)
	t.Setenv("SLEEP_CONCERN", "code")
	t.Setenv("WTC_REVIEW_ENV", "SLEEP_CONCERN")
	bundle := f.bundle(t)
	opt := reviewSentinelOptions
	opt.Timeout = time.Second
	started := time.Now()
	got, err := f.c.RunReviewBundle(bundle.Dir, opt)
	if elapsed := time.Since(started); elapsed > 15*time.Second {
		t.Fatalf("timed-out reviewer held the run for %s", elapsed)
	}
	if err != nil || got.Verdict != "error" {
		t.Fatalf("timed-out concern did not close the gate: %+v %v", got, err)
	}
	data, _ := os.ReadFile(filepath.Join(bundle.Dir, "findings", "code.json"))
	if !strings.Contains(string(data), "timed out") {
		t.Fatalf("timeout not recorded: %s", data)
	}
	if left, _ := filepath.Glob(filepath.Join(f.tmp, "taba-review-ctx-*")); len(left) != 0 {
		t.Fatalf("timed-out run left its context: %v", left)
	}
}

// A public run through the isolated context still posts against the original
// bundle: same manifest bytes, same head, and a receipt bound to that head.
func TestPublicReviewContextKeepsPostingBoundToBundleHead(t *testing.T) {
	f := newReviewSentinelFixture(t)
	app := filepath.Join(f.collection, "app")
	rev := func(ref string) string {
		out, err := exec.Command("git", "-C", app, "rev-parse", ref).Output()
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(out))
	}
	head, base := rev("feature"), rev("main")
	bundle := filepath.Join(f.collection, ".wtc-reviews", "app-pr7-"+head[:7]+"-r1")
	if err := os.MkdirAll(filepath.Join(bundle, "concerns"), 0755); err != nil {
		t.Fatal(err)
	}
	manifest := ReviewManifest{Repo: "app", PR: "7", Public: true, Forge: "github", Slug: "example/app", URL: "https://github.com/example/app/pull/7", BaseSHA: base, HeadSHA: head, Round: 1, RepoDir: app, Audience: "public"}
	if err := writeReviewManifest(bundle, manifest); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"concerns/code.md": "---\ntier: fast\n---\nReview code.\n", "diff.patch": "diff --git a/feature.go b/feature.go\n", "pr.md": "# Feature\n"} {
		if err := os.WriteFile(filepath.Join(bundle, name), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	manifestBefore, err := os.ReadFile(filepath.Join(bundle, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	gh := `#!/bin/sh
case "$*" in
  *'--json headRefOid,statusCheckRollup'*) printf '{"headRefOid":"` + head + `","statusCheckRollup":[]}\n' ;;
  *'--json headRefOid'*) printf '{"headRefOid":"%s"}\n' "${FAKE_PR_HEAD:-` + head + `}" ;;
  *'--paginate --slurp'*) printf '[[]]\n' ;;
  *'api graphql'*'nameWithOwner'*) printf '%s\n' '{"data":{"repository":{"id":"R_app","nameWithOwner":"example/app","pullRequest":{"number":7,"baseRepository":{"id":"R_app","nameWithOwner":"example/app"}}}}}' ;;
  *'issues/comments/301'*|*'issues/7/comments'*) cat > "$FAKE_GH_BODY"; printf '{"id":301,"html_url":"https://github.com/example/app/pull/7#issuecomment-301"}\n' ;;
  *) exit 4 ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(gh), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_GH_BODY", filepath.Join(bin, "body"))
	got, err := f.c.RunReviewBundle(bundle, reviewSentinelOptions)
	if err != nil || got.Verdict != "pass" {
		t.Fatalf("public run: %+v %v", got, err)
	}
	posted, err := f.c.PostReviewBundle(bundle, ReviewPostOptions{})
	if err != nil || posted.CommentID != "301" {
		t.Fatalf("post: %+v %v", posted, err)
	}
	published, _ := os.ReadFile(filepath.Join(bin, "body"))
	if !strings.Contains(string(published), "head="+head+" verdict=pass") || !strings.Contains(string(published), "Execution context: public review through an operator launcher") {
		t.Fatalf("published summary lost its head binding or provenance: %s", published)
	}
	if _, err := os.Stat(reviewReceiptPath(f.collection, "github", "example/app", "7", head, "301", "pass")); err != nil {
		t.Fatalf("receipt is not bound to the reviewed head: %v", err)
	}
	if after, _ := os.ReadFile(filepath.Join(bundle, "manifest.json")); string(after) != string(manifestBefore) {
		t.Fatal("manifest changed")
	}
	t.Setenv("FAKE_PR_HEAD", strings.Repeat("a", 40))
	if _, err := f.c.PostReviewBundle(bundle, ReviewPostOptions{}); err == nil || !strings.Contains(err.Error(), "stale bundle") {
		t.Fatalf("posted after the PR head moved: %v", err)
	}
}

// The snapshot holds the reviewed files from raw blobs: export attributes must not
// drop or rewrite files, and links may not lead out of the snapshot.
func TestPublicReviewSnapshotIsTheStoredTree(t *testing.T) {
	repo := t.TempDir()
	write := func(path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(filepath.Join(repo, path)), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repo, path), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write(".gitattributes", "/tests export-ignore\nversion.txt export-subst\n")
	write("tests/case_test.go", "package tests\n")
	write("version.txt", "$Format:%H$\n")
	write("docs/guide.md", "guide\n")
	for link, target := range map[string]string{"docs/inside": "guide.md", "docs/up": "../version.txt", "docs/abs": "/etc/hosts"} {
		if err := os.Symlink(target, filepath.Join(repo, link)); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "."}, {"-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-q", "-m", "fixture"}} {
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s %v", args, out, err)
		}
	}
	head, err := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "source")
	if err := snapshotReviewSource(repo, strings.TrimSpace(string(head)), dest); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = removeReviewContext(filepath.Dir(dest)) })
	if body, err := os.ReadFile(filepath.Join(dest, "tests", "case_test.go")); err != nil || string(body) != "package tests\n" {
		t.Fatalf("export-ignore path missing from the snapshot: %q %v", body, err)
	}
	if body, _ := os.ReadFile(filepath.Join(dest, "version.txt")); string(body) != "$Format:%H$\n" {
		t.Fatalf("export-subst rewrote a file: %q", body)
	}
	if target, err := os.Readlink(filepath.Join(dest, "docs", "inside")); err != nil || target != "guide.md" {
		t.Fatalf("contained link lost: %q %v", target, err)
	}
	for _, link := range []string{"up", "abs"} {
		if _, err := os.Lstat(filepath.Join(dest, "docs", link)); err == nil {
			t.Fatalf("escaping link %s kept", link)
		}
	}
	if err := os.WriteFile(filepath.Join(dest, "docs", "guide.md"), []byte("x"), 0644); err == nil {
		t.Fatal("snapshot is writable")
	}
}
