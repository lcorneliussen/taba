package wtc

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestTunnelFirstUseNotifierWaitsForOwnedRuntime(t *testing.T) {
	c := runtimeFixture(t)
	off := false
	if err := c.SetTunnelPolicy("widget", "web", "preview", tunnelPolicyEntry{Enabled: &off}, false); err != nil {
		t.Fatal(err)
	}
	before := aliasSafetySnapshot(t, c.runtimeRoot())
	c.signalServicesChanged()
	if !reflect.DeepEqual(before, aliasSafetySnapshot(t, c.runtimeRoot())) || !c.ServicesChangedAt().IsZero() {
		t.Fatal("policy-only notification changed pre-render state")
	}
	// Discovery reads the policy even though an immediate-refresh hint is absent.
	found := false
	for _, item := range c.recordedTunnelItems(c.runtimeDeclarations()) {
		if item.ManagedTunnel && item.TunnelName == "preview" {
			found = true
		}
	}
	if !found {
		t.Fatal("policy-only state disappeared without a notification")
	}
	if _, err := c.RenderRuntime(false); err != nil {
		t.Fatal(err)
	}
	if c.ServicesChangedAt().IsZero() {
		t.Fatal("owned runtime render did not notify")
	}
	old := time.Unix(100, 0)
	if err := os.Chtimes(c.statusSignalPath(), old, old); err != nil {
		t.Fatal(err)
	}
	c.signalServicesChanged()
	if !c.ServicesChangedAt().After(old) {
		t.Fatal("owned runtime notification was suppressed")
	}
	if _, err := os.Stat(filepath.Join(c.runtimeRoot(), "running")); !os.IsNotExist(err) {
		t.Fatal("notification started runner", err)
	}
}

func TestTunnelFirstUseAdmissionPreservesUnsafeState(t *testing.T) {
	for _, kind := range []string{"unrelated-file", "unexpected-directory", "regular-marker", "linked-marker", "dangling-marker", "directory-marker", "fifo-marker", "marker-without-journal", "malformed-journal", "foreign-journal", "unknown-journal-schema", "linked-journal", "directory-journal", "unknown-manifest-schema", "foreign-manifest", "linked-manifest", "missing-schema2-journal"} {
		t.Run(kind, func(t *testing.T) {
			c := runtimeFixture(t)
			off := false
			if err := c.SetTunnelPolicy("widget", "web", "preview", tunnelPolicyEntry{Enabled: &off}, false); err != nil {
				t.Fatal(err)
			}
			root := c.runtimeRoot()
			outside := t.TempDir()
			target := filepath.Join(outside, "keep")
			fixtureFile(t, target, "user bytes", 0600)
			marker := c.statusSignalPath()
			journal := c.tunnelJournalPath()
			manifest := filepath.Join(root, "manifest.json")
			removeJournal := func() {
				t.Helper()
				if err := os.Remove(journal); err != nil {
					t.Fatal(err)
				}
			}
			link := func(target, path string) {
				t.Helper()
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			}
			switch kind {
			case "unrelated-file":
				fixtureFile(t, filepath.Join(root, "keep"), "user bytes", 0600)
			case "unexpected-directory":
				fixtureFile(t, filepath.Join(root, "extra", "keep"), "user bytes", 0600)
			case "regular-marker":
				fixtureFile(t, marker, "user bytes", 0600)
			case "linked-marker":
				link(target, marker)
			case "dangling-marker":
				link(filepath.Join(outside, "missing"), marker)
			case "directory-marker":
				fixtureFile(t, filepath.Join(marker, "keep"), "user bytes", 0600)
			case "fifo-marker":
				if err := syscall.Mkfifo(marker, 0600); err != nil {
					t.Fatal(err)
				}
			case "marker-without-journal":
				removeJournal()
				fixtureFile(t, marker, "", 0600)
			case "malformed-journal":
				fixtureFile(t, journal, "{", 0600)
			case "foreign-journal":
				fixtureFile(t, journal, `{"schema":1,"owner":"foreign-owner"}`, 0600)
			case "unknown-journal-schema":
				fixtureFile(t, journal, `{"schema":99,"owner":"foreign-owner"}`, 0600)
			case "linked-journal":
				removeJournal()
				link(target, journal)
			case "directory-journal":
				removeJournal()
				fixtureFile(t, filepath.Join(journal, "keep"), "user bytes", 0600)
			case "unknown-manifest-schema":
				fixtureFile(t, manifest, `{"schema":99}`, 0600)
			case "foreign-manifest":
				data, _ := json.Marshal(runtimeManifest{Schema: 1, Root: outside, Binary: c.Config.Runtime.Binary})
				fixtureFile(t, manifest, string(data), 0600)
			case "linked-manifest":
				link(target, manifest)
			case "missing-schema2-journal":
				removeJournal()
				data, _ := json.Marshal(runtimeManifest{Schema: 2, Root: root, Binary: c.Config.Runtime.Binary})
				fixtureFile(t, manifest, string(data), 0600)
			}
			if strings.Contains(kind, "journal") || kind == "unrelated-file" || kind == "unexpected-directory" {
				fixtureFile(t, marker, "", 0600)
			}
			before, outsideBefore := aliasSafetySnapshot(t, root), aliasSafetySnapshot(t, outside)
			if _, err := c.RenderRuntime(false); err == nil {
				t.Fatal("unsafe first-use state was admitted")
			}
			c.signalServicesChanged()
			if !reflect.DeepEqual(before, aliasSafetySnapshot(t, root)) {
				t.Fatal("refused render/notification changed runtime bytes or paths")
			}
			if !reflect.DeepEqual(outsideBefore, aliasSafetySnapshot(t, outside)) {
				t.Fatal("refused render/notification changed outside target")
			}
			if _, err := os.Stat(filepath.Join(root, "calls")); !os.IsNotExist(err) {
				t.Fatal("refused render invoked runner", err)
			}
		})
	}
}

func TestTunnelFirstUseOwnedNotifierPreservesLinkedAndNonregularMarkers(t *testing.T) {
	for _, kind := range []string{"linked", "dangling", "directory", "fifo"} {
		t.Run(kind, func(t *testing.T) {
			c := runtimeFixture(t)
			if _, err := c.RenderRuntime(false); err != nil {
				t.Fatal(err)
			}
			marker := c.statusSignalPath()
			if err := os.Remove(marker); err != nil {
				t.Fatal(err)
			}
			outside := t.TempDir()
			target := filepath.Join(outside, "keep")
			fixtureFile(t, target, "user bytes", 0600)
			switch kind {
			case "linked":
				if err := os.Symlink(target, marker); err != nil {
					t.Fatal(err)
				}
			case "dangling":
				if err := os.Symlink(filepath.Join(outside, "missing"), marker); err != nil {
					t.Fatal(err)
				}
			case "directory":
				fixtureFile(t, filepath.Join(marker, "keep"), "user bytes", 0600)
			case "fifo":
				if err := syscall.Mkfifo(marker, 0600); err != nil {
					t.Fatal(err)
				}
			}
			before, outsideBefore := aliasSafetySnapshot(t, c.runtimeRoot()), aliasSafetySnapshot(t, outside)
			c.signalServicesChanged()
			if !reflect.DeepEqual(before, aliasSafetySnapshot(t, c.runtimeRoot())) || !reflect.DeepEqual(outsideBefore, aliasSafetySnapshot(t, outside)) {
				t.Fatal("owned notifier changed unsafe marker or outside target")
			}
		})
	}
}

func TestTunnelFirstUseReleasedMarkerRecoveryPreservesJournal(t *testing.T) {
	for _, namespace := range runtimeNamespaces {
		t.Run(namespace, func(t *testing.T) {
			c := runtimeFixture(t)
			if namespace == ".taba" {
				fixtureFile(t, filepath.Join(c.Collection, namespace, "svc", "keep.yaml"), "tasks: {}\n", 0600)
			}
			off := false
			if err := c.SetTunnelPolicy("widget", "web", "preview", tunnelPolicyEntry{Enabled: &off}, false); err != nil {
				t.Fatal(err)
			}
			root := c.runtimeRoot()
			if filepath.Base(filepath.Dir(root)) != namespace {
				t.Fatal("fixture selected wrong runtime home", root)
			}
			fixtureFile(t, c.statusSignalPath(), "", 0600)
			journal, err := os.ReadFile(c.tunnelJournalPath())
			if err != nil {
				t.Fatal(err)
			}
			if m, err := c.runtimeManifest(); err != nil || m != nil {
				t.Fatal("recovery fixture already has a manifest", err)
			}
			if _, err := c.RenderRuntime(false); err != nil {
				t.Fatal("owned released marker blocked normal recovery", err)
			}
			if after, err := os.ReadFile(c.tunnelJournalPath()); err != nil || string(after) != string(journal) {
				t.Fatal("recovery rewrote ownership journal", err)
			}
			if info, err := os.Lstat(c.statusSignalPath()); err != nil || !info.Mode().IsRegular() || info.Size() != 0 {
				t.Fatal("recovery lost advisory marker", err)
			}
			if m, err := c.runtimeManifest(); err != nil || m == nil || m.Root != root {
				t.Fatal("recovery did not create owned runtime", err)
			}
			if _, err := os.Stat(filepath.Join(root, "calls")); !os.IsNotExist(err) {
				t.Fatal("recovery invoked runner", err)
			}
			if _, err := os.Stat(filepath.Join(root, "running")); !os.IsNotExist(err) {
				t.Fatal("recovery started services", err)
			}
		})
	}
}
