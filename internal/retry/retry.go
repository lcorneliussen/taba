// Package retry repeats operations, and only those that are safe to repeat.
//
// Poll repeats an Idempotent attempt. A side effect is an Effect, which Poll
// does not accept: it runs through an Intent, whose marker file stops a second
// attempt once an outcome is unknown or an applied result was not recorded,
// in this process or any later one.
package retry

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"time"
)

// Idempotent is one attempt that is safe to repeat: an attempt that does not
// report done changed nothing. A read is idempotent, and so is taking a lock
// that is either taken or refused.
type Idempotent func(ctx context.Context) (done bool, err error)

// Policy bounds the attempts. The first attempt starts at once. With neither
// Attempts nor Within set, attempts continue until ctx ends.
type Policy struct {
	Every    time.Duration // pause between attempts
	Attempts int           // stop after this many attempts
	Within   time.Duration // start no attempt once this long has passed
	// Pause, if set, replaces the timer between attempts, for callers that own
	// their clock.
	Pause func(context.Context) error
}

// ErrExhausted reports that the policy ended before an attempt was done.
var ErrExhausted = errors.New("retry: attempts exhausted")

// Poll repeats try until it is done or fails, the policy ends (ErrExhausted),
// or ctx ends.
func Poll(ctx context.Context, p Policy, try Idempotent) error {
	start := time.Now()
	for attempt := 1; ; attempt++ {
		done, err := try(ctx)
		if err != nil || done {
			return err
		}
		if p.Attempts > 0 && attempt >= p.Attempts || p.Within > 0 && time.Since(start) >= p.Within {
			return ErrExhausted
		}
		if err := p.pause(ctx); err != nil {
			return err
		}
	}
}

func (p Policy) pause(ctx context.Context) error {
	if p.Pause != nil {
		return p.Pause(ctx)
	}
	timer := time.NewTimer(p.Every)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return ctx.Err()
	}
}

// Outcome is what one attempt at a side effect is known to have done.
type Outcome int

const (
	// Unknown: the attempt may have taken effect. It is the zero value, so an
	// attempt that does not classify its failure is never repeated.
	Unknown Outcome = iota
	// Refused: the receiver proved that the attempt took no effect.
	Refused
	// Busy: the receiver took no effect and may accept a later attempt.
	Busy
	// Applied: the attempt took effect.
	Applied
)

// Effect is one attempt at a side effect.
type Effect func(ctx context.Context) (Outcome, error)

// Intent is a side effect that must take effect at most once.
type Intent struct {
	// Marker is a file that exists from before the first attempt until the
	// outcome is known. While it exists, no other Do of this intent attempts.
	Marker string
	// Done, if set, reports whether an earlier run already achieved the
	// intent. Do asks while it holds the marker, so a run that began before
	// another one finished does not repeat the effect.
	Done func() (bool, error)
	// Record, if set, persists what an applied effect produced. The marker is
	// removed only after it succeeds.
	Record func() error
}

var (
	// ErrUnresolved reports that the marker exists: another run is attempting
	// the effect, an earlier attempt's outcome is unknown, or an applied
	// effect's result was not recorded. A person removes the marker after
	// confirming what that attempt did.
	ErrUnresolved = errors.New("an earlier attempt's outcome is unresolved")
	// ErrDone reports that Done found the intent already achieved.
	ErrDone = errors.New("an earlier run already achieved this")
)

// Do attempts effect once, and again only after a Busy attempt while p allows.
// It holds the marker throughout. After Applied it records the result and
// removes the marker. After Refused, or Busy until p ends, it removes the
// marker and returns the attempt's error. After Unknown, a failed Record or a
// crash, the marker stays, and every later Do fails with ErrUnresolved.
func (in Intent) Do(ctx context.Context, p Policy, effect Effect) error {
	marker, err := os.OpenFile(in.Marker, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if errors.Is(err, os.ErrExist) {
		return fmt.Errorf("%w (%s)", ErrUnresolved, in.Marker)
	}
	if err != nil {
		return err
	}
	if err := marker.Close(); err != nil {
		return errors.Join(err, os.Remove(in.Marker))
	}
	if in.Done != nil {
		done, err := in.Done()
		if err == nil && done {
			err = ErrDone
		}
		if err != nil {
			return errors.Join(err, os.Remove(in.Marker))
		}
	}
	outcome, last := Unknown, error(nil)
	err = Poll(ctx, p, func(ctx context.Context) (bool, error) {
		outcome, last = effect(ctx)
		return outcome != Busy, nil
	})
	switch outcome {
	case Applied:
		if in.Record != nil {
			if err := in.Record(); err != nil {
				return errors.Join(last, err)
			}
		}
		return errors.Join(last, os.Remove(in.Marker))
	case Refused, Busy:
		if err != nil && !errors.Is(err, ErrExhausted) {
			last = errors.Join(last, err)
		}
		return errors.Join(cmp.Or(last, errors.New("attempt refused")), os.Remove(in.Marker))
	}
	return cmp.Or(last, errors.New("attempt outcome unknown"))
}
