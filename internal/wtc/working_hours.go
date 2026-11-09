package wtc

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"time"
	_ "time/tzdata"
)

// WorkingSchedule uses local wall-clock minutes. Days use Sunday=0; an end
// before the start crosses midnight. Equal endpoints are invalid, not all-day.
type WorkingSchedule struct {
	Timezone string          `json:"timezone"`
	Windows  []WorkingWindow `json:"windows"`
}
type WorkingWindow struct {
	Days  []int  `json:"days"`
	Start string `json:"start"`
	End   string `json:"end"`
}
type HoursWindow struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}
type HoursExtension struct {
	Start time.Time `json:"start"`
	Until time.Time `json:"until"`
}
type WorkingHoursFacts struct {
	Version           int             `json:"version"`
	EvaluatedAt       time.Time       `json:"evaluated_at"`
	Configured        bool            `json:"configured"`
	UnavailableReason string          `json:"unavailable_reason,omitempty"`
	Continuous        bool            `json:"continuous,omitempty"`
	Phase             string          `json:"phase"`
	Timezone          string          `json:"timezone,omitempty"`
	Source            string          `json:"source,omitempty"`
	Current           *HoursWindow    `json:"current_window,omitempty"`
	Next              *HoursWindow    `json:"next_window,omitempty"`
	Transition        *time.Time      `json:"next_transition,omitempty"`
	Extension         *HoursExtension `json:"extension,omitempty"`
	ReminderClaimed   bool            `json:"reminder_claimed"`
	Guidance          string          `json:"guidance"`
	reminderKey       string
}

func clockMinute(s string) (int, error) {
	t, err := time.Parse("15:04", s)
	if err != nil || t.Format("15:04") != s {
		return 0, fmt.Errorf("expected HH:MM wall time")
	}
	return t.Hour()*60 + t.Minute(), nil
}
func (s WorkingSchedule) Validate() error {
	if s.Timezone == "" || s.Timezone == "Local" {
		return fmt.Errorf("an explicit IANA timezone is required")
	}
	if _, err := time.LoadLocation(s.Timezone); err != nil {
		return fmt.Errorf("unknown timezone")
	}
	if len(s.Windows) == 0 || len(s.Windows) > 64 {
		return fmt.Errorf("provide 1 to 64 weekly windows")
	}
	for _, w := range s.Windows {
		a, e1 := clockMinute(w.Start)
		b, e2 := clockMinute(w.End)
		if e1 != nil || e2 != nil || a == b {
			return fmt.Errorf("window requires distinct HH:MM start/end")
		}
		if len(w.Days) == 0 || len(w.Days) > 7 {
			return fmt.Errorf("window requires days from 0 (Sunday) to 6")
		}
		seen := map[int]bool{}
		for _, d := range w.Days {
			if d < 0 || d > 6 || seen[d] {
				return fmt.Errorf("invalid or repeated weekday")
			}
			seen[d] = true
		}
	}
	return nil
}

// EvaluateWorkingHours is pure. Minute membership in local time handles DST
// gaps and both folds without relying on time.Date's ambiguous-time choice.
// Overlapping/adjacent windows form one continuous working interval.
func EvaluateWorkingHours(s *WorkingSchedule, extension *HoursExtension, now time.Time) (WorkingHoursFacts, error) {
	f := WorkingHoursFacts{Version: 1, EvaluatedAt: now, Phase: "unconfigured", Guidance: "Set working hours explicitly; no schedule is active."}
	if s == nil {
		return f, nil
	}
	if err := s.Validate(); err != nil {
		return f, err
	}
	loc, _ := time.LoadLocation(s.Timezone)
	type window struct {
		days       map[int]bool
		start, end int
	}
	ws := []window{}
	for _, w := range s.Windows {
		a, _ := clockMinute(w.Start)
		b, _ := clockMinute(w.End)
		days := map[int]bool{}
		for _, d := range w.Days {
			days[d] = true
		}
		ws = append(ws, window{days, a, b})
	}
	member := func(t time.Time) bool {
		l := t.In(loc)
		d := int(l.Weekday())
		m := l.Hour()*60 + l.Minute()
		for _, w := range ws {
			if w.end > w.start {
				if w.days[d] && m >= w.start && m < w.end {
					return true
				}
			} else if (w.days[d] && m >= w.start) || (w.days[(d+6)%7] && m < w.end) {
				return true
			}
		}
		return false
	}
	// Search 8 days back for the current interval and 16 days ahead so a
	// window inside a DST gap can skip one weekly recurrence.
	start := now.UTC().Truncate(time.Minute).Add(-8 * 24 * time.Hour)
	end := now.UTC().Truncate(time.Minute).Add(16 * 24 * time.Hour)
	intervals := []HoursWindow{}
	var open *time.Time
	for t := start; !t.After(end); t = t.Add(time.Minute) {
		inside := member(t)
		if inside && open == nil {
			x := t
			open = &x
		}
		if !inside && open != nil {
			intervals = append(intervals, HoursWindow{open.In(loc), t.In(loc)})
			open = nil
		}
	}
	// Preserve coverage still open at the search horizon; the continuous
	// current-window case is detected below.
	if open != nil {
		intervals = append(intervals, HoursWindow{open.In(loc), end.In(loc)})
	}
	f.Configured = true
	f.Timezone = s.Timezone
	f.Phase = "outside"
	for i := range intervals {
		w := intervals[i]
		if !now.Before(w.Start) && now.Before(w.End) {
			f.Current = &w
		}
		if now.Before(w.Start) {
			f.Next = &w
			break
		}
	}
	if f.Current != nil {
		left := f.Current.End.Sub(now)
		f.Phase = "working"
		transition := f.Current.End.Add(-time.Hour)
		if left <= time.Hour {
			f.Phase = "wind-down"
			transition = f.Current.End.Add(-15 * time.Minute)
		}
		if left <= 15*time.Minute {
			f.Phase = "wrap-up"
			transition = f.Current.End
		}
		// Continuous weekly coverage has no end-of-day transition.
		if member(end) && f.Current.End.Equal(end.In(loc)) {
			f.Phase = "working"
			f.Continuous = true
			f.Current = nil
		} else {
			f.Transition = &transition
		}
	} else if f.Next != nil {
		t := f.Next.Start
		f.Transition = &t
	}
	if extension != nil && !now.Before(extension.Start) && now.Before(extension.Until) && extension.Until.After(extension.Start) && extension.Until.Sub(extension.Start) <= 4*time.Hour {
		f.Extension = extension
		f.Phase = "extended"
		t := extension.Until
		if f.Transition == nil || t.Before(*f.Transition) {
			f.Transition = &t
		}
	}
	switch f.Phase {
	case "working":
		f.Guidance = "Continue authorized work. Keep the next finishable priority clear."
	case "wind-down":
		f.Guidance = "Focus on finishable priorities; defer new scope. Identify what can continue independently."
	case "wrap-up":
		f.Guidance = "Save work, prepare authorized pushes or draft PRs, and record checks, decisions and the next-window plan."
	case "outside":
		f.Guidance = "Offer one clock-off nudge. Continue already-authorized independent work quietly; batch nonurgent questions until the next window. Offer a finite extension and respect explicit insistence."
	case "extended":
		f.Guidance = "Respect the explicit temporary extension without repeated debate. Recurring hours are unchanged; reassess when it expires."
	}
	data, _ := json.Marshal(s)
	next := "continuous"
	if f.Next != nil {
		next = f.Next.Start.UTC().Format(time.RFC3339)
	}
	f.reminderKey = fmt.Sprintf("%x", sha256.Sum256(append(data, []byte(next)...)))
	return f, nil
}

func (f WorkingHoursFacts) Summary() string {
	if f.UnavailableReason != "" {
		return "Working hours unavailable: " + f.UnavailableReason
	}
	if !f.Configured {
		return "Set working hours… (wtc hours setup --help)"
	}
	text := "Working hours: " + f.Phase + " · " + f.Timezone
	if f.Next != nil {
		text += " · next " + f.Next.Start.Format("Mon 15:04 MST")
	}
	if f.Extension != nil {
		text += " · until " + f.Extension.Until.In(hoursLocation(f.Timezone)).Format(time.RFC3339)
	}
	return text
}

func hoursLocation(zone string) *time.Location {
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return time.UTC
	}
	return loc
}
