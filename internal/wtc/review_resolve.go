package wtc

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
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
	manifest.ConfigRoot = c.ConfigRoot
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
	if forge == "bitbucket" && bitbucketGateway() {
		return c.resolveReviewBrokered(manifest, bundle, rows, opt)
	}
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
	switch route, err := selectedBitbucketWrite(m.bitbucketControlRoot(), true); {
	case err != nil:
		return err
	case route == bitbucketWriteBroker:
		return errBitbucketBrokered
	case route == bitbucketWriteTWG:
		_, err := twgCommentCreate(m, body, "", 0, id)
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
		switch route, err := selectedBitbucketWrite(m.bitbucketControlRoot(), false); {
		case err != nil:
			return err
		case route == bitbucketWriteBroker:
			return errBitbucketBrokered
		case route == bitbucketWriteTWG:
			return twgCommentResolve(m, id)
		}
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

// resolveReviewBrokered asks the person to confirm the replies and resolves
// on a gateway (#471), then records the threads whose resolve was done.
func (c *Context) resolveReviewBrokered(m ReviewManifest, bundle string, rows []ReviewInlineRecord, opt ReviewResolveOptions) (ReviewResolveResult, error) {
	result := ReviewResolveResult{}
	selected := func(row ReviewInlineRecord) bool {
		return row.ID != "" && !row.Resolved && (opt.Key == "" || row.Key == opt.Key) && (opt.Concern == "" || row.Concern == opt.Concern) && (opt.File == "" || row.File == opt.File) && (opt.Line == 0 || row.Line == opt.Line)
	}
	for _, row := range rows {
		if selected(row) && !prNumber.MatchString(row.ID) {
			return result, fmt.Errorf("invalid inline comment id for %s", row.Key)
		}
	}
	// Replies already made, by thread: a retry never posts one again.
	replied := map[string]bool{}
	plan := func() (operations []brokeredOperation, keys []string) {
		for _, row := range rows {
			if !selected(row) {
				continue
			}
			id, _ := strconv.ParseInt(row.ID, 10, 64)
			if opt.Reply != "" && !replied[row.Key] {
				operations = append(operations, brokeredOperation{Op: "comment.create", Text: opt.Reply, Parent: id})
				keys = append(keys, "reply:"+row.Key)
			}
			operations = append(operations, brokeredOperation{Op: "comment.resolve", Comment: id})
			keys = append(keys, row.Key)
		}
		return operations, keys
	}
	if operations, _ := plan(); len(operations) == 0 {
		return result, nil
	}
	gitDir, err := worktreeGitDir(m.RepoDir)
	if err != nil {
		return result, err
	}
	// The record of this selection's attempts: each intent and, per
	// operation index, the thread its resolve belongs to. Receipts are applied
	// from this record, never from a recomputed plan.
	selection := brokeredHash("resolve-v2", bundle, m.PR, opt.Reply, opt.Key, opt.Concern, opt.File, strconv.Itoa(opt.Line))
	recordPath := filepath.Join(bundle, "bitbucket-resolve."+selection+".json")
	var record struct {
		Attempts []struct {
			Intent string   `json:"intent"`
			Head   string   `json:"head"`
			Keys   []string `json:"keys"`
		} `json:"attempts"`
	}
	if raw, err := os.ReadFile(recordPath); err == nil && json.Unmarshal(raw, &record) != nil {
		return result, fmt.Errorf("the brokered resolve record is unreadable; reconcile by reading the pull request")
	}
	apply := func(keys []string, receipt *brokeredReceipt) {
		for index, entry := range receipt.Results {
			if index >= len(keys) || keys[index] == "" || entry.Status != "done" {
				continue
			}
			if thread, ok := strings.CutPrefix(keys[index], "reply:"); ok {
				replied[thread] = true
				continue
			}
			for i := range rows {
				if rows[i].Key == keys[index] && !rows[i].Resolved {
					rows[i].Resolved, rows[i].Error = true, ""
					result.Resolved++
				}
			}
		}
	}
	for _, attempt := range record.Attempts {
		receipt, err := readBrokeredReceipt(gitDir, m.Slug, m.PR, attempt.Intent)
		if err != nil {
			return result, err
		}
		if receipt == nil {
			return result, brokeredPending(gitDir, attempt.Intent, m.PR)
		}
		apply(attempt.Keys, receipt)
		if err := writeInlineRecords(bundle, rows); err != nil {
			return result, err
		}
		for _, entry := range receipt.Results {
			if entry.Status == "unknown" {
				return result, fmt.Errorf("a confirmed Bitbucket resolve for PR #%s has an unknown outcome; reconcile by reading the pull request before asking again", m.PR)
			}
		}
	}
	operations, keys := plan()
	if len(operations) == 0 {
		return result, nil
	}
	if len(record.Attempts) >= 20 {
		return result, fmt.Errorf("too many incomplete Bitbucket resolve attempts for PR #%s; inspect the pull request", m.PR)
	}
	head, err := reviewPRHead(m)
	if err != nil {
		return result, fmt.Errorf("cannot verify PR head: %w", err)
	}
	// The attempt is recorded only once its request exists (or its receipt
	// does): a refused ask leaves nothing behind and is asked again later.
	// Intents are numbered by attempt, so a record lost after the request was
	// written resumes on the same intent.
	intent := fmt.Sprintf("taba-resolve-%s-%d", selection, len(record.Attempts))
	receipt, err := brokeredWrites(m.RepoDir, m.Slug, m.PR, head, intent, operations)
	if err != nil && !errors.Is(err, ErrBitbucketAwaitingConfirmation) {
		return result, err
	}
	record.Attempts = append(record.Attempts, struct {
		Intent string   `json:"intent"`
		Head   string   `json:"head"`
		Keys   []string `json:"keys"`
	}{intent, head, keys})
	if saveErr := writePrivateJSON(recordPath, record); saveErr != nil {
		return result, saveErr
	}
	if receipt != nil {
		apply(keys, receipt)
		if err := writeInlineRecords(bundle, rows); err != nil {
			return result, err
		}
	}
	return result, err
}
