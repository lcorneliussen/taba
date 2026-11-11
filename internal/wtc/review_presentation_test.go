package wtc

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestReviewPresentationKeepsGateAndImportantFindingsVisible(t *testing.T) {
	m := ReviewManifest{Forge: "github", Slug: "example/app", HeadSHA: strings.Repeat("a", 40)}
	status := "`wtc-review v1 head=" + m.HeadSHA + " verdict=changes-requested blockers=1 round=1 lead=test:`"
	body := "**Local review: changes-requested** — Fix the edge.\n\n### Blockers\n\n- `src/a file.go:4` — Fails empty input.\n- `pr.md:50` — Clarify purpose.\n\n### Concerns\n\n| code | issues |\n\n### Minor / nits\n\n- Useful minor.\n\n### Addressed since previous round\n\n- Done.\n\n### Run stats\n\n| time | 1s |\n\n" + status + "\n"
	got := presentReviewSummary(body, m, map[string]bool{"src/a file.go": true})
	if !strings.HasPrefix(got, "🔴 **Local review:") || !strings.Contains(got, "/blob/"+m.HeadSHA+"/src/a%20file.go#L4") || !strings.Contains(got, "`pr.md:50`") || strings.Contains(got, "/pr.md#") {
		t.Fatalf("incorrect presentation: %s", got)
	}
	if strings.Count(got, "<details>") != 3 || strings.Count(got, "</details>") != 3 || strings.Count(got, status) != 1 || !strings.HasSuffix(got, status+"\n") {
		t.Fatalf("folds or gate record corrupted: %s", got)
	}
	if strings.Index(got, "### Blockers") > strings.Index(got, "<details>") || !strings.Contains(got, "</details>\n\n### Minor / nits") {
		t.Fatalf("open findings hidden: %s", got)
	}
	if got := presentReviewSummary(body, ReviewManifest{Forge: "bitbucket"}, nil); strings.Contains(got, "<details>") || strings.Contains(got, "/blob/") || !strings.Contains(got, "- `src/a file.go:4` — Fails empty input.") {
		t.Fatal("Bitbucket received GitHub presentation or lost a finding")
	}
}

func TestBitbucketReviewVerdictsAndGateReceipt(t *testing.T) {
	head := strings.Repeat("a", 40)
	for verdict, mark := range map[string]string{"pass": "🟢", "pass-with-notes": "🟡", "changes-requested": "🔴", "error": "🔴"} {
		t.Run(verdict, func(t *testing.T) {
			gate := "`wtc-review v1 head=" + head + " verdict=" + verdict + " blockers=1 round=2 lead=example:model`"
			body := "**Local review: " + verdict + "** — Round 2.\n\n### Major\n\n- Preserve this unresolved item.\n\n" + gate + "\n\n### Forge checks at publication\n\nPending.\n"
			got := presentReviewSummary(body, ReviewManifest{Forge: "bitbucket"}, nil)
			if !strings.HasPrefix(got, mark+" **Local review: "+verdict+"**") || strings.Count(got, gate) != 1 || !strings.HasSuffix(got, "### Gate record\n\n"+gate+"\n") || !strings.Contains(got, "### Major\n\n- Preserve this unresolved item.") || !strings.Contains(got, "### Forge checks at publication\n\nPending.") {
				t.Fatalf("verdict, findings or final marker changed: %s", got)
			}
			collection := t.TempDir()
			comments, err := json.Marshal(map[string]any{"values": []any{map[string]any{"id": 11, "content": map[string]string{"raw": got}}}})
			if err != nil {
				t.Fatal(err)
			}
			status, err := ParseReviewStatusComments(comments, 7, head, "bitbucket", "example/app", collection, true)
			if err != nil || status.State != "untrusted" {
				t.Fatalf("presentation bypassed receipt: %+v %v", status, err)
			}
			receipt := reviewReceiptPath(collection, "bitbucket", "example/app", "7", head, "11", verdict)
			if err := os.MkdirAll(filepath.Dir(receipt), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(receipt, nil, 0600); err != nil {
				t.Fatal(err)
			}
			status, err = ParseReviewStatusComments(comments, 7, head, "bitbucket", "example/app", collection, true)
			if err != nil || status.State != "current" || status.Verdict != verdict || *status.Blockers != 1 || *status.Round != 2 || status.CommentID != "11" {
				t.Fatalf("formatted gate incompatible with receipt: %+v %v", status, err)
			}
			legacy, _ := json.Marshal([]any{map[string]any{"id": 11, "body": body}})
			old, err := ParseReviewStatusComments(legacy, 7, head, "bitbucket", "example/app", collection, true)
			if err != nil || !reflect.DeepEqual(old, status) {
				t.Fatalf("legacy and compact records disagree: %+v %+v %v", old, status, err)
			}
		})
	}
}

func TestBitbucketReviewCompactsMixedConcernsWithoutLosingNotes(t *testing.T) {
	body := "### Concerns\n\n| Concern | Status | Open findings | Note |\n|---|---|---|---|\n| `correctness` | ok | 0 | - |\n| `security` | ok | 0 | Checked input validation. |\n| `tests` | issues | 2 | First unresolved item; second unresolved item. |\n| `compatibility` | skipped | 0 | Requires another repository; coverage unavailable. |\n| `tooling` | error | 0 | Reviewer unavailable. |\n\n### Minor / nits\n\n- Keep this minor.\n"
	got := presentReviewSummary(body, ReviewManifest{Forge: "bitbucket"}, nil)
	want := "### Concerns\n\n| Concern | Status | Open findings | Note |\n|---|---|---|---|\n| `tests` | issues | 2 | First unresolved item; second unresolved item. |\n\n🟢 `correctness` · 🟢 `security` — Checked input validation.\n\n⚪ `compatibility` — Requires another repository; coverage unavailable.\n\n🔴 `tooling` — error — Reviewer unavailable.\n\n### Minor / nits\n\n- Keep this minor.\n"
	if got != want {
		t.Fatalf("mixed concerns:\ngot: %s\nwant: %s", got, want)
	}
	clean := strings.ReplaceAll(body[:strings.Index(body, "| `tests`")], "| `security` | ok | 0 | Checked input validation. |\n", "")
	if got := presentReviewSummary(clean, ReviewManifest{Forge: "bitbucket"}, nil); strings.Contains(got, "| Concern") || !strings.Contains(got, "🟢 `correctness`") {
		t.Fatalf("clean concerns kept a table: %s", got)
	}
}

func TestBitbucketReviewRunUsesMeasuredTotalsAndPreservesUncertainty(t *testing.T) {
	for _, tokens := range []string{"100k / 10k (20k)", "100k+ / - (-)"} {
		body := "### Run stats\n\n| Concern | Agent | Time | Tokens in / out (cache read) | Cost |\n|---|---|---|---|---|\n| correctness | example:model | 2m00s | 60k / 5k (10k) | $0.30 |\n| lead | example:model | 4m00s | 40k / 5k (10k) | $0.20 |\n| **Total** | | 6m00s | " + tokens + " | $0.50+ |\n\nWall-clock for the whole run: 2m00s (agent time summed: 6m00s).\n"
		got := presentReviewSummary(body, ReviewManifest{Forge: "bitbucket"}, nil)
		want := "### Run\n\n2m00s wall · 6m00s agents · " + tokens + " tokens in / out (cache read) · $0.50+\n\n"
		if got != want {
			t.Fatalf("run totals changed:\n%s", got)
		}
		for _, malformed := range []string{strings.Replace(body, "| **Total** | | 6m00s", "| **Total** | | 7m00s", 1), body + "\nUnrecognized additional evidence.\n", strings.Replace(body, "| lead |", "| **Total** |", 1)} {
			if got := presentReviewSummary(malformed, ReviewManifest{Forge: "bitbucket"}, nil); got != malformed {
				t.Fatalf("unrecognized stats lost evidence: %s", got)
			}
		}
	}
}

func TestBitbucketReviewPreservesFencesAndUnknownSections(t *testing.T) {
	table := "### Concerns\n\n| Concern | Status | Open findings | Note |\n|---|---|---|---|\n| example | ok | 0 | - |\n"
	for _, fence := range []string{"```md", "~~~~markdown"} {
		close := strings.TrimRight(fence, "mdarkown")
		body := fence + "\n" + table + "**Local review: pass**\n`wtc-review v1 head=aaaaaaaaaaaa verdict=pass blockers=0 round=1`\n" + close + "\n\n### Unresolved findings\n\nKeep every word.\n"
		if got := presentReviewSummary(body, ReviewManifest{Forge: "bitbucket"}, nil); got != body {
			t.Fatalf("fenced or unknown content changed: %s", got)
		}
	}
	for _, change := range []string{
		"### Concerns\n\n|\n",
		"### Concerns\n\n  |  \n",
		"### Run stats\n\n|\n\nWall-clock for the whole run: 1s (agent time summed: 1s).\n",
		strings.Replace(table, "| example | ok | 0 | - |", "| example | ok | 1 | Unresolved. |", 1),
		strings.Replace(table, "| example | ok | 0 | - |", "| example | skipped | 0 | - |", 1),
		strings.Replace(table, "| example | ok | 0 | - |", "| example | unknown | 0 | Evidence. |", 1),
		strings.Replace(table, "| example | ok | 0 | - |", "| example | issues | many | Evidence. |", 1),
		strings.Replace(table, "| example | ok | 0 | - |", "| example | ok | 0 | A \\| B |", 1),
		table + "\nAdditional unresolved evidence.\n",
		table + "\n```md\nAdditional example.\n```\n",
	} {
		if got := presentReviewSummary(change, ReviewManifest{Forge: "bitbucket"}, nil); got != change {
			t.Fatalf("unknown concerns changed: %s", got)
		}
	}
	if got := presentReviewSummary(table, ReviewManifest{Forge: "unknown"}, nil); got != table {
		t.Fatal("unknown forge changed")
	}
}

func TestReviewSourceLinksPreserveWhitespaceInGitFilenames(t *testing.T) {
	repo := t.TempDir()
	fixtureGit(t, "-C", repo, "init", "-b", "main")
	fixtureGit(t, "-C", repo, "config", "user.email", "example@example.invalid")
	fixtureGit(t, "-C", repo, "config", "user.name", "Example")
	for _, name := range []string{"\tfirst.go", " leading.go", "trailing.go "} {
		fixtureFile(t, filepath.Join(repo, name), "package example\n", 0644)
	}
	fixtureGit(t, "-C", repo, "add", ".")
	fixtureGit(t, "-C", repo, "commit", "-m", "fixture")
	m := ReviewManifest{Forge: "github", RepoDir: repo, Slug: "example/app", HeadSHA: fixtureGit(t, "-C", repo, "rev-parse", "HEAD")}
	files := reviewSourceFiles(m)
	for _, name := range []string{"\tfirst.go", " leading.go", "trailing.go "} {
		if !files[name] {
			t.Fatalf("Git filename changed: %q in %+v", name, files)
		}
	}
	if files[""] {
		t.Fatal("empty NUL field became a source file")
	}
	body := "**Local review: pass**\n\n`\tfirst.go:1` and ` leading.go:1`\n"
	got := presentReviewSummary(body, m, files)
	if !strings.Contains(got, "/%09first.go#L1") || !strings.Contains(got, "/%20leading.go#L1") {
		t.Fatalf("whitespace file source links missing: %s", got)
	}
}

func TestReviewPresentationDoesNotRewriteCodeExamples(t *testing.T) {
	body := "**Local review: pass**\n\n```md\n### Concerns\n`feature.go:9`\n```\n\n### Concerns\n\nChecked.\n\n`wtc-review v1 head=aaaaaaaaaaaa verdict=pass blockers=0 round=1`\n"
	got := presentReviewSummary(body, ReviewManifest{Forge: "github"}, map[string]bool{"feature.go": true})
	if strings.Contains(got, "/blob/") || strings.Count(got, "<details>") != 1 || !strings.Contains(got, "```md\n### Concerns\n`feature.go:9`\n```") {
		t.Fatalf("code example rewritten: %s", got)
	}
}
