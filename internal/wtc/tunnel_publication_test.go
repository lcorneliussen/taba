package wtc

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func publicationFixtureState(t *testing.T, c *Context, raw string) tunnelHostSnapshot {
	t.Helper()
	address, err := parseTunnelAddress(raw)
	if err != nil {
		t.Fatal(err)
	}
	err = c.tunnelRuntimeStore().Update(context.Background(), func(s *tunnelState) error {
		s.Entries = []tunnelEntryState{{Key: testTunnelKey(), Provider: "cloudflare", Mode: "named", Definition: tunnelDigest([]byte("synthetic")), Active: &address}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := buildTunnelHostSnapshot([]tunnelActiveAddress{{testTunnelKey(), raw}})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestTunnelPublicationAtomicGenerationAndStaleRefusal(t *testing.T) {
	c := runtimeFixture(t)
	first := publicationFixtureState(t, c, "https://web.example.test/one")
	if err := c.publishTunnelSnapshot(first); err != nil {
		t.Fatal(err)
	}
	p, pair, err := c.readTunnelPublication()
	if err != nil || pair.Revision != first.Revision || p.HostsFile == p.OriginsFile {
		t.Fatal("paired generation not published", err)
	}
	second := publicationFixtureState(t, c, "https://web.example.test:8443/two")
	if err = c.publishTunnelSnapshot(first); err == nil {
		t.Fatal("stale publication overwrote current active state")
	}
	if err = c.publishTunnelSnapshot(second); err != nil {
		t.Fatal(err)
	}
	_, pair, err = c.readTunnelPublication()
	if err != nil || pair.Revision != second.Revision {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(p.HostsFile); err != nil || string(data) != string(first.Hosts) {
		t.Fatal("old immutable generation changed")
	}
	// An edited output is preserved for reconciliation; publication never
	// silently repairs it or claims the altered pair is a valid generation.
	current, _, _ := c.readTunnelPublication()
	fixtureFile(t, current.HostsFile, "unknown user content", 0600)
	if _, _, err = c.readTunnelPublication(); err == nil {
		t.Fatal("edited output accepted")
	}
	if err = c.publishTunnelSnapshot(second); err == nil {
		t.Fatal("edited output silently repaired")
	}
	data, _ := os.ReadFile(current.HostsFile)
	if string(data) != "unknown user content" {
		t.Fatal("unknown output overwritten")
	}
}

func TestTunnelPublicationConcurrentReadersSeeOnlyWholePairs(t *testing.T) {
	c := runtimeFixture(t)
	first := publicationFixtureState(t, c, "https://web.example.test/one")
	if err := c.publishTunnelSnapshot(first); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errors := make(chan error, 200)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 40; j++ {
				_, snapshot, err := c.readTunnelPublication()
				if err == nil {
					err = validateTunnelHostSnapshot(snapshot)
				}
				if err != nil {
					errors <- err
					return
				}
			}
		}()
	}
	for i := 0; i < 8; i++ {
		raw := "https://web.example.test/one"
		if i%2 == 0 {
			raw = "https://web.example.test:8443/two"
		}
		snapshot := publicationFixtureState(t, c, raw)
		if err := c.publishTunnelSnapshot(snapshot); err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		t.Fatal("reader observed half pair", err)
	}
}

func TestTunnelPublicationRefusesLinkedOutputRoot(t *testing.T) {
	c := runtimeFixture(t)
	snapshot := publicationFixtureState(t, c, "https://web.example.test")
	outside := t.TempDir()
	if err := os.Symlink(outside, c.tunnelPublicationRoot()); err != nil {
		t.Fatal(err)
	}
	if err := c.publishTunnelSnapshot(snapshot); err == nil {
		t.Fatal("linked publication output accepted")
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatal("publication wrote outside runtime root")
	}
	if _, err = os.Stat(filepath.Join(outside, "current.json")); !os.IsNotExist(err) {
		t.Fatal("outside pointer created")
	}
}
