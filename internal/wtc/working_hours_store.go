package wtc

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

type hoursProjectState struct {
	Extension *HoursExtension `json:"extension,omitempty"`
	Reminder  string          `json:"reminder,omitempty"`
}
type hoursRecord struct {
	Version  int                          `json:"version"`
	Personal *WorkingSchedule             `json:"personal,omitempty"`
	Projects map[string]WorkingSchedule   `json:"projects,omitempty"`
	State    map[string]hoursProjectState `json:"state,omitempty"`
}

// WorkingHoursStore is scoped to one OS user's private record and one project.
// It never selects a person from agent IDs, display names or repository text.
type WorkingHoursStore struct{ directory, project string }

func NewWorkingHoursStore(personRoot, projectRoot string) (WorkingHoursStore, error) {
	project, err := filepath.Abs(projectRoot)
	if err != nil {
		return WorkingHoursStore{}, fmt.Errorf("cannot identify project")
	}
	// Existing shared roots may themselves be symlinks.
	if resolved, err := filepath.EvalSymlinks(project); err == nil {
		project = resolved
	} else if !os.IsNotExist(err) {
		return WorkingHoursStore{}, fmt.Errorf("cannot identify project")
	}
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(project)))
	return WorkingHoursStore{filepath.Join(personRoot, "working-hours"), key}, nil
}

func DecodeWorkingSchedule(r io.Reader) (WorkingSchedule, error) {
	var s WorkingSchedule
	d := json.NewDecoder(io.LimitReader(r, 65537))
	d.DisallowUnknownFields()
	if err := d.Decode(&s); err != nil {
		return s, fmt.Errorf("invalid schedule JSON")
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return s, fmt.Errorf("schedule must contain one JSON object")
	}
	return s, s.Validate()
}

func privateHoursDir(path string, create bool) error {
	if create {
		if err := os.MkdirAll(path, 0700); err != nil {
			return fmt.Errorf("cannot create private working-hours directory")
		}
	}
	st, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !st.IsDir() || st.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("working-hours directory must be private (0700) and not a symlink")
	}
	return nil
}
func openHoursFile(path string, flags int) (*os.File, error) {
	fd, err := unix.Open(path, flags|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0600)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), "working-hours")
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 {
		f.Close()
		return nil, fmt.Errorf("working-hours files must be private regular files (0600)")
	}
	return f, nil
}
func (s WorkingHoursStore) read() (hoursRecord, error) {
	r := hoursRecord{Version: 1, Projects: map[string]WorkingSchedule{}, State: map[string]hoursProjectState{}}
	if err := privateHoursDir(s.directory, false); err != nil {
		if os.IsNotExist(err) {
			return r, nil
		}
		return r, fmt.Errorf("cannot read private working-hours directory")
	}
	f, err := openHoursFile(filepath.Join(s.directory, "profile.json"), unix.O_RDONLY)
	if os.IsNotExist(err) {
		return r, nil
	}
	if err != nil {
		return r, fmt.Errorf("cannot read private working-hours profile")
	}
	defer f.Close()
	r = hoursRecord{}
	d := json.NewDecoder(io.LimitReader(f, 1<<20))
	d.DisallowUnknownFields()
	if err := d.Decode(&r); err != nil || r.Version != 1 {
		return r, fmt.Errorf("invalid working-hours profile")
	}
	var trailing any
	if d.Decode(&trailing) != io.EOF {
		return r, fmt.Errorf("invalid working-hours profile")
	}
	if r.Personal != nil {
		if err := r.Personal.Validate(); err != nil {
			return r, err
		}
	}
	for _, p := range r.Projects {
		if err := p.Validate(); err != nil {
			return r, err
		}
	}
	if r.Projects == nil {
		r.Projects = map[string]WorkingSchedule{}
	}
	if r.State == nil {
		r.State = map[string]hoursProjectState{}
	}
	return r, nil
}
func (s WorkingHoursStore) change(fn func(*hoursRecord) error) error {
	if err := privateHoursDir(s.directory, true); err != nil {
		return err
	}
	lock, err := openHoursFile(filepath.Join(s.directory, "profile.lock"), unix.O_RDWR|unix.O_CREAT)
	if err != nil {
		return fmt.Errorf("cannot lock working-hours profile")
	}
	defer lock.Close()
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX); err != nil {
		return fmt.Errorf("cannot lock working-hours profile")
	}
	defer unix.Flock(int(lock.Fd()), unix.LOCK_UN)
	r, err := s.read()
	if err != nil {
		return err
	}
	if err := fn(&r); err != nil {
		return err
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	if len(b) >= 1<<20 {
		return fmt.Errorf("working-hours profile exceeds size limit")
	}
	tmp, err := os.CreateTemp(s.directory, ".profile-*")
	if err != nil {
		return fmt.Errorf("cannot save working-hours profile")
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.Write(append(b, '\n')); err == nil {
		err = tmp.Sync()
	}
	closeErr := tmp.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), filepath.Join(s.directory, "profile.json"))
	}
	if err != nil {
		return fmt.Errorf("cannot save working-hours profile")
	}
	return nil
}
func (s WorkingHoursStore) schedule(r hoursRecord) (*WorkingSchedule, string) {
	if p, ok := r.Projects[s.project]; ok {
		return &p, "project"
	}
	if r.Personal != nil {
		return r.Personal, "personal"
	}
	return nil, ""
}
func (s WorkingHoursStore) View() (*WorkingSchedule, string, error) {
	r, err := s.read()
	if err != nil {
		return nil, "", err
	}
	p, source := s.schedule(r)
	return p, source, nil
}
func (s WorkingHoursStore) Setup(p WorkingSchedule, project bool) error {
	if err := p.Validate(); err != nil {
		return err
	}
	return s.change(func(r *hoursRecord) error {
		if project {
			r.Projects[s.project] = p
			delete(r.State, s.project)
		} else {
			r.Personal = &p
			for key := range r.State {
				if _, overridden := r.Projects[key]; !overridden {
					delete(r.State, key)
				}
			}
		}
		return nil
	})
}
func (s WorkingHoursStore) Clear(project bool) error {
	return s.change(func(r *hoursRecord) error {
		if project {
			delete(r.Projects, s.project)
			delete(r.State, s.project)
		} else {
			r.Personal = nil
			for key := range r.State {
				if _, overridden := r.Projects[key]; !overridden {
					delete(r.State, key)
				}
			}
		}
		return nil
	})
}
func (s WorkingHoursStore) Facts(now time.Time) (WorkingHoursFacts, error) {
	r, err := s.read()
	if err != nil {
		return WorkingHoursFacts{}, err
	}
	p, source := s.schedule(r)
	f, err := EvaluateWorkingHours(p, r.State[s.project].Extension, now)
	f.Source = source
	return f, err
}
func (s WorkingHoursStore) Extend(now time.Time, duration time.Duration) error {
	if duration <= 0 || duration > 4*time.Hour {
		return fmt.Errorf("extension must be greater than zero and at most 4h")
	}
	return s.change(func(r *hoursRecord) error {
		if p, _ := s.schedule(*r); p == nil {
			return fmt.Errorf("set working hours before extending")
		}
		state := r.State[s.project]
		state.Extension = &HoursExtension{now, now.Add(duration)}
		r.State[s.project] = state
		return nil
	})
}
func (s WorkingHoursStore) EndExtension() error {
	return s.change(func(r *hoursRecord) error {
		state := r.State[s.project]
		state.Extension = nil
		r.State[s.project] = state
		return nil
	})
}

// ClaimReminder atomically reserves the one outside-window nudge shared by
// collections. Call only when prepared to deliver it; facts/status never claim.
func (s WorkingHoursStore) ClaimReminder(now time.Time) (WorkingHoursFacts, error) {
	var f WorkingHoursFacts
	err := s.change(func(r *hoursRecord) error {
		p, source := s.schedule(*r)
		state := r.State[s.project]
		var err error
		f, err = EvaluateWorkingHours(p, state.Extension, now)
		if err != nil {
			return err
		}
		f.Source = source
		if f.Phase == "outside" && state.Reminder != f.reminderKey {
			f.ReminderClaimed = true
			state.Reminder = f.reminderKey
			r.State[s.project] = state
		}
		return nil
	})
	return f, err
}
