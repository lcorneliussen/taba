package wtc

import (
	"fmt"
	"net/url"
	"strings"
)

// Only a syntactically valid, same-forge mismatch is a recovery candidate.
// Automatic verification is GitHub-only; retain all other local/URL guards.
type prIdentityMismatch struct{ old, current, forge string }

func (*prIdentityMismatch) Error() string { return "PR URL does not match repository identity" }

// statusReadOnlyPRForge validates a supplied PR URL against any local identity,
// or uses the existing repository forge when no URL is supplied. URL-only reads
// never add a local identity or grant checkout/branch authority.
func (c *Context) statusReadOnlyPRForge(repos prRepositories, record PRRecord) (slug, forge string, err error) {
	if record.URL == "" {
		slug, forge = repos.forge(record.Repo)
		return slug, forge, nil
	}
	repo, resolveErr := repos.resolve(record.Repo)
	if resolveErr != nil {
		return "", "", fmt.Errorf("ambiguous repository identity; verify registry remotes and checkout directories")
	}
	if repo == nil {
		return statusParsePRURL(record.URL, record.Number)
	}
	identity := statusPRForgeIdentity(repo.remote)
	if identity == "" {
		return "", "", fmt.Errorf("repository identity cannot be verified; configure a supported forge remote")
	}
	// The shared mapper can use an exact checkout origin when its local-alias
	// validator cannot classify a registry remote. Recheck the raw registry
	// identity here using forge path rules before accepting that fallback.
	for _, registered := range c.Registry.Repos {
		if registered.Answers(repo.name) {
			if raw := statusPRForgeIdentity(registered.Remote); raw != "" && raw != identity {
				return "", "", fmt.Errorf("registry and checkout repository identities disagree")
			}
		}
	}
	slug, forge, err = statusParsePRURLShape(record.URL, record.Number, true)
	if err != nil {
		return "", "", err
	}
	if identity != forge+"/"+slug {
		if strings.HasPrefix(identity, forge+"/") {
			return "", "", &prIdentityMismatch{old: slug, current: strings.TrimPrefix(identity, forge+"/"), forge: forge}
		}
		return "", "", fmt.Errorf("PR URL does not match repository identity")
	}
	slug, forge = repos.forge(record.Repo)
	return slug, forge, nil
}

func statusParsePRURL(raw, number string) (slug, forge string, err error) {
	return statusParsePRURLShape(raw, number, false)
}

func statusParsePRURLShape(raw, number string, local bool) (slug, forge string, err error) {
	invalid := fmt.Errorf("invalid PR URL; use an HTTPS GitHub or Bitbucket pull-request URL without credentials")
	parsed, parseErr := url.Parse(raw)
	if parseErr != nil || (parsed.Scheme != "https" && !(local && parsed.Scheme == "http")) || parsed.User != nil || parsed.Opaque != "" || parsed.RawPath != "" {
		return "", "", invalid
	}
	forge = strings.ToLower(parsed.Host)
	segment := "pull"
	switch forge {
	case "github.com":
	case "bitbucket.org":
		segment = "pull-requests"
	default:
		return "", "", invalid
	}
	parts := strings.Split(strings.TrimSuffix(parsed.Path, "/"), "/")
	if len(parts) < 5 || (!local && len(parts) != 5) || parts[0] != "" || parts[3] != segment {
		return "", "", invalid
	}
	for _, part := range parts[1:3] {
		if !statusPRForgePart(part) {
			return "", "", invalid
		}
	}
	for _, part := range parts[5:] {
		if !statusPRForgePart(part) {
			return "", "", invalid
		}
	}
	if !prNumber.MatchString(parts[4]) || strings.HasPrefix(parts[4], "0") || parts[4] != number {
		return "", "", fmt.Errorf("PR URL number does not match a positive enlisted PR number")
	}
	slug = parts[1] + "/" + parts[2]
	if forge == "github.com" {
		slug = strings.ToLower(slug)
	}
	return slug, forge, nil
}

// Forge path components are not local repository aliases: names such as .github
// are valid. Keep URL parsing bounded to literal ASCII components, excluding
// traversal, separators and encoded/credential-bearing paths.
func statusPRForgePart(part string) bool {
	if part == "" || part == "." || part == ".." {
		return false
	}
	for _, ch := range part {
		if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '.' || ch == '_' || ch == '-') {
			return false
		}
	}
	return true
}

func statusPRForgeIdentity(remote string) string {
	slug, forge := catchUpForge(remote)
	parts := strings.Split(slug, "/")
	if forge == "" || len(parts) != 2 || !statusPRForgePart(parts[0]) || !statusPRForgePart(parts[1]) {
		return ""
	}
	if forge == "github.com" {
		slug = strings.ToLower(slug)
	}
	return forge + "/" + slug
}
