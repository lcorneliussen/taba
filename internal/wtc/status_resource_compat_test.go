package wtc

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStatusResourceCompatibilityGitPipeLifetime(t *testing.T) {
	resourceCancellationFixture(t)
	bin := t.TempDir()
	finished := filepath.Join(bin, "finished")
	fixtureFile(t, filepath.Join(bin, "git"), "#!/bin/sh\n(/bin/sleep .5; touch '"+finished+"'; printf tail) &\nprintf head\nexit 0\n", 0700)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))

	for _, tc := range []struct {
		name string
		run  func() (string, error)
	}{
		{"compatibility", func() (string, error) { return gitOutput("status") }},
		{"background-context", func() (string, error) { return gitOutputContext(context.Background(), "status") }},
		{"snapshot-compatibility", func() (string, error) { return projectSnapshotGit("synthetic-owner", "status") }},
		{"snapshot-background", func() (string, error) {
			return projectSnapshotGitContext(context.Background(), "synthetic-owner", "status")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_ = os.Remove(finished)
			got, err := tc.run()
			limit := time.Now().Add(2 * time.Second)
			for time.Now().Before(limit) {
				if _, e := os.Stat(finished); e == nil {
					break
				}
				time.Sleep(5 * time.Millisecond)
			}
			if err != nil || got != "headtail" {
				t.Fatalf("compatibility Git pipe changed: output=%q err=%v", got, err)
			}
		})
	}

}
