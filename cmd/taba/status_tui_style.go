package main

import (
	"net/url"
	"os"
	"regexp"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"taba.sh/taba/internal/wtc"
)

const (
	statusToneHeading       = "1"
	statusToneLabel         = "1"
	statusToneDim           = "2"
	statusToneLink          = "36"
	statusToneSecondaryLink = "0"
	statusToneSuccess       = "32"
	statusToneWarning       = "33"
	statusToneFailure       = "31"
)

var statusTUILinkPattern = regexp.MustCompile(`(?i)\x1b]8;;https?://[^\x07\x1b]*(?:\x07|\x1b\\)(.*?)\x1b]8;;(?:\x07|\x1b\\)`)

func statusTUIHasVisibleLink(line string) bool {
	for _, match := range statusTUILinkPattern.FindAllStringSubmatch(line, -1) {
		if strings.Trim(ansi.Strip(match[1]), " \t…") != "" {
			return true
		}
	}
	return false
}

func statusTUIStyle(value, tone string) string {
	if value == "" || os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return value
	}
	return "\x1b[" + tone + "m" + value + "\x1b[0m"
}

func statusTUISafe(value string) string {
	return strings.Map(func(r rune) rune {
		if r < ' ' || r == 0x7f || r >= 0x80 && r <= 0x9f {
			return ' '
		}
		return r
	}, ansi.Strip(value))
}

func statusTUIURL(raw string) string {
	if raw == "" || strings.IndexFunc(raw, func(r rune) bool { return r < ' ' || r == 0x7f || r >= 0x80 && r <= 0x9f }) >= 0 {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" {
		return ""
	}
	return raw
}

func statusTUILink(label, rawURL string) string {
	return statusTUILinkTone(label, rawURL, statusToneLink)
}

func statusTUILinkTone(label, rawURL, tone string) string {
	if os.Getenv("TERM") == "dumb" {
		return label
	}
	target := statusTUIURL(rawURL)
	if target == "" {
		return label
	}
	return ansi.SetHyperlink(target) + statusTUIStyle(label, tone) + ansi.ResetHyperlink()
}

func statusTUILinkCell(cell, rawURL, tone string) string {
	label := strings.TrimRight(cell, " ")
	return statusTUILinkTone(label, rawURL, tone) + strings.TrimPrefix(cell, label)
}

func statusTUIFitANSI(value string, width int) string {
	if width <= 0 {
		return ""
	}
	visible := ansi.StringWidth(value)
	if visible <= width {
		return value + strings.Repeat(" ", width-visible)
	}
	if width == 1 {
		return "…"
	}
	if os.Getenv("TERM") == "dumb" {
		return ansi.Truncate(value, width, "…")
	}
	return ansi.Truncate(value, width, "…") + ansi.ResetHyperlink() + "\x1b[0m"
}

func statusTUIBuildLink(cell string, build *wtc.StatusBuild) string {
	if build == nil {
		return statusTUIStyle(cell, statusToneDim)
	}
	label := strings.TrimRight(cell, " ")
	padding := strings.TrimPrefix(cell, label)
	if build.HeadInBuild != nil && !*build.HeadInBuild && build.Error == "" {
		if build.URL != nil && statusTUIURL(*build.URL) != "" {
			return statusTUILinkTone(label, *build.URL, statusToneDim) + padding
		}
		return statusTUIStyle(label, statusToneDim) + padding
	}
	if build.URL != nil && statusTUIURL(*build.URL) != "" {
		tone := statusToneLink
		if build.Checks != nil {
			switch *build.Checks {
			case "SUCCESS":
				tone = statusToneSuccess
			case "FAILURE", "ERROR":
				tone = statusToneFailure
			case "PENDING", "EXPECTED":
				tone = statusToneWarning
			}
		}
		return statusTUILinkTone(label, *build.URL, tone) + padding
	}
	if build.Checks != nil {
		switch *build.Checks {
		case "SUCCESS":
			return statusTUIStyle(label, statusToneSuccess) + padding
		case "FAILURE", "ERROR":
			return statusTUIStyle(label, statusToneFailure) + padding
		case "PENDING", "EXPECTED":
			return statusTUIStyle(label, statusToneWarning) + padding
		}
	}
	return statusTUIStyle(label, statusToneDim) + padding
}
