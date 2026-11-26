package wtc

import (
	"strings"
	"testing"
)

func TestStatusProcessesKeepsOnlyHerdrDescendants(t *testing.T) {
	output := `100 1 0.1 1.0 10240 herdr --session sample server
101 100 2.5 0.5 20480 codex task
102 101 8.0 1.2 40960 worker child
103 1 99.0 2.0 99999 unrelated worker
104 100 1.0 0.1 1024 shell
`
	rows := statusParseProcesses(output, "sample")
	if len(rows) != 3 || rows[0].PID != 102 || rows[1].PID != 101 || rows[2].PID != 104 {
		t.Fatalf("wrong descendant selection or CPU order: %+v", rows)
	}
	text := StatusProcessesText(rows)
	if !strings.Contains(text, "worker child") || strings.Contains(text, "unrelated worker") {
		t.Fatalf("process view wrong: %s", text)
	}
	if got := statusParseProcesses(output, "other"); len(got) != 0 {
		t.Fatalf("matched another session: %+v", got)
	}
}

func TestStatusProcessesInPaneShellsKeepsTheShells(t *testing.T) {
	output := `200 1 0.5 1.0 2048 -zsh
201 200 9.0 1.0 4096 claude
202 201 3.0 0.5 1024 node mcp
203 1 0.1 0.1 512 -zsh
204 1 50.0 1.0 9999 unrelated worker
`
	rows := statusProcessTree(statusProcessTable(output), map[int]bool{200: true, 203: true})
	if len(rows) != 4 || rows[0].PID != 201 || rows[1].PID != 202 || rows[2].PID != 200 || rows[3].PID != 203 {
		t.Fatalf("pane shells and their descendants, busiest first, expected: %+v", rows)
	}
}
