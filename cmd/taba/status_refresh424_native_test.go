package main

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"

	"taba.sh/taba/internal/wtc"
)

// The child runs the real model and collector in the Python fixture's PTY.
func TestStatus424NativeChild(t *testing.T) {
	root := os.Getenv("NATIVE_424_CHILDROOT")
	if root == "" {
		return
	}
	c, err := wtc.OpenCollection(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := runStatusTUI(c, false, false, false, true, true, false, time.Hour, time.Hour, time.Hour); err != nil {
		t.Fatal(err)
	}
}

func TestStatus424NativeForegroundFeedback(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal("Python3 required for synthetic PTY acceptance")
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, python, "testdata/status-refresh424/native.py", executable, "test-child")
	if raw, err := command.CombinedOutput(); err != nil {
		t.Fatalf("native Refresh: %v\n%s", err, raw)
	} else {
		t.Log(string(raw))
	}
}
