package wtc

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"unicode"
)

// All three observations come from one live provider response. No identity
// observation, error, credential fingerprint or completed proof is cached.
const prIdentityQuery = `query($oldOwner:String!,$oldName:String!,$currentOwner:String!,$currentName:String!,$number:Int!){
 old:repository(owner:$oldOwner,name:$oldName){id nameWithOwner pullRequest(number:$number){number url baseRepository{id nameWithOwner}}}
 current:repository(owner:$currentOwner,name:$currentName){id nameWithOwner}
}`

func prIdentityGitHub(m *prIdentityMismatch, number string, onRequest func()) (string, error) {
	if m.forge != "github.com" {
		return "", fmt.Errorf("automatic repository rename recovery is unavailable for this forge; verify the enlisted URL with its owner")
	}
	old, current := strings.Split(m.old, "/"), strings.Split(m.current, "/")
	n, err := prIdentityGitHubNumber(number)
	if err != nil || n < 1 || len(old) != 2 || len(current) != 2 {
		return "", prIdentityIncomplete()
	}
	if !prIdentityGitHubReady() {
		return "", fmt.Errorf("forge credential preflight unavailable; verify forge login and refresh status")
	}
	raw, _, err := statusWithForgeBudget(m.forge, nil, func() ([]byte, error) {
		if onRequest != nil {
			onRequest()
		}
		return statusJSON("gh", "api", "graphql", "--hostname", "github.com", "-f", "query="+prIdentityQuery,
			"-f", "oldOwner="+old[0], "-f", "oldName="+old[1], "-f", "currentOwner="+current[0], "-f", "currentName="+current[1], "-F", "number="+number)
	})
	if err != nil {
		return "", prIdentityUnavailable(err)
	}
	type repository struct {
		ID   string `json:"id"`
		Name string `json:"nameWithOwner"`
	}
	var result struct {
		Errors []json.RawMessage `json:"errors"`
		Data   struct {
			Old struct {
				repository
				PR struct {
					Number int64      `json:"number"`
					URL    string     `json:"url"`
					Base   repository `json:"baseRepository"`
				} `json:"pullRequest"`
			} `json:"old"`
			Current repository `json:"current"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &result) != nil || len(result.Errors) != 0 {
		return "", prIdentityIncomplete()
	}
	a, b, base := result.Data.Old.repository, result.Data.Current, result.Data.Old.PR.Base
	valid := func(r repository) bool {
		parts := strings.Split(r.Name, "/")
		return r.ID != "" && len(r.ID) <= 512 && strings.TrimSpace(r.ID) == r.ID && !strings.ContainsAny(r.ID, " \t\r\n") && len(parts) == 2 && statusPRForgePart(parts[0]) && statusPRForgePart(parts[1]) && strings.ToLower(r.Name) == m.current
	}
	if !valid(a) || !valid(b) || !valid(base) || a.ID != b.ID || a.ID != base.ID || result.Data.Old.PR.Number != n || result.Data.Old.PR.URL != prURLForStatus("github.com", a.Name, number) {
		return "", fmt.Errorf("provider repository identities, PR base or canonical URL disagree; verify the registry remote and enlisted PR URL")
	}
	return prURLForStatus("github.com", m.current, number), nil
}

func prIdentityIncomplete() error {
	return fmt.Errorf("provider returned incomplete repository identity; verify forge access and refresh status")
}
func prIdentityUnavailable(err error) error {
	if errors.Is(err, errForgeCooling) || errors.Is(err, errForgeBudget) {
		return fmt.Errorf("repository identity verification deferred; retry status after cooldown: %w", err)
	}
	return fmt.Errorf("repository or PR identity lookup unavailable; verify forge login, network and repository/PR access, then refresh status")
}

func prIdentityGitHubNumber(number string) (int64, error) {
	n, err := strconv.ParseInt(number, 10, 32)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("PR number is outside GitHub GraphQL Int range; verify the enlisted reference")
	}
	return n, nil
}

// Match strings.Fields' unicode.IsSpace definition while retaining byte spans.
func prURLSpan(line string) (int, int, bool) {
	field, start := 0, -1
	for offset, r := range line {
		if unicode.IsSpace(r) {
			if start >= 0 {
				field++
				if field == 4 {
					return start, offset, true
				}
				start = -1
			}
		} else if start < 0 {
			start = offset
		}
	}
	if start >= 0 && field == 3 {
		return start, len(line), true
	}
	return 0, 0, false
}

// Compare only the URL observed before the network lookup, under the shared
// legacy lock. Branch/title edits, merge comments and unrelated bytes survive.
func (c *Context) reconcilePRURL(before PRRecord, canonical string) (PRRecord, bool, error) {
	return c.reconcilePRURLChecked(before, canonical, nil)
}

func (c *Context) reconcilePRURLChecked(before PRRecord, canonical string, check func() error) (PRRecord, bool, error) {
	var latest PRRecord
	changed := false
	err := c.withPRLock(func() error {
		path, err := c.prInput()
		if err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("refusing non-regular PR registry")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		records, err := c.ListPRs()
		if err != nil {
			return err
		}
		count := 0
		for _, record := range records {
			if record.Repo == before.Repo && record.Number == before.Number {
				latest = record
				count++
			}
		}
		if count != 1 || latest.URL != before.URL {
			return nil
		}
		lines := strings.SplitAfter(string(data), "\n")
		for i, line := range lines {
			record, ok := parsePR(line)
			if !ok || record.Repo != before.Repo || record.Number != before.Number {
				continue
			}
			start, end, found := prURLSpan(line)
			if !found || line[start:end] != before.URL {
				return fmt.Errorf("enlisted PR URL field unavailable")
			}
			lines[i] = line[:start] + canonical + line[end:]
		}
		if check != nil {
			if err := check(); err != nil {
				return err
			}
		}
		if err := c.writePRFile([]byte(strings.Join(lines, ""))); err != nil {
			return err
		}
		latest.URL, changed = canonical, true
		return nil
	})
	return latest, changed, err
}
