package main

import (
	"fmt"
	"testing"
	"time"

	"taba.sh/taba/internal/wtc"
)

func BenchmarkStatusResourceUnchangedRuntimeView(b *testing.B) {
	for _, width := range []int{42, 160} {
		b.Run(fmt.Sprint(width), func(b *testing.B) {
			m := statusTUIModel{width: width, height: 40, paintAt: time.Now(), snapshot: wtc.StatusSnapshot{
				Collection: "fixture", Repos: []wtc.StatusRepo{{Repo: "widget", Dir: "widget", BranchDisplay: "topic", Tree: "clean"}},
				PRs: []wtc.StatusPRRow{{Repo: "widget", Number: "7", State: "OPEN", Title: "Synthetic change", Freshness: "cached"}},
			}}
			want := m.View().Content
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				next, _ := m.Update(statusRuntimeTickMsg{})
				m = next.(statusTUIModel)
				if got := m.View().Content; got != want {
					b.Fatal("unchanged service timer changed the rendered facts")
				}
			}
		})
	}
}
