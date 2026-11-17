package main

import (
	"fmt"
	"taba.sh/taba/internal/wtc"
)

func doctorRequirements(c *wtc.Context, flags requirementsFlags) (map[string]any, wtc.RequirementsInventory, error) {
	inventory, err := c.ListConfigRequirements(wtc.RequirementsOptions{Examples: flags.examples, ProcessEnvironment: flags.process})
	if err != nil {
		return nil, inventory, err
	}
	detail := fmt.Sprintf("%d requirement(s), %d diagnostic(s); local readiness remains %s", len(inventory.Items), len(inventory.Diagnostics), inventory.LocalReadiness)
	return map[string]any{"name": "requirements", "ok": inventory.ReadinessOK, "detail": detail, "inventory": inventory}, inventory, nil
}
