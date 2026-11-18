package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"taba.sh/taba/internal/wtc"
)

func TestStatusTUIAuth441LegacyStartupDoesNotPaint(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GH_TOKEN", "synthetic-context-B")
	t.Setenv("GH_CONFIG_DIR", t.TempDir())
	c := &wtc.Context{Collection: t.TempDir()}
	raw := `{"schema":1,"collection":"` + filepath.Base(c.Collection) + `","repos":[],"prs":[{"repo":"widget","number":"7","state":"MERGED","title":"Legacy context A"}]}`
	if err := os.WriteFile(filepath.Join(c.Collection, ".wtc-status.json"), []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	model := newStatusTUIModel(c, false, false, false, true, true, time.Minute, 5*time.Minute, time.Minute, ctx, cancel)
	model.width, model.height = 100, 30
	if model.snapshotLoaded || len(model.snapshot.PRs) != 0 || strings.Contains(model.View().Content, "Legacy context A") {
		t.Fatal("active startup briefly painted an unscoped snapshot")
	}
	// The explicit offline reader remains a separate user-selected local record.
	if snapshot, err := c.ReadStatusSnapshot(); err != nil || snapshot.PRs[0].Title != "Legacy context A" {
		t.Fatal("offline record unavailable", err)
	}
}
