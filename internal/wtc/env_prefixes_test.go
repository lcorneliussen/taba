package wtc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"taba.sh/taba/internal/envcompat"
)

func TestRenderEnvEmitsBothPrefixes(t *testing.T) {
	c := fixture(t)
	data, err := c.RenderEnv()
	if err != nil {
		t.Fatal(err)
	}
	values := envcompat.Assignments(data)
	for _, suffix := range []string{"COLLECTION", "AGENT_NAME", "CONFIG_ROOT"} {
		legacy, legacySet := values["WTC_"+suffix]
		current, currentSet := values["TABA_"+suffix]
		if !legacySet || !currentSet || legacy != current || legacy == "" {
			t.Errorf("%s: WTC_=%q (%t), TABA_=%q (%t); want one non-empty value under both", suffix, legacy, legacySet, current, currentSet)
		}
	}
	if strings.Index(string(data), "WTC_COLLECTION=") > strings.Index(string(data), "TABA_COLLECTION=") {
		t.Error("legacy spelling should come first")
	}
}

// A hook started from a shell that belongs to another collection must see
// this collection under both spellings; readers prefer TABA_, so leaving an
// inherited TABA_COLLECTION in place would point the hook at the wrong one.
func TestHooksOverrideBothPrefixes(t *testing.T) {
	c := fixture(t)
	t.Setenv("TABA_COLLECTION", "other")
	t.Setenv("TABA_CONFIG_ROOT", "/elsewhere")
	hooks := filepath.Join(c.Harness, "hooks", "wtc")
	if err := os.MkdirAll(hooks, 0755); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "env")
	script := "#!/bin/sh\nprintf '%s|%s|%s|%s' \"$WTC_COLLECTION\" \"$TABA_COLLECTION\" \"$WTC_CONFIG_ROOT\" \"$TABA_CONFIG_ROOT\" > " + out + "\n"
	if err := os.WriteFile(filepath.Join(hooks, "env.post.sh"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	if err := c.RunHook("env.post", nil); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	want := "demo|demo|" + c.ConfigRoot + "|" + c.ConfigRoot
	if string(got) != want {
		t.Fatalf("hook saw %q, want %q", got, want)
	}
}
