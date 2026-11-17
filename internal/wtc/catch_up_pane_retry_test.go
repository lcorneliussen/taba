package wtc

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// Retry decisions use production orchestration and parsing with a monotonic
// clock. Launching many real processes cannot guarantee a positive fifth read
// within 7.5s under arbitrary scheduler load. Real cancellation, EOF, reaping
// and late-output refusal are exercised separately by the deadline tests.
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
			c, _ := reloadDeadlineFixture(t)
			start := time.Unix(1700000000, 0)
			now := start
			ctx := reloadLogicalContext{Context: context.Background(), deadline: start.Add(catchUpStatusReloadTimeout)}
			interrupts, runs, interruptReads, restartReads, waits := 0, 0, 0, 0, 0
			calls := []string{}
			const commandCost = 10 * time.Millisecond
			ops := catchUpReloadOps{ctx: ctx, now: func() time.Time { return now }}
			ops.wait = func(ctx context.Context) error {
				if err := catchUpReloadContextErrAt(ctx, now); err != nil {
					return err
				}
				waits++
				now = now.Add(time.Second)
				return catchUpReloadContextErrAt(ctx, now)
			}
			ops.run = func(_ context.Context, name, dir string, args ...string) ([]byte, error) {
				key := strings.Join(args[2:4], " ")
				calls = append(calls, key)
				now = now.Add(commandCost)
				var body any
				switch key {
				case "workspace list":
					body = map[string]any{"result": map[string]any{"workspaces": []any{map[string]any{"label": "main", "workspace_id": "w1"}}}}
				case "pane list":
					pane := "w1:p3"
					var agent any
					if interrupts > 0 {
						if tc.mode == "pane-replaced" {
							pane = "w1:p4"
						}
						if tc.mode == "agent-appeared" {
							agent = "synthetic-agent"
						}
					}
					body = map[string]any{"result": map[string]any{"panes": []any{map[string]any{"label": "status", "pane_id": pane, "agent": agent}}}}
				case "pane process-info":
					argv := []string{"wtc", "status", "--tui"}
					if runs > 0 {
						restartReads++
						if tc.mode == "restart-four" && restartReads <= 4 || tc.mode == "restart-always" || tc.mode == "restart-once" && restartReads == 1 {
							return nil, errors.New("synthetic observation unavailable")
						}
						if tc.mode == "restart-shell" {
							argv = []string{"zsh"}
						}
					} else if interrupts > 0 {
						interruptReads++
						if tc.mode == "interrupt-four" && interruptReads <= 4 || tc.mode == "interrupt-always" || tc.mode == "interrupt-once" && interruptReads == 1 {
							return nil, errors.New("synthetic observation unavailable")
						}
						argv = []string{"zsh"}
						if tc.mode == "interrupt-unrelated" || tc.mode == "shell-changed" && interruptReads > 1 {
							argv = []string{"nvim"}
						}
					}
					// The matching foreground leader controls the decision;
					// the unrelated entry must never supply a false status match.
					body = map[string]any{"result": map[string]any{"process_info": map[string]any{"foreground_process_group_id": 42, "foreground_processes": []any{map[string]any{"pid": 43, "argv": []string{"wtc", "status"}}, map[string]any{"pid": 42, "argv": argv}}}}}
				case "pane send-keys":
					interrupts++
				case "pane run":
					runs++
				default:
					t.Fatalf("unexpected command: %s %v", name, args)
				}
				return json.Marshal(body)
			}
			report := CatchUpReport{Initiator: c.Collection}
			c.catchUpReloadStatusWithOps(&report, reloadTarget(c), CatchUpOptions{ReloadStatus: true}, ops)
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
			if interrupts != 1 || runs != tc.runs {
				t.Errorf("controls %d/%d, want 1/%d", interrupts, runs, tc.runs)
			}
			// A successful shell read must be independently revalidated.
			wantReads := tc.observations
			if tc.runs == 1 || tc.mode == "shell-changed" {
				wantReads++
			}
			if interruptReads != wantReads {
				t.Errorf("interrupt reads %d, want %d", interruptReads, wantReads)
			}
			wantRestart := 0
			if tc.runs == 1 {
				wantRestart = 1
			}
			if tc.mode == "restart-once" {
				wantRestart = 2
			}
			if tc.mode == "restart-always" || tc.mode == "restart-shell" || tc.mode == "restart-four" {
				wantRestart = 5
			}
			if restartReads != wantRestart {
				t.Errorf("restart reads %d, want %d", restartReads, wantRestart)
			}
			wantWaits := 0
			switch tc.mode {
			case "interrupt-once", "restart-once":
				wantWaits = 1
			case "interrupt-four", "restart-four", "interrupt-always", "restart-always", "interrupt-unrelated", "restart-shell":
				wantWaits = 4
			}
			if waits != wantWaits {
				t.Errorf("between-attempt waits %d, want %d (no final failed sleep)", waits, wantWaits)
			}
			if elapsed := now.Sub(start); elapsed != time.Duration(wantWaits)*time.Second+time.Duration(len(calls))*commandCost || elapsed >= catchUpStatusReloadTimeout {
				t.Errorf("logical budget %s, calls=%d", elapsed, len(calls))
			}
			for i, key := range calls {
				if key == "pane send-keys" || key == "pane run" {
					if i < 3 || strings.Join(calls[i-3:i], ",") != "workspace list,pane list,pane process-info" {
						t.Errorf("write lacked fresh identity/occupancy/foreground fence: %v", calls[:i+1])
					}
				}
			}
		})
	}
}
