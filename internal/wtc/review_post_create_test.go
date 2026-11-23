package wtc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Two posts of one bundle overlap: this one reads no comment ID, then the
// other creates the comment and saves its ID while this one collects checks.
// This post must not create a second comment.
func TestReviewPostDoesNotCreateACommentAnOverlappingPostCreated(t *testing.T) {
	collection := filepath.Join(t.TempDir(), "sample")
	repo := filepath.Join(collection, "app")
	bundle := filepath.Join(collection, "bundle")
	bin := t.TempDir()
	for _, dir := range []string{filepath.Join(collection, "harness"), filepath.Join(repo, ".git"), bundle} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(path, body string, mode os.FileMode) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), mode); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(collection, "harness", ".harness-repos.yml"), "repos:\n  - name: app\n    remote: https://github.com/example/app.git\n", 0644)
	head := "1234567890abcdef1234567890abcdef12345678"
	m := ReviewManifest{Repo: "app", PR: "7", Public: true, Forge: "github", Slug: "example/app", HeadSHA: head, Round: 1, RepoDir: repo}
	if err := writeReviewManifest(bundle, m); err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(bundle, "summary.md"), "**Local review: pass**\n\n`wtc-review v1 head="+head+" verdict=pass blockers=0 round=1 lead=test:`\n", 0644)
	write(filepath.Join(bin, "gh"), `#!/bin/sh
case "$*" in
 *'--json headRefOid,statusCheckRollup'*) printf '301\n' > "$BUNDLE/comment.id"; echo '{"headRefOid":"`+head+`","statusCheckRollup":[]}';;
 *'--json headRefOid'*) echo '{"headRefOid":"`+head+`"}';;
 *'api graphql'*'nameWithOwner'*) echo '{"data":{"repository":{"id":"R_app","nameWithOwner":"example/app","pullRequest":{"number":7,"baseRepository":{"id":"R_app","nameWithOwner":"example/app"}}}}}';;
 *'api -X POST'*) cat >/dev/null; echo POST >> "$FORGE_LOG"; echo '{"id":302}';;
 *) exit 8;;
esac
`, 0755)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("BUNDLE", bundle)
	t.Setenv("FORGE_LOG", filepath.Join(bin, "forge.log"))
	t.Setenv("WTC_FORGE_MIN_INTERVAL", "0")
	c, err := OpenCollection(collection)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.PostReviewBundle(bundle, ReviewPostOptions{}); err == nil || !strings.Contains(err.Error(), "post again") {
		t.Fatalf("overlapping post: %v", err)
	}
	if log, _ := os.ReadFile(filepath.Join(bin, "forge.log")); len(log) != 0 {
		t.Fatalf("created a second comment: %q", log)
	}
	if _, err := os.Stat(filepath.Join(bundle, "comment.create-pending")); !os.IsNotExist(err) {
		t.Fatalf("a create that never started left its marker: %v", err)
	}
}
