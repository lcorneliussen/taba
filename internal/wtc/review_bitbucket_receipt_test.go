package wtc

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReviewCommentIDTextKeepsExactDecimalIdentity(t *testing.T) {
	for raw, want := range map[string]string{
		``:                     "",
		`null`:                 "",
		`640123457`:            "640123457",
		`"640123457"`:          "640123457",
		`9007199254740993`:     "9007199254740993", // 2^53+1: float64 would round it
		`"9007199254740993"`:   "9007199254740993",
		`18446744073709551617`: "18446744073709551617", // beyond uint64
		` 640123457 `:          "640123457",
		`"640"`:                "640",
	} {
		got, err := reviewCommentIDText(json.RawMessage(raw))
		if err != nil || got != want {
			t.Errorf("%s: %q %v, want %q", raw, got, err, want)
		}
	}
	for _, raw := range []string{`0`, `"0"`, `-1`, `"-1"`, `1.0`, `1.5`, `6.4e8`, `1E3`, `"6.4e8"`, `"1.0"`, `01`, `"01"`, `""`, `" 1"`, `"+1"`, `true`, `{}`, `[]`, `"0x10"`, `"1 "`, `"` + strings.Repeat("9", 40) + `"`} {
		if got, err := reviewCommentIDText(json.RawMessage(raw)); err == nil {
			t.Errorf("%s accepted as %q", raw, got)
		}
	}
}

func TestParseBitbucketCreatedCommentShapes(t *testing.T) {
	link := func(id string) string {
		return `"links":{"html":{"href":"https://bitbucket.org/example/app/pull-requests/7#comment-` + id + `"}}`
	}
	big := "9007199254740993"
	for name, tc := range map[string]struct{ raw, want, url string }{
		"bb-2.2.1":       {`{"success":true,"pullRequestId":7,"comment":{"id":640123457,` + link("640123457") + `}}`, "640123457", "#comment-640123457"},
		"bb-inline":      {`{"success":true,"pullRequestId":7,"comment":{"id":640123457},"inline":{"path":"a.go","to":4}}`, "640123457", ""},
		"rest":           {`{"id":640123457,` + link("640123457") + `}`, "640123457", "#comment-640123457"},
		"top-and-nested": {`{"id":640123457,"success":true,"pullRequestId":7,"comment":{"id":640123457}}`, "640123457", ""},
		"quoted":         {`{"success":true,"pullRequestId":"7","comment":{"id":"640123457"}}`, "640123457", ""},
		"beyond-2^53":    {`{"success":true,"pullRequestId":7,"comment":{"id":` + big + `,` + link(big) + `}}`, big, "#comment-" + big},
		"link-only":      {`{"success":true,"pullRequestId":7,"comment":{` + link("640123457") + `}}`, "640123457", "#comment-640123457"},
		"null-error":     {`{"id":640123457,"error":null}`, "640123457", ""},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := parseBitbucketCreatedComment([]byte(tc.raw), "7", "")
			if err != nil || got.CommentID != tc.want || !strings.HasSuffix(got.URL, tc.url) {
				t.Fatalf("%+v %v", got, err)
			}
		})
	}
	for name, raw := range map[string]string{
		"conflict":       `{"id":1,"comment":{"id":2}}`,
		"link-conflict":  `{"comment":{"id":2,` + link("3") + `}}`,
		"top-link":       `{"id":2,` + link("3") + `,"comment":{"id":2}}`,
		"other-pr":       `{"pullRequestId":8,"comment":{"id":2}}`,
		"bad-pr":         `{"pullRequestId":7.0,"comment":{"id":2}}`,
		"failed":         `{"success":false,"comment":{"id":2}}`,
		"success-string": `{"success":"true","comment":{"id":2}}`,
		"error":          `{"type":"error","error":{"message":"denied"}}`,
		"error-with-id":  `{"id":2,"error":"denied"}`,
		"empty":          `{}`,
		"array":          `[{"id":2}]`,
		"exponent":       `{"comment":{"id":2e3}}`,
		"trailing":       `{"id":2}x`,
	} {
		if got, err := parseBitbucketCreatedComment([]byte(raw), "7", ""); err == nil {
			t.Errorf("%s accepted as %+v", name, got)
		}
	}
	if _, err := parseBitbucketCreatedComment([]byte(`{"id":640123458}`), "7", "640123457"); err == nil {
		t.Error("update response for another comment accepted")
	}
}

func TestReviewStatusCommentIDsAreExact(t *testing.T) {
	head := "1234567890abcdef1234567890abcdef12345678"
	line := `wtc-review v1 head=` + head + ` verdict=pass blockers=0 round=1`
	comment := func(id, href string) string {
		return `{"pullRequestId":7,"count":1,"comments":[{"id":` + id + `,"content":{"raw":"` + line + `"},"created_on":"2026-01-01T00:00:00Z","links":{"html":{"href":"` + href + `"}}}]}`
	}
	url := "https://bitbucket.org/example/app/pull-requests/7#comment-"
	for name, tc := range map[string]struct{ raw, want string }{
		"number":        {comment("640123457", url+"640123457"), "640123457"},
		"quoted":        {comment(`"640123457"`, url+"640123457"), "640123457"},
		"beyond-2^53":   {comment("9007199254740993", url+"9007199254740993"), "9007199254740993"},
		"link-only":     {comment("null", url+"640123457"), "640123457"},
		"no-link":       {comment("640123457", ""), "640123457"},
		"exponent":      {comment("6.40123457e8", url+"640123457"), ""},
		"contradiction": {comment("640123458", url+"640123457"), ""},
		"github-link":   {comment("640123457", "https://bitbucket.org/x#issuecomment-1"), "640123457"},
	} {
		t.Run(name, func(t *testing.T) {
			status, err := ParseReviewStatusComments([]byte(tc.raw), 7, head, "bitbucket", "example/app", t.TempDir(), false)
			if err != nil || status.State != "current" || status.CommentID != tc.want {
				t.Fatalf("%+v %v", status, err)
			}
		})
	}
	collection := t.TempDir()
	receipt := reviewReceiptPath(collection, "bitbucket", "example/app", "7", head, "9007199254740993", "pass")
	if err := os.MkdirAll(filepath.Dir(receipt), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(receipt, nil, 0600); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{"9007199254740993": "current", "9007199254740992": "untrusted", "9007199254740993.0": "untrusted"} {
		status, err := ParseReviewStatusComments([]byte(comment(id, "")), 7, head, "bitbucket", "example/app", collection, true)
		if err != nil || status.State != want {
			t.Errorf("%s: %+v %v, want %s", id, status, err, want)
		}
	}
	// A receipt for another PR, head, repository or forge is not this one.
	for _, other := range [][2]string{{"github", "example/app"}, {"bitbucket", "example/other"}} {
		status, _ := ParseReviewStatusComments([]byte(comment("9007199254740993", "")), 7, head, other[0], other[1], collection, true)
		if status.State != "untrusted" {
			t.Errorf("%v trusted another receipt: %+v", other, status)
		}
	}
	if status, _ := ParseReviewStatusComments([]byte(comment("9007199254740993", "")), 8, head, "bitbucket", "example/app", collection, true); status.State != "untrusted" {
		t.Errorf("other PR trusted: %+v", status)
	}
	if _, err := ParseReviewStatusComments([]byte(comment("1", "")+" []"), 7, head, "bitbucket", "example/app", collection, false); err == nil {
		t.Error("trailing data accepted")
	}
}

func TestReviewStatusGitHubUsesLinkNotNodeID(t *testing.T) {
	head := "1234567890abcdef1234567890abcdef12345678"
	raw := `{"comments":[{"id":"IC_kwDOABCDEF","body":"wtc-review v1 head=` + head + ` verdict=pass blockers=0 round=1","createdAt":"2026-01-01T00:00:00Z","url":"https://github.com/example/app/pull/7#issuecomment-640123457"}]}`
	status, err := ParseReviewStatusComments([]byte(raw), 7, head, "github", "example/app", t.TempDir(), false)
	if err != nil || status.CommentID != "640123457" {
		t.Fatalf("%+v %v", status, err)
	}
}

func TestBitbucketInlineCreateReadsNestedEnvelope(t *testing.T) {
	bundle := t.TempDir()
	if err := os.Mkdir(filepath.Join(bundle, "findings"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bundle, "findings/code.json"), []byte(`{"concern":"code","findings":[{"file":"sample.go","line":4,"title":"Synthetic finding"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	script := `#!/bin/sh
shift 4
case "$*" in
  'pr comments list 7 --limit 1000 --json') printf '{"pullRequestId":7,"count":0,"comments":[]}\n' ;;
  'pr comments add 7 '*' --file sample.go --line-to 4 --json') printf '{"success":true,"pullRequestId":7,"comment":{"id":640123460,"links":{"html":{"href":"https://bitbucket.org/example/app/pull-requests/7#comment-640123460"}}},"inline":{"path":"sample.go","to":4}}\n' ;;
  *) exit 4 ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "bb"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"BB_API_USER", "BB_API_PASS", "BB_USERNAME", "BB_API_TOKEN"} {
		t.Setenv(key, "")
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	posted, failed, _ := postReviewInline(ReviewManifest{PR: "7", Forge: "bitbucket", Slug: "example/app", RepoDir: bundle}, bundle)
	rows := readInlineRecords(bundle)
	if posted != 1 || failed != 0 || len(rows) != 1 || rows[0].ID != "640123460" || !strings.HasSuffix(rows[0].URL, "#comment-640123460") {
		t.Fatalf("posted=%d failed=%d rows=%+v", posted, failed, rows)
	}
}
