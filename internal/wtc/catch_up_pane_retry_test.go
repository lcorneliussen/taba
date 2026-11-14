package wtc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCatchUpStatusReloadRetriesObservations(t *testing.T) {
	for _, tc := range []struct {
		name, mode, outcome string
		runs, observations  int
	}{
		{"healthy", "healthy", "restarted", 1, 1},
		{"interrupt transient", "interrupt-once", "restarted", 1, 2},
		{"restart transient", "restart-once", "restarted", 1, 1},
		{"interrupt last attempt", "interrupt-four", "restarted", 1, 5},
		{"restart last attempt", "restart-four", "restarted", 1, 1},
		{"interrupt unavailable", "interrupt-always", "failed", 0, 5},
		{"restart unavailable", "restart-always", "failed", 1, 1},
		{"interrupt unrelated", "interrupt-unrelated", "needs-owner", 0, 5},
		{"restart shell", "restart-shell", "failed", 1, 1},
		{"pane replaced", "pane-replaced", "failed", 0, 1},
		{"agent appeared", "agent-appeared", "needs-owner", 0, 1},
		{"shell changed", "shell-changed", "needs-owner", 0, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newWorkspaceFixture(t)
			bin := filepath.Join(c.Workspace, "bin")
			state := filepath.Join(c.Workspace, "state")
			if err := os.Mkdir(state, 0755); err != nil {
				t.Fatal(err)
			}
			fixtureFile(t, filepath.Join(bin, "herdr"), `#!/bin/sh
shift 2
case "$1 $2" in
 'workspace list') echo '{"result":{"workspaces":[{"label":"main","workspace_id":"w1"}]}}' ;;
 'pane list')
  pane=w1:p3; agent=null
  if [ -f "$STATE/interrupted" ]; then
   [ "$MODE" = pane-replaced ] && pane=w1:p4
   [ "$MODE" = agent-appeared ] && agent='"codex"'
  fi
  printf '{"result":{"panes":[{"label":"status","pane_id":"%s","agent":%s}]}}\n' "$pane" "$agent" ;;
 'pane process-info')
  args='["wtc","status","--tui"]'
  if [ -f "$STATE/restarted" ]; then
   echo read >> "$STATE/restart-reads"
   n=$(wc -l < "$STATE/restart-reads")
   [ "$MODE" = restart-four ] && [ "$n" -le 4 ] && exit 1
   [ "$MODE" = restart-always ] && exit 1
   [ "$MODE" = restart-once ] && [ "$n" -eq 1 ] && exit 1
   [ "$MODE" = restart-shell ] && args='["zsh"]'
  elif [ -f "$STATE/interrupted" ]; then
   echo read >> "$STATE/interrupt-reads"
   n=$(wc -l < "$STATE/interrupt-reads")
   [ "$MODE" = interrupt-four ] && [ "$n" -le 4 ] && exit 1
   [ "$MODE" = interrupt-always ] && exit 1
   [ "$MODE" = interrupt-once ] && [ "$n" -eq 1 ] && exit 1
   args='["zsh"]'
   [ "$MODE" = interrupt-unrelated ] && args='["nvim"]'
   [ "$MODE" = shell-changed ] && [ "$n" -gt 1 ] && args='["nvim"]'
  fi
  printf '{"result":{"process_info":{"foreground_process_group_id":42,"foreground_processes":[{"pid":43,"argv":["wtc","status"]},{"pid":42,"argv":%s}]}}}\n' "$args" ;;
 'pane send-keys') touch "$STATE/interrupted" ;;
 'pane run') echo run >> "$STATE/runs"; touch "$STATE/restarted" ;;
esac
`, 0755)
			t.Setenv("PATH", bin+":/usr/bin:/bin")
			t.Setenv("HERDR_SESSION", "fixture")
			t.Setenv("HARNESS_HERDR_SESSION", "fixture")
			t.Setenv("STATE", state)
			t.Setenv("MODE", tc.mode)
			report := CatchUpReport{Initiator: c.Collection}
			start := time.Now()
			c.catchUpReloadStatus(&report, catchUpTarget{collection: "main", path: c.Harness, repo: "agent-harness", harness: true}, CatchUpOptions{ReloadStatus: true})
			if elapsed := time.Since(start); elapsed > 8*time.Second {
				t.Errorf("observation budget exceeded: %s", elapsed)
			}
			if len(report.Outcomes) != 1 || report.Outcomes[0].Outcome != tc.outcome {
				t.Fatalf("outcome: %+v, want %s", report.Outcomes, tc.outcome)
			}
			switch tc.mode {
			case "interrupt-always", "interrupt-unrelated", "restart-always", "restart-shell":
				reason := report.Outcomes[0].Reason
				if !strings.Contains(reason, "inspect pane before retrying reload") {
					t.Errorf("missing recovery hint: %s", reason)
				}
				if strings.HasPrefix(tc.mode, "interrupt-") && !strings.Contains(reason, "no command sent") {
					t.Errorf("missing command-delivery state: %s", reason)
				}
			}
			count := func(name string) int {
				data, _ := os.ReadFile(filepath.Join(state, name))
				return strings.Count(string(data), "\n")
			}
			if got := count("runs"); got != tc.runs {
				t.Errorf("sent %d commands, want %d", got, tc.runs)
			}
			// A successful shell observation is revalidated before sending the command.
			wantReads := tc.observations
			if tc.runs == 1 || tc.mode == "shell-changed" {
				wantReads++
			}
			if got := count("interrupt-reads"); got != wantReads {
				t.Errorf("interrupt reads %d, want %d", got, wantReads)
			}
			if tc.mode == "restart-always" || tc.mode == "restart-shell" || tc.mode == "restart-four" {
				if got := count("restart-reads"); got != 5 {
					t.Errorf("restart reads %d, want 5", got)
				}
			}
		})
	}
}
