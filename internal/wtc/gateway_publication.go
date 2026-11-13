package wtc

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
)

// EnlistGatewayPublication records a native publisher's receipt without hooks,
// fetching or changing Git state. The caller owns the authenticated publication;
// this validates local collection membership and the forge destination, with
// case-insensitive GitHub repository names and exact Bitbucket names.
func (c *Context) EnlistGatewayPublication(worktree string, record PRRecord) (PRRecord, error) {
	real, err := filepath.EvalSymlinks(worktree)
	if err != nil {
		return record, err
	}
	if record.Branch == "" || strings.HasPrefix(record.Branch, "-") || strings.ContainsAny(record.Branch, " \t\r\n") {
		return record, fmt.Errorf("publication requires its captured branch")
	}
	if normalized, err := gitOutput("check-ref-format", "--branch", record.Branch); err != nil || normalized != record.Branch {
		return record, fmt.Errorf("invalid published branch")
	}
	u, err := url.Parse(record.URL)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" || u.RawQuery != "" || u.Fragment != "" {
		return record, fmt.Errorf("invalid publication URL")
	}
	parts := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")
	if len(parts) != 4 || u.RawPath != "" || parts[3] == "" || parts[3][0] == '0' || ValidatePRIdentity("repo", parts[3]) != nil {
		return record, fmt.Errorf("invalid pull request URL")
	}
	if u.Host != "github.com" && u.Host != "bitbucket.org" || u.Host == "github.com" && parts[2] != "pull" || u.Host == "bitbucket.org" && parts[2] != "pull-requests" {
		return record, fmt.Errorf("unsupported publication URL")
	}
	number := parts[3]
	var match *prRepository
	repos := c.prRepositories()
	seen := map[*prRepository]bool{}
	for _, candidates := range repos {
		for _, repo := range candidates {
			if seen[repo] {
				continue
			}
			seen[repo] = true
			for _, dir := range repo.dirs {
				checkout := filepath.Join(c.Collection, dir)
				target, e := filepath.EvalSymlinks(checkout)
				if e != nil || target != real {
					continue
				}
				if match != nil && match != repo {
					return record, fmt.Errorf("ambiguous publication worktree")
				}
				resolved, e := repos.resolve(repo.name)
				if e != nil || resolved != repo {
					return record, fmt.Errorf("ambiguous publication repository")
				}
				remote, e := gitOutput("-C", checkout, "config", "--local", "--no-includes", "--get", "remote.origin.url")
				expected := prURL(remote, number)
				if e != nil || expected == "" || prForgeIdentity(remote) != prForgeIdentity(repo.remote) {
					return record, fmt.Errorf("publication repository identity changed")
				}
				// GitHub repository names are case insensitive; Bitbucket workspace/slug
				// identity remains exact. The comparison below also verifies owner and
				// repository; the checks above validate the URL shape and PR number.
				equal := expected == record.URL
				if u.Host == "github.com" {
					equal = strings.EqualFold(expected, record.URL)
				}
				if !equal {
					return record, fmt.Errorf("publication URL does not match the collection repository")
				}
				match = repo
			}
		}
	}
	if match == nil {
		return record, fmt.Errorf("published worktree is not an unambiguous collection member")
	}
	record.Repo, record.Number = match.name, number
	return c.EnlistPR(record)
}
