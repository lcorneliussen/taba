package wtc

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"
)

func TestStatus319OmittedObservationIsDeferred(t *testing.T) {
	for _, draft := range []bool{false, true} {
		t.Run(map[bool]string{false: "open", true: "draft"}[draft], func(t *testing.T) {
			t.Setenv("TMPDIR", t.TempDir())
			t.Setenv("WTC_FORGE_MIN_INTERVAL", "3600")
			forge, slug := "bitbucket.org", "example/widget"
			raw, _ := statusEncodeOpenPRs(nil, false, time.Now())
			statusWriteForgeEntry("openprs", forge, slug, "open", raw)
			body, _ := json.Marshal(map[string]any{"id": 7, "state": "OPEN", "draft": draft})
			statusWriteForgeCache(forge, slug, "7", body)
			past := time.Now().Add(-3 * time.Minute)
			if err := os.Chtimes(statusForgeCachePath(forge, slug, "7"), past, past); err != nil {
				t.Fatal(err)
			}
			if !statusTakeForgeToken(forge) {
				t.Fatal("initial token")
			}
			got := statusEnrichRecordMetadata(PRRecord{Number: "7"}, slug, forge)
			encoded, _ := json.Marshal(got)
			var fields map[string]any
			_ = json.Unmarshal(encoded, &fields)
			if fields["Freshness"] != "deferred" || fields["ObservedAt"] == nil || fields["ObservedAt"] == "" {
				t.Fatalf("old fact claimed current: %s", encoded)
			}
			want := "OPEN"
			if draft {
				want = "DRAFT"
			}
			if got.State != want {
				t.Fatalf("cached fact lost: %+v", got)
			}
			if _, ok := statusReadForgeEntry("omit", forge, slug, "7"); ok {
				t.Fatal("deferred confirmation marked complete")
			}
		})
	}
}

func TestStatus319CompetingPanesEventuallyConfirm(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("WTC_FORGE_MIN_INTERVAL", "3600")
	forge, slug := "bitbucket.org", "example/widget"
	raw, _ := statusEncodeOpenPRs(nil, false, time.Now())
	statusWriteForgeEntry("openprs", forge, slug, "open", raw)
	statusWriteForgeCache(forge, slug, "7", []byte(`{"id":7,"state":"OPEN","draft":true}`))
	dir := t.TempDir()
	fixtureFile(t, dir+"/bb", "#!/bin/sh\nprintf '%s' '{\"id\":7,\"state\":\"MERGED\",\"merged_on\":\"2026-11-04T10:01:00Z\"}'\n", 0700)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	for tick := 0; tick < 3; tick++ {
		// One replenished token per scheduling interval; another pane always arrives first.
		if err := os.WriteFile(statusForgeStatePath("budget", forge), []byte("1 "+fmt.Sprint(time.Now().UnixNano())), 0600); err != nil {
			t.Fatal(err)
		}
		_, _ = statusSingleFlight("openprs", forge, fmt.Sprintf("example/competitor%d", tick), "open", func() ([]byte, error) { return []byte(`{"prs":[]}`), nil })
		got := statusEnrichRecordMetadata(PRRecord{Number: "7"}, slug, forge)
		if got.State == "MERGED" {
			return
		}
	}
	t.Fatal("omitted draft starved behind competing panes for every replenished token")
}
