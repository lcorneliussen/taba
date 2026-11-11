package wtc

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

type prProviderRepository struct {
	ID   string
	Slug string
}

type prIdentityRepositoryResult struct {
	repo       prProviderRepository
	err        error
	observedAt time.Time
}

// Repository results, including sanitized failures, live for one refresh only.
// Bounded partial attempts never persist old-name identity or lookup failures.
type prIdentityProof struct {
	repositories map[string]prIdentityRepositoryResult
	auth         map[string]prIdentityAuth
	scope        string
	context      string
}

func (p *prIdentityProof) repository(forge, slug string) (prProviderRepository, error) {
	key := forge + "/" + slug
	if result, ok := p.repositories[key]; ok {
		return result.repo, result.err
	}
	if p.repositories == nil {
		p.repositories = map[string]prIdentityRepositoryResult{}
	}
	started := time.Now()
	raw, err := p.get(forge, slug, "")
	var repo prProviderRepository
	if err != nil {
		err = prIdentityUnavailable(err)
	} else {
		repo, err = prIdentityRepository(forge, raw)
	}
	p.repositories[key] = prIdentityRepositoryResult{repo: repo, err: err, observedAt: started}
	return repo, err
}

func (p *prIdentityProof) verify(m *prIdentityMismatch, number string) (string, error) {
	// Do not spend another row's network calls after a shared endpoint failed.
	var sharedErr error
	for _, slug := range []string{m.current, m.old} {
		if result, ok := p.repositories[m.forge+"/"+slug]; ok && result.err != nil {
			sharedErr = result.err
			break
		}
	}
	auth, authErr := p.credentials(m.forge)
	path := prIdentityAttemptPath(m, number, p.scope)
	unlock, err := statusLockCache(path)
	if err != nil {
		return "", prIdentityAttemptUnavailable()
	}
	defer unlock()
	if authErr != nil {
		if err := discardPRIdentityAttempt(path); err != nil {
			return "", err
		}
		return "", authErr
	}
	attempt, err := readPRIdentityAttempt(path, m, number, auth.binding)
	if err != nil {
		return "", err
	}
	canonical, proofErr := "", sharedErr
	if proofErr == nil {
		canonical, proofErr = p.advance(&attempt, m, number)
	}
	if proofErr == nil || (prIdentityDeferred(proofErr) && attempt.Current.ID != "") {
		if err := p.checkCredentials(m.forge, auth.binding); err != nil {
			canonical, proofErr = "", err
		}
	}
	if prIdentityDeferred(proofErr) && attempt.Current.ID != "" && attempt.fresh(time.Now()) {
		if err := writePRIdentityAttempt(path, attempt); err != nil {
			return "", err
		}
	} else if err := discardPRIdentityAttempt(path); err != nil {
		return "", err
	}
	return canonical, proofErr
}

func (p *prIdentityProof) advance(a *prIdentityAttempt, m *prIdentityMismatch, number string) (string, error) {
	if a.Current.ID == "" {
		current, err := p.repository(m.forge, m.current)
		if err != nil {
			return "", err
		}
		if current.Slug != m.current {
			return "", fmt.Errorf("provider canonical repository identity disagrees; verify the registry remote")
		}
		a.Current = current
		a.Started = p.repositories[m.forge+"/"+m.current].observedAt
	}
	if !a.fresh(time.Now()) {
		return "", fmt.Errorf("repository identity attempt expired; refresh status for fresh provider proof")
	}
	if a.Base.ID == "" {
		started := time.Now()
		raw, err := p.get(m.forge, m.current, number)
		if err != nil {
			return "", prIdentityUnavailable(err)
		}
		base, err := prIdentityBase(m.forge, raw, number)
		if err != nil || base != a.Current {
			return "", fmt.Errorf("provider PR base repository identity disagrees; verify the enlisted PR and registry remote")
		}
		a.Base, a.BaseObserved = base, started
	}
	// Complete with a live old-name observation in this refresh. Its immutable
	// ID and canonical full_name must attest the same repository as both prior
	// observations. Reused names cannot be accepted from a persisted old ID.
	old, err := p.repository(m.forge, m.old)
	if err != nil {
		return "", err
	}
	if !a.fresh(time.Now()) || time.Since(p.repositories[m.forge+"/"+m.old].observedAt) >= prIdentityAttemptAge {
		return "", fmt.Errorf("repository identity attempt expired; refresh status for fresh provider proof")
	}
	if old != a.Current || old != a.Base || old.Slug != m.current {
		return "", fmt.Errorf("provider repository identities disagree; verify the registry remote and enlisted PR URL")
	}
	return prURLForStatus(m.forge, m.current, number), nil
}

func prIdentityBase(forge string, raw []byte, number string) (prProviderRepository, error) {
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
		return prProviderRepository{}, prIdentityIncomplete()
	}
	base, n := pr.Base.Repo, pr.Number
	if forge == "bitbucket.org" {
		base, n = pr.Destination.Repository, pr.ID
	}
	repo, err := prIdentityRepository(forge, base)
	if err != nil || fmt.Sprint(n) != number {
		return prProviderRepository{}, prIdentityIncomplete()
	}
	return repo, nil
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
		return fmt.Errorf("repository identity verification deferred; retry status after cooldown: %w", err)
	}
	// Provider errors may contain credentials, response bodies or inaccessible
	// repository names. Report the recovery action without echoing that output.
	return fmt.Errorf("repository or PR identity lookup unavailable; verify forge login, network and repository/PR access, then refresh status")
}

func (p *prIdentityProof) get(forge, slug, number string) ([]byte, error) {
	auth, authErr := p.credentials(forge)
	if authErr != nil {
		return nil, authErr
	}
	raw, _, err := statusWithForgeBudget(forge, nil, func() ([]byte, error) {
		if forge == "github.com" {
			endpoint := "repos/" + slug
			if number != "" {
				endpoint += "/pulls/" + number
			}
			return statusRun(30*time.Second, func(cmd *exec.Cmd) ([]byte, error) {
				// Keep every request on the credential snapshot whose digest binds
				// the attempt. Never put a token in argv, diagnostics or cache data.
				cmd.Env = append(os.Environ(), "GH_TOKEN="+auth.token, "GITHUB_TOKEN="+auth.token)
				return forgeOutput(cmd)
			}, auth.gh, "api", endpoint, "--hostname", "github.com", "--method", "GET")
		}
		endpoint := statusBBAPI + "/repositories/" + slug
		if number != "" {
			endpoint += "/pullrequests/" + number
		}
		return statusBBGET(endpoint, auth.user, auth.pass)
	})
	return raw, err
}

func (c *Context) repairPRReferences(records []PRRecord) {
	repos := c.prRepositories()
	proof := prIdentityProof{scope: prIdentityContextPath(c.Collection), context: fmt.Sprintf("%s\x00%s\x00%t", prIdentityContextPath(c.Harness), prIdentityContextPath(c.ConfigRoot), c.AdHoc)}
	work := make([]int, 0, len(records))
	for _, settled := range []bool{false, true} {
		for i, record := range records {
			if (record.MergedOn != "") == settled {
				work = append(work, i)
			}
		}
	}
	activeCurrent := true
	for _, record := range records {
		if record.MergedOn != "" {
			continue
		}
		// The ordinary collector runs next. Archive proof must yield tokens
		// until active references have usable detail at its normal freshness.
		slug, forge, err := c.statusReadOnlyPRForge(repos, record)
		if err != nil {
			var mismatch *prIdentityMismatch
			if errors.As(err, &mismatch) {
				activeCurrent = false
			}
			continue
		}
		if slug == "" || forge == "" {
			continue
		}
		raw, fresh := statusReadForgeCache(forge, slug, record.Number)
		detail, parseErr := statusParseForgeDetail(forge, raw, record)
		if !fresh || parseErr != nil || detail.State == "" || detail.State == "UNKNOWN" {
			activeCurrent = false
		}
	}
	for _, i := range work {
		record := records[i]
		if record.MergedOn != "" && !activeCurrent {
			continue
		}
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
