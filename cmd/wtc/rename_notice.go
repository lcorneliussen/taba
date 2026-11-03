package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/x/term"
)

// RenameNoticeEnv silences the notice printed when the legacy command name
// runs.
const RenameNoticeEnv = "TABA_NO_RENAME_NOTICE"

// renameNoticeInput is what decides whether the legacy name is announced.
type renameNoticeInput struct {
	argv      []string
	getenv    func(string) string
	stderrTTY bool
	hasTaba   bool
}

// renameNotice returns the one-line notice for `wtc` run on a terminal, or "".
// It needs stderr to be a terminal, so piped and captured runs skip it, as do
// --json and shell completion. A hook run from an interactive command shares
// that terminal; printRenameNotice keeps such nested calls quiet.
func renameNotice(in renameNoticeInput) string {
	if len(in.argv) == 0 || !in.stderrTTY {
		return ""
	}
	switch filepath.Base(in.argv[0]) {
	case "wtc", "wtc.exe":
	default:
		return ""
	}
	if v := in.getenv(RenameNoticeEnv); v != "" && v != "0" {
		return ""
	}
	for _, arg := range in.argv[1:] {
		if arg == "--" {
			break
		}
		if arg == "--json" || strings.HasPrefix(arg, "--json=") || strings.HasPrefix(arg, "__complete") {
			return ""
		}
	}
	if in.hasTaba {
		return "wtc is now taba: `taba` is the same command and `wtc` keeps working (" + RenameNoticeEnv + "=1 hides this)"
	}
	return "wtc is being renamed to taba; see https://taba.sh (" + RenameNoticeEnv + "=1 hides this)"
}

// printRenameNotice shows the notice at most once per command tree: hooks,
// services and nested calls started from here inherit the silencing variable.
func printRenameNotice(w io.Writer) {
	printRenameNoticeFor(w, os.Args, term.IsTerminal(os.Stderr.Fd()))
}

func printRenameNoticeFor(w io.Writer, argv []string, stderrTTY bool) {
	defer func() { _ = os.Setenv(RenameNoticeEnv, "1") }()
	_, err := exec.LookPath("taba")
	notice := renameNotice(renameNoticeInput{
		argv:      argv,
		getenv:    os.Getenv,
		stderrTTY: stderrTTY,
		hasTaba:   err == nil,
	})
	if notice != "" {
		fmt.Fprintln(w, notice)
	}
}
