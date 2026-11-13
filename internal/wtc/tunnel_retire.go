package wtc

import (
	"fmt"
	"os"
	"path/filepath"
)

// This assessment is shared by dry preflight and final deletion. Only the
// exact tool-written, non-secret policy with a matching sole-journal receipt
// is disposable. No directory-wide .config deletion or force exemption exists.
func (c *Context) assessTunnelPrivateRoot(root string) ([]string, error) {
	info, err := os.Lstat(root)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("retirement blocked: collection private control path requires manual preservation")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, nil
	}
	expected := filepath.Join(c.Collection, ".config", "tunnels.toml")
	if len(entries) != 1 || filepath.Join(root, entries[0].Name()) != expected || entries[0].Name() != "tunnels.toml" || !entries[0].Type().IsRegular() {
		return nil, fmt.Errorf("retirement blocked: preserve collection private control/unknown files before removal")
	}
	state, err := c.readTunnelJournal()
	if err != nil {
		return nil, err
	}
	data, err := readTunnelBoundedFile(expected, 1<<20)
	if err != nil {
		return nil, err
	}
	if state.Policy == nil || state.Policy.Path != expected || state.Policy.Digest != tunnelDigest(data) {
		return nil, fmt.Errorf("retirement blocked: private control policy ownership changed or is unknown")
	}
	if _, err = parseTunnelPolicy(expected, data); err != nil {
		return nil, fmt.Errorf("retirement blocked: malformed private control policy requires preservation")
	}
	return []string{expected}, nil
}

func (c *Context) assessTunnelPrivateControl(remove bool) error {
	scope, err := c.ValidateControlRoot()
	if err != nil {
		return err
	}
	canonicalTarget, err := canonicalPath(c.Collection)
	if err != nil {
		return err
	}
	roots := []string{filepath.Join(c.Collection, ".config")}
	if withinPath(canonicalTarget, scope.Root) && !sameCanonicalPath(roots[0], scope.Root) {
		roots = append(roots, scope.Root)
	}
	if remove {
		if err = c.refuseTunnelObligations(); err != nil {
			return err
		}

	}
	type assessment struct {
		root  string
		files []string
	}
	assessments := []assessment{}
	for _, root := range roots {
		files, err := c.assessTunnelPrivateRoot(root)
		if err != nil {
			return err
		}
		assessments = append(assessments, assessment{root, files})
	}
	if remove {
		for _, a := range assessments {
			for _, file := range a.files {
				if err = os.Remove(file); err != nil {
					return err
				}
			}
			if err = os.Remove(a.root); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
		// A policy-only collection can have the owned journal without ever
		// having rendered a runner. Remove only that exact, validated journal
		// when it is the sole output; unrelated runtime files are preserved.
		m, err := c.runtimeManifest()
		if err != nil {
			return err
		}
		if m == nil {
			entries, err := os.ReadDir(c.runtimeRoot())
			if err != nil && !os.IsNotExist(err) {
				return err
			}
			if len(entries) == 1 && entries[0].Name() == "tunnels.json" {
				if err = os.Remove(c.tunnelJournalPath()); err != nil {
					return err
				}
				if err = os.Remove(c.runtimeRoot()); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
