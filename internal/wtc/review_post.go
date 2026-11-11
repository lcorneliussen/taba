package wtc

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"taba.sh/taba/internal/branding"
	"time"
)

type ReviewPostOptions struct {
	Mode   string
	Body   string
	Reason string
	Force  bool
}

type ReviewPostResult struct {
	CommentID     string `json:"comment_id"`
	URL           string `json:"url"`
	InlinePosted  int    `json:"inline_posted"`
	InlineFailed  int    `json:"inline_failed"`
	InlineSkipped int    `json:"inline_skipped"`
}

type ReviewInlineRecord struct {
	Key      string `json:"key"`
	Concern  string `json:"concern"`
	File     string `json:"file"`
	Line     int    `json:"line"`
	Severity string `json:"severity"`
	Title    string `json:"title"`
	ID       string `json:"id"`
	URL      string `json:"url"`
	Error    string `json:"error"`
	Skipped  string `json:"skipped,omitempty"`
	Resolved bool   `json:"resolved"`
}

func readReviewManifest(bundle string) (ReviewManifest, error) {
	data, err := os.ReadFile(filepath.Join(bundle, "manifest.json"))
	if err != nil {
		return ReviewManifest{}, err
	}
	var manifest ReviewManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return ReviewManifest{}, err
	}
	if manifest.PR == "" || !prNumber.MatchString(manifest.PR) || len(manifest.HeadSHA) < 12 {
		return ReviewManifest{}, fmt.Errorf("review bundle has no valid PR or head")
	}
	return manifest, nil
}

func reviewForgeCommand(worktree, name string, args []string, input []byte) ([]byte, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = worktree
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if input != nil {
		cmd.Stdin = bytes.NewReader(input)
	}
	out, err := forgeOutput(cmd)
	if err != nil {
		return out, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// Pin CLI fallbacks to the same destination whose audience was checked. The
// worktree origin or a user's default bb workspace may name another repository.
func reviewBitbucketCommand(m ReviewManifest, args []string, input []byte) ([]byte, error) {
	parts := splitReviewSlug(m.Slug)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return nil, fmt.Errorf("invalid Bitbucket repository slug")
	}
	return reviewForgeCommand(m.RepoDir, "bb", append([]string{"--workspace", parts[0], "--repo", parts[1]}, args...), input)
}

func reviewPRHead(manifest ReviewManifest) (string, error) {
	var out []byte
	var err error
	if manifest.Forge == "github" {
		out, err = reviewForgeCommand(manifest.RepoDir, "gh", []string{"pr", "view", manifest.PR, "--repo", manifest.Slug, "--json", "headRefOid"}, nil)
	} else if manifest.Forge == "bitbucket" {
		out, err = reviewBitbucketCommand(manifest, []string{"pr", "view", manifest.PR, "--json"}, nil)
	} else {
		return "", fmt.Errorf("unknown forge %q", manifest.Forge)
	}
	if err != nil {
		return "", err
	}
	var p struct {
		HeadRefOid string `json:"headRefOid"`
		Source     struct {
			Commit struct {
				Hash string `json:"hash"`
			} `json:"commit"`
		} `json:"source"`
	}
	if err := json.Unmarshal(out, &p); err != nil {
		return "", err
	}
	if p.HeadRefOid != "" {
		return p.HeadRefOid, nil
	}
	if p.Source.Commit.Hash != "" {
		return p.Source.Commit.Hash, nil
	}
	return "", fmt.Errorf("PR has no head commit")
}

func (c *Context) PostReviewBundle(bundle string, opt ReviewPostOptions) (ReviewPostResult, error) {
	bundle, err := filepath.Abs(bundle)
	if err != nil {
		return ReviewPostResult{}, err
	}
	manifest, err := readReviewManifest(bundle)
	if err != nil {
		return ReviewPostResult{}, err
	}
	worktree, slug, forge, err := c.ReviewRepo(manifest.Repo)
	if err != nil {
		return ReviewPostResult{}, err
	}
	if worktree != manifest.RepoDir || slug != manifest.Slug || forge != manifest.Forge {
		return ReviewPostResult{}, fmt.Errorf("bundle repository does not match this collection")
	}
	if err := reviewPublicationAllowed(manifest); err != nil {
		return ReviewPostResult{}, err
	}
	mode := opt.Mode
	if mode == "" {
		mode = "summary"
	}
	var body string
	switch mode {
	case "summary":
		data, err := os.ReadFile(filepath.Join(bundle, "summary.md"))
		if err != nil || len(bytes.TrimSpace(data)) == 0 {
			return ReviewPostResult{}, fmt.Errorf("summary.md is missing or empty")
		}
		body = string(data)
		matches := reviewStatusLine.FindAllStringSubmatch(body, -1)
		if len(matches) != 1 || matches[0][1] != manifest.HeadSHA {
			return ReviewPostResult{}, fmt.Errorf("summary needs exactly one status line for the bundle head")
		}
	case "progress":
		if opt.Body != "" {
			data, err := os.ReadFile(opt.Body)
			if err != nil || len(bytes.TrimSpace(data)) == 0 {
				return ReviewPostResult{}, fmt.Errorf("progress body is missing or empty")
			}
			body = string(data)
		} else {
			body = fmt.Sprintf("⏳ **Local review: in progress**\n\nRound %d, head `%s`. Separate reviewers are running.\n\n`wtc-review v1 head=%s verdict=pending blockers=0 round=%d lead=pending`\n", manifest.Round, manifest.HeadSHA[:7], manifest.HeadSHA, manifest.Round)
		}
		matches := reviewStatusLine.FindAllStringSubmatch(body, -1)
		if len(matches) != 1 || matches[0][1] != manifest.HeadSHA || matches[0][2] != "pending" {
			return ReviewPostResult{}, fmt.Errorf("progress body needs one pending status line for the bundle head")
		}
	case "failed":
		body = fmt.Sprintf("❌ **Local review: failed**\n\nRound %d, head `%s`. The run did not finish; the gate stays closed.\n", manifest.Round, manifest.HeadSHA[:7])
		if opt.Reason != "" {
			body += "\nReason: " + strings.ReplaceAll(opt.Reason, "\n", " ") + "\n"
		}
		body += fmt.Sprintf("\n`wtc-review v1 head=%s verdict=error blockers=0 round=%d lead=error`\n", manifest.HeadSHA, manifest.Round)
		opt.Force = true
	default:
		return ReviewPostResult{}, fmt.Errorf("invalid review post mode %q", mode)
	}
	prHead, err := reviewPRHead(manifest)
	if err != nil && !opt.Force {
		return ReviewPostResult{}, fmt.Errorf("cannot verify PR head: %w", err)
	}
	if err == nil && !SameReviewSHA(manifest.HeadSHA, prHead) && !opt.Force {
		return ReviewPostResult{}, fmt.Errorf("stale bundle: reviewed %s, PR head %s", manifest.HeadSHA[:7], prHead[:min(7, len(prHead))])
	}
	idPath := filepath.Join(bundle, "comment.id")
	idBytes, _ := os.ReadFile(idPath)
	cid := strings.TrimSpace(string(idBytes))
	if cid != "" && !prNumber.MatchString(cid) {
		return ReviewPostResult{}, fmt.Errorf("invalid saved comment id")
	}
	if cid != "" {
		for _, verdict := range []string{"pass", "pass-with-notes", "changes-requested", "error", "pending"} {
			removeReviewReceipts(c.Collection, manifest.Forge, manifest.Slug, manifest.PR, manifest.HeadSHA, cid, verdict)
		}
	}
	body = reviewModeBody(body, manifest)
	finalBody := body
	if mode == "summary" {
		evidence, err := writeReviewChecks(bundle, manifest)
		if err != nil {
			return ReviewPostResult{}, err
		}
		body += "\n\n### Forge checks at publication\n\n" + strings.TrimPrefix(evidence.report(), "### Forge checks\n\n")
		finalBody = presentReviewSummary(body, manifest, reviewSourceFiles(manifest))
		body = fmt.Sprintf("⏳ **Local review: posting findings**\n\nRound %d, head `%s`.\n\n`wtc-review v1 head=%s verdict=pending blockers=0 round=%d lead=pending`\n", manifest.Round, manifest.HeadSHA[:7], manifest.HeadSHA, manifest.Round)
	}
	body = reviewModeBody(body, manifest)
	createPending := filepath.Join(bundle, "comment.create-pending")
	if cid == "" {
		// Record intent before sending: a lost response or interrupted process
		// must not turn the next invocation into another uncertain create.
		pending, err := os.OpenFile(createPending, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return ReviewPostResult{}, fmt.Errorf("cannot start review comment create; reconcile any prior uncertain create (comment.create-pending): %w", err)
		}
		if err := pending.Close(); err != nil {
			return ReviewPostResult{}, err
		}
	}
	posted, err := postReviewComment(manifest, cid, body)
	if err != nil {
		// A failed update is not evidence that the original comment is absent.
		// Keep the saved ID and let an explicit retry update the same comment.
		return ReviewPostResult{}, err
	}
	if posted.CommentID != "" {
		if err := os.WriteFile(idPath, []byte(posted.CommentID+"\n"), 0644); err != nil {
			return posted, err
		}
		if err := os.Remove(createPending); err != nil && !os.IsNotExist(err) {
			return posted, err
		}
	}
	if mode == "summary" {
		posted.InlinePosted, posted.InlineFailed, posted.InlineSkipped = postReviewInline(manifest, bundle)
		if posted.InlineFailed != 0 {
			return posted, fmt.Errorf("%d inline comments failed to post; review remains pending at %s", posted.InlineFailed, posted.URL)
		}
		finalPost, err := postReviewComment(manifest, posted.CommentID, finalBody)
		if err != nil {
			return posted, err
		}
		posted.CommentID, posted.URL = finalPost.CommentID, finalPost.URL
		if posted.CommentID != "" {
			receipt := reviewReceiptPath(c.Collection, manifest.Forge, manifest.Slug, manifest.PR, manifest.HeadSHA, posted.CommentID, reviewStatusLine.FindStringSubmatch(finalBody)[2])
			if err := os.MkdirAll(filepath.Dir(receipt), 0755); err != nil {
				return posted, err
			}
			if err := os.WriteFile(receipt, nil, 0600); err != nil {
				return posted, err
			}
		}
	}
	return posted, nil
}

func postReviewComment(m ReviewManifest, cid, body string) (ReviewPostResult, error) {
	if m.Forge == "github" {
		payload, _ := json.Marshal(map[string]string{"body": body})
		method := "POST"
		endpoint := fmt.Sprintf("repos/%s/issues/%s/comments", m.Slug, m.PR)
		if cid != "" {
			method = "PATCH"
			endpoint = fmt.Sprintf("repos/%s/issues/comments/%s", m.Slug, cid)
		}
		out, err := reviewForgeCommand(m.RepoDir, "gh", []string{"api", "-X", method, endpoint, "--input", "-"}, payload)
		if err != nil {
			return ReviewPostResult{}, err
		}
		var result struct {
			ID      int64  `json:"id"`
			HTMLURL string `json:"html_url"`
		}
		if err := json.Unmarshal(out, &result); err != nil || result.ID == 0 {
			return ReviewPostResult{}, fmt.Errorf("GitHub comment response has no id: %v", err)
		}
		return ReviewPostResult{CommentID: strconv.FormatInt(result.ID, 10), URL: result.HTMLURL}, nil
	}
	if result, ok, err := bitbucketReviewCommentAPI(m, cid, body, "", 0, ""); ok {
		return result, err
	}
	if len(body) > 8192 || strings.HasPrefix(body, "-") {
		return ReviewPostResult{}, fmt.Errorf("Bitbucket comment requires API credentials for a long or flag-like body")
	}
	var args []string
	if cid == "" {
		args = []string{"pr", "comments", "add", m.PR, body, "--json"}
	} else {
		args = []string{"pr", "comments", "edit", m.PR, cid, body}
	}
	out, err := reviewBitbucketCommand(m, args, nil)
	if err != nil {
		return ReviewPostResult{}, err
	}
	if cid != "" {
		return ReviewPostResult{CommentID: cid, URL: m.URL}, nil
	}
	var result struct {
		ID    int64 `json:"id"`
		Links struct {
			HTML struct {
				Href string `json:"href"`
			} `json:"html"`
		} `json:"links"`
	}
	if err := json.Unmarshal(out, &result); err != nil {
		return ReviewPostResult{}, err
	}
	if result.ID == 0 {
		return ReviewPostResult{}, fmt.Errorf("Bitbucket comment response has no id")
	}
	return ReviewPostResult{CommentID: strconv.FormatInt(result.ID, 10), URL: result.Links.HTML.Href}, nil
}

func bitbucketReviewCommentAPI(m ReviewManifest, cid, body, inlineFile string, inlineLine int, parent string) (ReviewPostResult, bool, error) {
	user, pass := os.Getenv("BB_API_USER"), os.Getenv("BB_API_PASS")
	if user == "" || pass == "" {
		user, pass = os.Getenv("BB_USERNAME"), os.Getenv("BB_API_TOKEN")
	}
	if user == "" || pass == "" {
		return ReviewPostResult{}, false, nil
	}
	parts := splitReviewSlug(m.Slug)
	if len(parts) != 2 {
		return ReviewPostResult{}, true, fmt.Errorf("invalid Bitbucket repository slug")
	}
	endpoint := "https://api.bitbucket.org/2.0/repositories/" + url.PathEscape(parts[0]) + "/" + url.PathEscape(parts[1]) + "/pullrequests/" + m.PR + "/comments"
	method := http.MethodPost
	if cid != "" {
		endpoint += "/" + cid
		method = http.MethodPut
	}
	payload := map[string]any{"content": map[string]string{"raw": body}}
	if inlineFile != "" {
		payload["inline"] = map[string]any{"path": inlineFile, "to": inlineLine}
	}
	if parent != "" {
		parentID, err := strconv.Atoi(parent)
		if err != nil {
			return ReviewPostResult{}, true, err
		}
		payload["parent"] = map[string]int{"id": parentID}
	}
	encoded, _ := json.Marshal(payload)
	request, err := http.NewRequest(method, endpoint, bytes.NewReader(encoded))
	if err != nil {
		return ReviewPostResult{}, true, err
	}
	request.SetBasicAuth(user, pass)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	response, err := (&http.Client{Timeout: 60 * time.Second}).Do(request)
	if err != nil {
		return ReviewPostResult{}, true, err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return ReviewPostResult{}, true, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return ReviewPostResult{}, true, fmt.Errorf("Bitbucket comment API returned %s: %s", response.Status, strings.TrimSpace(string(raw[:min(400, len(raw))])))
	}
	var result struct {
		ID    int64 `json:"id"`
		Links struct {
			HTML struct {
				Href string `json:"href"`
			} `json:"html"`
		} `json:"links"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return ReviewPostResult{}, true, err
	}
	if result.ID == 0 {
		return ReviewPostResult{}, true, fmt.Errorf("Bitbucket comment response has no id")
	}
	return ReviewPostResult{CommentID: strconv.FormatInt(result.ID, 10), URL: result.Links.HTML.Href}, true, nil
}

func inlineReviewKey(concern, path string, line int, title string) string {
	text := fmt.Sprintf("%s\n%s\n%d\n%s", concern, path, line, title)
	h := sha1.Sum([]byte(text))
	return hex.EncodeToString(h[:])[:10]
}

func readInlineRecords(bundle string) []ReviewInlineRecord {
	data, err := os.ReadFile(filepath.Join(bundle, "inline-comments.json"))
	if err != nil {
		return nil
	}
	var rows []ReviewInlineRecord
	_ = json.Unmarshal(data, &rows)
	return rows
}

func writeInlineRecords(bundle string, rows []ReviewInlineRecord) error {
	data, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(bundle, "inline-comments.json"), append(data, '\n'), 0644)
}

func saveInlineRecord(bundle string, rows []ReviewInlineRecord, row ReviewInlineRecord) ([]ReviewInlineRecord, error) {
	unique := make([]ReviewInlineRecord, 0, len(rows)+1)
	for _, existing := range rows {
		if existing.Key != row.Key {
			unique = append(unique, existing)
		}
	}
	unique = append(unique, row)
	return unique, writeInlineRecords(bundle, unique)
}

var reviewInlineMarker = regexp.MustCompile(`wtc-review-inline v1 key=([0-9a-f]{10})`)

func priorReviewInlineKeys(m ReviewManifest, bundle string) map[string]bool {
	keys := map[string]bool{}
	data, _ := os.ReadFile(filepath.Join(bundle, "prior", "inline-keys.txt"))
	for _, line := range strings.Split(string(data), "\n") {
		if len(line) == 10 {
			keys[line] = true
		}
	}
	if m.Forge == "github" {
		out, err := reviewForgeCommand(m.RepoDir, "gh", []string{"api", fmt.Sprintf("repos/%s/pulls/%s/comments?per_page=100", m.Slug, m.PR), "--paginate", "--slurp"}, nil)
		if err != nil {
			fmt.Fprintf(os.Stderr, branding.Text("{{command}}: read prior GitHub inline comments: %v: %s\n"), err, strings.TrimSpace(string(out)))
			return nil
		}
		for _, match := range reviewInlineMarker.FindAllStringSubmatch(string(out), -1) {
			keys[match[1]] = true
		}
	} else {
		out, err := reviewBitbucketCommand(m, []string{"pr", "comments", "list", m.PR, "--all", "--json"}, nil)
		if err != nil {
			fmt.Fprintf(os.Stderr, branding.Text("{{command}}: read prior Bitbucket inline comments: %v\n"), err)
			return nil
		}
		for _, match := range reviewInlineMarker.FindAllStringSubmatch(string(out), -1) {
			keys[match[1]] = true
		}
	}
	return keys
}

func postReviewInline(m ReviewManifest, bundle string) (posted, failed, skipped int) {
	prior := priorReviewInlineKeys(m, bundle)
	if prior == nil {
		fmt.Fprintln(os.Stderr, branding.Text("{{command}}: could not read prior inline comments; review remains pending"))
		return 0, 1, 0
	}
	rows := readInlineRecords(bundle)
	previouslySkipped := map[string]bool{}
	for _, row := range rows {
		if row.ID != "" {
			prior[row.Key] = true
		}
		if row.Skipped != "" {
			previouslySkipped[row.Key] = true
		}
	}
	var diff reviewDiffLines
	diffLoaded := false
	paths, _ := filepath.Glob(filepath.Join(bundle, "findings", "*.json"))
	sort.Strings(paths)
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			failed++
			continue
		}
		var file struct {
			Concern  string `json:"concern"`
			Findings []struct {
				Severity   string `json:"severity"`
				File       string `json:"file"`
				Line       int    `json:"line"`
				Title      string `json:"title"`
				Detail     string `json:"detail"`
				Suggestion string `json:"suggestion"`
				Prior      string `json:"prior"`
			} `json:"findings"`
		}
		if json.Unmarshal(data, &file) != nil {
			failed++
			continue
		}
		for _, f := range file.Findings {
			if f.Prior == "addressed" || f.Line < 1 || f.File == "" || filepath.IsAbs(f.File) || strings.Contains(f.File, "..") || f.Title == "" {
				continue
			}
			key := inlineReviewKey(file.Concern, f.File, f.Line, f.Title)
			if prior[key] {
				continue
			}
			prior[key] = true
			if previouslySkipped[key] {
				skipped++
				continue
			}
			row := ReviewInlineRecord{Key: key, Concern: file.Concern, File: f.File, Line: f.Line, Severity: f.Severity, Title: f.Title}
			if m.Forge == "github" {
				if !diffLoaded {
					data, err := os.ReadFile(filepath.Join(bundle, "diff.patch"))
					if err == nil {
						diff, err = parseReviewDiff(data)
					}
					if err != nil {
						fmt.Fprintf(os.Stderr, branding.Text("{{command}}: cannot validate inline positions: %v\n"), err)
						return posted, failed + 1, skipped
					}
					diffLoaded = true
				}
				if !diff.contains(f.File, f.Line) {
					row.Skipped = "not in diff"
					rows, err = saveInlineRecord(bundle, rows, row)
					if err != nil {
						failed++
					}
					skipped++
					fmt.Fprintf(os.Stderr, branding.Text("{{command}}: inline %s:%d skipped: not in diff (finding remains in summary)\n"), f.File, f.Line)
					continue
				}
			}
			body := fmt.Sprintf("**%s** · `%s` — %s\n\n%s\n", f.Severity, file.Concern, f.Title, f.Detail)
			if f.Suggestion != "" {
				body += "\nSuggestion: " + f.Suggestion + "\n"
			}
			body += fmt.Sprintf("\nRound %d.\n\n`wtc-review-inline v1 key=%s concern=%s file=%s line=%d`\n", m.Round, key, file.Concern, f.File, f.Line)
			if m.Forge == "github" {
				payload, _ := json.Marshal(map[string]any{"body": body, "commit_id": m.HeadSHA, "path": f.File, "line": f.Line, "side": "RIGHT"})
				out, err := reviewForgeCommand(m.RepoDir, "gh", []string{"api", "-X", "POST", fmt.Sprintf("repos/%s/pulls/%s/comments", m.Slug, m.PR), "--input", "-"}, payload)
				if err == nil {
					var result struct {
						ID      int64  `json:"id"`
						HTMLURL string `json:"html_url"`
					}
					if json.Unmarshal(out, &result) == nil && result.ID != 0 {
						row.ID = strconv.FormatInt(result.ID, 10)
						row.URL = result.HTMLURL
					}
				}
				if reviewUnresolvablePosition(out, err) {
					row.Skipped = "not in diff"
				} else if row.ID == "" {
					row.Error = "GitHub returned no inline comment id"
					if err != nil {
						row.Error = err.Error()
						if len(bytes.TrimSpace(out)) != 0 {
							row.Error += ": " + strings.TrimSpace(string(out))
						}
					}
				}
			} else {
				if result, ok, err := bitbucketReviewCommentAPI(m, "", body, f.File, f.Line, ""); ok {
					if err == nil {
						row.ID, row.URL = result.CommentID, result.URL
					} else {
						row.Error = err.Error()
					}
				} else if len(body) <= 8192 && !strings.HasPrefix(body, "-") {
					out, err := reviewBitbucketCommand(m, []string{"pr", "comments", "add", m.PR, body, "--file", f.File, "--line-to", strconv.Itoa(f.Line), "--json"}, nil)
					if err == nil {
						var result struct {
							ID int64 `json:"id"`
						}
						if json.Unmarshal(out, &result) == nil && result.ID != 0 {
							row.ID = strconv.FormatInt(result.ID, 10)
						}
					} else {
						row.Error = err.Error()
					}
				}
				if row.ID == "" && row.Error == "" {
					row.Error = "post failed"
				}
			}
			if row.Skipped != "" {
				skipped++
				fmt.Fprintf(os.Stderr, branding.Text("{{command}}: inline %s:%d skipped: not in diff (GitHub could not resolve position)\n"), f.File, f.Line)
			} else if row.ID == "" {
				failed++
				fmt.Fprintf(os.Stderr, branding.Text("{{command}}: inline %s:%d: %s\n"), f.File, f.Line, row.Error)
			} else {
				posted++
			}
			rows, err = saveInlineRecord(bundle, rows, row)
			if err != nil {
				failed++
			}
		}
	}
	return posted, failed, skipped
}
