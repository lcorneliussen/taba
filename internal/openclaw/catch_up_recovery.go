package openclaw

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const RecoveryCompletionKind = "taba.catch-up-recovery-completed/v1"
const recoveryCompletionFile = "wtc-catch-up-recovery-completed.json"

var recoveryRefPattern = regexp.MustCompile(`^refs/wtc-catch-up/([0-9a-f]{40}|[0-9a-f]{64})-[0-9]+$`)
var recoveryHeadPattern = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)

// RecoveryCompletion acknowledges agent-verified restoration. Structural
// checks protect the exact operation; semantic reconciliation stays the agent's.
type RecoveryCompletion struct {
	Kind        string `json:"kind"`
	RecoveryRef string `json:"recoveryRef"`
	Head        string `json:"head"`
	IndexTree   string `json:"indexTree"`
	CompletedAt string `json:"completedAt"`
}

func (c *Checkout) catchUpLock() (func(), error) {
	p, err := git(c.Dir, "rev-parse", "--path-format=absolute", "--git-path", "wtc-catch-up.lock")
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("catch-up is already running or needs recovery; inspect %s: %w", p, err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(p)
		return nil, err
	}
	return func() { _ = os.Remove(p) }, nil
}

func (c *Checkout) recoveryMarker() (string, bool, error) {
	p, err := git(c.Dir, "rev-parse", "--path-format=absolute", "--git-path", "wtc-catch-up-recovery")
	if err != nil {
		return "", false, err
	}
	data, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", true, err
	}
	ref := strings.TrimSpace(string(data))
	if !recoveryRefPattern.MatchString(ref) {
		return "", true, errors.New("catch-up recovery marker is invalid; inspect it before changing the checkout")
	}
	return ref, true, nil
}

// A restored subset can be conflict-free while the complete saved intent is
// still awaiting verification. Do not advance or publish it as finished work.
func (c *Checkout) requireRecoveredChanges() error {
	ref, exists, err := c.recoveryMarker()
	if err != nil {
		return err
	}
	if exists {
		return fmt.Errorf("local changes still need recovery from %s; verify and finish that recovery before merging or publishing", ref)
	}
	return nil
}

func (c *Checkout) recoveryCompletion() (*RecoveryCompletion, error) {
	p, err := git(c.Dir, "rev-parse", "--path-format=absolute", "--git-path", recoveryCompletionFile)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var r RecoveryCompletion
	if json.Unmarshal(data, &r) != nil || r.Kind != RecoveryCompletionKind || !recoveryRefPattern.MatchString(r.RecoveryRef) || !recoveryHeadPattern.MatchString(r.Head) || !recoveryHeadPattern.MatchString(r.IndexTree) {
		return nil, errors.New("catch-up recovery completion receipt is invalid")
	}
	if _, err := time.Parse(time.RFC3339Nano, r.CompletedAt); err != nil {
		return nil, errors.New("catch-up recovery completion time is invalid")
	}
	return &r, nil
}

// FinishCatchUpRecovery does not apply files, stage, commit, merge or delete
// backups. The caller has reviewed the restored intent and names the operation
// and HEAD it verified. Only its active marker is archived after the checks.
func (c *Checkout) FinishCatchUpRecovery(ref, expectedHead string) (*RecoveryCompletion, error) {
	if !recoveryRefPattern.MatchString(ref) || !recoveryHeadPattern.MatchString(expectedHead) {
		return nil, errors.New("finish recovery requires the exact recovery ref and full verified HEAD")
	}
	unlock, err := c.catchUpLock()
	if err != nil {
		return nil, err
	}
	defer unlock()
	head, err := git(c.Dir, "rev-parse", "HEAD")
	if err != nil {
		return nil, err
	}
	if head != expectedHead {
		return nil, errors.New("HEAD changed since recovery was verified; inspect the checkout again")
	}
	active, exists, err := c.recoveryMarker()
	if err != nil {
		return nil, err
	}
	if !exists {
		previous, err := c.recoveryCompletion()
		if err == nil && previous != nil && previous.RecoveryRef == ref && previous.Head == head {
			return previous, nil
		}
		return nil, errors.New("no matching active recovery; inspect current status before finishing")
	}
	if active != ref {
		return nil, errors.New("the active recovery changed; refusing to finish a different operation")
	}
	saved, err := git(c.Dir, "rev-parse", "--verify", ref+"^{commit}")
	if err != nil || saved != recoveryRefPattern.FindStringSubmatch(ref)[1] || !gitOK(c.Dir, "cat-file", "-e", ref+"^2^{commit}") {
		return nil, errors.New("the recovery copy or saved index is unavailable; keep the marker and inspect the backup")
	}
	if what := c.inProgress(); what != "" {
		return nil, fmt.Errorf("%s is still in progress; finish it before acknowledging recovery", what)
	}
	files, err := git(c.Dir, "diff", "--name-only", "--diff-filter=U")
	if err != nil {
		return nil, err
	}
	if files != "" {
		return nil, fmt.Errorf("recovery still has unmerged files: %s", files)
	}
	if err := c.checkRecoveryMarkers(ref); err != nil {
		return nil, err
	}
	index, err := git(c.Dir, "write-tree")
	if err != nil {
		return nil, err
	}
	r := &RecoveryCompletion{Kind: RecoveryCompletionKind, RecoveryRef: ref, Head: head, IndexTree: index, CompletedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	p, err := git(c.Dir, "rev-parse", "--path-format=absolute", "--git-path", recoveryCompletionFile)
	if err != nil {
		return nil, err
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return nil, err
	}
	f, err := os.CreateTemp(filepath.Dir(p), ".recovery-completion-*")
	if err != nil {
		return nil, err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(append(data, '\n')); err != nil {
		f.Close()
		return nil, err
	}
	if err = f.Close(); err != nil {
		return nil, err
	}
	// Recheck identities after collecting evidence and before publishing it.
	if current, _, e := c.recoveryMarker(); e != nil || current != ref {
		return nil, errors.New("the active recovery changed while finishing")
	}
	if current, e := git(c.Dir, "rev-parse", "HEAD"); e != nil || current != head {
		return nil, errors.New("HEAD changed while finishing recovery")
	}
	if err := os.Rename(f.Name(), p); err != nil {
		return nil, err
	}
	marker, err := git(c.Dir, "rev-parse", "--path-format=absolute", "--git-path", "wtc-catch-up-recovery")
	if err != nil {
		return nil, err
	}
	archive := marker + "-" + strings.TrimPrefix(ref, "refs/wtc-catch-up/") + ".finished"
	if err := os.Rename(marker, archive); err != nil {
		return nil, fmt.Errorf("recovery receipt saved but active marker could not be archived: %w", err)
	}
	return r, nil
}

func (c *Checkout) checkRecoveryMarkers(ref string) error {
	// Conflict-marker text already present on either side is retained. Reject new
	// marker lines added in both comparisons, in the index or tracked edits.
	for _, cached := range []bool{false, true} {
		added := func(base string) (map[string]string, error) {
			args := []string{"diff", "--no-color", "--no-ext-diff", "--no-textconv", "--no-renames", "--src-prefix=a/", "--dst-prefix=b/", "-U0"}
			if cached {
				args = append(args, "--cached")
			}
			args = append(args, base, "--")
			out, err := git(c.Dir, args...)
			if err != nil {
				return nil, err
			}
			m := map[string]string{}
			file := ""
			for _, line := range strings.Split(out, "\n") {
				if name, ok := strings.CutPrefix(line, "+++ b/"); ok {
					file = name
					continue
				}
				if body, ok := strings.CutPrefix(line, "+"); ok && isConflictMarker(body) {
					m[file+"\x00"+body] = file
				}
			}
			return m, nil
		}
		a, err := added("HEAD")
		if err != nil {
			return err
		}
		b, err := added(ref)
		if err != nil {
			return err
		}
		files := map[string]bool{}
		for key, file := range a {
			if _, ok := b[key]; ok {
				files[file] = true
			}
		}
		if len(files) > 0 {
			names := []string{}
			for file := range files {
				names = append(names, file)
			}
			sort.Strings(names)
			return fmt.Errorf("recovery still adds conflict markers in %s", strings.Join(names, ", "))
		}
	}
	return nil
}
