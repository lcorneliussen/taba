package wtc

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestStatusVersionNotesTargets(t *testing.T) {
	for _, tc := range []struct{ version, tag string }{
		{"0.1.61", "v0.1.61"}, {"v1.2.3", "v1.2.3"},
		{"dev", ""}, {"0.1.62-dev.7+abc1234", ""}, {"1.2.3-rc.1", ""},
		{"1.2.3+local", ""}, {"unknown", ""}, {"", ""}, {"1.2", ""},
		{"1.2.3/../../latest", ""}, {"1.2.3\x1b]8;;https://example.invalid\a", ""},
		{"1.2.3\u009b", ""},
	} {
		t.Run(tc.version, func(t *testing.T) {
			got := StatusVersionNotesURL(tc.version)
			if tc.tag == "" {
				if got != "" {
					t.Fatalf("invented release target for %q: %q", tc.version, got)
				}
			} else if got != "https://github.com/lcorneliussen/taba/releases/tag/"+tc.tag {
				t.Fatalf("wrong release target: %q", got)
			}
		})
	}
}

func TestStatusSnapshotRecordsRunningProducerVersion(t *testing.T) {
	oldVersion := CLIVersion
	t.Cleanup(func() { CLIVersion = oldVersion })
	CLIVersion = "1.2.3"
	c := newWorkspaceFixture(t)
	fixtureFile(t, filepath.Join(c.Harness, ".wtc-cli-version"), "9.9.9\n", 0644)
	for _, all := range []bool{false, true} {
		snapshot, err := c.StatusLocalSnapshot(all)
		if err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(snapshot)
		if err != nil || snapshot.CLIVersion != "1.2.3" || !strings.Contains(string(data), `"cli_version":"1.2.3"`) {
			t.Fatalf("running producer version missing: %s (%v)", data, err)
		}
		if !strings.Contains(snapshot.Markdown(), "CLI: taba/wtc 1.2.3") || !strings.Contains(snapshot.ReposMarkdown(), "CLI: taba/wtc 1.2.3") {
			t.Fatal("text lost producer observation")
		}
		if all {
			continue
		}
		if err := c.WriteStatusSnapshot(snapshot); err != nil {
			t.Fatal(err)
		}
		CLIVersion = "2.0.0"
		cached, err := c.ReadStatusSnapshot()
		if err != nil || cached.CLIVersion != "1.2.3" || RunningCLIVersion() != "2.0.0" {
			t.Fatalf("cache reader overwrote producer: %+v (%v)", cached, err)
		}
		CLIVersion = "1.2.3"
	}
	// Schema 1 caches from older CLIs remain readable; an absent producer is
	// unknown, even when read by a known newer CLI.
	fixtureFile(t, filepath.Join(c.Collection, ".wtc-status.json"), `{"schema":1,"collection":"main","repos":[],"prs":[],"orphans":[]}`, 0644)
	cached, err := c.ReadStatusSnapshot()
	if err != nil || cached.CLIVersion != "" || !strings.Contains(cached.Markdown(), "CLI: taba/wtc unknown") {
		t.Fatalf("old cache compatibility: %+v (%v)", cached, err)
	}
	for _, input := range []string{"", " \n\u009b"} {
		CLIVersion = input
		if got := RunningCLIVersion(); got != "unknown" {
			t.Fatalf("missing build version = %q", got)
		}
	}
}
