package wtc

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const prIdentityAttemptAge = 5 * time.Minute

// These are pending observations, not a reusable successful proof. The old
// endpoint is always read in the completing refresh. This cache has no stale
// fallback and does not inherit WTC_FORGE_CACHE_AGE or merged-cache lifetimes.
type prIdentityAttempt struct {
	Version      int                  `json:"version"`
	Forge        string               `json:"forge"`
	Old          string               `json:"old"`
	Canonical    string               `json:"canonical"`
	Number       string               `json:"number"`
	Binding      string               `json:"binding"`
	Started      time.Time            `json:"started"`
	Current      prProviderRepository `json:"current"`
	Base         prProviderRepository `json:"base"`
	BaseObserved time.Time            `json:"base_observed"`
}

func prIdentityAttemptPath(m *prIdentityMismatch, number, scope string) string {
	return statusForgeEntryPath("identity-attempt-v1", m.forge, m.old+"\x00"+m.current, scope+"\x00"+number)
}

func prIdentityContextPath(path string) string {
	if path == "" {
		return ""
	}
	if real, err := filepath.EvalSymlinks(path); err == nil {
		return real
	}
	abs, _ := filepath.Abs(path)
	return abs
}

type prIdentityAuth struct {
	binding, gh, token, user, pass string
	err                            error
}

func (p *prIdentityProof) readCredentials(forge string) prIdentityAuth {
	a := prIdentityAuth{}
	parts := []string{p.scope, p.context, forge, os.Getenv("WTC_CONFIG_ROOT"), os.Getenv("TABA_CONFIG_ROOT")}
	if forge == "github.com" {
		gh, err := exec.LookPath("gh")
		if err != nil {
			a.err = fmt.Errorf("repository identity credential context unavailable; verify forge login and refresh status")
			return a
		}
		// A credential is small: capture it in memory, never in the regular
		// temporary output file used for potentially large forge JSON bodies.
		raw, err := statusRun(30*time.Second, (*exec.Cmd).Output, gh, "auth", "token", "--hostname", "github.com")
		a.gh, a.token = gh, strings.TrimSpace(string(raw))
		if err != nil || a.token == "" || strings.ContainsAny(a.token, " \t\r\n") {
			a.err = fmt.Errorf("repository identity credential context unavailable; verify forge login and refresh status")
			return a
		}
		parts = append(parts, gh, os.Getenv("GH_CONFIG_DIR"), a.token)
	} else {
		a.user, a.pass = bitbucketCredentials()
		if a.user == "" || a.pass == "" {
			a.err = fmt.Errorf("repository identity credential context unavailable; verify forge login and refresh status")
			return a
		}
		parts = append(parts, statusBBAPI, a.user, a.pass)
	}
	digest := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	a.binding = hex.EncodeToString(digest[:])
	return a
}

func (p *prIdentityProof) credentials(forge string) (prIdentityAuth, error) {
	if a, ok := p.auth[forge]; ok {
		return a, a.err
	}
	if p.auth == nil {
		p.auth = map[string]prIdentityAuth{}
	}
	a := p.readCredentials(forge)
	p.auth[forge] = a
	return a, a.err
}

func (p *prIdentityProof) checkCredentials(forge, binding string) error {
	a := p.readCredentials(forge)
	if a.err != nil || a.binding != binding {
		err := fmt.Errorf("repository identity credential or collection context changed; refresh status for fresh proof")
		// Stop this forge for the rest of the refresh. Do not install the new
		// credential beside observations obtained under the old snapshot.
		p.auth[forge] = prIdentityAuth{err: err}
		for key := range p.repositories {
			if strings.HasPrefix(key, forge+"/") {
				delete(p.repositories, key)
			}
		}
		return err
	}
	return nil
}

func (a prIdentityAttempt) fresh(now time.Time) bool {
	return !a.Started.IsZero() && !a.Started.After(now) && now.Sub(a.Started) < prIdentityAttemptAge
}

func prIdentityDeferred(err error) bool {
	return errors.Is(err, errForgeBudget) || errors.Is(err, errForgeCooling)
}

func prIdentityAttemptUnavailable() error {
	return fmt.Errorf("repository identity attempt cache unavailable; refresh status to retry safely")
}

func prIdentityCachedRepositoryValid(forge string, repo prProviderRepository, slug string) bool {
	if repo.Slug != slug {
		return false
	}
	if forge == "github.com" {
		id, err := strconv.ParseInt(repo.ID, 10, 64)
		return err == nil && id > 0 && strconv.FormatInt(id, 10) == repo.ID
	}
	return forge == "bitbucket.org" && prProviderUUID.MatchString(repo.ID)
}

func readPRIdentityAttempt(path string, m *prIdentityMismatch, number, binding string) (prIdentityAttempt, error) {
	empty := prIdentityAttempt{Version: 1, Forge: m.forge, Old: m.old, Canonical: m.current, Number: number, Binding: binding}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return empty, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return empty, prIdentityAttemptUnavailable()
	}
	var a prIdentityAttempt
	if info.Size() <= 16<<10 {
		raw, err := os.ReadFile(path)
		if err != nil {
			return empty, prIdentityAttemptUnavailable()
		}
		if json.Unmarshal(raw, &a) == nil && a.Version == 1 && a.Forge == m.forge && a.Old == m.old && a.Canonical == m.current && a.Number == number && a.Binding == binding &&
			a.fresh(time.Now()) && prIdentityCachedRepositoryValid(m.forge, a.Current, m.current) &&
			((a.Base == (prProviderRepository{}) && a.BaseObserved.IsZero()) ||
				(a.Base == a.Current && !a.BaseObserved.Before(a.Started) && !a.BaseObserved.After(time.Now()))) {
			return a, nil
		}
	}
	if err := os.Remove(path); err != nil {
		return empty, prIdentityAttemptUnavailable()
	}
	return empty, nil
}

func discardPRIdentityAttempt(path string) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil || !info.Mode().IsRegular() || os.Remove(path) != nil {
		return prIdentityAttemptUnavailable()
	}
	return nil
}

// Callers hold the dedicated attempt lock. Persist only validated observations;
// errors remain in the per-refresh memo and never reach this file.
func writePRIdentityAttempt(path string, a prIdentityAttempt) error {
	raw, err := json.Marshal(a)
	if err != nil || len(raw) > 16<<10 {
		return prIdentityAttemptUnavailable()
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".identity-attempt-")
	if err != nil {
		return prIdentityAttemptUnavailable()
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return prIdentityAttemptUnavailable()
	}
	if err := tmp.Close(); err != nil {
		return prIdentityAttemptUnavailable()
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return prIdentityAttemptUnavailable()
	}
	return nil
}
