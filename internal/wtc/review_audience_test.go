package wtc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func reviewAudienceFixture(t *testing.T) *Context {
	t.Helper()
	collection := t.TempDir()
	for _, name := range []string{"harness", "app"} {
		repo := filepath.Join(collection, name)
		fixtureFile(t, filepath.Join(repo, "file.go"), "package example\n", 0644)
		fixtureGit(t, "-C", repo, "init", "-b", "main")
		fixtureGit(t, "-C", repo, "add", ".")
		fixtureGit(t, "-C", repo, "-c", "user.name=Example", "-c", "user.email=example@example.invalid", "commit", "-qm", "base")
	}
	fixtureFile(t, filepath.Join(collection, "harness", ".harness-repos.yml"), "repos:\n  - name: harness\n    remote: https://github.com/example/harness.git\n  - name: app\n    remote: https://github.com/example/app.git\n    default_ref: main\n", 0644)
	fixtureFile(t, filepath.Join(collection, "harness", "review", "concerns", "local.md"), "---\nid: local\n---\nSynthetic harness rule.\n", 0644)
	fixtureFile(t, filepath.Join(collection, "harness", "review", "concerns.d", "overlay.md"), "---\nid: overlay\n---\nSynthetic overlay.\n", 0644)
	fixtureFile(t, filepath.Join(collection, "harness", "review", "prompts", "concern.md"), "Synthetic harness prompt.\n", 0644)
	bin := t.TempDir()
	// Both forges use the same scripted repository responses. Every mutation
	// is recorded, so refusal tests can prove they ran before any write.
	script := `#!/bin/sh
case "$(basename "$0"):$1" in
  bb:repo|bb:--workspace) ;;
  bb:*) exit 91 ;;
esac
if [ "$1" = --workspace ]; then
  [ "$2" = example ] && [ "$3" = --repo ] && [ "$4" = app ] || exit 90
  shift 4
fi
case "$*" in
  'repo view '*'--json visibility'|'repo view '*'--json')
    [ "${AUDIENCE_FAIL:-}" != 1 ] || exit 1
    printf '%s\n' "$AUDIENCE_JSON" ;;
  *'--json headRefOid') printf '{"headRefOid":"%s"}\n' "$REVIEW_HEAD" ;;
  'pr view 7 --json') printf '{"source":{"commit":{"hash":"%s"}}}\n' "$REVIEW_HEAD" ;;
  *'--paginate --slurp'|'pr comments list 7 --all --json') echo '[]' ;;
  'api graphql '*'nameWithOwner'*) echo '{"data":{"repository":{"id":"R_app","nameWithOwner":"example/app","pullRequest":{"number":7,"baseRepository":{"id":"R_app","nameWithOwner":"example/app"}}}}}' ;;
  'pr comments list 7 --limit 1000 --json') echo '{"count":0,"comments":[]}' ;;
  *)
    printf '%s\n' "$*" >> "$REVIEW_WRITES"
    case "$*" in
      'pr comments '*) printf '%s' "$*" > "$REVIEW_BODY" ;;
      *) cat > "$REVIEW_BODY" ;;
    esac
    echo '{"id":301,"html_url":"https://github.com/example/app/pull/7#issuecomment-301"}' ;;
esac
`
	for _, name := range []string{"gh", "bb"} {
		fixtureFile(t, filepath.Join(bin, name), script, 0755)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("AUDIENCE_JSON", `{"visibility":"PRIVATE"}`)
	t.Setenv("AUDIENCE_FAIL", "")
	t.Setenv("REVIEW_WRITES", filepath.Join(collection, "writes"))
	t.Setenv("REVIEW_BODY", filepath.Join(collection, "body"))
	for _, key := range []string{"BB_API_USER", "BB_API_PASS", "BB_USERNAME", "BB_API_TOKEN"} {
		t.Setenv(key, "")
	}
	c, err := OpenCollection(collection)
	if err != nil {
		t.Fatal(err)
	}
	c.Config.Harness.Name = "harness"
	return c
}

func TestReviewAudienceDefaults(t *testing.T) {
	c := reviewAudienceFixture(t)
	for _, tc := range []struct {
		name, response, remote            string
		fail, public, private, wantPublic bool
	}{
		{name: "public", response: `{"visibility":"PUBLIC"}`, wantPublic: true},
		{name: "private", response: `{"visibility":"PRIVATE"}`},
		{name: "internal", response: `{"visibility":"INTERNAL"}`},
		{name: "missing", response: `{}`, wantPublic: true},
		{name: "malformed", response: `broken`, wantPublic: true},
		{name: "future visibility", response: `{"visibility":"OTHER"}`, wantPublic: true},
		{name: "unavailable", response: `{"visibility":"PRIVATE"}`, fail: true, wantPublic: true},
		{name: "different owner", response: `{"visibility":"PRIVATE"}`, remote: "https://github.com/elsewhere/app.git", wantPublic: true},
		{name: "different forge", response: `{"is_private":true}`, remote: "https://bitbucket.org/example/app.git", wantPublic: true},
		{name: "explicit private public", response: `{"visibility":"PUBLIC"}`, private: true},
		{name: "explicit private unknown", response: `{}`, private: true},
		{name: "explicit public private", response: `{"visibility":"PRIVATE"}`, public: true, wantPublic: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("AUDIENCE_JSON", tc.response)
			if tc.fail {
				t.Setenv("AUDIENCE_FAIL", "1")
			}
			original := c.Registry.Repos[1].Remote
			if tc.remote != "" {
				c.Registry.Repos[1].Remote = tc.remote
			}
			defer func() { c.Registry.Repos[1].Remote = original }()
			bundle, err := c.BuildReviewBundle(ReviewBundleOptions{Repo: "app", Base: "main", Public: tc.public, Private: tc.private})
			if err != nil {
				t.Fatal(err)
			}
			if bundle.Manifest.Public != tc.wantPublic {
				t.Fatalf("mode: %+v", bundle.Manifest)
			}
			if tc.wantPublic {
				for _, path := range []string{"concerns/local.md", "concerns/overlay.md", "related", "downstream", "upstream"} {
					if _, err := os.Stat(filepath.Join(bundle.Dir, path)); !os.IsNotExist(err) {
						t.Fatalf("public bundle included %s", path)
					}
				}
				prompt, err := c.reviewPromptTemplate(bundle.Manifest, "concern.md")
				if err != nil || strings.Contains(string(prompt), "Synthetic harness prompt") {
					t.Fatalf("public prompt: %s %v", prompt, err)
				}
			}
			manifest, err := os.ReadFile(filepath.Join(bundle.Dir, "manifest.env"))
			if err != nil || !strings.Contains(string(manifest), "REVIEW_MODE='"+bundle.Manifest.Mode()+"'") {
				t.Fatalf("mode not persisted: %s %v", manifest, err)
			}
		})
	}
	if _, err := c.BuildReviewBundle(ReviewBundleOptions{Repo: "app", Public: true, Private: true}); err == nil {
		t.Fatal("accepted contradictory flags")
	}
	// An absent/unsupported harness is not evidence of shared ownership.
	c.Config.Harness.Name = "absent"
	fixtureFile(t, filepath.Join(c.Harness, ".git", "config"), "", 0644)
	c.Registry.Repos = c.Registry.Repos[1:]
	b, err := c.BuildReviewBundle(ReviewBundleOptions{Repo: "app", Base: "main"})
	if err != nil || !b.Manifest.Public {
		t.Fatalf("unknown harness namespace: %+v %v", b, err)
	}
}

func TestReviewAudienceForgeParsing(t *testing.T) {
	c := reviewAudienceFixture(t)
	for _, tc := range []struct{ forge, raw, want string }{
		{"github", `{"visibility":"PUBLIC"}`, "public"},
		{"github", `{"visibility":"PRIVATE"}`, "private"},
		{"github", `{"is_private":true}`, "unknown"},
		{"bitbucket", `{"is_private":false}`, "public"},
		{"bitbucket", `{"is_private":true}`, "private"},
		{"bitbucket", `{"is_private":null}`, "unknown"},
		{"bitbucket", `{}`, "unknown"},
		{"bitbucket", `{"is_private":"false"}`, "unknown"},
	} {
		t.Setenv("AUDIENCE_JSON", tc.raw)
		if got := reviewRepositoryAudience(c.Collection, tc.forge, "example/app"); got != tc.want {
			t.Fatalf("%s %s: %s", tc.forge, tc.raw, got)
		}
	}
}

func TestReviewPrivatePublicationRechecksAudienceBeforeAllWrites(t *testing.T) {
	c := reviewAudienceFixture(t)
	for _, forge := range []string{"github", "bitbucket"} {
		t.Run(forge, func(t *testing.T) {
			remote := c.Registry.Repos[1].Remote
			c.Registry.Repos[1].Remote = "https://" + map[string]string{"github": "github.com", "bitbucket": "bitbucket.org"}[forge] + "/example/app.git"
			defer func() { c.Registry.Repos[1].Remote = remote }()
			// Explicit private remains useful locally, including outside the
			// harness namespace. Only fresh private evidence permits posting.
			b, err := c.BuildReviewBundle(ReviewBundleOptions{Repo: "app", Base: "main", PR: "", Private: true, Dir: filepath.Join(t.TempDir(), "bundle")})
			if err != nil {
				t.Fatal(err)
			}
			b.Manifest.PR = "7"
			if err := writeReviewManifest(b.Dir, b.Manifest); err != nil {
				t.Fatal(err)
			}
			t.Setenv("REVIEW_HEAD", b.Manifest.HeadSHA)
			fixtureFile(t, filepath.Join(b.Dir, "summary.md"), "**Local review: pass**\n\n`wtc-review v1 head="+b.Manifest.HeadSHA+" verdict=pass blockers=0 round=1 lead=test`\n", 0644)
			if err := writeInlineRecords(b.Dir, []ReviewInlineRecord{{Key: "012345abcd", ID: "401"}}); err != nil {
				t.Fatal(err)
			}
			for _, audience := range []string{"public", "unknown", "private"} {
				raw := `{"visibility":"PRIVATE","is_private":true}`
				if audience == "public" {
					raw = `{"visibility":"PUBLIC","is_private":false}`
				}
				if audience == "unknown" {
					raw = `{}`
				}
				t.Setenv("AUDIENCE_JSON", raw)
				for _, mode := range []string{"summary", "progress", "failed"} {
					fixtureFile(t, os.Getenv("REVIEW_WRITES"), "", 0644)
					_, err := c.PostReviewBundle(b.Dir, ReviewPostOptions{Mode: mode, Force: true})
					if audience == "private" {
						if err != nil {
							t.Fatalf("private %s %s: %v", forge, mode, err)
						}
						body, _ := os.ReadFile(os.Getenv("REVIEW_BODY"))
						if !strings.Contains(string(body), "Review bundle mode: **private**.") {
							t.Fatalf("mode missing: %s", body)
						}
						continue
					}
					if err == nil || !strings.Contains(err.Error(), "refusing to publish") {
						t.Fatalf("%s %s was not refused: %v", audience, mode, err)
					}
					writes, _ := os.ReadFile(os.Getenv("REVIEW_WRITES"))
					if len(writes) != 0 {
						t.Fatalf("unsafe write: %s", writes)
					}
				}
				if audience != "private" {
					if _, err := c.ResolveReviewBundle(b.Dir, ReviewResolveOptions{Reply: "Synthetic reply"}); err == nil || !strings.Contains(err.Error(), "refusing to publish") {
						t.Fatalf("unsafe resolution: %v", err)
					}
				}
			}
		})
	}
}

func TestPublicReviewRulesUseTargetBaseOnly(t *testing.T) {
	c := reviewAudienceFixture(t)
	t.Setenv("AUDIENCE_JSON", `{"visibility":"PUBLIC"}`)
	repo := filepath.Join(c.Collection, "app")
	fixtureFile(t, filepath.Join(repo, ".review/concerns/target.md"), "---\nid: target\n---\nTarget base rule.\n", 0644)
	fixtureGit(t, "-C", repo, "add", ".")
	fixtureGit(t, "-C", repo, "-c", "user.name=Example", "-c", "user.email=example@example.invalid", "commit", "-qm", "target rules")
	fixtureGit(t, "-C", repo, "switch", "-c", "feature")
	fixtureFile(t, filepath.Join(repo, ".review/concerns/target.md"), "Head rule must not apply.\n", 0644)
	fixtureGit(t, "-C", repo, "add", ".")
	fixtureGit(t, "-C", repo, "-c", "user.name=Example", "-c", "user.email=example@example.invalid", "commit", "-qm", "head rules")
	fixtureFile(t, filepath.Join(repo, ".review/concerns/target.md"), "Local rule must not apply.\n", 0644)
	b, err := c.BuildReviewBundle(ReviewBundleOptions{Repo: "app", Base: "main"})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(b.Dir, "concerns/target.md"))
	if err != nil || !strings.Contains(string(data), "Target base rule.") || b.Concerns != 1 {
		t.Fatalf("target rules: %s %+v %v", data, b, err)
	}
}

func TestPublicReviewRulesIgnoreSymlinkOnlyDirectory(t *testing.T) {
	c := reviewAudienceFixture(t)
	t.Setenv("AUDIENCE_JSON", `{"visibility":"PUBLIC"}`)
	repo := filepath.Join(c.Collection, "app")
	path := filepath.Join(repo, "review/concerns/link.md")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../../../harness/review/concerns/local.md", path); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, "-C", repo, "add", ".")
	fixtureGit(t, "-C", repo, "-c", "user.name=Example", "-c", "user.email=example@example.invalid", "commit", "-qm", "symlink fixture")
	b, err := c.BuildReviewBundle(ReviewBundleOptions{Repo: "app", Base: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if b.Concerns == 0 {
		t.Fatal("missing embedded fallback")
	}
	if _, err := os.Stat(filepath.Join(b.Dir, "concerns/link.md")); !os.IsNotExist(err) {
		t.Fatal("symlink used as concern")
	}
}
