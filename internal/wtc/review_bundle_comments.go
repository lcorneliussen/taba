package wtc

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
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
		raw, err := reviewBitbucketCommand(manifest, []string{"--json", "pr", "comments", "list", manifest.PR, "--limit", "1000", "--no-truncate"}, nil)
		if err != nil {
			fmt.Fprintf(os.Stderr, branding.Text("{{command}}: warning: Bitbucket review comments unavailable: %v\n"), err)
			return
		}
		comments, err := parseBitbucketBundleComments(raw)
		if err != nil {
			fmt.Fprintf(os.Stderr, branding.Text("{{command}}: warning: Bitbucket review comments could not be parsed: %v\n"), err)
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

func parseBitbucketBundleComments(raw []byte) ([]bundleComment, error) {
	const commentLimit = 1000
	var comments []bundleComment
	if err := json.Unmarshal(raw, &comments); err != nil {
		var page struct {
			Comments []bundleComment `json:"comments"`
			Values   []bundleComment `json:"values"`
			Count    *int            `json:"count"`
		}
		if err := json.Unmarshal(raw, &page); err != nil {
			return nil, err
		}
		comments = page.Comments
		if comments == nil {
			comments = page.Values
		}
		if page.Count != nil && *page.Count > len(comments) {
			return nil, fmt.Errorf("Bitbucket returned %d of %d comments", len(comments), *page.Count)
		}
	}
	// The CLI's count can mean the number collected rather than the remote
	// total. Reaching the requested limit therefore cannot prove completeness.
	if len(comments) >= commentLimit {
		return nil, fmt.Errorf("Bitbucket returned the %d-comment limit; review context may be incomplete", commentLimit)
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
