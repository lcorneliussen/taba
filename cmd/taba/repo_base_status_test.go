package main

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"taba.sh/taba/internal/wtc"
)

func TestRepositoryBaseStatusDetailsBoundedAndLiteral(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	base := &wtc.RepositoryBase{Source: "collection", EffectiveRef: "origin/βeta", Reason: "Feature staging"}
	for _, width := range []int{16, 24, 40, 80, 120} {
		for _, styled := range []bool{false, true} {
			for _, reason := range []string{"Feature staging", strings.Repeat("界 🐾 é ", 100), "cached\x1b]8;;https://bad.invalid\acontrol\x1b]8;;\a\nreason"} {
				base.Reason = reason
				lines := statusTUIBaseDetails(base, width, styled)
				if len(lines) > 3 || len(lines) == 0 {
					t.Fatalf("unbounded details: %d", len(lines))
				}
				joined := ansi.Strip(strings.Join(lines, ""))
				if strings.Contains(joined, "\n") || strings.Contains(joined, "\x1b") {
					t.Fatal("unescaped cached controls")
				}
				if strings.Contains(strings.Join(lines, ""), "\x1b]8;") {
					t.Fatal("detail acquired hyperlink")
				}
				for _, line := range lines {
					if ansi.StringWidth(line) > width {
						t.Fatalf("width %d: %q", width, line)
					}
					if !styled && strings.Contains(line, "\x1b") {
						t.Fatal("plain detail styled")
					}
				}
				if len(reason) > 100 && !strings.Contains(joined, "repo base show") {
					t.Fatal("truncated reason lacks explicit full-value hint")
				}
			}
		}
	}
	if statusTUIBaseDetails(nil, 80, false) != nil || statusTUIBaseDetails(&wtc.RepositoryBase{Source: "registry"}, 80, false) != nil {
		t.Fatal("no-override rows changed")
	}
	base.Reason = "Feature staging"
	text := ansi.Strip(strings.Join(statusTUIBaseDetails(base, 120, false), ""))
	if !strings.Contains(text, "origin/βeta") || !strings.Contains(text, "Feature staging") {
		t.Fatal("normal status hides base or reason")
	}
}

func TestRepositoryBaseStatusMappingAndScrolling(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	url := "https://example.invalid/pull/7"
	snapshot := wtc.StatusSnapshot{Collection: "fixture", Repos: []wtc.StatusRepo{
		{Collection: "fixture", Repo: "first", Dir: "first", BranchDisplay: "topic", DevelopmentBase: &wtc.RepositoryBase{Source: "collection", EffectiveRef: "origin/βeta", Reason: strings.Repeat("界 staging ", 40)}},
		{Collection: "fixture", Repo: "widget", Dir: "widget", BranchDisplay: "topic", PR: &wtc.StatusPRFacts{Number: "7", URL: url}, Runtime: "ready"},
	}, Runtime: []wtc.RuntimeItem{{Collection: "fixture", Repo: "widget", Dir: "widget", Path: "widget/web/server", Group: "widget/web", Kind: "service", State: "ready"}, {Collection: "fixture", Repo: "widget", Dir: "widget", Path: "widget/web/tunnel", Group: "widget/web", Kind: "endpoint", State: "ready", URL: "https://example.invalid", Exposure: wtc.ExposureProtected, ExposureBasis: wtc.ExposureDeclared}}, PRs: []wtc.StatusPRRow{{Repo: "widget", Number: "7", URL: &url}}}
	for _, width := range []int{24, 40, 80, 120} {
		for _, styled := range []bool{false, true} {
			indices := map[string]int{}
			lines := statusTUIRepoLines(snapshot, width, styled, func(i int, r wtc.StatusRepo) { indices[r.Repo] = i })
			if indices["widget"] != indices["first"]+4 {
				t.Fatalf("following repo shifted incorrectly: %+v", indices)
			}
			for _, line := range lines {
				if ansi.StringWidth(line) > width {
					t.Fatalf("repo width %d: %q", width, line)
				}
			}
		}
		model := statusTUIModel{snapshot: snapshot, width: width, height: 8}
		content := model.contentLayout()
		first := -1
		for _, row := range content.rows {
			if row.repo != nil && row.repo.Repo == "first" {
				first = row.start
			}
		}
		if first < 0 {
			t.Fatal("first repo missing")
		}
		for scroll := 0; scroll <= len(content.lines); scroll++ {
			model.scroll = scroll
			v := model.viewport(content)
			for offset := 1; offset <= 3; offset++ {
				y := first + offset - v.scroll
				if y < v.header || y >= v.header+v.body {
					continue
				}
				for x := 0; x < width; x++ {
					if _, ok := model.visibleRow(x, y); ok {
						t.Fatalf("detail actionable at %d/%d/%d", width, x, y)
					}
					if _, ok := model.runtimeItemAt(x, y); ok {
						t.Fatal("detail selected service")
					}
					if got := model.buildClickTarget(x, y); got != "" {
						t.Fatal("detail opened URL")
					}
				}
			}
			for _, row := range content.rows {
				y := row.start - v.scroll
				if y < v.header || y >= v.header+v.body {
					continue
				}
				got, ok := model.visibleRow(1, y)
				if !ok || got.key != row.key {
					t.Fatalf("scroll mapping %s => %+v", row.key, got)
				}
				if row.pr != nil && model.buildClickTarget(1, y) != url {
					t.Fatal("following PR click shifted")
				}
				if row.repo != nil && row.repo.Repo == "widget" {
					layout := statusTUIRepoLayout(snapshot, width)
					if model.buildClickTarget(layout.prStart(), y) != url {
						t.Fatal("following repository PR click shifted")
					}
					if layout.showRuntime {
						if item, ok := model.runtimeItemAt(layout.runtimeStart(), y); !ok || item.Kind != "service" {
							t.Fatal("following repository service click shifted")
						}
					}
				}
				if row.service != nil {
					layout := statusTUIRuntimeLayout(snapshot, width)
					if target := model.buildClickTarget(layout.tunnelStart("")+2, y); target != "https://example.invalid" && width >= 80 {
						t.Fatalf("service link shifted: %s", target)
					}
				}
			}
		}
	}
}
