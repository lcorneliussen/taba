package wtc

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// This cursor orders requests only. It never contains provider observations,
// credentials, failures or completed proofs, and never authorizes URL repair.
type prIdentitySchedule struct {
	Version       int
	Repair        bool
	OrdinaryAfter string
	RepairAfter   string
}

func prIdentityKey(record PRRecord) string {
	digest := sha256.Sum256([]byte(record.Repo + "\x00" + record.Number + "\x00" + record.URL))
	return hex.EncodeToString(digest[:])
}

func (c *Context) prIdentitySchedulePath(forge string) string {
	return statusForgeEntryPath("identity-schedule-v1", forge, prIdentityPath(c.Collection), prIdentityPath(c.Harness)+"\x00"+prIdentityPath(c.ConfigRoot))
}

func readPRIdentitySchedule(path string) (prIdentitySchedule, error) {
	empty := prIdentitySchedule{Version: 1}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return empty, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > 4096 {
		return empty, fmt.Errorf("rename scheduling state unavailable")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return empty, err
	}
	var state prIdentitySchedule
	if json.Unmarshal(raw, &state) != nil || state.Version != 1 {
		return empty, nil
	}
	for _, key := range []string{state.OrdinaryAfter, state.RepairAfter} {
		if key != "" {
			if decoded, err := hex.DecodeString(key); err != nil || len(decoded) != 32 {
				return empty, nil
			}
		}
	}
	return state, nil
}

func writePRIdentitySchedule(path string, state prIdentitySchedule) error {
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".identity-schedule-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err := file.Write(raw); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

func prIdentityOrder(records []PRRecord, indices []int, after string) []int {
	for n, i := range indices {
		if prIdentityKey(records[i]) == after {
			return append(append([]int{}, indices[n+1:]...), indices[:n+1]...)
		}
	}
	return indices
}

// Check local credential availability without capturing or storing the token.
// This is preflight, not a provider request and not a scheduling opportunity.
func prIdentityGitHubReady() bool {
	gh, err := exec.LookPath("gh")
	if err != nil {
		return false
	}
	_, err = statusRun(5*time.Second, func(cmd *exec.Cmd) ([]byte, error) {
		cmd.Stdout = io.Discard
		cmd.Stderr = io.Discard
		return nil, cmd.Run()
	}, gh, "auth", "token", "--hostname", "github.com")
	return err == nil
}

func (c *Context) prIdentityOrdinary(records []PRRecord, indices []int, state *prIdentitySchedule) (bool, error) {
	repos := c.prRepositories()
	for _, i := range prIdentityOrder(records, indices, state.OrdinaryAfter) {
		record := records[i]
		slug, forge, err := c.statusReadOnlyPRForge(repos, record)
		if err != nil || forge != "github.com" {
			continue
		}
		unlock, err := statusLockCache(statusForgeCachePath(forge, slug, record.Number))
		if err != nil {
			return false, err
		}
		raw, fresh := statusReadForgeCache(forge, slug, record.Number)
		detail, parseErr := statusParseForgeDetail(forge, raw, record)
		if fresh && parseErr == nil && detail.State != "" && detail.State != "UNKNOWN" && !statusFullRefreshDue(forge, slug, record.Number) {
			unlock()
			continue
		}
		if !prIdentityGitHubReady() {
			unlock()
			return false, fmt.Errorf("forge credential preflight unavailable")
		}
		issued := false
		started := time.Now()
		raw, err = statusFetchPRRawWithRequest(record, slug, forge, func() { issued = true })
		if issued {
			state.OrdinaryAfter = prIdentityKey(record)
			state.Repair = true
		}
		if err == nil {
			if _, parseErr := statusParseForgeDetail(forge, raw, record); parseErr == nil {
				statusWriteForgeEntrySince("pr", forge, slug, record.Number, raw, started)
				statusMarkFullRefresh(forge, slug, record.Number)
			}
		}
		unlock()
		return issued, err
	}
	return false, nil
}

func (c *Context) prIdentityRepair(records []PRRecord, indices []int, state *prIdentitySchedule) (bool, error) {
	repos := c.prRepositories()
	for _, i := range prIdentityOrder(records, indices, state.RepairAfter) {
		record := records[i]
		_, _, err := c.statusReadOnlyPRForge(repos, record)
		var mismatch *prIdentityMismatch
		if !errors.As(err, &mismatch) || mismatch.forge != "github.com" {
			continue
		}
		input, inputErr := c.prIdentityInput(record, mismatch)
		if inputErr != nil {
			c.statusProgress("Cannot recover enlisted PR: repository input context changed or unavailable; refresh status")
			continue
		}
		issued := false
		canonical, err := prIdentityGitHub(mismatch, record.Number, func() { issued = true })
		if issued {
			state.RepairAfter = prIdentityKey(record)
			state.Repair = false
		}
		if err == nil {
			var changed bool
			record, changed, err = c.reconcilePRURLChecked(record, canonical, func() error {
				current, inputErr := c.prIdentityInput(record, mismatch)
				if inputErr != nil || current != input {
					return fmt.Errorf("repository input context changed during identity verification; refresh status")
				}
				return nil
			})
			if err == nil && !changed {
				err = fmt.Errorf("enlistment changed during identity verification; refresh status to retry")
			}
			if err == nil {
				records[i] = record
			}
		}
		if err != nil {
			c.statusProgress(fmt.Sprintf("Cannot recover enlisted PR %s#%s: %v", records[i].Repo, records[i].Number, err))
		}
		return issued, err
	}
	return false, nil
}

// The outer lock precedes entry and budget locks. It protects one selected
// operation through its issued-state commit across fresh processes. Other
// forges and ordinary fanout do not share this lock or this attempt bound.
func (c *Context) repairPRReferences(records []PRRecord) {
	repos := c.prRepositories()
	ordinary, repair := []int{}, []int{}
	for _, settled := range []bool{false, true} {
		for i, record := range records {
			if (record.MergedOn != "") != settled || ValidatePRIdentity(record.Repo, record.Number) != nil {
				continue
			}
			_, forge, err := c.statusReadOnlyPRForge(repos, record)
			var mismatch *prIdentityMismatch
			if errors.As(err, &mismatch) {
				if mismatch.forge == "github.com" {
					repair = append(repair, i)
				} else {
					c.statusProgress(fmt.Sprintf("Cannot recover enlisted PR %s#%s: automatic recovery is unavailable for this forge; verify with its owner", record.Repo, record.Number))
				}
			} else if err == nil && forge == "github.com" && !settled {
				ordinary = append(ordinary, i)
			}
		}
	}
	if len(repair) == 0 {
		return
	}
	path := c.prIdentitySchedulePath("github.com")
	unlock, err := statusLockCache(path)
	if err != nil {
		return
	}
	defer unlock()
	state, err := readPRIdentitySchedule(path)
	if err != nil {
		return
	}
	before := state
	var issued bool
	if state.Repair {
		issued, err = c.prIdentityRepair(records, repair, &state)
		if !issued && err == nil {
			issued, err = c.prIdentityOrdinary(records, ordinary, &state)
		}
	} else {
		issued, err = c.prIdentityOrdinary(records, ordinary, &state)
		if !issued && err == nil {
			issued, err = c.prIdentityRepair(records, repair, &state)
		}
	}
	if issued && state != before {
		if err := writePRIdentitySchedule(path, state); err != nil {
			c.statusProgress("Could not save rename scheduling progress; refresh status to retry")
		}
	}
}

func prIdentityPath(path string) string {
	if path == "" {
		return ""
	}
	if real, err := filepath.EvalSymlinks(path); err == nil {
		return real
	}
	abs, _ := filepath.Abs(path)
	return abs
}

func (c *Context) prIdentityInput(record PRRecord, want *prIdentityMismatch) (string, error) {
	fresh, err := OpenCollection(c.Collection)
	if err != nil {
		return "", err
	}
	if prIdentityPath(fresh.Harness) != prIdentityPath(c.Harness) || prIdentityPath(fresh.ConfigRoot) != prIdentityPath(c.ConfigRoot) || fresh.AdHoc != c.AdHoc {
		return "", fmt.Errorf("selected context changed")
	}
	_, _, err = fresh.statusReadOnlyPRForge(fresh.prRepositories(), record)
	var mismatch *prIdentityMismatch
	if !errors.As(err, &mismatch) || *mismatch != *want {
		return "", fmt.Errorf("selected repository changed")
	}
	config, err := configInput(fresh.Harness)
	if err != nil {
		return "", err
	}
	var snapshot strings.Builder
	for _, path := range []string{fresh.registryPath(), config} {
		snapshot.WriteString(path + "\x00" + prIdentityPath(path) + "\x00")
		raw, err := os.ReadFile(path)
		if err != nil && !os.IsNotExist(err) {
			return "", err
		}
		if os.IsNotExist(err) {
			snapshot.WriteString("absent\x00")
		} else {
			digest := sha256.Sum256(raw)
			snapshot.WriteString(hex.EncodeToString(digest[:]) + "\x00")
		}
	}
	return snapshot.String(), nil
}
