package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/charmbracelet/x/ansi"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"taba.sh/taba/internal/wtc"
)

func TestStatusResourceUnknownFocusAndActivation(t *testing.T) {
	now := time.Now()
	m := statusTUIModel{context: &wtc.Context{Collection: t.TempDir()}, runtimeReadAt: now, background: 5 * time.Minute, interval: 30 * time.Second}
	if m.resourceObserved(now) || m.runtimeDue(now.Add(15*time.Second)) {
		t.Fatal("unknown focus admitted foreground runtime cadence")
	}
	if !m.runtimeDue(now.Add(time.Minute)) {
		t.Fatal("unknown focus disabled bounded runtime safety read")
	}
	next, _ := m.Update(tea.FocusMsg{})
	m = next.(statusTUIModel)
	if !m.resourceObserved(now.Add(time.Second)) || !m.runtimeDue(now.Add(3*time.Second)) {
		t.Fatal("focus did not activate useful observations")
	}
	if m.resourceObserved(now.Add(2 * time.Minute)) {
		t.Fatal("one focus event became a permanent attachment claim")
	}
	next, _ = m.Update(tea.BlurMsg{})
	m = next.(statusTUIModel)
	if m.resourceObserved(now) {
		t.Fatal("blur left foreground hint")
	}
}

func TestStatusResourceOriginalSnapshotAge(t *testing.T) {
	at := time.Now().Add(-time.Hour).Truncate(time.Second)
	snapshot := wtc.StatusSnapshot{GeneratedAt: at.Format(time.RFC3339Nano)}
	if got := statusSnapshotTime(snapshot); !got.Equal(at) {
		t.Fatalf("original age replaced: %v", got)
	}
	for _, stamp := range []string{"", "invalid", time.Now().Add(time.Hour).Format(time.RFC3339Nano)} {
		snapshot.GeneratedAt = stamp
		if !statusSnapshotTime(snapshot).IsZero() {
			t.Fatal("invalid/future observation received a current age")
		}
	}
}

func TestStatusResourceOldRuntimeCannotClobberNewerFullObservation(t *testing.T) {
	now := time.Now()
	c := &wtc.Context{Collection: t.TempDir()}
	m := statusTUIModel{context: c, runtimeObservedAt: now, snapshot: wtc.StatusSnapshot{Collection: "fixture", Runtime: []wtc.RuntimeItem{{Path: "widget/web", State: "ready", URL: "http://new.example.test"}}}}
	next, _ := m.Update(statusRuntimeMsg{at: now.Add(-time.Minute), items: []wtc.RuntimeItem{{Path: "widget/web", State: "failed"}}})
	m = next.(statusTUIModel)
	if m.snapshot.Runtime[0].State != "ready" {
		t.Fatal("older runtime completion replaced newer facts")
	}
	next, _ = m.Update(statusLoadedMsg{at: now.Add(time.Second), snapshot: wtc.StatusSnapshot{Collection: "fixture", RuntimeObservedAt: now.Add(-time.Second), Runtime: []wtc.RuntimeItem{{Path: "widget/web", State: "failed"}}}})
	m = next.(statusTUIModel)
	if m.snapshot.Runtime[0].State != "ready" {
		t.Fatal("older full collector replaced newer service facts")
	}
	next, _ = m.Update(statusLoadedMsg{at: now.Add(2 * time.Second), snapshot: wtc.StatusSnapshot{Collection: "fixture", RuntimeObservedAt: now.Add(time.Second), Runtime: []wtc.RuntimeItem{{Path: "widget/web", State: "stopped"}}}})
	m = next.(statusTUIModel)
	if m.snapshot.Runtime[0].State != "stopped" {
		t.Fatal("new full observation was hidden by saved runtime")
	}
}

func TestStatusResourceLoadingHasNoAuthorityAndResponds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := statusStartupModel{ctx: ctx, cancel: cancel, width: 34, height: 12}
	if got := m.View().Content; strings.Contains(got, "widget") || !strings.Contains(got, "loading") || !strings.Contains(got, "q quit") {
		t.Fatal("loading disclosed unvalidated data or lacked controls")
	}
	next, _ := m.Update(tea.KeyPressMsg{Code: '?'})
	m = next.(statusStartupModel)
	if !strings.Contains(m.View().Content, "scope validation") {
		t.Fatal("loading help did not respond")
	}
	next, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m = next.(statusStartupModel)
	if m.width != 100 || m.height != 24 {
		t.Fatal("loading resize blocked")
	}
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'q'})
	if cmd == nil || ctx.Err() == nil {
		t.Fatal("loading quit did not cancel owned discovery")
	}
}

func TestStatusResourceUnknownDevFollowHasBoundedSafetyCheck(t *testing.T) {
	now := time.Now()
	m := statusTUIModel{context: &wtc.Context{Collection: t.TempDir()}, devAllowed: true, devCollection: "synthetic", devCheckAt: now, nextRefresh: now.Add(time.Hour)}
	next, _ := m.Update(statusTickMsg(now))
	m = next.(statusTUIModel)
	if m.devChecking {
		t.Fatal("unknown focus immediately checked development source")
	}
	m.devCheckAt = now.Add(-6 * time.Minute)
	next, cmd := m.Update(statusTickMsg(now))
	m = next.(statusTUIModel)
	if !m.devChecking || cmd == nil {
		t.Fatal("unknown focus disabled bounded developer source reconciliation")
	}
}

func TestStatusResourcePendingWatcherBlocksCachedActions(t *testing.T) {
	c, rows := batchFixture(t)
	m := statusTUIModel{context: c, watcherPending: true, showNext: true, snapshot: wtc.StatusSnapshot{Repos: rows}, width: 100, height: 24}
	if len(statusNextRepos(m.snapshot)) == 0 {
		t.Fatal("fixture has no cached next choices")
	}
	for _, key := range []tea.KeyPressMsg{{Code: tea.KeyEnter}, {Code: 'r'}, {Code: 'n'}} {
		next, cmd := m.Update(key)
		got := next.(statusTUIModel)
		if cmd != nil || got.nextRunning || got.pendingForeground {
			t.Fatal("watcher admission allowed cached actions or collection")
		}
	}
}

func TestStatusResourceSavedUnknownAgeIsNotFresh(t *testing.T) {
	m := statusTUIModel{snapshotLoaded: true, snapshot: wtc.StatusSnapshot{Collection: "fixture"}}
	if !strings.Contains(m.headerLine(), "age unknown") || strings.Contains(m.headerLine(), "0s old") {
		t.Fatal("unknown saved age became current")
	}
}

func TestStatusResourceNativeLoadingAndSavedFrames(t *testing.T) {
	now := time.Now()
	stamp := now.Add(-time.Hour).Format(time.RFC3339Nano)
	merged := "MERGED"
	for _, width := range []int{34, 100} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			loading := statusStartupModel{width: width, height: 30}
			snapshot := wtc.StatusSnapshot{Schema: 1, Collection: "fixture", GeneratedAt: stamp, Repos: []wtc.StatusRepo{{Collection: "fixture", Repo: "widget", Dir: "widget", BranchKind: "branch", Branch: "topic", BranchDisplay: "topic", Tree: "clean", PRUnconfirmed: true}}, PRs: []wtc.StatusPRRow{{Repo: "widget", Slug: "example/widget", Number: "7", State: "MERGED", Merge: &merged, Freshness: "recorded", ObservedAt: stamp, Title: "Recorded merge"}}}
			saved := statusTUIModel{context: &wtc.Context{Collection: t.TempDir()}, snapshot: snapshot, snapshotLoaded: true, lastRefresh: statusSnapshotTime(snapshot), width: width, height: 30, watcherPending: true, refreshing: true, progressStep: "Registering status watcher"}
			firstRender := time.Now()
			frames := map[string]string{"loading": loading.View().Content, "saved-watcher-pending": saved.View().Content}
			saved.watcherPending, saved.refreshing = false, false
			frames["unknown-idle"] = saved.View().Content
			saved.focused, saved.observedUntil = true, now.Add(time.Minute)
			saved.showHelp = true
			frames["recent-observed-help"] = saved.View().Content
			lastRender := time.Now()
			for name, frame := range frames {
				plain := ansi.Strip(frame)
				if name == "loading" {
					if strings.Contains(plain, "widget") {
						t.Fatal("loading inherited unvalidated repository facts")
					}
				} else if name != "recent-observed-help" {
					if !strings.Contains(plain, "widget") || !strings.Contains(plain, "PRs") || !strings.Contains(plain, "merged") {
						t.Fatalf("saved frame lost sections, terminal authority or original age: %s", plain)
					}
					match := regexp.MustCompile(`(\d+)s`).FindStringSubmatch(strings.Split(plain, "\n")[0])
					if len(match) != 2 {
						t.Fatal("saved header has no original age")
					}
					age, err := strconv.Atoi(match[1])
					if err != nil || age < int(firstRender.Sub(saved.lastRefresh).Seconds()) || age > int(lastRender.Sub(saved.lastRefresh).Seconds()) {
						t.Fatalf("saved header renewed original observation: %s", plain)
					}
				}
				for _, line := range strings.Split(frame, "\n") {
					if ansi.StringWidth(line) > width {
						t.Fatal("resource frame wraps")
					}
				}
				if dir := os.Getenv("WTC_RESOURCE_FRAME_DIR"); dir != "" {
					raw, _ := json.Marshal(map[string]any{"case": name, "columns": width, "content": frame})
					if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%s-%d.json", name, width)), raw, 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
		})
	}
}

func TestStatusResourceCompletedReceiverDoesNotKeepCollectorCallback(t *testing.T) {
	// The returned receiver may hold a progress function tied to a closed stream.
	receiver := &wtc.Context{Collection: t.TempDir(), StatusProgress: func(string) { t.Fatal("retained completed collector callback") }}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := statusTUIModel{uiContext: ctx, context: &wtc.Context{Collection: receiver.Collection}}
	next, _ := m.Update(statusLoadedMsg{receiver: receiver, at: time.Now(), snapshot: wtc.StatusSnapshot{Collection: "fixture"}})
	got := next.(statusTUIModel)
	if got.context.StatusProgress != nil {
		t.Fatal("action recheck retained the completed collector stream")
	}
	if receiver.StatusProgress == nil {
		t.Fatal("binding mutated the completed receiver")
	}
}
