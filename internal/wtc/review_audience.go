package wtc

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Audience is fresh forge evidence, never inferred from authentication, the
// harness visibility, or a cached PR. Missing fields must not mean private.
func reviewRepositoryAudience(root, worktree, forge, slug string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var raw []byte
	var err error
	switch forge {
	case "github":
		cmd := exec.CommandContext(ctx, "gh", "repo", "view", slug, "--json", "visibility")
		cmd.Dir = worktree
		raw, err = forgeOutput(cmd)
	case "bitbucket":
		raw, err = bitbucketReadRepo(bitbucketReadOperation(ctx, root), slug)
	default:
		return "unknown"
	}
	if err != nil {
		return "unknown"
	}
	var repo struct {
		Visibility string `json:"visibility"`
		Private    *bool  `json:"is_private"`
	}
	if json.Unmarshal(raw, &repo) != nil {
		return "unknown"
	}
	if forge == "github" {
		switch repo.Visibility {
		case "PUBLIC":
			return "public"
		case "PRIVATE", "INTERNAL":
			return "private"
		}
	} else if repo.Private != nil {
		if *repo.Private {
			return "private"
		}
		return "public"
	}
	return "unknown"
}

func (c *Context) reviewBundleMode(opt ReviewBundleOptions, forge, slug, audience string) (bool, string) {
	if opt.Public {
		return true, "explicit --public"
	}
	if opt.Private {
		return false, "explicit --private"
	}
	if audience != "private" {
		return true, audience + " audience"
	}
	_, harnessSlug, harnessForge, err := c.ReviewRepo("harness")
	target, harness := splitReviewSlug(slug), splitReviewSlug(harnessSlug)
	if err != nil || len(target) != 2 || len(harness) != 2 {
		return true, "harness namespace unknown"
	}
	if forge != harnessForge || !strings.EqualFold(target[0], harness[0]) {
		return true, "outside harness namespace"
	}
	return false, "private audience in harness namespace"
}

func (m ReviewManifest) Mode() string {
	if m.Public {
		return "public"
	}
	return "private"
}

func reviewModeBody(body string, m ReviewManifest) string {
	var lines []string
	for _, line := range strings.Split(body, "\n") {
		if line != "Review bundle mode: **public**." && line != "Review bundle mode: **private**." {
			lines = append(lines, line)
		}
	}
	first, rest, _ := strings.Cut(strings.Join(lines, "\n"), "\n")
	return first + "\n\nReview bundle mode: **" + m.Mode() + "**.\n" + rest
}

// Force only overrides the stale-head check. It cannot override this boundary,
// including for failed/progress posts or bundles made before audience discovery.
func reviewPublicationAllowed(m ReviewManifest) error {
	if !m.Public && reviewRepositoryAudience(m.ConfigRoot, m.RepoDir, m.Forge, m.Slug) != "private" {
		return fmt.Errorf("refusing to publish a private review bundle to a public or unknown audience; rebuild with --public and rerun the review")
	}
	return nil
}
