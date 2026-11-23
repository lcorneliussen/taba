package wtc

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"taba.sh/taba/internal/branding"
)

// A GitHub write to a renamed repository's old REST path is answered with a
// redirect that gh does not replay for POST or PATCH. Reads follow the rename,
// so a review can be built and checked under the old name and still fail to
// post. Review writes therefore go to the repository's verified canonical
// name, while the bundle, its receipts and the status gate stay keyed by the
// configured slug they were made with.
//
// The identity rule matches status rename recovery (pr_identity.go): the old
// and the canonical name and the PR's base repository must be one immutable
// repository ID. Write authority needs more than that agreement: the ID must
// have been recorded when the bundle was built, so a reused name cannot inherit
// a review made for another repository. A redirect, a URL, a basename, a PR
// number or matching commits are never identity.

const reviewIdentityQuery = `query($owner:String!,$name:String!,$number:Int!){repository(owner:$owner,name:$name){id nameWithOwner pullRequest(number:$number){number baseRepository{id nameWithOwner}}}}`

const reviewCanonicalQuery = `query($owner:String!,$name:String!){repository(owner:$owner,name:$name){id nameWithOwner}}`

type reviewGitHubIdentity struct {
	// ID is the immutable GitHub node ID of the repository.
	ID string
	// Slug is the owner/name review writes use: the configured slug when it
	// is still the repository's name, else its verified canonical name.
	Slug string
	// Renamed reports that Slug differs from the configured slug.
	Renamed bool
}

type reviewGitHubRepositoryNode struct {
	ID   string `json:"id"`
	Name string `json:"nameWithOwner"`
}

func (r reviewGitHubRepositoryNode) valid() bool {
	parts := strings.Split(r.Name, "/")
	return r.ID != "" && len(r.ID) <= 512 && !strings.ContainsAny(r.ID, " \t\r\n") &&
		len(parts) == 2 && statusPRForgePart(parts[0]) && statusPRForgePart(parts[1])
}

// reviewGitHubAPI runs one gh API call against github.com. ReviewRepo maps
// only github.com remotes to the github forge, so an inherited GH_HOST must not
// send identity checks or review writes to another host.
var reviewGitHubAPI = func(dir string, args []string, input []byte) ([]byte, error) {
	return reviewForgeCommand(dir, "gh", append(append([]string{"api"}, args...), "--hostname", "github.com"), input)
}

// resolveReviewGitHubIdentity observes the repository a review targets.
// recorded is the repository ID saved in the bundle; empty for bundles made
// before IDs were recorded, or when the ID was unavailable at build time.
func resolveReviewGitHubIdentity(dir, slug, number, recorded string) (reviewGitHubIdentity, error) {
	parts := splitReviewSlug(slug)
	if len(parts) != 2 || !statusPRForgePart(parts[0]) || !statusPRForgePart(parts[1]) {
		return reviewGitHubIdentity{}, fmt.Errorf("invalid GitHub repository slug %q", slug)
	}
	n, err := prIdentityGitHubNumber(number)
	if err != nil {
		return reviewGitHubIdentity{}, err
	}
	raw, err := reviewGitHubAPI(dir, []string{"graphql", "-f", "query=" + reviewIdentityQuery, "-f", "owner=" + parts[0], "-f", "name=" + parts[1], "-F", "number=" + number}, nil)
	if err != nil {
		return reviewGitHubIdentity{}, fmt.Errorf("cannot verify GitHub repository identity of %s: %w; check gh auth status and network access, then retry", slug, err)
	}
	var result struct {
		Errors []json.RawMessage `json:"errors"`
		Data   struct {
			Repository *struct {
				reviewGitHubRepositoryNode
				PullRequest *struct {
					Number int64                      `json:"number"`
					Base   reviewGitHubRepositoryNode `json:"baseRepository"`
				} `json:"pullRequest"`
			} `json:"repository"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &result) != nil || len(result.Errors) != 0 || result.Data.Repository == nil || result.Data.Repository.PullRequest == nil {
		return reviewGitHubIdentity{}, fmt.Errorf("GitHub returned no repository identity for %s PR #%s; check repository and PR access, then retry", slug, number)
	}
	repo, pr := result.Data.Repository.reviewGitHubRepositoryNode, result.Data.Repository.PullRequest
	if !repo.valid() || !pr.Base.valid() || pr.Number != n || pr.Base.ID != repo.ID || !strings.EqualFold(pr.Base.Name, repo.Name) {
		return reviewGitHubIdentity{}, fmt.Errorf("GitHub repository identity of %s and its PR #%s disagree; verify the registry remote and the PR", slug, number)
	}
	if recorded != "" && repo.ID != recorded {
		return reviewGitHubIdentity{}, fmt.Errorf("%s is no longer the repository this review was built for (repository ID %s, now %s): the name may have been reused; nothing was posted; verify the registry remote and build a fresh review", slug, recorded, repo.ID)
	}
	if strings.EqualFold(repo.Name, slug) {
		return reviewGitHubIdentity{ID: repo.ID, Slug: slug}, nil
	}
	canonical := splitReviewSlug(repo.Name)
	if !strings.EqualFold(canonical[0], parts[0]) {
		return reviewGitHubIdentity{}, fmt.Errorf("GitHub reports %s as %s under another owner; review writes do not follow a transfer; verify the move and update the registry remote", slug, repo.Name)
	}
	raw, err = reviewGitHubAPI(dir, []string{"graphql", "-f", "query=" + reviewCanonicalQuery, "-f", "owner=" + canonical[0], "-f", "name=" + canonical[1]}, nil)
	if err != nil {
		return reviewGitHubIdentity{}, fmt.Errorf("cannot verify canonical GitHub repository %s: %w; check gh auth status and network access, then retry", repo.Name, err)
	}
	var current struct {
		Errors []json.RawMessage `json:"errors"`
		Data   struct {
			Repository *reviewGitHubRepositoryNode `json:"repository"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &current) != nil || len(current.Errors) != 0 || current.Data.Repository == nil || !current.Data.Repository.valid() ||
		current.Data.Repository.ID != repo.ID || !strings.EqualFold(current.Data.Repository.Name, repo.Name) {
		return reviewGitHubIdentity{}, fmt.Errorf("GitHub names %s and %s are not one verified repository; verify the registry remote", slug, repo.Name)
	}
	return reviewGitHubIdentity{ID: repo.ID, Slug: repo.Name, Renamed: true}, nil
}

// reviewWriteTarget returns the manifest review writes use. Bitbucket and an
// unrenamed GitHub repository keep the bundle's slug. A rename is followed only
// for a bundle that recorded the repository ID it was built for; an older
// bundle has no identity to carry forward and must be rebuilt.
func reviewWriteTarget(m ReviewManifest) (ReviewManifest, error) {
	if m.Forge != "github" {
		return m, nil
	}
	identity, err := resolveReviewGitHubIdentity(m.RepoDir, m.Slug, m.PR, m.RepositoryID)
	if err != nil {
		return m, err
	}
	if !identity.Renamed {
		return m, nil
	}
	if m.RepositoryID == "" {
		return m, fmt.Errorf("GitHub repository %s was renamed to %s, and this review bundle recorded no repository identity (it predates recorded IDs, or the lookup failed when it was built); nothing was posted; build a fresh review to post under the verified name", m.Slug, identity.Slug)
	}
	fmt.Fprintf(os.Stderr, branding.Text("{{command}}: GitHub repository %s is now %s (same repository ID); posting there. Update the registry remote to the new name.\n"), m.Slug, identity.Slug)
	m.Slug = identity.Slug
	return m, nil
}

// reviewRecordedRepositoryID observes the repository ID a new bundle binds.
// Building stays possible without it; posting then cannot follow a rename.
func reviewRecordedRepositoryID(dir, slug, number string) string {
	identity, err := resolveReviewGitHubIdentity(dir, slug, number, "")
	if err != nil {
		fmt.Fprintf(os.Stderr, branding.Text("{{command}}: warning: %v; the bundle records no repository ID and cannot post after a rename\n"), err)
		return ""
	}
	if identity.Renamed {
		fmt.Fprintf(os.Stderr, branding.Text("{{command}}: GitHub repository %s is now %s (same repository ID); update the registry remote to the new name\n"), slug, identity.Slug)
	}
	return identity.ID
}
