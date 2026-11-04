package openclaw

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var projectNamePart = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

func validProjectName(name string) bool {
	return projectNamePart.MatchString(name) && name != "." && name != ".."
}
func validProjectOrigin(o Origin) bool {
	return o.Forge != "" && o.Forge != ForgeUnknown && validProjectName(o.Owner) && validProjectName(o.Repo)
}

// GatewayProjectOrigin is the canonical origin of an existing clone. Its
// directory and registered project ID need not change when the repository does.
func GatewayProjectOrigin(dir string) Origin {
	remote, err := git(dir, "remote", "get-url", "origin")
	if err != nil {
		return Origin{}
	}
	return ParseOrigin(remote)
}

func gatewayProjectDirs(stateDir string) []string {
	dirs, _ := filepath.Glob(filepath.Join(stateDir, "projects", "*", "*"))
	others, _ := filepath.Glob(filepath.Join(stateDir, "repos", "*"))
	return append(dirs, others...)
}

// Alias records bind a verified forge identity to this clone's current origin.
// They are not inferred from names, and do not add executable Git config keys.
// Listing never contacts the forge or changes the record, origin, or worktrees.
func projectOrigins(dir string) []Origin {
	current := GatewayProjectOrigin(dir)
	if !validProjectOrigin(current) {
		return nil
	}
	origins := []Origin{current}
	common, err := git(dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return origins
	}
	f, err := os.Open(filepath.Join(common, "wtc-origin-aliases.json"))
	if err != nil {
		return origins
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 65537))
	if err != nil || len(data) > 65536 {
		return origins
	}
	var record struct {
		Version      int      `json:"version"`
		Origin       string   `json:"origin"`
		RepositoryID string   `json:"repositoryId"`
		Aliases      []string `json:"aliases"`
	}
	if json.Unmarshal(data, &record) != nil || record.Version != 1 || record.RepositoryID == "" || len(record.Aliases) > 128 || !sameOrigin(current, ParseOrigin(record.Origin)) {
		return origins
	}
	for _, remote := range record.Aliases {
		alias := ParseOrigin(strings.TrimSpace(remote))
		if alias.Forge == current.Forge && validProjectOrigin(alias) {
			origins = append(origins, alias)
		}
	}
	return origins
}

func sameOrigin(a, b Origin) bool {
	return a.Forge != "" && a.Forge != ForgeUnknown && a.Forge == b.Forge &&
		strings.EqualFold(a.Owner, b.Owner) && strings.EqualFold(a.Repo, b.Repo)
}

func projectMatchesOrigin(dir string, requested Origin) bool {
	current := GatewayProjectOrigin(dir)
	if sameOrigin(current, requested) {
		return true
	}
	for _, origin := range projectOrigins(dir) {
		if sameOrigin(origin, requested) {
			return true
		}
	}
	return false
}

func projectNamed(stateDir, name string) string {
	var found string
	for _, dir := range gatewayProjectDirs(stateDir) {
		for _, origin := range projectOrigins(dir) {
			if strings.EqualFold(origin.Repo, name) {
				if found != "" && found != dir {
					return "" // A short name must not choose between different projects.
				}
				found = dir
				break
			}
		}
	}
	return found
}
