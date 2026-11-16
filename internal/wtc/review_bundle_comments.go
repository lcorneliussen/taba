package wtc

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"taba.sh/taba/internal/branding"
)

type bundleComment struct {
	Body    string `json:"body"`
	When    string `json:"createdAt"`
	Content struct {
		Raw string `json:"raw"`
	} `json:"content"`
	Author struct {
		Login string `json:"login"`
	} `json:"author"`
	User struct {
		Login       string `json:"login"`
		DisplayName string `json:"display_name"`
		Nickname    string `json:"nickname"`
	} `json:"user"`
	CreatedAt string `json:"created_at"`
	CreatedOn string `json:"created_on"`
}

func (c bundleComment) body() string {
	if c.Content.Raw != "" {
		return c.Content.Raw
	}
	return c.Body
}

func (c bundleComment) author() string {
	if c.User.DisplayName != "" {
		return c.User.DisplayName
	}
	if c.Author.Login != "" {
		return c.Author.Login
	}
	if c.User.Login != "" {
		return c.User.Login
	}
	if c.User.Nickname != "" {
		return c.User.Nickname
	}
	return "unknown"
}

func (c bundleComment) when() string {
	if c.CreatedOn != "" {
		return c.CreatedOn
	}
	if c.When != "" {
		return c.When
	}
	return c.CreatedAt
}

func isBundleStatusComment(body string) bool {
	lines := strings.Split(strings.TrimSpace(body), "\n")
	if len(lines) < 2 || !strings.Contains(lines[0], "**Local review:") {
		return false
	}
	status := strings.TrimSpace(lines[len(lines)-1])
	return strings.HasPrefix(status, "`wtc-review v1 ") && strings.HasSuffix(status, "`") && reviewStatusLine.MatchString(status)
}

func isBundleInlineFinding(body string) bool {
	lines := strings.Split(strings.TrimSpace(body), "\n")
	if len(lines) < 2 {
		return false
	}
	first := lines[0]
	knownSeverity := false
	for _, severity := range []string{"blocker", "major", "minor", "nit"} {
		if strings.HasPrefix(first, "**"+severity+"** · ") {
			knownSeverity = true
			break
		}
	}
	last := strings.TrimSpace(lines[len(lines)-1])
	return knownSeverity && strings.HasPrefix(last, "`wtc-review-inline v1 key=") && strings.HasSuffix(last, "`") && reviewInlineMarker.MatchString(last)
}

// Forge comments are optional review context. An unavailable forge leaves a
// usable local bundle, while posting still verifies the current remote head.
func copyPublicReviewComments(dir string, manifest ReviewManifest) {
	if manifest.PR == "" {
		return
	}
	if manifest.Forge == "bitbucket" {
		comments, err := readBitbucketReviewComments(manifest)
		if err != nil {
			fmt.Fprintf(os.Stderr, branding.Text("{{command}}: warning: Bitbucket review comments unavailable: %v\n"), err)
			return
		}
		if err := writeBundleComments(dir, comments); err != nil {
			fmt.Fprintf(os.Stderr, branding.Text("{{command}}: warning: Bitbucket review comments could not be saved: %v\n"), err)
		}
		return
	}
	if manifest.Forge != "github" {
		return
	}
	conversation, err := reviewForgeCommand(manifest.RepoDir, "gh", []string{"pr", "view", manifest.PR, "--repo", manifest.Slug, "--json", "comments"}, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, branding.Text("{{command}}: warning: review comments unavailable: %v\n"), err)
		return
	}
	inline, err := reviewForgeCommand(manifest.RepoDir, "gh", []string{"api", fmt.Sprintf("repos/%s/pulls/%s/comments?per_page=100", manifest.Slug, manifest.PR), "--paginate", "--slurp"}, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, branding.Text("{{command}}: warning: inline review comments unavailable: %v\n"), err)
		return
	}
	comments, err := parseBundleComments(conversation, inline)
	if err != nil {
		fmt.Fprintf(os.Stderr, branding.Text("{{command}}: warning: review comments could not be parsed: %v\n"), err)
		return
	}
	if err := writeBundleComments(dir, comments); err != nil {
		fmt.Fprintf(os.Stderr, branding.Text("{{command}}: warning: review comments could not be saved: %v\n"), err)
	}
}

const bitbucketReviewCommentLimit = 1000

// Both supported community CLI versions collect pages into a comments/count envelope.
func readBitbucketReviewComments(m ReviewManifest) ([]bundleComment, error) {
	raw, err := reviewBitbucketCommand(m, []string{"pr", "comments", "list", m.PR, "--limit", strconv.Itoa(bitbucketReviewCommentLimit), "--json"}, nil)
	if err != nil {
		return nil, err
	}
	return parseBitbucketBundleComments(raw)
}

func parseBitbucketBundleComments(raw []byte) ([]bundleComment, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, fmt.Errorf("Bitbucket comments need a CLI collection wrapper")
	}
	fields := map[string]json.RawMessage{}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		name, ok := token.(string)
		if !ok {
			return nil, fmt.Errorf("invalid Bitbucket comments field")
		}
		switch name {
		case "comments", "count", "pullRequestId", "workspace", "repoSlug", "filters":
		default:
			return nil, fmt.Errorf("unsupported Bitbucket comments field %q; completeness is unknown", name)
		}
		if _, exists := fields[name]; exists {
			return nil, fmt.Errorf("duplicate Bitbucket comments field %q", name)
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		fields[name] = value
	}
	if _, err := decoder.Token(); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, fmt.Errorf("invalid trailing Bitbucket comments data")
	}
	var count int
	countRaw, hasCount := fields["count"]
	if !hasCount || bytes.Equal(countRaw, []byte("null")) || json.Unmarshal(countRaw, &count) != nil || count < 0 {
		return nil, fmt.Errorf("Bitbucket comments need a nonnegative collection count")
	}
	var rows []json.RawMessage
	commentsRaw := bytes.TrimSpace(fields["comments"])
	if len(commentsRaw) == 0 || commentsRaw[0] != '[' || json.Unmarshal(commentsRaw, &rows) != nil {
		return nil, fmt.Errorf("Bitbucket comments need a comments array")
	}
	if count != len(rows) {
		return nil, fmt.Errorf("Bitbucket returned %d of %d comments", len(rows), count)
	}
	// The CLI count is collected length. Reaching the requested cap therefore
	// cannot establish completeness, even when count matches the array length.
	if len(rows) >= bitbucketReviewCommentLimit {
		return nil, fmt.Errorf("Bitbucket returned the %d-comment limit; review context may be incomplete", bitbucketReviewCommentLimit)
	}
	comments := make([]bundleComment, 0, len(rows))
	for _, row := range rows {
		var shape struct {
			Content *struct {
				Raw *string `json:"raw"`
			} `json:"content"`
		}
		if json.Unmarshal(row, &shape) != nil || shape.Content == nil || shape.Content.Raw == nil {
			return nil, fmt.Errorf("Bitbucket comment has no valid content.raw")
		}
		var comment bundleComment
		if err := json.Unmarshal(row, &comment); err != nil {
			return nil, err
		}
		// A Bitbucket comment's text comes only from REST content.raw. The body
		// field belongs to GitHub and must not supply markers or transcript text.
		comment.Body = ""
		comments = append(comments, comment)
	}
	sort.SliceStable(comments, func(i, j int) bool { return comments[i].when() < comments[j].when() })
	return comments, nil
}

func parseBundleComments(conversation, inline []byte) ([]bundleComment, error) {
	var outer struct {
		Comments []bundleComment `json:"comments"`
	}
	if err := json.Unmarshal(conversation, &outer); err != nil {
		return nil, err
	}
	comments := outer.Comments
	var pages []json.RawMessage
	if err := json.Unmarshal(inline, &pages); err != nil {
		return nil, err
	}
	for _, page := range pages {
		var rows []bundleComment
		if err := json.Unmarshal(page, &rows); err != nil {
			return nil, err
		}
		comments = append(comments, rows...)
	}
	sort.SliceStable(comments, func(i, j int) bool { return comments[i].when() < comments[j].when() })
	return comments, nil
}

func writeBundleComments(dir string, comments []bundleComment) error {
	if len(comments) == 0 {
		return nil
	}
	prior := filepath.Join(dir, "prior")
	if err := os.MkdirAll(prior, 0755); err != nil {
		return err
	}
	latestReview := -1
	keys := map[string]bool{}
	for i, comment := range comments {
		body := comment.body()
		if isBundleStatusComment(body) {
			latestReview = i
		}
		for _, match := range reviewInlineMarker.FindAllStringSubmatch(body, -1) {
			keys[match[1]] = true
		}
	}
	if len(keys) > 0 {
		data, err := os.ReadFile(filepath.Join(prior, "inline-keys.txt"))
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		for _, key := range strings.Fields(string(data)) {
			if len(key) == 10 {
				keys[key] = true
			}
		}
		var list []string
		for key := range keys {
			list = append(list, key)
		}
		sort.Strings(list)
		if err := os.WriteFile(filepath.Join(prior, "inline-keys.txt"), []byte(strings.Join(list, "\n")+"\n"), 0644); err != nil {
			return err
		}
	}
	var transcript strings.Builder
	for i, comment := range comments {
		body := comment.body()
		if i <= latestReview || isBundleStatusComment(body) || isBundleInlineFinding(body) || strings.TrimSpace(body) == "" {
			continue
		}
		fmt.Fprintf(&transcript, "### %s, %s\n\n%s\n\n", comment.author(), comment.when(), strings.TrimSpace(body))
	}
	if transcript.Len() > 0 {
		return os.WriteFile(filepath.Join(prior, "comments.md"), []byte(transcript.String()), 0644)
	}
	return nil
}
