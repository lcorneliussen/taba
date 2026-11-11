//go:build recording

package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"taba.sh/taba/internal/next"
	"taba.sh/taba/internal/wtc"
)

// Recording-only adapter for the real terminal model. It never calls Init,
// invokes a command, reads a collection or dispatches an agent handoff.
func TestStatusRecordingFrames(t *testing.T) {
	input, output := os.Getenv("WTC_RECORDING_INPUT"), os.Getenv("WTC_RECORDING_OUTPUT")
	if input == "" || output == "" {
		t.Skip("supply recording input/output files")
	}
	var request struct {
		Columns int `json:"columns"`
		Rows    int `json:"rows"`
		Frames  []struct {
			At       float64            `json:"at"`
			Phase    string             `json:"phase"`
			Snapshot wtc.StatusSnapshot `json:"snapshot"`
			Facts    []next.Facts       `json:"facts"`
			Keys     []string           `json:"keys"`
		} `json:"frames"`
	}
	data, err := os.ReadFile(input)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &request); err != nil {
		t.Fatal(err)
	}
	if request.Columns < 30 || request.Columns > 200 || request.Rows < 10 || request.Rows > 80 {
		t.Fatal("invalid recording terminal dimensions")
	}
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "")
	model := statusTUIModel{width: request.Columns, height: request.Rows, focused: true}
	var frames []map[string]any
	for _, frame := range request.Frames {
		if len(frame.Facts) != len(frame.Snapshot.Repos) {
			t.Fatal("each repository needs its own shared fixture facts")
		}
		for i := range frame.Snapshot.Repos {
			choices := next.ChoicesFor(frame.Facts[i])
			frame.Snapshot.Repos[i].Choices = &choices
			frame.Snapshot.Repos[i].Next = choices.Primary()
		}
		model.snapshot, model.lastRefresh = frame.Snapshot, time.Now()
		for _, key := range frame.Keys {
			code := rune(0)
			switch key {
			case "n", "s", "j", "k", "?":
				code = []rune(key)[0]
			case "space":
				code = tea.KeySpace
			case "right":
				code = tea.KeyRight
			case "esc":
				code = tea.KeyEscape
			default:
				t.Fatalf("recording key %q could dispatch work; refused", key)
			}
			updated, command := model.Update(tea.KeyPressMsg(tea.Key{Code: code, Text: key}))
			if command != nil {
				t.Fatal("recording selection unexpectedly returned a command")
			}
			model = updated.(statusTUIModel)
		}
		content := model.View().Content
		for _, line := range strings.Split(content, "\n") {
			if ansi.StringWidth(line) > request.Columns {
				t.Fatal("terminal recording would wrap a native row")
			}
		}
		frames = append(frames, map[string]any{"at": frame.At, "phase": frame.Phase, "content": content, "keys": frame.Keys})
	}
	encoded, err := json.MarshalIndent(map[string]any{"columns": request.Columns, "rows": request.Rows, "frames": frames, "renderer": "native statusTUIModel.View; synthetic facts; selection only"}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(output, append(encoded, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
}
