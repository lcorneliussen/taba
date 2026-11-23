package wtc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReviewProgressPostingFailuresDoNotCreateDuplicates(t *testing.T) {
	for _, tc := range []struct {
		name, response string
		saveID         bool
	}{
		{"update-transport", "exit 9", true},
		{"update-malformed", "echo '{}'", true},
		{"create-id-save", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			collection := t.TempDir()
			repo := filepath.Join(collection, "app")
			bundle := filepath.Join(collection, "bundle")
			bin := filepath.Join(collection, "bin")
			for _, dir := range []string{filepath.Join(collection, "harness"), filepath.Join(repo, ".git"), bundle, bin} {
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
			m := ReviewManifest{Repo: "app", PR: "7", Public: true, Forge: "github", Slug: "example/app", HeadSHA: "1234567890abcdef1234567890abcdef12345678", Round: 1, RepoDir: repo}
			if err := writeReviewManifest(bundle, m); err != nil {
				t.Fatal(err)
			}
			if tc.saveID {
				write(filepath.Join(bundle, "comment.id"), "301\n", 0644)
			} else if err := os.Mkdir(filepath.Join(bundle, "comment.id"), 0755); err != nil {
				t.Fatal(err)
			}
			c, err := OpenCollection(collection)
			if err != nil {
				t.Fatal(err)
			}
			receipt := reviewReceiptPath(collection, m.Forge, m.Slug, m.PR, m.HeadSHA, "301", "pass")
			if err := os.MkdirAll(filepath.Dir(receipt), 0755); err != nil {
				t.Fatal(err)
			}
			if tc.saveID {
				write(receipt, "", 0600)
			}
			write(filepath.Join(bin, "gh"), `#!/bin/sh
case "$*" in
 *'--json headRefOid'*) echo '{"headRefOid":"1234567890abcdef1234567890abcdef12345678"}';;
 *'api graphql'*'nameWithOwner'*) echo '{"data":{"repository":{"id":"R_app","nameWithOwner":"example/app","pullRequest":{"number":7,"baseRepository":{"id":"R_app","nameWithOwner":"example/app"}}}}}';;
 *'api -X PATCH'*) cat >/dev/null; echo PATCH >> "$FORGE_LOG"; `+tc.response+`;;
 *'api -X POST'*) cat >/dev/null; echo POST >> "$FORGE_LOG"; echo '{"id":302}';;
 *) exit 8;;
esac
`, 0755)
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("FORGE_LOG", filepath.Join(collection, "forge.log"))
			for i := 0; i < 2; i++ {
				if _, err := c.PostReviewBundle(bundle, ReviewPostOptions{Mode: "progress"}); err == nil {
					t.Fatal("ambiguous update did not fail")
				}
			}
			log, err := os.ReadFile(filepath.Join(collection, "forge.log"))
			want := "PATCH\nPATCH\n"
			if !tc.saveID {
				want = "POST\n"
			}
			if err != nil || string(log) != want {
				t.Fatalf("blind create/retry: %s %v", log, err)
			}
			if tc.saveID {
				id, err := os.ReadFile(filepath.Join(bundle, "comment.id"))
				if err != nil || strings.TrimSpace(string(id)) != "301" {
					t.Fatalf("saved ID lost: %s %v", id, err)
				}
			} else if _, err := os.Stat(filepath.Join(bundle, "comment.create-pending")); err != nil {
				t.Fatalf("lost uncertain-create marker: %v", err)
			}
			if _, err := os.Stat(receipt); !os.IsNotExist(err) {
				t.Fatalf("passing receipt survived ambiguous write: %v", err)
			}
		})
	}
}
