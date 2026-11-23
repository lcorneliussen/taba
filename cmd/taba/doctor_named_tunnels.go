package main

import (
	"fmt"
	"io"

	"taba.sh/taba/internal/wtc"
)

// namedTunnelsCheck reports whether the configured named tunnels resolve. It
// is advisory: doctor's result does not depend on it. It reads local tunnel
// policy and task declarations and stats provider inputs, never reading
// credential contents; see wtc.NamedTunnelDiagnostics.
func namedTunnelsCheck(c *wtc.Context) (map[string]any, []wtc.NamedTunnelDiagnostic) {
	diagnostics, err := c.NamedTunnelDiagnostics()
	if err != nil {
		return map[string]any{"name": "named-tunnels", "ok": false, "detail": "cannot read tunnel policy: " + err.Error()}, nil
	}
	if len(diagnostics) == 0 {
		return map[string]any{"name": "named-tunnels", "ok": true, "detail": "none configured"}, nil
	}
	problems := 0
	for _, d := range diagnostics {
		if d.State == wtc.NamedTunnelStateProblem {
			problems++
		}
	}
	detail := fmt.Sprintf("%d configured, all resolve", len(diagnostics))
	if problems > 0 {
		detail = fmt.Sprintf("%d configured, %d with problems", len(diagnostics), problems)
	}
	return map[string]any{"name": "named-tunnels", "ok": problems == 0, "detail": detail, "tunnels": diagnostics}, diagnostics
}

func printNamedTunnelSection(w io.Writer, diagnostics []wtc.NamedTunnelDiagnostic) {
	if len(diagnostics) == 0 {
		return
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Named tunnels (checked locally, without provider access):")
	for _, d := range diagnostics {
		about := d.Provider
		if d.Target != "" {
			about += " → " + d.Target
		}
		fmt.Fprintf(w, "  %-28s %-8s %s\n", d.Tunnel, d.State, about)
		for _, p := range d.Problems {
			fmt.Fprintf(w, "    %s\n", p.Problem)
			if p.Fix != "" {
				fmt.Fprintf(w, "      fix: %s\n", p.Fix)
			}
		}
	}
}
