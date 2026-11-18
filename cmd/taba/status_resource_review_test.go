package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"taba.sh/taba/internal/wtc"
)

func TestStatusResourceNamedViewKeepsInvokingDevAuthority(t *testing.T) {
	t.Setenv("WTC_CONFIG_ROOT", "")
	t.Setenv("TABA_CONFIG_ROOT", "")
	t.Setenv("WTC_DEV_BUILD", "off")
	t.Setenv("TABA_DEV_BUILD", "off")
	invoking, _ := batchFixture(t)
	viewed, _ := batchFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for _, tc := range []struct{ cwd, want string }{{invoking.Collection, invoking.Collection}, {t.TempDir(), ""}} {
		t.Chdir(tc.cwd)
		m := newStatusTUIModel(viewed, false, false, false, true, true, time.Minute, 5*time.Minute, time.Hour, ctx, cancel)
		if m.devCollection != tc.want {
			t.Errorf("viewed collection changed invoking authority: got %q want %q", m.devCollection, tc.want)
		}
	}
}

func TestStatusResourceProcessesDateActualObservation(t *testing.T) {
	at := time.Now().Add(-2 * time.Second)
	m := statusTUIModel{procs: true, refreshing: true, interval: time.Minute, background: 5 * time.Minute}
	next, _ := m.Update(statusLoadedMsg{at: at})
	got := next.(statusTUIModel)
	if !got.lastRefresh.Equal(at) || strings.Contains(got.headerLine(), "age unknown") {
		t.Fatal("completed process observation lost its age")
	}
}

func TestStatusResourceUnknownAgeFocusExpiryKeepsBoundedSchedule(t *testing.T) {
	for _, stamp := range []string{"", "invalid", time.Now().Add(time.Hour).Format(time.RFC3339Nano)} {
		m := statusTUIModel{watcherPending: true, focused: true, observedUntil: time.Now().Add(-time.Second), lastRefresh: statusSnapshotTime(wtc.StatusSnapshot{GeneratedAt: stamp}), background: 5 * time.Minute}
		before := time.Now()
		next, _ := m.Update(statusTickMsg(before))
		got := next.(statusTUIModel)
		if !got.lastRefresh.IsZero() {
			t.Fatal("invalid saved age became fresh")
		}
		if got.nextRefresh.Before(before.Add(got.background)) {
			t.Fatal("unknown age scheduled background refresh in the past")
		}
	}
}
