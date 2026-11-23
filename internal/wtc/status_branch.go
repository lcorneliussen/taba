package wtc

import (
	"context"
	"encoding/json"
	"fmt"
)

func statusParseBranchList(forge string, raw []byte, branch string) (string, error) {
	switch forge {
	case "github.com":
		var prs []struct {
			Number      int    `json:"number"`
			HeadRefName string `json:"headRefName"`
		}
		if err := json.Unmarshal(raw, &prs); err != nil {
			return "", err
		}
		for _, pr := range prs {
			if pr.Number > 0 && pr.HeadRefName == branch {
				return fmt.Sprint(pr.Number), nil
			}
		}
	case "bitbucket.org":
		var result struct {
			PullRequests []struct {
				ID     int `json:"id"`
				Source struct {
					Branch struct {
						Name string `json:"name"`
					} `json:"branch"`
				} `json:"source"`
			} `json:"pullRequests"`
		}
		if err := json.Unmarshal(raw, &result); err != nil {
			return "", err
		}
		for _, pr := range result.PullRequests {
			if pr.ID > 0 && pr.Source.Branch.Name == branch {
				return fmt.Sprint(pr.ID), nil
			}
		}
	default:
		return "", fmt.Errorf("unsupported forge")
	}
	return "", nil
}

func statusDiscoverBranch(forge, slug, branch string) (string, error) {
	return statusDiscoverBranchContext(context.Background(), forge, slug, branch)
}

func statusDiscoverBranchContext(ctx context.Context, forge, slug, branch string) (string, error) {
	ctx = statusAuthOperation(ctx)
	if forge == "" || slug == "" || branch == "" {
		return "", nil
	}
	listed, truncated, err := statusOpenPRsContext(ctx, forge, slug)
	if err != nil {
		return "", err
	}
	for _, pr := range listed {
		if pr.Number != "" && pr.Branch == branch {
			return pr.Number, nil
		}
	}
	switch forge {
	case "github.com":
		if truncated || len(listed) >= statusOpenListLimit {
			return statusDiscoverGitHubBranchContext(ctx, slug, branch)
		}
	case "bitbucket.org":
		if truncated {
			return statusDiscoverBitbucketBranchContext(ctx, slug, branch)
		}
	}
	return "", nil
}

func statusDiscoverGitHubBranchContext(ctx context.Context, slug, branch string) (string, error) {
	ctx = statusAuthOperation(ctx)
	raw, err := statusSingleFlightContext(ctx, "branch", "github.com", slug, branch, func() ([]byte, error) {
		return statusJSONContext(ctx, "gh", "pr", "list", "--repo", slug, "--head", branch, "--state", "open", "--limit", "20", "--json", "number,headRefName")
	})
	if err != nil {
		return "", err
	}
	return statusParseBranchList("github.com", raw, branch)
}

func statusDiscoverBitbucketBranch(slug, branch string) (string, error) {
	return statusDiscoverBitbucketBranchContext(context.Background(), slug, branch)
}

func statusDiscoverBitbucketBranchContext(ctx context.Context, slug, branch string) (string, error) {
	ctx = statusAuthOperation(ctx)
	raw, err := statusSingleFlightContext(ctx, "branch", "bitbucket.org", slug, branch, func() ([]byte, error) {
		return statusFetchBBBranchContext(ctx, slug, branch)
	})
	if err != nil {
		return "", err
	}
	return statusParseBBBranchList(raw, branch)
}
