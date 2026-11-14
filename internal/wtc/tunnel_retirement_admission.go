package wtc

import (
	"context"
	"fmt"
	"os"
)

// Admission ownership is part of the existing tunnel journal, not another
// operation ledger. An interrupted attempt keeps allocation fenced until a
// validated retry; a surviving intact receiver may release its own attempt.
type tunnelRetirementAttempt struct {
	ID          string                `json:"id"`
	Process     tunnelProcessIdentity `json:"process"`
	Interrupted bool                  `json:"interrupted,omitempty"`
}

func (c *Context) beginTunnelRetirement(ctx context.Context) (string, error) {
	process, err := currentTunnelProcess()
	if err != nil {
		return "", err
	}
	id, err := tunnelOperationID()
	if err != nil {
		return "", err
	}
	err = c.tunnelRuntimeStore().Update(ctx, func(s *tunnelState) error {
		if s.Retiring {
			if s.Retirement == nil {
				return fmt.Errorf("retirement admission owner is unknown; restore journal evidence before retry")
			}
			if !s.Retirement.Interrupted && tunnelProcessState(s.Retirement.Process, readTunnelProcess) != "absent" {
				return fmt.Errorf("retirement is active or its owning process is uncertain")
			}
		}
		s.Retiring = true
		s.Retirement = &tunnelRetirementAttempt{ID: id, Process: process}
		return nil
	})
	return id, err
}
func (c *Context) endTunnelRetirement(ctx context.Context, id string, release bool) error {
	if _, err := os.Lstat(c.Collection); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	return c.tunnelRuntimeStore().Update(ctx, func(s *tunnelState) error {
		if !s.Retiring && s.Retirement == nil {
			return nil
		}
		if s.Retirement == nil || s.Retirement.ID != id {
			return fmt.Errorf("retirement admission ownership changed; refusing to clear it")
		}
		if release {
			s.Retiring = false
			s.Retirement = nil
		} else {
			s.Retirement.Interrupted = true
		}
		return nil
	})
}
