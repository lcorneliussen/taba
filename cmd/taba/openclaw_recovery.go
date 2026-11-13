package main

import (
	"fmt"
	"os"
	"path/filepath"

	"taba.sh/taba/internal/openclaw"
)

func finishCheckoutRecovery(ref, head string, asJSON bool) error {
	if openclaw.GatewayFromEnv(os.Getenv) != nil || currentHost() != openclaw.HostOpenClaw {
		return fmt.Errorf("--finish-recovery is for the owning gateway worktree; sandbox recovery uses --continue")
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	checkout, err := openclaw.OpenCatchUpCheckout(cwd, "")
	if err != nil {
		return err
	}
	// Resolve collection-folder symlinks, then require a native managed path.
	actual, err := filepath.EvalSymlinks(checkout.Dir)
	if err != nil {
		return err
	}
	managed, err := openclaw.ManagedCollections(gatewayStateDir())
	if err != nil {
		return err
	}
	found := false
	for _, paths := range managed {
		for _, path := range paths {
			if resolved, e := filepath.EvalSymlinks(path); e == nil && resolved == actual {
				found = true
			}
		}
	}
	if !found {
		return fmt.Errorf("finish recovery from the owning gateway-managed checkout, not another copy")
	}
	r, err := checkout.FinishCatchUpRecovery(ref, head)
	if err != nil {
		return err
	}
	return emit(envelope{OK: true, Data: r, Summary: "Recovery verified; active marker archived, backup and checkout contents retained."}, asJSON)
}
