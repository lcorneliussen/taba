package wtc

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
)

type prProviderRepository struct {
	ID   string
	Slug string
}

// Compare immutable IDs at both repository names and in the PR base. This memo
// lasts one refresh and never reads the forge cache; a reused name fails closed.
type prIdentityProof struct {
	repositories map[string]prProviderRepository
}

func (p *prIdentityProof) repository(forge, slug string) (prProviderRepository, error) {
	key := forge + "/" + slug
	if repo, ok := p.repositories[key]; ok {
		return repo, nil
	}
	raw, err := prIdentityGET(forge, slug, "")
	if err != nil {
		return prProviderRepository{}, prIdentityUnavailable(err)
	}
	repo, err := prIdentityRepository(forge, raw)
	if err == nil {
		p.repositories[key] = repo
	}
	return repo, err
}

func (p *prIdentityProof) verify(m *prIdentityMismatch, number string) (string, error) {
	old, err := p.repository(m.forge, m.old)
	if err != nil {
		return "", err
	}
	current, err := p.repository(m.forge, m.current)
	if err != nil {
		return "", err
	}
	if old.ID != current.ID || old.Slug != current.Slug || current.Slug != m.current {
		return "", fmt.Errorf("provider repository identities disagree; verify the registry remote and enlisted PR URL")
	}
	raw, err := prIdentityGET(m.forge, current.Slug, number)
	if err != nil {
		return "", prIdentityUnavailable(err)
	}
	var pr struct {
		Number int64 `json:"number"`
		ID     int64 `json:"id"`
		Base   struct {
			Repo json.RawMessage `json:"repo"`
		} `json:"base"`
		Destination struct {
			Repository json.RawMessage `json:"repository"`
		} `json:"destination"`
	}
	if json.Unmarshal(raw, &pr) != nil {
		return "", prIdentityIncomplete()
	}
	base, n := pr.Base.Repo, pr.Number
	if m.forge == "bitbucket.org" {
		base, n = pr.Destination.Repository, pr.ID
	}
	repo, err := prIdentityRepository(m.forge, base)
	if err != nil || fmt.Sprint(n) != number || repo != current {
		return "", fmt.Errorf("provider PR base repository identity disagrees; verify the enlisted PR and registry remote")
	}
	return prURLForStatus(m.forge, current.Slug, number), nil
}

var prProviderUUID = regexp.MustCompile(`^\{[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\}$`)

func prIdentityRepository(forge string, raw []byte) (prProviderRepository, error) {
	var value struct {
		ID       int64  `json:"id"`
		UUID     string `json:"uuid"`
		FullName string `json:"full_name"`
	}
	if json.Unmarshal(raw, &value) != nil {
		return prProviderRepository{}, prIdentityIncomplete()
	}
	parts := strings.Split(value.FullName, "/")
	if len(parts) != 2 || !statusPRForgePart(parts[0]) || !statusPRForgePart(parts[1]) {
		return prProviderRepository{}, prIdentityIncomplete()
	}
	repo := prProviderRepository{Slug: value.FullName}
	if forge == "github.com" && value.ID > 0 {
		repo.ID, repo.Slug = fmt.Sprint(value.ID), strings.ToLower(value.FullName)
	} else if forge == "bitbucket.org" && prProviderUUID.MatchString(strings.ToLower(value.UUID)) {
		repo.ID = strings.ToLower(value.UUID)
	} else {
		return prProviderRepository{}, prIdentityIncomplete()
	}
	return repo, nil
}

func prIdentityIncomplete() error {
	return fmt.Errorf("provider returned incomplete repository identity; verify forge access and refresh status")
}

func prIdentityUnavailable(err error) error {
	if errors.Is(err, errForgeCooling) || errors.Is(err, errForgeBudget) {
		return fmt.Errorf("repository identity verification deferred by forge rate limits; retry status after cooldown")
	}
	// Provider errors may contain credentials, response bodies or inaccessible
	// repository names. Report the recovery action without echoing that output.
	return fmt.Errorf("repository or PR identity lookup unavailable; verify forge login, network and repository/PR access, then refresh status")
}

func prIdentityGET(forge, slug, number string) ([]byte, error) {
	raw, _, err := statusWithForgeBudget(forge, nil, func() ([]byte, error) {
		if forge == "github.com" {
			endpoint := "repos/" + slug
			if number != "" {
				endpoint += "/pulls/" + number
			}
			return statusJSON("gh", "api", endpoint, "--hostname", "github.com", "--method", "GET")
		}
		user, pass := bitbucketCredentials()
		if user == "" || pass == "" {
			return nil, fmt.Errorf("Bitbucket credentials unavailable")
		}
		endpoint := statusBBAPI + "/repositories/" + slug
		if number != "" {
			endpoint += "/pullrequests/" + number
		}
		return statusBBGET(endpoint, user, pass)
	})
	return raw, err
}

func (c *Context) repairPRReferences(records []PRRecord) {
	repos := c.prRepositories()
	proof := prIdentityProof{repositories: map[string]prProviderRepository{}}
	for i, record := range records {
		if ValidatePRIdentity(record.Repo, record.Number) != nil {
			continue
		}
		_, _, err := c.statusReadOnlyPRForge(repos, record)
		var mismatch *prIdentityMismatch
		if !errors.As(err, &mismatch) {
			continue
		}
		canonical, err := proof.verify(mismatch, record.Number)
		if err == nil {
			var changed bool
			record, changed, err = c.reconcilePRURL(record, canonical)
			if err == nil && !changed {
				err = fmt.Errorf("enlistment changed during identity verification; refresh status to retry")
			}
		}
		if err != nil {
			c.statusProgress(fmt.Sprintf("Cannot recover enlisted PR %s#%s: %v", records[i].Repo, records[i].Number, err))
			continue
		}
		records[i] = record
	}
}

var prURLField = regexp.MustCompile(`^(\s*\S+\s+\S+\s+\S+\s+)(\S+)`)

// Compare only the URL observed before the network lookup, under the shared
// legacy lock. Branch/title edits, merge comments and unrelated bytes survive.
func (c *Context) reconcilePRURL(before PRRecord, canonical string) (PRRecord, bool, error) {
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
			span := prURLField.FindStringSubmatchIndex(line)
			if span == nil {
				return fmt.Errorf("enlisted PR URL field unavailable")
			}
			lines[i] = line[:span[4]] + canonical + line[span[5]:]
		}
		if err := c.writePRFile([]byte(strings.Join(lines, ""))); err != nil {
			return err
		}
		latest.URL, changed = canonical, true
		return nil
	})
	return latest, changed, err
}
