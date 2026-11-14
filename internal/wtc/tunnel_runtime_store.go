package wtc

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

type tunnelPolicyReceipt struct {
	Path   string `json:"path"`
	Digest string `json:"digest"`
}

type tunnelHeldRuntimeLock struct{}

func (c *Context) withTunnelRuntimeLock(ctx context.Context, fn func() error) error {
	if root, ok := ctx.Value(tunnelHeldRuntimeLock{}).(string); ok && root == c.runtimeRoot() {
		return fn()
	}
	return c.withRuntimeLock(fn)
}

func (c *Context) recordTunnelPolicyLocked(path string, data []byte) error {
	s, err := c.readTunnelJournal()
	if err != nil {
		return err
	}
	s.Policy = &tunnelPolicyReceipt{path, tunnelDigest(data)}
	encoded, err := encodeTunnelState(s, c.tunnelOwner())
	if err != nil {
		return err
	}
	if err = os.MkdirAll(c.runtimeRoot(), 0700); err != nil {
		return err
	}
	if err = c.runtimePathsSafe(); err != nil {
		return err
	}
	return runtimeWrite(c.tunnelJournalPath(), encoded)
}

func (c *Context) tunnelOwner() string       { return "tunnel-" + tunnelDigest([]byte(c.runtimeRoot()))[:32] }
func (c *Context) tunnelJournalPath() string { return filepath.Join(c.runtimeRoot(), "tunnels.json") }

// A missing manifest never suppresses the independent ownership journal read.
// Status uses this bounded read without credentials, locks, writes or repair.
func (c *Context) readTunnelJournal() (tunnelState, error) {
	empty := tunnelState{Schema: 1, Owner: c.tunnelOwner()}
	if err := c.runtimePathsSafe(); err != nil {
		return empty, err
	}
	path := c.tunnelJournalPath()
	if err := runtimeRegular(path); os.IsNotExist(err) {
		m, manifestErr := c.runtimeManifest()
		if manifestErr != nil {
			return empty, manifestErr
		}
		if m != nil && m.Schema == 2 {
			return empty, fmt.Errorf("managed tunnel journal is missing; restore ownership before continuing")
		}
		return empty, nil
	} else if err != nil {
		return empty, err
	}
	f, err := os.Open(path)
	if err != nil {
		return empty, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, tunnelStateLimit+1))
	if err != nil {
		return empty, err
	}
	return decodeTunnelState(data, empty.Owner)
}

func tunnelJournalObligations(s tunnelState) bool {
	for _, e := range s.Entries {
		if e.needsCleanup() {
			return true
		}
	}
	return false
}

func (c *Context) tunnelRuntimeStore() tunnelLifecycleStore {
	return tunnelLifecycleStore{
		ProviderEffect: func(ctx context.Context, run func(context.Context) (tunnelEffectResult, error)) (tunnelEffectResult, error) {
			var result tunnelEffectResult
			err := c.withRuntimeLock(func() error {
				if _, err := c.ValidateControlRoot(); err != nil {
					return err
				}
				var err error
				result, err = run(context.WithValue(ctx, tunnelHeldRuntimeLock{}, c.runtimeRoot()))
				return err
			})
			return result, err
		},
		Read: func(context.Context) (tunnelState, error) { return c.readTunnelJournal() },
		Update: func(ctx context.Context, change func(*tunnelState) error) error {
			if _, err := c.ValidateControlRoot(); err != nil {
				return err
			}
			return c.withTunnelRuntimeLock(ctx, func() error {
				if _, err := c.ValidateControlRoot(); err != nil {
					return err
				}
				s, err := c.readTunnelJournal()
				if err != nil {
					return err
				}
				if err = change(&s); err != nil {
					return err
				}
				data, err := encodeTunnelState(s, c.tunnelOwner())
				if err != nil {
					return err
				}
				if err = os.MkdirAll(c.runtimeRoot(), 0700); err != nil {
					return err
				}
				if err = c.runtimePathsSafe(); err != nil {
					return err
				}
				return runtimeWrite(c.tunnelJournalPath(), data)
			})
		},
		Fence: func(context.Context) error {
			if _, err := c.ValidateControlRoot(); err != nil {
				return err
			}
			return c.withRuntimeLock(func() error {
				if _, err := c.ValidateControlRoot(); err != nil {
					return err
				}
				m, err := c.runtimeManifest()
				if err != nil {
					return err
				}
				if m == nil {
					m, err = c.renderRuntime()
					if err != nil {
						return err
					}
				}
				m.Schema = 2
				return runtimeSaveManifest(m)
			})
		},
	}
}

func (c *Context) refuseTunnelObligations() error {
	s, err := c.readTunnelJournal()
	if err != nil {
		return err
	}
	if tunnelJournalObligations(s) {
		return fmt.Errorf("tunnel cleanup/reconciliation must complete before runtime teardown")
	}
	return nil
}
