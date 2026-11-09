package wtc

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func hoursAt(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}
func testSchedule(zone string, days []int, start, end string) WorkingSchedule {
	return WorkingSchedule{zone, []WorkingWindow{{days, start, end}}}
}
func mustHours(t *testing.T, p *WorkingSchedule, e *HoursExtension, at string) WorkingHoursFacts {
	t.Helper()
	f, err := EvaluateWorkingHours(p, e, hoursAt(t, at))
	if err != nil {
		t.Fatal(err)
	}
	return f
}
func TestWorkingHoursPhasesAndHalfOpenBoundaries(t *testing.T) {
	p := testSchedule("Etc/UTC", []int{1, 2, 3, 4, 5}, "10:00", "16:00")
	for _, tc := range []struct{ at, phase, transition string }{
		{"2026-10-26T09:59:59Z", "outside", "2026-10-26T10:00:00Z"},
		{"2026-10-26T10:00:00Z", "working", "2026-10-26T15:00:00Z"},
		{"2026-10-26T15:00:00Z", "wind-down", "2026-10-26T15:45:00Z"},
		{"2026-10-26T15:45:00Z", "wrap-up", "2026-10-26T16:00:00Z"},
		{"2026-10-26T16:00:00Z", "outside", "2026-10-27T10:00:00Z"},
	} {
		t.Run(tc.at, func(t *testing.T) {
			f := mustHours(t, &p, nil, tc.at)
			if f.Phase != tc.phase || f.Transition == nil || !f.Transition.Equal(hoursAt(t, tc.transition)) {
				t.Fatalf("unexpected facts: %+v", f)
			}
		})
	}
	f := mustHours(t, nil, nil, "2026-10-26T12:00:00Z")
	if f.Configured || f.Phase != "unconfigured" || f.Next != nil {
		t.Fatalf("guessed configuration: %+v", f)
	}
}
func TestWorkingHoursSplitOvernightProjectWeekdaysAndUnion(t *testing.T) {
	p := testSchedule("Asia/Tokyo", []int{1}, "22:00", "02:00")
	f := mustHours(t, &p, nil, "2026-10-26T16:30:00Z")
	if f.Current == nil || f.Phase != "wind-down" || f.Current.Start.Weekday() != time.Monday || f.Next.Start.Weekday() != time.Monday {
		t.Fatalf("overnight: %+v", f)
	}
	p = WorkingSchedule{"Etc/UTC", []WorkingWindow{{[]int{1}, "10:00", "12:00"}, {[]int{1}, "14:00", "16:00"}}}
	f = mustHours(t, &p, nil, "2026-10-26T12:00:00Z")
	if f.Phase != "outside" || f.Next.Start.Hour() != 14 {
		t.Fatalf("split: %+v", f)
	}
	p.Windows = append(p.Windows, WorkingWindow{[]int{1}, "11:00", "14:00"})
	f = mustHours(t, &p, nil, "2026-10-26T11:30:00Z")
	if f.Current == nil || f.Current.Start.Hour() != 10 || f.Current.End.Hour() != 16 || f.Phase != "working" {
		t.Fatalf("union: %+v", f)
	}
}
func TestWorkingHoursDSTGapFoldAndWeeklyNextWindow(t *testing.T) {
	p := testSchedule("Europe/Berlin", []int{0}, "01:30", "03:30")
	f := mustHours(t, &p, nil, "2026-03-29T01:00:00Z")
	if f.Current == nil || f.Current.End.Sub(f.Current.Start) != time.Hour || f.Current.End.Hour() != 3 {
		t.Fatalf("spring: %+v", f)
	}
	f = mustHours(t, &p, nil, "2026-10-25T01:00:00Z")
	if f.Current == nil || f.Current.End.Sub(f.Current.Start) != 3*time.Hour {
		t.Fatalf("fold: %+v", f)
	}
	p = testSchedule("Europe/Berlin", []int{0}, "02:15", "02:45")
	f = mustHours(t, &p, nil, "2026-10-25T00:20:00Z")
	if f.Current == nil || f.Next == nil || !f.Next.Start.Equal(hoursAt(t, "2026-10-25T01:15:00Z")) {
		t.Fatalf("repeated hour: %+v", f)
	}
	f = mustHours(t, &p, nil, "2026-03-23T00:00:00Z")
	if f.Next == nil || f.Next.Start.Day() != 5 || f.Next.Start.Month() != time.April {
		t.Fatalf("nonexistent minutes must skip: %+v", f)
	}
	p = testSchedule("America/New_York", []int{1}, "10:00", "16:00")
	f = mustHours(t, &p, nil, "2026-10-26T21:00:00Z")
	if !f.Next.Start.Equal(hoursAt(t, "2026-11-02T15:00:00Z")) {
		t.Fatalf("next after DST: %+v", f)
	}
}
func TestWorkingHoursExplicitExtensionExpiryAndContinuousCoverage(t *testing.T) {
	p := testSchedule("Etc/UTC", []int{1}, "10:00", "16:00")
	e := &HoursExtension{hoursAt(t, "2026-10-26T16:30:00Z"), hoursAt(t, "2026-10-26T17:00:00Z")}
	if f := mustHours(t, &p, e, "2026-10-26T16:45:00Z"); f.Phase != "extended" || !f.Transition.Equal(e.Until) {
		t.Fatalf("extension: %+v", f)
	}
	for _, at := range []string{"2026-10-26T16:00:00Z", "2026-10-26T17:00:00Z"} {
		if f := mustHours(t, &p, e, at); f.Phase != "outside" || f.Extension != nil {
			t.Fatalf("expiry: %+v", f)
		}
	}
	p.Windows = []WorkingWindow{{[]int{0, 1, 2, 3, 4, 5, 6}, "00:00", "12:00"}, {[]int{0, 1, 2, 3, 4, 5, 6}, "12:00", "00:00"}}
	f := mustHours(t, &p, nil, "2026-10-26T16:00:00Z")
	if f.Phase != "working" || f.Transition != nil || f.Next != nil || !f.Continuous || f.Current != nil {
		t.Fatalf("continuous: %+v", f)
	}
}
func TestWorkingHoursValidation(t *testing.T) {
	for _, s := range []string{
		`{}`, `{"timezone":"Local","windows":[{"days":[1],"start":"10:00","end":"16:00"}]}`,
		`{"timezone":"Etc/UTC","windows":[{"days":[7],"start":"10:00","end":"16:00"}]}`,
		`{"timezone":"Etc/UTC","windows":[{"days":[1,1],"start":"10:00","end":"16:00"}]}`,
		`{"timezone":"Etc/UTC","windows":[{"days":[1],"start":"1:00","end":"16:00"}]}`,
		`{"timezone":"Etc/UTC","windows":[{"days":[1],"start":"10:00","end":"10:00"}]}`,
		`{"timezone":"Etc/UTC","windows":[{"days":[1],"start":"10:00","end":"16:00"}],"unexpected":true}`,
		`{"timezone":"Etc/UTC","windows":[{"days":[1],"start":"10:00","end":"16:00"}]} {}`,
	} {
		if _, err := DecodeWorkingSchedule(strings.NewReader(s)); err == nil {
			t.Fatalf("accepted %s", s)
		}
	}
}
func newTestHoursStore(t *testing.T, person, project string) WorkingHoursStore {
	t.Helper()
	s, err := NewWorkingHoursStore(person, project)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func TestWorkingHoursPersonProjectAndCollectionIsolation(t *testing.T) {
	person := t.TempDir()
	otherPerson := t.TempDir()
	project := t.TempDir()
	a := newTestHoursStore(t, person, project)
	b := newTestHoursStore(t, person, project)
	other := newTestHoursStore(t, otherPerson, project)
	anotherProject := newTestHoursStore(t, person, t.TempDir())
	at := hoursAt(t, "2026-10-26T12:00:00Z")
	f, err := a.Facts(at)
	if err != nil || f.Configured {
		t.Fatal(f, err)
	}
	if _, err := os.Stat(a.directory); !os.IsNotExist(err) {
		t.Fatal("unset facts must not create state")
	}
	p := testSchedule("Etc/UTC", []int{1}, "10:00", "16:00")
	if err := a.Setup(p, false); err != nil {
		t.Fatal(err)
	}
	for _, s := range []WorkingHoursStore{a, b, anotherProject} {
		f, err := s.Facts(at)
		if err != nil || f.Phase != "working" || f.Source != "personal" {
			t.Fatal(f, err)
		}
	}
	if f, _ := other.Facts(at); f.Configured {
		t.Fatal("leaked another person's schedule")
	}
	override := testSchedule("Etc/UTC", []int{3}, "10:00", "16:00")
	if err := b.Setup(override, true); err != nil {
		t.Fatal(err)
	}
	if f, _ := a.Facts(at); f.Phase != "outside" || f.Source != "project" || f.Next.Start.Weekday() != time.Wednesday {
		t.Fatal(f)
	}
	if f, _ := anotherProject.Facts(at); f.Phase != "working" {
		t.Fatal("override crossed project", f)
	}
	if err := a.Clear(true); err != nil {
		t.Fatal(err)
	}
	if f, _ := b.Facts(at); f.Source != "personal" {
		t.Fatal(f)
	}
	info, err := os.Stat(filepath.Join(a.directory, "profile.json"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("profile permissions", info, err)
	}
	f, _ = b.Facts(at)
	raw, _ := json.Marshal(f)
	if strings.Contains(string(raw), project) || strings.Contains(string(raw), a.project) {
		t.Fatal("private identity in facts")
	}
}
func TestWorkingHoursConcurrentReminderClaimAndExtensionShared(t *testing.T) {
	person, project := t.TempDir(), t.TempDir()
	s := newTestHoursStore(t, person, project)
	if err := s.Setup(testSchedule("Etc/UTC", []int{1}, "10:00", "16:00"), false); err != nil {
		t.Fatal(err)
	}
	now := hoursAt(t, "2026-10-26T17:00:00Z")
	var claims atomic.Int32
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			store := newTestHoursStore(t, person, project)
			f, err := store.ClaimReminder(now)
			if err != nil {
				t.Error(err)
			}
			if f.ReminderClaimed {
				claims.Add(1)
			}
		})
	}
	wg.Wait()
	if claims.Load() != 1 {
		t.Fatalf("%d claims", claims.Load())
	}
	if err := s.Extend(now, 30*time.Minute); err != nil {
		t.Fatal(err)
	}
	other := newTestHoursStore(t, person, project)
	f, err := other.ClaimReminder(now.Add(10 * time.Minute))
	if err != nil || f.Phase != "extended" || f.ReminderClaimed {
		t.Fatal(f, err)
	}
	f, err = other.ClaimReminder(now.Add(30 * time.Minute))
	if err != nil || f.Phase != "outside" || f.ReminderClaimed {
		t.Fatal("must not nag after explicit insistence", f, err)
	}
	f, err = other.ClaimReminder(now.Add(7 * 24 * time.Hour))
	if err != nil || !f.ReminderClaimed {
		t.Fatal("next outside interval", f, err)
	}
	for _, duration := range []time.Duration{0, -time.Minute, 4*time.Hour + time.Second} {
		if err := s.Extend(now, duration); err == nil {
			t.Fatal("unbounded extension")
		}
	}
	if err := s.EndExtension(); err != nil {
		t.Fatal(err)
	}
}
func TestWorkingHoursRejectsUnsafeState(t *testing.T) {
	for _, kind := range []string{"directory-symlink", "profile-symlink", "public-profile", "invalid"} {
		t.Run(kind, func(t *testing.T) {
			s := newTestHoursStore(t, t.TempDir(), t.TempDir())
			target := t.TempDir()
			if kind == "directory-symlink" {
				if err := os.Symlink(target, s.directory); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Mkdir(s.directory, 0700); err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(s.directory, "profile.json")
				switch kind {
				case "profile-symlink":
					if err := os.Symlink(filepath.Join(target, "missing"), path); err != nil {
						t.Fatal(err)
					}
				case "public-profile":
					if err := os.WriteFile(path, []byte(`{"version":1}`), 0644); err != nil {
						t.Fatal(err)
					}
				case "invalid":
					if err := os.WriteFile(path, []byte(`{"version":2}`), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			if _, err := s.Facts(time.Now()); err == nil {
				t.Fatal("unsafe state accepted")
			}
		})
	}
}
