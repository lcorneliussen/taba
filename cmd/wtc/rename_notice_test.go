package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenameNotice(t *testing.T) {
	env := func(vals map[string]string) func(string) string {
		return func(k string) string { return vals[k] }
	}
	none := env(nil)
	cases := []struct {
		name string
		in   renameNoticeInput
		want bool
	}{
		{"legacy name on a terminal", renameNoticeInput{argv: []string{"/opt/bin/wtc", "status"}, getenv: none, stderrTTY: true, hasTaba: true}, true},
		{"legacy name without taba installed", renameNoticeInput{argv: []string{"wtc"}, getenv: none, stderrTTY: true}, true},
		{"new name", renameNoticeInput{argv: []string{"/opt/bin/taba", "status"}, getenv: none, stderrTTY: true, hasTaba: true}, false},
		{"unknown wrapper name", renameNoticeInput{argv: []string{"wtc.test"}, getenv: none, stderrTTY: true}, false},
		{"stderr not a terminal", renameNoticeInput{argv: []string{"wtc", "status"}, getenv: none}, false},
		{"silenced", renameNoticeInput{argv: []string{"wtc"}, getenv: env(map[string]string{RenameNoticeEnv: "1"}), stderrTTY: true}, false},
		{"zero does not silence", renameNoticeInput{argv: []string{"wtc"}, getenv: env(map[string]string{RenameNoticeEnv: "0"}), stderrTTY: true}, true},
		{"json output", renameNoticeInput{argv: []string{"wtc", "status", "--json"}, getenv: none, stderrTTY: true}, false},
		{"json flag with value", renameNoticeInput{argv: []string{"wtc", "status", "--json=true"}, getenv: none, stderrTTY: true}, false},
		{"json after -- is an argument", renameNoticeInput{argv: []string{"wtc", "svc", "run", "--", "tool", "--json"}, getenv: none, stderrTTY: true}, true},
		{"shell completion", renameNoticeInput{argv: []string{"wtc", "__complete", "st"}, getenv: none, stderrTTY: true}, false},
		{"empty argv", renameNoticeInput{getenv: none, stderrTTY: true}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := renameNotice(c.in)
			if (got != "") != c.want {
				t.Fatalf("notice = %q, want shown=%v", got, c.want)
			}
			if c.want && (got == renameNotice(renameNoticeInput{argv: c.in.argv, getenv: c.in.getenv, stderrTTY: true, hasTaba: !c.in.hasTaba})) {
				t.Fatalf("notice does not depend on whether taba is installed: %q", got)
			}
		})
	}
}

func TestPrintRenameNoticeOncePerTree(t *testing.T) {
	bin := t.TempDir()
	taba := filepath.Join(bin, "taba")
	if err := os.WriteFile(taba, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv(RenameNoticeEnv, "")
	var first, second bytes.Buffer
	printRenameNoticeFor(&first, []string{"wtc", "status"}, true)
	if !strings.Contains(first.String(), "wtc is now taba") {
		t.Fatalf("first notice = %q, want the taba-installed text", first.String())
	}
	if got := os.Getenv(RenameNoticeEnv); got != "1" {
		t.Fatalf("%s = %q after printing, want 1 for child processes", RenameNoticeEnv, got)
	}
	printRenameNoticeFor(&second, []string{"wtc", "status"}, true)
	if second.Len() != 0 {
		t.Fatalf("nested call printed %q", second.String())
	}

	t.Setenv(RenameNoticeEnv, "")
	var quiet bytes.Buffer
	printRenameNoticeFor(&quiet, []string{"taba", "status"}, true)
	if quiet.Len() != 0 || os.Getenv(RenameNoticeEnv) != "1" {
		t.Fatalf("taba printed %q or left children unsilenced", quiet.String())
	}
}
