package wtc

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

type ReviewResolveOptions struct {
	Reply   string
	File    string
	Line    int
	Concern string
	Key     string
}

type ReviewResolveResult struct {
	Resolved int `json:"resolved"`
}

func (c *Context) ResolveReviewBundle(bundle string, opt ReviewResolveOptions) (ReviewResolveResult, error) {
	if err := c.RefuseInReviewMode("resolving review threads"); err != nil {
		return ReviewResolveResult{}, err
	}
	bundle, err := filepath.Abs(bundle)
	if err != nil {
		return ReviewResolveResult{}, err
	}
	manifest, err := readReviewManifest(bundle)
	if err != nil {
		return ReviewResolveResult{}, err
	}
	worktree, slug, forge, err := c.ReviewRepo(manifest.Repo)
	if err != nil {
		return ReviewResolveResult{}, err
	}
	if worktree != manifest.RepoDir || slug != manifest.Slug || forge != manifest.Forge {
		return ReviewResolveResult{}, fmt.Errorf("bundle repository does not match this collection")
	}
	if err := reviewPublicationAllowed(manifest); err != nil {
		return ReviewResolveResult{}, err
	}
	// Replies are REST writes by repository name. The thread lookup reads by
	// name through GraphQL, which follows a rename; resolving a thread is a
	// write by node ID.
	target := manifest
	if opt.Reply != "" {
		if target, err = reviewWriteTarget(manifest); err != nil {
			return ReviewResolveResult{}, err
		}
	}
	rows := readInlineRecords(bundle)
	result := ReviewResolveResult{}
	for i := range rows {
		row := &rows[i]
		if row.ID == "" || row.Resolved || opt.Key != "" && row.Key != opt.Key || opt.Concern != "" && row.Concern != opt.Concern || opt.File != "" && row.File != opt.File || opt.Line != 0 && row.Line != opt.Line {
			continue
		}
		if !prNumber.MatchString(row.ID) {
			return result, fmt.Errorf("invalid inline comment id for %s", row.Key)
		}
		threadID := ""
		if forge == "github" {
			var already bool
			threadID, already, err = reviewThreadID(manifest, row.ID)
			if err != nil {
				return result, err
			}
			if already {
				row.Resolved = true
				row.Error = ""
				if err := writeInlineRecords(bundle, rows); err != nil {
					return result, err
				}
				result.Resolved++
				continue
			}
		}
		if opt.Reply != "" {
			if err := replyReviewInline(target, row.ID, opt.Reply); err != nil {
				return result, err
			}
		}
		if err := resolveReviewInline(manifest, row.ID, threadID); err != nil {
			return result, err
		}
		row.Resolved = true
		row.Error = ""
		if err := writeInlineRecords(bundle, rows); err != nil {
			return result, err
		}
		result.Resolved++
	}
	return result, nil
}

func replyReviewInline(m ReviewManifest, id, body string) error {
	if m.Forge == "github" {
		num, _ := strconv.Atoi(id)
		payload, _ := json.Marshal(map[string]any{"body": body, "in_reply_to": num})
		_, err := reviewGitHubAPI(m.RepoDir, []string{"-X", "POST", fmt.Sprintf("repos/%s/pulls/%s/comments", m.Slug, m.PR), "--input", "-"}, payload)
		return err
	}
	if _, ok, err := bitbucketReviewCommentAPI(m, "", body, "", 0, id); ok {
		return err
	}
	if len(body) > 8192 || strings.HasPrefix(body, "-") {
		return fmt.Errorf("Bitbucket reply requires API credentials for a long or flag-like body")
	}
	_, err := reviewBitbucketCommand(m, []string{"pr", "comments", "reply", m.PR, id, body}, nil)
	return err
}

func resolveReviewInline(m ReviewManifest, id, threadID string) error {
	if m.Forge == "bitbucket" {
		_, err := reviewBitbucketCommand(m, []string{"pr", "comments", "resolve", m.PR, id}, nil)
		return err
	}
	mutation := `mutation($id:ID!){resolveReviewThread(input:{threadId:$id}){thread{isResolved}}}`
	_, err := reviewGitHubAPI(m.RepoDir, []string{"graphql", "-f", "query=" + mutation, "-f", "id=" + threadID}, nil)
	return err
}

func reviewThreadID(m ReviewManifest, id string) (string, bool, error) {
	parts := splitReviewSlug(m.Slug)
	if len(parts) != 2 {
		return "", false, fmt.Errorf("invalid GitHub repository slug")
	}
	query := `query($o:String!,$n:String!,$num:Int!,$after:String){repository(owner:$o,name:$n){pullRequest(number:$num){reviewThreads(first:100,after:$after){pageInfo{hasNextPage endCursor} nodes{id isResolved comments(first:1){nodes{databaseId}}}}}}}`
	target, _ := strconv.ParseInt(id, 10, 64)
	cursor := ""
	for page := 0; page < 100; page++ {
		args := []string{"graphql", "-f", "query=" + query, "-f", "o=" + parts[0], "-f", "n=" + parts[1], "-F", "num=" + m.PR}
		if cursor != "" {
			args = append(args, "-f", "after="+cursor)
		}
		out, err := reviewGitHubAPI(m.RepoDir, args, nil)
		if err != nil {
			return "", false, err
		}
		var graph struct {
			Data struct {
				Repository struct {
					PullRequest struct {
						ReviewThreads struct {
							PageInfo struct {
								HasNextPage bool   `json:"hasNextPage"`
								EndCursor   string `json:"endCursor"`
							} `json:"pageInfo"`
							Nodes []struct {
								ID         string `json:"id"`
								IsResolved bool   `json:"isResolved"`
								Comments   struct {
									Nodes []struct {
										DatabaseID int64 `json:"databaseId"`
									} `json:"nodes"`
								} `json:"comments"`
							} `json:"nodes"`
						} `json:"reviewThreads"`
					} `json:"pullRequest"`
				} `json:"repository"`
			} `json:"data"`
		}
		if err := json.Unmarshal(out, &graph); err != nil {
			return "", false, err
		}
		threads := graph.Data.Repository.PullRequest.ReviewThreads
		for _, thread := range threads.Nodes {
			for _, comment := range thread.Comments.Nodes {
				if comment.DatabaseID == target {
					return thread.ID, thread.IsResolved, nil
				}
			}
		}
		if !threads.PageInfo.HasNextPage || threads.PageInfo.EndCursor == "" || threads.PageInfo.EndCursor == cursor {
			break
		}
		cursor = threads.PageInfo.EndCursor
	}
	return "", false, fmt.Errorf("GitHub review thread for comment %s not found", id)
}

func splitReviewSlug(slug string) []string {
	for i := 0; i < len(slug); i++ {
		if slug[i] == '/' {
			return []string{slug[:i], slug[i+1:]}
		}
	}
	return nil
}
