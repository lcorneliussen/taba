package openclaw

import (
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Git can overwrite ignored files while merging even when no tracked edits
// are present. They are deliberately outside a recovery stash, so refuse any
// newly tracked path that would replace one (including directory collisions).
func (c *Checkout) protectIgnored(ref string) error {
	return c.protectIgnoredBetween("HEAD", ref)
}

func (c *Checkout) protectIgnoredBetween(base, ref string) error {
	added, err := git(c.Dir, "diff", "--name-only", "--diff-filter=AMRT", "-z", base, ref)
	if err != nil || added == "" {
		return err
	}
	return c.protectIgnoredPaths(added)
}

func (c *Checkout) protectIgnoredPaths(paths string) error {
	ignored, err := git(c.Dir, "ls-files", "--others", "--ignored", "--exclude-standard", "--directory", "-z")
	if err != nil {
		return err
	}
	for _, a := range strings.Split(paths, "\x00") {
		for _, b := range strings.Split(ignored, "\x00") {
			b = strings.TrimSuffix(b, "/")
			if a != "" && b != "" && (a == b || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/")) {
				return fmt.Errorf("catch-up would replace ignored local data at %s; ask the owning conversation to preserve it first", filepath.ToSlash(b))
			}
		}
	}
	return nil
}

// withCatchUpChanges keeps a recoverable Git copy before moving a dirty
// checkout. Stash objects are local recovery data, never published commits.
// Do not drop by stash position: other worktrees share the stash stack.
// Older plugin clients hand off the "need recovery", "still need recovery"
// and "safe in" phrases through needsLocalRecovery in the Control UI. Other
// errors, including preparatory "set aside" failures, remain plain failures.
func (c *Checkout) withCatchUpChanges(result CatchUp, update func() (CatchUp, error)) (CatchUp, error) {
	unlock, err := c.catchUpLock()
	if err != nil {
		return result, err
	}
	defer unlock()
	if what := c.inProgress(); what != "" {
		return result, fmt.Errorf("%s is in progress in %s: finish or abort it first", what, c.Dir)
	}
	if files, err := git(c.Dir, "diff", "--name-only", "--diff-filter=U"); err != nil {
		return result, err
	} else if files != "" {
		return result, fmt.Errorf("resolve existing unmerged files before catching up: %s", files)
	}
	marker, err := git(c.Dir, "rev-parse", "--path-format=absolute", "--git-path", "wtc-catch-up-recovery")
	if err != nil {
		return result, err
	}
	if ref, exists, err := c.recoveryMarker(); err != nil || exists {
		result.RecoveryRequired = true
		result.RecoveryRef = ref
		if err != nil {
			return result, err
		}
		return result, fmt.Errorf("local changes still need recovery from %s; inspect %s before catching up again", ref, marker)
	}
	changes, err := c.pending()
	if err != nil || len(changes) == 0 {
		if err != nil {
			return result, err
		}
		return update()
	}
	// Stashing resets tracked paths to HEAD. A locally replaced directory
	// can contain ignored files that Git would otherwise remove in that reset.
	tracked, err := git(c.Dir, "ls-tree", "-r", "--name-only", "-z", "HEAD")
	if err != nil {
		return result, err
	}
	if err := c.protectIgnoredPaths(tracked); err != nil {
		return result, err
	}
	message := "taba catch-up recovery " + rand.Text()
	if _, err := git(c.Dir, append(identity(c.Dir), "stash", "push", "--quiet", "--include-untracked", "-m", message, "--", ".", ":(exclude)"+strings.TrimSuffix(sandboxSkills, "/"))...); err != nil {
		return result, fmt.Errorf("cannot preserve local changes; catch-up did not move HEAD: %w", err)
	}
	// Another worktree may have pushed its stash meanwhile. Identify this
	// snapshot by its unique message, never by the top of the shared stack.
	entries, err := git(c.Dir, "reflog", "show", "--format=%H%x00%gs", "refs/stash")
	hash := recoveryStash(entries, message)
	if err != nil {
		return result, fmt.Errorf("local changes were set aside; inspect the Git stash before retrying: %w", err)
	}
	if hash == "" {
		return result, fmt.Errorf("local changes were set aside but their recovery snapshot could not be identified; inspect the Git stash before retrying")
	}
	// The pin is repository-wide; the recovery marker below is per worktree.
	// Keep the copy reachable independently of shared stash-stack rewrites.
	ref := fmt.Sprintf("refs/wtc-catch-up/%s-%d", hash, time.Now().UnixNano())
	if _, err := git(c.Dir, "update-ref", ref, hash); err != nil {
		_, restoreErr := git(c.Dir, "stash", "apply", "--index", hash)
		return result, errors.Join(fmt.Errorf("could not pin recovery stash %s; catch-up did not move HEAD: %w", hash, err), restoreErr)
	}
	markerFile, err := os.OpenFile(marker, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err == nil {
		_, err = markerFile.WriteString(ref + "\n")
		err = errors.Join(err, markerFile.Close())
	}
	if err != nil {
		_, restoreErr := git(c.Dir, "stash", "apply", "--index", ref)
		return result, errors.Join(fmt.Errorf("could not record recovery copy %s; catch-up did not move HEAD: %w", ref, err), restoreErr)
	}
	result, updateErr := update()
	result.RecoveryRef = ref
	if what := c.inProgress(); what != "" {
		result.RecoveryRequired = true
		return result, errors.Join(updateErr, fmt.Errorf("%s still in progress; local changes are safe in %s. Ask the owning conversation to recover them", what, ref))
	}
	if _, err := git(c.Dir, "stash", "apply", "--index", ref); err != nil {
		result.RecoveryRequired = true
		return result, errors.Join(updateErr, fmt.Errorf("local changes need recovery after catch-up; their complete copy, including staging, is in %s. Ask the owning conversation to inspect the worktree and restore or resolve it; do not reapply blindly: %w", ref, err))
	}
	result.ChangesRestored = true
	if err := os.Remove(marker); err != nil {
		result.RecoveryRequired = true
		return result, errors.Join(updateErr, fmt.Errorf("local changes restored, but recovery marker cleanup failed (%s): %w", marker, err))
	}
	return result, updateErr
}

func recoveryStash(entries, message string) string {
	for _, line := range strings.Split(entries, "\n") {
		hash, subject, ok := strings.Cut(line, "\x00")
		if ok && strings.HasSuffix(subject, ": "+message) {
			return hash
		}
	}
	return ""
}
