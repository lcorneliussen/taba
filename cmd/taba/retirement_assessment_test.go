package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"taba.sh/taba/internal/wtc"
)

func TestRetireDryRunExplicitInputsAndEnvelope(t *testing.T) {
	collection := filepath.Join(t.TempDir(), "example-task")
	harness := filepath.Join(collection, "harness")
	if err := os.MkdirAll(harness, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(harness, ".harness-repos.yml"), []byte("repos:\n  - name: widget\n    remote: https://example.invalid/widget.git\n"), 0600); err != nil {
		t.Fatal(err)
	}
	transcript := filepath.Join(t.TempDir(), "task.jsonl")
	line, err := json.Marshal(map[string]any{"type": "assistant", "cwd": collection, "timestamp": "2026-01-01T00:00:00Z", "message": map[string]any{"role": "assistant", "content": []any{map[string]string{"type": "text", "text": "Done. Still verify current facts."}}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(transcript, append(line, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(collection)
	for _, asJSON := range []bool{true, false} {
		var jsonOutput bool
		root := &cobra.Command{Use: "wtc", SilenceUsage: true, SilenceErrors: true}
		root.PersistentFlags().BoolVar(&jsonOutput, "json", false, "")
		addRetireCommand(root, &jsonOutput)
		args := []string{"retire", "--dry-run", "--transcript", transcript, "--transcript", transcript}
		if asJSON {
			args = append(args, "--json")
		} else {
			args = append(args, "example-task")
		}
		root.SetArgs(args)
		read, write, e := os.Pipe()
		if e != nil {
			t.Fatal(e)
		}
		stdout := os.Stdout
		os.Stdout = write
		// Drain concurrently: the report can exceed a pipe's buffer capacity.
		output := make(chan []byte, 1)
		go func() { b, _ := io.ReadAll(read); output <- b }()
		runErr := root.Execute()
		os.Stdout = stdout
		write.Close()
		raw := <-output
		read.Close()
		if runErr != nil {
			t.Fatal(runErr)
		}
		if asJSON {
			var got struct {
				OK   bool                 `json:"ok"`
				Data wtc.RetirementReport `json:"data"`
			}
			if e := json.Unmarshal(raw, &got); e != nil {
				t.Fatalf("invalid JSON: %v %s", e, raw)
			}
			if !got.OK || got.Data.Schema != 1 || got.Data.Collection != collection || got.Data.Assessment.State != "unknown" || len(got.Data.Transcripts) != 2 || got.Data.Transcripts[0].ArchiveState != "unknown" || got.Data.Transcripts[0].MatchedNotes != 1 || len(got.Data.Assessment.Unknowns) == 0 {
				t.Fatalf("lost evidence contract: %s", raw)
			}
		} else if !strings.Contains(string(raw), "no cleanup authorization") || !strings.Contains(string(raw), "Untrusted hint") {
			t.Fatalf("missing evidence labels: %s", raw)
		}
	}
}

func TestRetireTranscriptAndForceFlagsCannotReachExecution(t *testing.T) {
	for _, args := range [][]string{{"retire"}, {"retire", "example", "--transcript", "untrusted.jsonl"}, {"retire", "example", "--dry-run", "--force"}} {
		var asJSON bool
		root := &cobra.Command{Use: "wtc", SilenceUsage: true, SilenceErrors: true}
		addRetireCommand(root, &asJSON)
		root.SetArgs(args)
		if err := root.Execute(); err == nil {
			t.Fatalf("accepted unsafe flag combination: %v", args)
		}
	}
}

func TestRetirementSummaryInTerminalStatus(t *testing.T) {
	snapshot := wtc.StatusSnapshot{Collection: "example", Retirement: []wtc.RetirementAssessment{{Collection: "example", State: "blocked", Blockers: []string{"Preserve local work"}, Unknowns: []string{"Archive state unknown"}}}}
	if text := statusTable(snapshot, false, 120); !strings.Contains(text, "Retirement example: blocked") || !strings.Contains(text, "retire --dry-run") {
		t.Fatalf("missing terminal assessment: %s", text)
	}
	model := statusTUIModel{snapshot: snapshot, width: 120}
	if text := strings.Join(model.contentLines(), "\n"); !strings.Contains(text, "Retirement example: blocked") {
		t.Fatalf("missing live assessment: %s", text)
	}
}
