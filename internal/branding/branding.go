// Package branding names the command for display: Name and Text follow the
// invoked executable, and Product is the fixed project label for headings.
// On-disk and protocol identifiers are not renamed during the compatibility
// window.
package branding

import (
	"os"
	"path/filepath"
	"strings"
)

// Product names the project in release-note headings, status headers and the
// status snapshot's CLI line.
// The 0.2 releases carry both names while the wtc command remains; 0.3.0
// drops "/wtc" (#203).
const Product = "taba/wtc"

// Name follows argv[0], including symlinks and dev-build handovers. Unknown
// executable names retain the released command's spelling.
func Name() string {
	return name(os.Args[0])
}

func name(argv0 string) string {
	switch filepath.Base(argv0) {
	case "taba", "taba.exe":
		return "taba"
	default:
		return "wtc"
	}
}

// Text expands an explicit command placeholder in authored human-facing text.
// Pass templates before interpolation so user values, paths, plugin IDs and
// machine payloads are never rewritten as display text.
func Text(template string) string {
	return strings.ReplaceAll(template, "{{command}}", Name())
}
