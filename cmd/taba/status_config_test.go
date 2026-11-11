package main

import (
	"os"
	"path/filepath"
	"testing"

	"taba.sh/taba/internal/wtc"
)

func TestStatusSettingsUseControlRootAndEnvironmentOverride(t *testing.T) {
	root := t.TempDir()
	config := "WTC_STATUS_WATCH='17'\nexport WTC_STATUS_WATCH_BG=123\nWTC_STATUS_NO_CLICK=yes\n"
	if err := os.WriteFile(filepath.Join(root, "wtc.env"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	c := &wtc.Context{ConfigRoot: root}
	t.Setenv("WTC_STATUS_WATCH", "")
	t.Setenv("WTC_STATUS_WATCH_BG", "")
	t.Setenv("WTC_STATUS_NO_CLICK", "")
	if got := statusIntervalSetting(c, "WTC_STATUS_WATCH", 30); got != 17 {
		t.Fatalf("control-root interval: %d", got)
	}
	if got := statusIntervalSetting(c, "WTC_STATUS_WATCH_BG", 300); got != 123 {
		t.Fatalf("background interval: %d", got)
	}
	if !statusTrue(statusSetting(c, "WTC_STATUS_NO_CLICK")) {
		t.Fatal("control-root no-click setting ignored")
	}
	t.Setenv("WTC_STATUS_WATCH", "9")
	if got := statusIntervalSetting(c, "WTC_STATUS_WATCH", 30); got != 9 {
		t.Fatalf("environment did not override file: %d", got)
	}
}

func TestTabaSettingsKeepEnvironmentBeforeFilePrecedence(t *testing.T) {
	c := &wtc.Context{ConfigRoot: t.TempDir()}
	if err := os.WriteFile(filepath.Join(c.ConfigRoot, "wtc.env"), []byte("TABA_STATUS_WATCH='19'\nWTC_STATUS_WATCH=17\nTABA_AGENT_KIND=codex\nWTC_AGENT_KIND=claude\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"WTC_STATUS_WATCH", "TABA_STATUS_WATCH", "WTC_AGENT_KIND", "TABA_AGENT_KIND"} {
		t.Setenv(key, "")
	}
	if err := os.Unsetenv("TABA_STATUS_WATCH"); err != nil {
		t.Fatal(err)
	}
	if got := statusIntervalSetting(c, "WTC_STATUS_WATCH", 30); got != 19 {
		t.Fatalf("new-prefix file setting = %d", got)
	}
	if got := statusSetting(c, "WTC_AGENT_KIND"); got != "codex" {
		t.Fatalf("agent selection ignored new-prefix default: %q", got)
	}
	t.Setenv("WTC_STATUS_WATCH", "9")
	if got := statusIntervalSetting(c, "WTC_STATUS_WATCH", 30); got != 9 {
		t.Fatalf("legacy environment must override new-prefix file setting: %d", got)
	}
	t.Setenv("TABA_STATUS_WATCH", "7")
	if got := statusIntervalSetting(c, "WTC_STATUS_WATCH", 30); got != 7 {
		t.Fatalf("new-prefix environment must override legacy environment: %d", got)
	}
	t.Setenv("TABA_STATUS_WATCH", "")
	if got := statusIntervalSetting(c, "WTC_STATUS_WATCH", 30); got != 19 {
		t.Fatalf("empty new-prefix environment should fall back to file defaults: %d", got)
	}
}

func TestTabaMachineDefaultsFile(t *testing.T) {
	c := &wtc.Context{ConfigRoot: t.TempDir()}
	t.Setenv("WTC_STATUS_WATCH", "")
	t.Setenv("TABA_STATUS_WATCH", "")
	if err := os.WriteFile(filepath.Join(c.ConfigRoot, "taba.env"), []byte("TABA_STATUS_WATCH=19\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := statusIntervalSetting(c, "WTC_STATUS_WATCH", 30); got != 19 {
		t.Fatalf("alias defaults file ignored: %d", got)
	}
	if err := os.WriteFile(filepath.Join(c.ConfigRoot, "wtc.env"), []byte("WTC_STATUS_WATCH=9\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := statusIntervalSetting(c, "WTC_STATUS_WATCH", 30); got != 9 {
		t.Fatalf("legacy defaults file lost authority: %d", got)
	}
}
