package retry

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestPollStopsWhenDoneFailedOrExhausted(t *testing.T) {
	boom := errors.New("boom")
	for _, tc := range []struct {
		name  string
		p     Policy
		after int // the attempt that reports done (0: never)
		fail  int // the attempt that fails (0: never)
		tries int
		err   error
	}{
		{name: "done", p: Policy{Attempts: 5}, after: 3, tries: 3},
		{name: "failed", p: Policy{Attempts: 5}, fail: 2, tries: 2, err: boom},
		{name: "attempts", p: Policy{Attempts: 4}, tries: 4, err: ErrExhausted},
		{name: "within", p: Policy{Every: time.Millisecond, Within: 20 * time.Millisecond}, err: ErrExhausted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tries := 0
			err := Poll(context.Background(), tc.p, func(context.Context) (bool, error) {
				tries++
				if tries == tc.fail {
					return false, boom
				}
				return tries == tc.after, nil
			})
			if !errors.Is(err, tc.err) || tc.err == nil && err != nil {
				t.Fatalf("err %v, want %v", err, tc.err)
			}
			if tc.tries != 0 && tries != tc.tries {
				t.Fatalf("%d attempts, want %d", tries, tc.tries)
			}
		})
	}
}

func TestPollPausesBetweenAttemptsAndStopsWithItsContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	pauses := 0
	err := Poll(ctx, Policy{Pause: func(ctx context.Context) error {
		if pauses++; pauses == 2 {
			cancel()
		}
		return ctx.Err()
	}}, func(context.Context) (bool, error) { return false, nil })
	if !errors.Is(err, context.Canceled) || pauses != 2 {
		t.Fatalf("err %v after %d pauses", err, pauses)
	}
}

// attempts is a receiver that counts the side effects it applied.
type attempts struct {
	mu      sync.Mutex
	applied int
	replies []Outcome // one per attempt; the last one repeats
}

func (a *attempts) effect(context.Context) (Outcome, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	reply := a.replies[0]
	if len(a.replies) > 1 {
		a.replies = a.replies[1:]
	}
	if reply == Applied || reply == Unknown {
		a.applied++ // An unknown outcome is the worst case: it did apply.
	}
	if reply == Applied {
		return reply, nil
	}
	return reply, errors.New("no confirmation")
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func TestIntentNeverAttemptsAgainAfterAnUnknownOutcome(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "create-pending")
	receiver := &attempts{replies: []Outcome{Unknown, Applied}}
	in := Intent{Marker: marker}
	if err := in.Do(context.Background(), Policy{Attempts: 15}, receiver.effect); err == nil {
		t.Fatal("unknown outcome reported success")
	}
	if err := in.Do(context.Background(), Policy{Attempts: 15}, receiver.effect); !errors.Is(err, ErrUnresolved) {
		t.Fatalf("second run: %v, want ErrUnresolved", err)
	}
	if receiver.applied != 1 || !exists(marker) {
		t.Fatalf("applied %d times, marker kept %v", receiver.applied, exists(marker))
	}
}

func TestIntentRepeatsOnlyBusyAttempts(t *testing.T) {
	for _, tc := range []struct {
		name    string
		replies []Outcome
		applied int
		err     bool
	}{
		{"busy then applied", []Outcome{Busy, Busy, Applied}, 1, false},
		{"busy throughout", []Outcome{Busy}, 0, true},
		{"refused", []Outcome{Refused, Applied}, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			marker := filepath.Join(t.TempDir(), "pending")
			receiver := &attempts{replies: tc.replies}
			err := Intent{Marker: marker}.Do(context.Background(), Policy{Attempts: 4}, receiver.effect)
			if (err != nil) != tc.err || receiver.applied != tc.applied {
				t.Fatalf("err %v, applied %d", err, receiver.applied)
			}
			if exists(marker) {
				t.Fatal("a known outcome left its marker")
			}
		})
	}
}

func TestIntentKeepsItsMarkerUntilTheResultIsRecorded(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "pending")
	receiver := &attempts{replies: []Outcome{Applied}}
	in := Intent{Marker: marker, Record: func() error { return errors.New("disk full") }}
	if err := in.Do(context.Background(), Policy{Attempts: 1}, receiver.effect); err == nil || !exists(marker) {
		t.Fatalf("unrecorded result: err %v, marker kept %v", err, exists(marker))
	}
	if err := in.Do(context.Background(), Policy{Attempts: 1}, receiver.effect); !errors.Is(err, ErrUnresolved) || receiver.applied != 1 {
		t.Fatalf("second run: %v, applied %d", err, receiver.applied)
	}
}

// A run that checked for a result before another run recorded one asks again
// once it holds the marker.
func TestIntentChecksForAResultWhileHoldingTheMarker(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "pending")
	receiver := &attempts{replies: []Outcome{Applied}}
	recorded := false
	in := Intent{Marker: marker, Done: func() (bool, error) { return recorded, nil }, Record: func() error { recorded = true; return nil }}
	if err := in.Do(context.Background(), Policy{Attempts: 1}, receiver.effect); err != nil {
		t.Fatal(err)
	}
	if err := in.Do(context.Background(), Policy{Attempts: 1}, receiver.effect); !errors.Is(err, ErrDone) {
		t.Fatalf("second run: %v, want ErrDone", err)
	}
	if receiver.applied != 1 || exists(marker) {
		t.Fatalf("applied %d times, marker kept %v", receiver.applied, exists(marker))
	}
}

func TestIntentLetsOneOfConcurrentRunsAttempt(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "pending")
	release := make(chan struct{})
	receiver := &attempts{replies: []Outcome{Applied}}
	in := Intent{Marker: marker}
	results := make(chan error, 8)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			results <- in.Do(context.Background(), Policy{Attempts: 1}, func(ctx context.Context) (Outcome, error) {
				<-release
				return receiver.effect(ctx)
			})
		})
	}
	for range 7 {
		if err := <-results; !errors.Is(err, ErrUnresolved) {
			t.Fatalf("concurrent run: %v, want ErrUnresolved", err)
		}
	}
	close(release)
	wg.Wait()
	if err := <-results; err != nil || receiver.applied != 1 {
		t.Fatalf("holder: %v, applied %d", err, receiver.applied)
	}
}
