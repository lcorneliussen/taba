package wtc

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Synthetic envelopes follow @pilatos/bitbucket-cli comments.list.command.ts:
// 1.8.3 at 441072126c26aed6b54e227eee89271a45cb8d85 and 2.2.1 at
// 0096d5cb3545eaf7065634551f996352eae3f951 (also base-command.ts runList).
// count is collected length, not the remote total; neither emits hasMore.
func TestBitbucketCommentReadContractEnvelopes(t *testing.T) {
	for _, raw := range []string{
		`{"pullRequestId":7,"count":1,"comments":[{"id":301,"content":{"raw":"Full reply"},"created_on":"2026-01-01T11:00:00Z"}]}`,
		`{"workspace":"example","repoSlug":"app","pullRequestId":7,"filters":{"resolution":null},"count":1,"comments":[{"id":301,"content":{"raw":"Full reply"},"created_on":"2026-01-01T11:00:00Z"}]}`,
		`{"pullRequestId":7,"count":0,"comments":[]}`,
	} {
		rows, err := parseBitbucketBundleComments([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) > 0 && rows[0].Content.Raw != "Full reply" {
			t.Fatalf("lost content: %+v", rows)
		}
	}
}
func TestBitbucketCommentReadContractRefusesUnprovenCollections(t *testing.T) {
	cases := []string{
		`[]`, `{}`, `null`, `{"comments":[]}`, `{"count":0}`, `{"count":0,"comments":null}`,
		`{"count":0,"comments":{}}`, `{"count":-1,"comments":[]}`, `{"count":1,"comments":[]}`,
		`{"count":0,"comments":[{"content":{"raw":"x"}}]}`, `{"count":"0","comments":[]}`, `{"count":0.5,"comments":[]}`,
		`{"count":0,"comments":[],"next":"page2"}`, `{"count":0,"comments":[],"next":null}`,
		`{"count":0,"comments":[],"size":0}`, `{"count":0,"comments":[],"pagelen":50}`,
		`{"count":0,"comments":[],"values":[]}`, `{"count":0,"comments":[],"hasMore":false}`, `{"count":0,"comments":[],"unknown":true}`,
		`{"count":1,"comments":[null]}`, `{"count":1,"comments":[{}]}`, `{"count":1,"comments":[{"content":{"raw":4}}]}`,
		`{"count":1,"comments":[{"body":"not REST content"}]}`, `{"count":0,"count":1,"comments":[]}`, `{"count":0,"comments":[]} trailing`,
	}
	rows := make([]map[string]any, 1000)
	for i := range rows {
		rows[i] = map[string]any{"content": map[string]string{"raw": "x"}}
	}
	capJSON, _ := json.Marshal(map[string]any{"count": 1000, "comments": rows})
	cases = append(cases, string(capJSON))
	for i, raw := range cases {
		t.Run(string(rune('A'+i)), func(t *testing.T) {
			if _, err := parseBitbucketBundleComments([]byte(raw)); err == nil {
				t.Fatalf("accepted unproven collection: %.180s", raw)
			}
		})
	}
}
func bitbucketReadContractCLI(t *testing.T, raw string, fail bool) string {
	t.Helper()
	dir := t.TempDir()
	response := filepath.Join(dir, "response.json")
	log := filepath.Join(dir, "calls")
	if err := os.WriteFile(response, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$CONTRACT_CALLS"
[ "$*" = '--workspace example --repo app pr comments list 7 --limit 1000 --json' ] || exit 90
[ "$CONTRACT_FAIL" != yes ] || exit 9
/bin/cat "$CONTRACT_RESPONSE"
`
	if err := os.WriteFile(filepath.Join(dir, "bb"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("CONTRACT_CALLS", log)
	t.Setenv("CONTRACT_RESPONSE", response)
	flag := "no"
	if fail {
		flag = "yes"
	}
	t.Setenv("CONTRACT_FAIL", flag)
	return log
}
func TestBitbucketCommentReadContractSharedArgsAndDecodedMarkers(t *testing.T) {
	key := "0123456789"
	raw := `{"pullRequestId":7,"count":2,"comments":[{"content":{"raw":"` + strings.Repeat("Full content ", 30) + `\u0077tc-review-inline v1 key=` + key + `"}},{"content":{"raw":"wtc-review-inline v1 key=` + key + `"}}]}`
	log := bitbucketReadContractCLI(t, raw, false)
	dir := t.TempDir()
	m := ReviewManifest{PR: "7", Forge: "bitbucket", Slug: "example/app", RepoDir: dir}
	keys := priorReviewInlineKeys(m, dir)
	if len(keys) != 1 || !keys[key] {
		t.Fatalf("decoded/deduplicated markers: %v", keys)
	}
	copyPublicReviewComments(dir, m)
	calls, err := os.ReadFile(log)
	want := "--workspace example --repo app pr comments list 7 --limit 1000 --json\n"
	if err != nil || string(calls) != want+want {
		t.Fatalf("shared read arguments: %q %v", calls, err)
	}
}
func TestBitbucketCommentReadContractIgnoresMetadataMarkers(t *testing.T) {
	raw := `{"workspace":"wtc-review-inline v1 key=0123456789","repoSlug":"app","pullRequestId":7,"filters":{"resolution":null},"count":0,"comments":[]}`
	bitbucketReadContractCLI(t, raw, false)
	keys := priorReviewInlineKeys(ReviewManifest{PR: "7", Forge: "bitbucket", Slug: "example/app", RepoDir: t.TempDir()}, t.TempDir())
	if keys == nil || len(keys) != 0 {
		t.Fatalf("metadata became a finding: %v", keys)
	}
}
func TestBitbucketCommentReadContractUncertainReadsPreventCreates(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		fail      bool
	}{
		{"read-error", `{"count":0,"comments":[]}`, true}, {"missing-count", `{"comments":[]}`, false},
		{"REST-page", `{"values":[],"next":"page2","size":1}`, false}, {"partial", `{"count":2,"comments":[]}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			log := bitbucketReadContractCLI(t, tc.raw, tc.fail)
			bundle := t.TempDir()
			os.Mkdir(filepath.Join(bundle, "findings"), 0700)
			os.WriteFile(filepath.Join(bundle, "findings/code.json"), []byte(`{"concern":"code","findings":[{"file":"sample.go","line":4,"title":"Synthetic finding"}]}`), 0600)
			m := ReviewManifest{PR: "7", Forge: "bitbucket", Slug: "example/app", RepoDir: bundle}
			for i := 0; i < 2; i++ {
				posted, failed, _ := postReviewInline(m, bundle)
				if posted != 0 || failed != 1 {
					t.Fatalf("not pending: posted%d failed%d", posted, failed)
				}
			}
			calls, _ := os.ReadFile(log)
			if strings.Count(string(calls), "\n") != 2 || strings.Contains(string(calls), "comments add") {
				t.Fatalf("write after uncertain read: %q", calls)
			}
		})
	}
}
