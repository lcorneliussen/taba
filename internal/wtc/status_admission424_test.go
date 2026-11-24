package wtc

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"
)

// Bounded ordinary-read admission behind the Bitbucket confirmation FIFO.
// Every test drives the real admission function under its real lock and
// shared files; only the decision clock and the refill wait are fake, so
// minutes of contention run deterministically in one goroutine. No provider
// is contacted: fetches are Go funcs and the fixture's bb/gh only count.

type admission424Clock struct{ now time.Time }

func admission424FakeClock(t *testing.T) *admission424Clock {
	t.Helper()
	clock := &admission424Clock{now: time.Now()}
	now, wait := statusAdmissionNow, statusAdmissionWait
	statusAdmissionNow = func() time.Time { return clock.now }
	statusAdmissionWait = func(ctx context.Context, delay time.Duration) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		clock.now = clock.now.Add(delay)
		return ctx.Err()
	}
	t.Cleanup(func() { statusAdmissionNow, statusAdmissionWait = now, wait })
	return clock
}

// admission424Queue seeds n background Bitbucket confirmations whose
// consumers never poll again (a dormant FIFO head).
func admission424Queue(t *testing.T, forge string, n int, now time.Time) []statusPendingConfirmation {
	t.Helper()
	queue := make([]statusPendingConfirmation, 0, n)
	for i := 0; i < n; i++ {
		queue = append(queue, statusPendingConfirmation{Key: statusConfirmationKey(fmt.Sprintf("dormant-%d", i)), Seen: now})
	}
	if n > 0 && !statusSavePendingConfirmations(forge, queue) {
		t.Fatal("seed queue")
	}
	return queue
}

func admission424Tokens(t *testing.T, forge string) string {
	t.Helper()
	raw, _ := os.ReadFile(statusForgeStatePath("budget", forge))
	return string(raw)
}

func admission424Marked(forge string, now time.Time) bool {
	return len(statusReadOrdinaryWaiters(forge, now).Queue) > 0
}

// Multi-cycle cold-cache reproduction: every cycle the shared bucket has
// refilled to its burst (nobody consumed it), and one ordinary read asks for
// admission. Queues of burst or more must not refuse every cycle.
func TestStatusAdmission424OrdinaryProgressBehindDormantQueue(t *testing.T) {
	const forge, cycles = "bitbucket.org", 20
	for _, size := range []int{0, 7, 8, 9} {
		t.Run(fmt.Sprintf("queue%d", size), func(t *testing.T) {
			foreground424Fixture(t, "exit 0\n")
			burst, _ := statusForgeAllowance(forge)
			now := time.Now()
			want := admission424Queue(t, forge, size, now)
			admitted := 0
			for cycle := 0; cycle < cycles; cycle++ {
				foreground424Budget(t, forge, burst, time.Now())
				if statusTakeForgeTokenForContext(context.Background(), forge, "") {
					admitted++
				}
			}
			t.Logf("queue %d, burst %d: ordinary admitted %d of %d cycles", size, burst, admitted, cycles)
			if admitted != cycles {
				t.Fatalf("queue %d: ordinary admitted %d of %d full-bucket cycles", size, admitted, cycles)
			}
			got := statusReadPendingConfirmations(forge, time.Now())
			if len(got) != len(want) {
				t.Fatalf("queue length %d, want %d", len(got), len(want))
			}
			for i := range want {
				if got[i].Key != want[i].Key {
					t.Fatalf("FIFO order changed at %d", i)
				}
			}
			// A full bucket still leaves burst-1 tokens for the queue.
			if size > 0 && admission424Tokens(t, forge)[0] != byte('0'+burst-1) {
				t.Fatalf("budget after ordinary read = %q", admission424Tokens(t, forge))
			}
		})
	}
}

// admission424Actor is one client of the shared budget. The scheduler always
// runs the earliest-due actor next; an actor due while another waits inside
// the budget lock runs right after it, as a blocked lock waiter would.
type admission424Actor struct {
	next time.Time
	run  func() (again time.Duration, done bool)
}

func admission424Run(clock *admission424Clock, until time.Time, actors *[]*admission424Actor) {
	for {
		at := -1
		for i, actor := range *actors {
			if at < 0 || actor.next.Before((*actors)[at].next) {
				at = i
			}
		}
		if at < 0 || !(*actors)[at].next.Before(until) {
			return
		}
		actor := (*actors)[at]
		if clock.now.Before(actor.next) {
			clock.now = actor.next
		}
		again, done := actor.run()
		if done {
			for i := range *actors {
				if (*actors)[i] == actor {
					*actors = append((*actors)[:i], (*actors)[i+1:]...)
					break
				}
			}
			continue
		}
		actor.next = clock.now.Add(again)
	}
}

// Sustained eligible contention for 30 fake minutes: a dormant head for its
// five-minute lifetime, then active confirmation consumers arriving faster
// than the refill (a growing queue, each polling every 5 s), while list,
// check and build readers poll every 30 s. Finite-load assumption: arrivals
// stay below the 256-entry queue bound and every waiter keeps polling.
func TestStatusAdmission424SustainedContentionStaysFairAndBounded(t *testing.T) {
	const forge = "bitbucket.org"
	const duration, arrive, confirmPoll, ordinaryPoll = 30 * time.Minute, 6 * time.Second, 5 * time.Second, 30 * time.Second
	foreground424Fixture(t, "exit 0\n")
	clock := admission424FakeClock(t)
	burst, every := statusForgeAllowance(forge)
	age := statusOrdinaryAge(forge)
	start := clock.now
	foreground424Budget(t, forge, burst, start)
	admission424Queue(t, forge, 1, start) // dormant head
	ctx := context.Background()

	var actors []*admission424Actor
	var arrivals, confirmed []string
	maxQueue, ordinary, total := 0, 0, 0
	var waitingSince time.Time
	maxWait, maxKindGap := time.Duration(0), time.Duration(0)
	kinds := []string{"list", "check", "build"}
	last := map[string]time.Time{}
	for i, kind := range kinds {
		last[kind] = start
		read := statusOrdinaryRead(ctx, kind, forge, "example/widget", "main")
		actors = append(actors, &admission424Actor{next: start.Add(time.Duration(i) * 10 * time.Second), run: func() (time.Duration, bool) {
			ok, retry := statusTakeForgeTokenRetry(read, forge, "")
			if !ok {
				if waitingSince.IsZero() {
					waitingSince = clock.now
				}
				if retry.IsZero() || retry.Before(clock.now) {
					t.Fatalf("%s refused at %s without a future retry (%s)", kind, clock.now.Sub(start), retry)
				}
				return ordinaryPoll, false
			}
			total, ordinary = total+1, ordinary+1
			if !waitingSince.IsZero() {
				maxWait = max(maxWait, clock.now.Sub(waitingSince))
				waitingSince = time.Time{}
			}
			maxKindGap = max(maxKindGap, clock.now.Sub(last[kind]))
			last[kind] = clock.now
			return ordinaryPoll, false
		}})
	}
	seq := 0
	actors = append(actors, &admission424Actor{next: start, run: func() (time.Duration, bool) {
		key := statusConfirmationKey(fmt.Sprintf("active-%d", seq))
		seq++
		arrivals = append(arrivals, key)
		actors = append(actors, &admission424Actor{next: clock.now, run: func() (time.Duration, bool) {
			if ok, _ := statusTakeForgeTokenRetry(ctx, forge, key); ok {
				total++
				confirmed = append(confirmed, key)
				return 0, true
			}
			maxQueue = max(maxQueue, len(arrivals)-len(confirmed))
			return confirmPoll, false
		}})
		return arrive, false
	}})
	admission424Run(clock, start.Add(duration), &actors)
	elapsed := clock.now.Sub(start)
	for _, kind := range kinds {
		maxKindGap = max(maxKindGap, clock.now.Sub(last[kind]))
	}
	t.Logf("%s: %d arrivals, %d confirmed, %d ordinary, peak queue %d; longest ordinary wait %s, longest per-kind gap %s",
		elapsed.Round(time.Second), len(arrivals), len(confirmed), ordinary, maxQueue, maxWait, maxKindGap)

	if maxQueue <= burst {
		t.Fatalf("fixture never grew the queue past the burst (peak %d)", maxQueue)
	}
	// Total budget: never more than the burst plus one token per refill.
	if ceiling := burst + int(elapsed/every); total > ceiling {
		t.Fatalf("admitted %d reads, ceiling %d", total, ceiling)
	}
	// Finite ordinary progress: aging window + one refill + one poll interval
	// (+ one refill the head may wait inside the lock).
	if bound := age + 2*every + ordinaryPoll; maxWait > bound {
		t.Fatalf("ordinary reads waited %s, bound %s", maxWait, bound)
	}
	// Per reader: phase-locked readers rotate through the waiter FIFO, so
	// each waits at most its position + 1 windows (three readers here).
	if bound := 3 * (age + 2*every + ordinaryPoll); maxKindGap > bound {
		t.Fatalf("an ordinary reader went %s without admission, bound %s", maxKindGap, bound)
	}
	// Confirmation progress: FIFO, and every token not taken by the bounded
	// ordinary path (or spent while the dormant head blocked the FIFO).
	for i := range confirmed {
		if confirmed[i] != arrivals[i] {
			t.Fatalf("confirmation %d admitted out of FIFO order", i)
		}
	}
	dormant := int(statusPendingLifetime / every)
	if floor := int(elapsed/every) - ordinary - dormant - burst; len(confirmed) < floor {
		t.Fatalf("confirmed %d, floor %d", len(confirmed), floor)
	}
	if share := int((elapsed-statusPendingLifetime)/age) + 1 + dormant + burst; ordinary > share {
		t.Fatalf("ordinary reads took %d tokens, above their bounded share %d", ordinary, share)
	}
}

// Sparse confirmations, about one per refill: each one finds the FIFO empty,
// becomes head at once and waits in the lock for the next refill. Ordinary
// readers that found the bucket empty must still get a bounded turn.
func TestStatusAdmission424SparseConfirmationsDoNotWinEveryRefill(t *testing.T) {
	const forge = "bitbucket.org"
	const duration, ordinaryPoll = 20 * time.Minute, 30 * time.Second
	foreground424Fixture(t, "exit 0\n")
	clock := admission424FakeClock(t)
	_, every := statusForgeAllowance(forge)
	age := statusOrdinaryAge(forge)
	start := clock.now
	foreground424Budget(t, forge, 0, start)
	ctx := context.Background()
	var actors []*admission424Actor
	var waitingSince time.Time
	maxWait, ordinary, confirmed, seq := time.Duration(0), 0, 0, 0
	actors = append(actors, &admission424Actor{next: start.Add(3 * time.Second), run: func() (time.Duration, bool) {
		ok, _ := statusTakeForgeTokenRetry(ctx, forge, "")
		if !ok {
			if waitingSince.IsZero() {
				waitingSince = clock.now
			}
			return ordinaryPoll, false
		}
		ordinary++
		if !waitingSince.IsZero() {
			maxWait = max(maxWait, clock.now.Sub(waitingSince))
			waitingSince = time.Time{}
		}
		return ordinaryPoll, false
	}})
	actors = append(actors, &admission424Actor{next: start, run: func() (time.Duration, bool) {
		key := statusConfirmationKey(fmt.Sprintf("sparse-%d", seq))
		seq++
		actors = append(actors, &admission424Actor{next: clock.now, run: func() (time.Duration, bool) {
			if ok, _ := statusTakeForgeTokenRetry(ctx, forge, key); ok {
				confirmed++
				return 0, true
			}
			return 5 * time.Second, false
		}})
		return every, false
	}})
	admission424Run(clock, start.Add(duration), &actors)
	if !waitingSince.IsZero() {
		maxWait = max(maxWait, clock.now.Sub(waitingSince))
	}
	t.Logf("%s: %d confirmed, %d ordinary; longest ordinary wait %s", clock.now.Sub(start).Round(time.Second), confirmed, ordinary, maxWait)
	if bound := age + 2*every + ordinaryPoll; ordinary == 0 || maxWait > bound {
		t.Fatalf("ordinary reads waited %s (%d admitted), bound %s", maxWait, ordinary, bound)
	}
	if floor := int(duration/every) - ordinary - 2; confirmed < floor {
		t.Fatalf("confirmed %d, floor %d", confirmed, floor)
	}
}

// Cold cache, several PRs, FIFO kept saturated by active consumers whose
// head polls every second: each PR detail is eventually read live, and until
// then reports the real refusal with a retry and unknown checks. The cache
// judges age by the wall clock, which barely moves here, so entries never
// expire and each PR is read exactly once; ExpiringPRsEachKeepProgressing
// models expiry.
func TestStatusAdmission424ColdCachePRsProgressBehindActiveHead(t *testing.T) {
	const forge, slug, poll = "bitbucket.org", "example/widget", 30 * time.Second
	foreground424Fixture(t, "exit 0\n")
	clock := admission424FakeClock(t)
	_, every := statusForgeAllowance(forge)
	age := statusOrdinaryAge(forge)
	start := clock.now
	foreground424Budget(t, forge, 0, start)
	ctx := context.Background()
	var actors []*admission424Actor
	confirmations, seq := 0, 0
	var consumer func() *admission424Actor
	consumer = func() *admission424Actor {
		key := statusConfirmationKey(fmt.Sprintf("head-%d", seq))
		seq++
		// Join the FIFO tail without asking for a token.
		statusSavePendingConfirmations(forge, statusQueueConfirmation(statusReadPendingConfirmations(forge, clock.now), key, clock.now))
		return &admission424Actor{next: clock.now.Add(time.Second), run: func() (time.Duration, bool) {
			if ok, _ := statusTakeForgeTokenRetry(ctx, forge, key); ok {
				confirmations++
				actors = append(actors, consumer()) // keep the FIFO saturated
				return 0, true
			}
			return time.Second, false
		}}
	}
	for i := 0; i < 12; i++ {
		actors = append(actors, consumer())
	}
	prs := []string{"11", "12", "13", "14", "15"}
	fetched := map[string]int{}
	done := map[string]time.Duration{}
	actors = append(actors, &admission424Actor{next: start, run: func() (time.Duration, bool) {
		for _, number := range prs {
			if _, ok := done[number]; ok {
				continue
			}
			fetch := func() ([]byte, error) {
				fetched[number]++
				return []byte(`{"id":` + number + `,"state":"OPEN"}`), nil
			}
			_, live, refused, err := statusFetchSharedAdmissionContext(ctx, "pr", forge, slug, number, fetch)
			if live {
				done[number] = clock.now.Sub(start)
				continue
			}
			if !errors.Is(err, errForgeBudget) || refused.reason != "shared-budget" || refused.retry.IsZero() {
				t.Fatalf("PR %s refused as err=%v %+v", number, err, refused)
			}
			row := refused.apply(statusDeferredDetail(PRRecord{Number: number}))
			observation := statusDetailObservation(row)
			if row.Checks != "UNKNOWN" || observation.Reason != "shared-budget" || observation.RetryAt == "" {
				t.Fatalf("never-observed PR %s lost its refusal: %+v %+v", number, row, observation)
			}
		}
		return poll, len(done) == len(prs)
	}})
	bound := time.Duration(len(prs)) * (age + 2*every + poll)
	admission424Run(clock, start.Add(bound), &actors)
	t.Logf("PR progress %v after %d confirmations", done, confirmations)
	if len(done) != len(prs) {
		t.Fatalf("%d of %d PRs read within %s", len(done), len(prs), bound)
	}
	for _, number := range prs {
		if fetched[number] != 1 {
			t.Fatalf("PR %s fetched %d times", number, fetched[number])
		}
		// The shared cache serves the observation to every other view.
		if _, live, _, err := statusFetchSharedAdmissionContext(ctx, "pr", forge, slug, number, func() ([]byte, error) {
			t.Fatal("cached PR refetched")
			return nil, nil
		}); err != nil || live {
			t.Fatalf("cached PR %s: live=%v err=%v", number, live, err)
		}
	}
	// Confirmations kept nearly the whole refill rate.
	if elapsed := clock.now.Sub(start); confirmations < int(elapsed/every)-len(prs)-1 {
		t.Fatalf("confirmations stalled: %d in %s", confirmations, elapsed)
	}
}

// The same saturated FIFO with cache expiry modelled: the cache judges age by
// the wall clock, so once 90 fake seconds pass after a live read its entry is
// re-dated through the real writer to be expired. A collector that walks its
// PRs in a fixed order must not hand every turn to its first PR: each PR is
// re-read within its place in the waiter FIFO.
func TestStatusAdmission424ExpiringPRsEachKeepProgressing(t *testing.T) {
	const forge, slug, poll, duration = "bitbucket.org", "example/widget", 30 * time.Second, 30 * time.Minute
	foreground424Fixture(t, "exit 0\n")
	clock := admission424FakeClock(t)
	_, every := statusForgeAllowance(forge)
	age := statusOrdinaryAge(forge)
	start := clock.now
	foreground424Budget(t, forge, 0, start)
	ctx := context.Background()
	var actors []*admission424Actor
	seq := 0
	var consumer func() *admission424Actor
	consumer = func() *admission424Actor {
		key := statusConfirmationKey(fmt.Sprintf("head-%d", seq))
		seq++
		statusSavePendingConfirmations(forge, statusQueueConfirmation(statusReadPendingConfirmations(forge, clock.now), key, clock.now))
		return &admission424Actor{next: clock.now.Add(time.Second), run: func() (time.Duration, bool) {
			if ok, _ := statusTakeForgeTokenRetry(ctx, forge, key); ok {
				actors = append(actors, consumer())
				return 0, true
			}
			return time.Second, false
		}}
	}
	for i := 0; i < 12; i++ {
		actors = append(actors, consumer())
	}
	prs := []string{"11", "12", "13", "14", "15"}
	reads := map[string]int{}
	last := map[string]time.Time{}
	maxGap := time.Duration(0)
	for _, number := range prs {
		last[number] = start
	}
	actors = append(actors, &admission424Actor{next: start, run: func() (time.Duration, bool) {
		for _, number := range prs {
			raw := []byte(`{"id":` + number + `,"state":"OPEN"}`)
			if reads[number] > 0 && !clock.now.Before(last[number].Add(statusForgeCacheAge())) {
				statusWriteForgeEntryObserved("pr", forge, slug, number, raw, time.Now().Add(-statusForgeCacheAge()-time.Second))
			}
			if _, live, _, _ := statusFetchSharedAdmissionContext(ctx, "pr", forge, slug, number, func() ([]byte, error) { return raw, nil }); live {
				reads[number]++
				maxGap = max(maxGap, clock.now.Sub(last[number]))
				last[number] = clock.now
			}
		}
		return poll, false
	}})
	admission424Run(clock, start.Add(duration), &actors)
	for _, number := range prs {
		maxGap = max(maxGap, clock.now.Sub(last[number]))
	}
	t.Logf("reads per PR %v; longest gap %s", reads, maxGap)
	for _, number := range prs {
		if reads[number] < 3 {
			t.Fatalf("PR %s read %d times in %s", number, reads[number], duration)
		}
	}
	// Five waiters: each waits at most five windows after its entry expires.
	if bound := statusForgeCacheAge() + time.Duration(len(prs))*(age+2*every+poll); maxGap > bound {
		t.Fatalf("a PR went %s without a live read, bound %s", maxGap, bound)
	}
}

// The finite-cache competition with everything at once for 30 fake minutes:
// a dormant confirmation head, then confirmations arriving faster than the
// refill; three keyed list/check/build readers; a collector walking five PRs
// whose entries expire (re-dated through the real writer, as above); and one
// ordinary waiter that never returns, so the head must rotate past it.
// Finite-load assumptions: every live waiter retries within 30 s, under the
// five-minute grace; both queues stay below their 256-entry bound.
func TestStatusAdmission424FiniteCacheCompetitionWithExpiry(t *testing.T) {
	const forge, slug = "bitbucket.org", "example/widget"
	const duration, arrive, confirmPoll, poll = 30 * time.Minute, 6 * time.Second, 5 * time.Second, 30 * time.Second
	foreground424Fixture(t, "exit 0\n")
	clock := admission424FakeClock(t)
	burst, every := statusForgeAllowance(forge)
	age := statusOrdinaryAge(forge)
	start := clock.now
	foreground424Budget(t, forge, burst, start)
	admission424Queue(t, forge, 1, start) // dormant confirmation head
	dormant := statusConfirmationKey("ordinary-reader-that-never-returns")
	statusSaveOrdinaryWaiters(forge, statusOrdinaryWaiters{Queue: []statusOrdinaryWaiter{{Key: dormant, Since: start, Seen: start}}})
	ctx := context.Background()

	var actors []*admission424Actor
	var arrivals, confirmed []string
	total, maxQueue := 0, 0
	seq := 0
	actors = append(actors, &admission424Actor{next: start, run: func() (time.Duration, bool) {
		key := statusConfirmationKey(fmt.Sprintf("active-%d", seq))
		seq++
		arrivals = append(arrivals, key)
		actors = append(actors, &admission424Actor{next: clock.now, run: func() (time.Duration, bool) {
			if ok, _ := statusTakeForgeTokenRetry(ctx, forge, key); ok {
				total++
				confirmed = append(confirmed, key)
				return 0, true
			}
			maxQueue = max(maxQueue, len(arrivals)-len(confirmed))
			return confirmPoll, false
		}})
		return arrive, false
	}})
	last := map[string]time.Time{}
	gap := map[string]time.Duration{}
	reads := map[string]int{}
	admit := func(name string) {
		total++
		reads[name]++
		gap[name] = max(gap[name], clock.now.Sub(last[name]))
		last[name] = clock.now
	}
	for i, kind := range []string{"list", "check", "build"} {
		read := statusOrdinaryRead(ctx, kind, forge, slug, "main")
		last[kind] = start
		actors = append(actors, &admission424Actor{next: start.Add(time.Duration(i) * 10 * time.Second), run: func() (time.Duration, bool) {
			if ok, _ := statusTakeForgeTokenRetry(read, forge, ""); ok {
				admit(kind)
			}
			return poll, false
		}})
	}
	prs := []string{"11", "12", "13", "14", "15"}
	for _, number := range prs {
		last["#"+number] = start
	}
	actors = append(actors, &admission424Actor{next: start.Add(5 * time.Second), run: func() (time.Duration, bool) {
		for _, number := range prs {
			raw := []byte(`{"id":` + number + `,"state":"OPEN"}`)
			if reads["#"+number] > 0 && !clock.now.Before(last["#"+number].Add(statusForgeCacheAge())) {
				statusWriteForgeEntryObserved("pr", forge, slug, number, raw, time.Now().Add(-statusForgeCacheAge()-time.Second))
			}
			if _, live, _, _ := statusFetchSharedAdmissionContext(ctx, "pr", forge, slug, number, func() ([]byte, error) { return raw, nil }); live {
				admit("#" + number)
			}
		}
		return poll, false
	}})
	// Observe every recorded aged turn and whether the dormant waiter was
	// passed over, from the shared state after each step.
	var turns []time.Time
	rotated := false
	actors = append(actors, &admission424Actor{next: start, run: func() (time.Duration, bool) {
		waiters := statusReadOrdinaryWaiters(forge, clock.now)
		if !waiters.Last.IsZero() && (len(turns) == 0 || !waiters.Last.Equal(turns[len(turns)-1])) {
			turns = append(turns, waiters.Last)
		}
		if waiters.position(dormant) != 0 && len(turns) > 0 {
			rotated = true
		}
		return time.Second, false
	}})
	admission424Run(clock, start.Add(duration), &actors)
	elapsed := clock.now.Sub(start)
	for name := range last {
		gap[name] = max(gap[name], clock.now.Sub(last[name]))
	}
	t.Logf("%s: %d arrivals, %d confirmed, peak queue %d, %d aged turns; reads %v; gaps %v",
		elapsed.Round(time.Second), len(arrivals), len(confirmed), maxQueue, len(turns), reads, gap)

	if maxQueue <= burst {
		t.Fatalf("fixture never grew the queue past the burst (peak %d)", maxQueue)
	}
	if ceiling := burst + int(elapsed/every); total > ceiling {
		t.Fatalf("admitted %d reads, ceiling %d", total, ceiling)
	}
	for i := 1; i < len(turns); i++ {
		if turns[i].Sub(turns[i-1]) < age {
			t.Fatalf("aged turns %s apart, window %s", turns[i].Sub(turns[i-1]), age)
		}
	}
	for i := range confirmed {
		if confirmed[i] != arrivals[i] {
			t.Fatalf("confirmation %d admitted out of FIFO order", i)
		}
	}
	if !rotated {
		t.Fatal("the dormant waiter was never passed over")
	}
	if end := statusReadOrdinaryWaiters(forge, clock.now); end.position(dormant) >= 0 {
		t.Fatal("the dormant waiter outlived its lifetime")
	}
	// Every identified waiter: one window per waiter (eight live ones) plus
	// one dormant grace, plus cache age for an expiring PR. Under saturation
	// each of N keyed waiters gets one turn per N windows (here 8 x 96 s).
	window := age + 2*every + poll
	for name, longest := range gap {
		bound := 8*window + statusPendingLifetime
		if name[0] == '#' {
			bound += statusForgeCacheAge()
		}
		if reads[name] < 2 || longest > bound {
			t.Fatalf("%s: %d reads, longest gap %s, bound %s", name, reads[name], longest, bound)
		}
	}
}

func TestStatusAdmission424HoldCooldownForegroundAndCancellation(t *testing.T) {
	const forge = "bitbucket.org"
	// An anonymous ordinary waiter whose turn has just opened.
	aged := func(t *testing.T, clock *admission424Clock) {
		t.Helper()
		since := clock.now.Add(-statusOrdinaryAge(forge))
		statusSaveOrdinaryWaiters(forge, statusOrdinaryWaiters{Queue: []statusOrdinaryWaiter{{Key: statusOrdinaryIdentity(context.Background()), Since: since, Seen: since}}})
		if !admission424Marked(forge, clock.now) {
			t.Fatal("seed waiter")
		}
	}
	t.Run("aged waiter holds the last token, then clears", func(t *testing.T) {
		foreground424Fixture(t, "exit 0\n")
		clock := admission424FakeClock(t)
		queue := admission424Queue(t, forge, 9, clock.now)
		aged(t, clock)
		foreground424Budget(t, forge, 1, clock.now)
		// The head neither takes the held token nor keeps the lock waiting
		// for another; it learns the next refill instead.
		statusAdmissionWait = func(context.Context, time.Duration) error {
			t.Fatal("head waited in the lock while a token was held")
			return nil
		}
		ok, retry := statusTakeForgeTokenRetry(context.Background(), forge, queue[0].Key)
		if ok || !retry.After(clock.now) || admission424Tokens(t, forge)[0] != '1' {
			t.Fatalf("head with a held token: ok=%v retry=%s budget %q", ok, retry.Sub(clock.now), admission424Tokens(t, forge))
		}
		if ok, _ := statusTakeForgeTokenRetry(context.Background(), forge, ""); !ok {
			t.Fatal("aged ordinary waiter not admitted")
		}
		if waiters := statusReadOrdinaryWaiters(forge, clock.now); len(waiters.Queue) != 0 || !waiters.Last.Equal(clock.now) {
			t.Fatalf("admission left the waiter or lost its turn: %+v", waiters)
		}
		foreground424Budget(t, forge, 1, clock.now)
		if ok, _ := statusTakeForgeTokenRetry(context.Background(), forge, queue[0].Key); !ok {
			t.Fatal("head blocked after the waiter was served")
		}
	})
	t.Run("a dormant head moves to the tail after its grace", func(t *testing.T) {
		foreground424Fixture(t, "exit 0\n")
		clock := admission424FakeClock(t)
		admission424Queue(t, forge, 9, clock.now)
		long := clock.now.Add(-statusOrdinaryAge(forge) - statusPendingLifetime)
		dormant := statusConfirmationKey("dormant-reader")
		statusSaveOrdinaryWaiters(forge, statusOrdinaryWaiters{Queue: []statusOrdinaryWaiter{
			{Key: dormant, Since: long, Seen: long},
			{Key: statusOrdinaryIdentity(context.Background()), Since: long, Seen: clock.now.Add(-time.Second)},
		}})
		foreground424Budget(t, forge, 1, clock.now)
		if ok, _ := statusTakeForgeTokenRetry(context.Background(), forge, ""); !ok {
			t.Fatal("a dormant head kept the turn past its grace")
		}
		if waiters := statusReadOrdinaryWaiters(forge, clock.now); len(waiters.Queue) != 1 || waiters.Queue[0].Key != dormant {
			t.Fatalf("dormant waiter lost or kept at head: %+v", waiters)
		}
	})
	t.Run("a head that asked after its turn and went quiet still moves", func(t *testing.T) {
		foreground424Fixture(t, "exit 0\n")
		clock := admission424FakeClock(t)
		admission424Queue(t, forge, 9, clock.now)
		age := statusOrdinaryAge(forge)
		// Refused once its turn was open, then quiet: one second short of
		// its grace now.
		asked := clock.now.Add(-statusPendingLifetime + time.Second)
		since := asked.Add(-age - time.Minute)
		quiet := statusConfirmationKey("asked-then-quiet")
		statusSaveOrdinaryWaiters(forge, statusOrdinaryWaiters{Queue: []statusOrdinaryWaiter{
			{Key: quiet, Since: since, Seen: asked},
			{Key: statusOrdinaryIdentity(context.Background()), Since: since, Seen: clock.now.Add(-time.Second)},
		}})
		foreground424Budget(t, forge, 1, clock.now)
		if ok, _ := statusTakeForgeTokenRetry(context.Background(), forge, ""); ok {
			t.Fatal("head moved before its grace")
		}
		clock.now = clock.now.Add(time.Second)
		foreground424Budget(t, forge, 1, clock.now)
		if ok, _ := statusTakeForgeTokenRetry(context.Background(), forge, ""); !ok {
			t.Fatal("a head quiet for its grace after asking kept the turn")
		}
	})
	t.Run("rotating every waiter still gives a real retry", func(t *testing.T) {
		foreground424Fixture(t, "exit 0\n")
		clock := admission424FakeClock(t)
		admission424Queue(t, forge, 9, clock.now)
		long := clock.now.Add(-statusOrdinaryAge(forge) - statusPendingLifetime)
		statusSaveOrdinaryWaiters(forge, statusOrdinaryWaiters{Queue: []statusOrdinaryWaiter{
			{Key: statusConfirmationKey("quiet-1"), Since: long, Seen: long},
			{Key: statusConfirmationKey("quiet-2"), Since: long, Seen: long},
		}})
		foreground424Budget(t, forge, 1, clock.now)
		ok, retry := statusTakeForgeTokenRetry(context.Background(), forge, "")
		if want := clock.now.Add(3 * statusOrdinaryAge(forge)); ok || !retry.Equal(want) {
			t.Fatalf("after a full rotation: ok=%v retry=%s, want +%s", ok, retry.Sub(clock.now), want.Sub(clock.now))
		}
	})
	t.Run("only the head of the waiters takes the turn", func(t *testing.T) {
		foreground424Fixture(t, "exit 0\n")
		clock := admission424FakeClock(t)
		admission424Queue(t, forge, 9, clock.now)
		aged(t, clock)
		foreground424Budget(t, forge, 1, clock.now)
		other := statusOrdinaryRead(context.Background(), "pr", forge, "example/widget", "9")
		ok, retry := statusTakeForgeTokenRetry(other, forge, "")
		if ok || !retry.Equal(clock.now.Add(statusOrdinaryAge(forge))) {
			t.Fatalf("second waiter overtook the head: ok=%v retry=%s", ok, retry.Sub(clock.now))
		}
		if ok, _ := statusTakeForgeTokenRetry(context.Background(), forge, ""); !ok {
			t.Fatal("head lost its turn to a later waiter")
		}
		// One turn per window: the next waiter waits a full window.
		foreground424Budget(t, forge, 1, clock.now)
		if ok, _ := statusTakeForgeTokenRetry(other, forge, ""); ok {
			t.Fatal("second turn within one window")
		}
	})
	t.Run("a held token spent outside the waiter order delays the turn one refill", func(t *testing.T) {
		foreground424Fixture(t, "exit 0\n")
		clock := admission424FakeClock(t)
		admission424Queue(t, forge, 9, clock.now)
		aged(t, clock)
		// Released clients ignore the waiters; their spend of the held token
		// is modelled as the bucket emptied under the shared protocol.
		foreground424Budget(t, forge, 0, clock.now)
		ok, retry := statusTakeForgeTokenRetry(context.Background(), forge, "")
		if ok || !retry.Equal(clock.now.Add(8*time.Second)) {
			t.Fatalf("after a released spend: ok=%v retry=%s", ok, retry.Sub(clock.now))
		}
		clock.now = retry
		if ok, _ := statusTakeForgeTokenRetry(context.Background(), forge, ""); !ok {
			t.Fatal("turn not taken at the next refill")
		}
	})
	t.Run("a fresh waiter marks and waits its age", func(t *testing.T) {
		foreground424Fixture(t, "exit 0\n")
		clock := admission424FakeClock(t)
		admission424Queue(t, forge, 9, clock.now)
		foreground424Budget(t, forge, 3, clock.now)
		ok, retry := statusTakeForgeTokenRetry(context.Background(), forge, "")
		if ok || !admission424Marked(forge, clock.now) || !retry.Equal(clock.now.Add(statusOrdinaryAge(forge))) {
			t.Fatalf("fresh waiter: ok=%v marked=%v retry=%s", ok, admission424Marked(forge, clock.now), retry.Sub(clock.now))
		}
		clock.now = retry
		if ok, _ := statusTakeForgeTokenRetry(context.Background(), forge, ""); !ok {
			t.Fatal("aged waiter not admitted at its retry time")
		}
	})
	t.Run("an expired waiter grants nothing", func(t *testing.T) {
		foreground424Fixture(t, "exit 0\n")
		clock := admission424FakeClock(t)
		admission424Queue(t, forge, 9, clock.now)
		gone := clock.now.Add(-statusOrdinaryLifetime)
		statusSaveOrdinaryWaiters(forge, statusOrdinaryWaiters{Queue: []statusOrdinaryWaiter{{Key: statusOrdinaryIdentity(context.Background()), Since: gone, Seen: gone}}})
		if admission424Marked(forge, clock.now) {
			t.Fatal("a waiter unseen for its lifetime is still waiting")
		}
		foreground424Budget(t, forge, 3, clock.now)
		if ok, _ := statusTakeForgeTokenRetry(context.Background(), forge, ""); ok {
			t.Fatal("expired waiter admitted an ordinary read")
		}
	})
	t.Run("one-token allowance holds nothing back", func(t *testing.T) {
		foreground424Fixture(t, "exit 0\n")
		t.Setenv("WTC_FORGE_MIN_INTERVAL", "8")
		clock := admission424FakeClock(t)
		queue := admission424Queue(t, forge, 9, clock.now)
		aged(t, clock)
		foreground424Budget(t, forge, 1, clock.now)
		if ok, _ := statusTakeForgeTokenRetry(context.Background(), forge, queue[0].Key); !ok {
			t.Fatal("a one-token allowance stalled the head behind a hold it cannot fill")
		}
	})
	t.Run("cooldown outranks an aged waiter", func(t *testing.T) {
		foreground424Fixture(t, "exit 0\n")
		clock := admission424FakeClock(t)
		admission424Queue(t, forge, 9, clock.now)
		aged(t, clock)
		burst, _ := statusForgeAllowance(forge)
		foreground424Budget(t, forge, burst, clock.now)
		before := admission424Tokens(t, forge)
		statusForgeCool(forge, time.Minute)
		_, live, refused, err := statusWithForgeAdmissionContext(context.Background(), forge, "", nil, func() ([]byte, error) {
			t.Fatal("fetched while cooling")
			return nil, nil
		})
		if live || !errors.Is(err, errForgeCooling) || refused.reason != "provider-cooldown" || refused.retry.IsZero() || admission424Tokens(t, forge) != before {
			t.Fatalf("cooling: live=%v err=%v %+v", live, err, refused)
		}
	})
	t.Run("a foreground lease outranks an aged waiter", func(t *testing.T) {
		foreground424Fixture(t, "exit 0\n")
		clock := admission424FakeClock(t)
		queue, ok := statusQueueForeground(admission424Queue(t, forge, 9, clock.now), statusConfirmationKey("manual"), clock.now.Add(time.Minute))
		if !ok || !statusSavePendingConfirmations(forge, queue) {
			t.Fatal("seed lease")
		}
		aged(t, clock)
		foreground424Budget(t, forge, 8, clock.now)
		if ok, _ := statusTakeForgeTokenRetry(context.Background(), forge, ""); ok {
			t.Fatal("ordinary read overtook a foreground lease")
		}
	})
	t.Run("cancellation spends nothing and marks nothing", func(t *testing.T) {
		foreground424Fixture(t, "exit 0\n")
		clock := admission424FakeClock(t)
		queue := admission424Queue(t, forge, 9, clock.now)
		foreground424Budget(t, forge, 0, clock.now)
		before := admission424Tokens(t, forge)
		cancelled, cancel := context.WithCancel(context.Background())
		cancel()
		if ok, _ := statusTakeForgeTokenRetry(cancelled, forge, ""); ok || admission424Marked(forge, clock.now) {
			t.Fatal("cancelled ordinary read admitted or marked")
		}
		// The head's bounded in-lock refill wait observes cancellation too.
		waiting, stop := context.WithCancel(context.Background())
		statusAdmissionWait = func(context.Context, time.Duration) error { stop(); return context.Canceled }
		if ok, _ := statusTakeForgeTokenRetry(waiting, forge, queue[0].Key); ok || admission424Tokens(t, forge) != before {
			t.Fatalf("cancelled head admitted or spent: %q", admission424Tokens(t, forge))
		}
		if got := statusReadPendingConfirmations(forge, clock.now); len(got) != 9 || got[0].Key != queue[0].Key {
			t.Fatal("cancellation reordered the FIFO")
		}
	})
}

// The full-detail audit reads by PR number; each PR keeps its own place among
// ordinary waiters instead of sharing one anonymous turn.
func TestStatusAdmission424FullDetailAuditsWaitAsTheirOwnPRs(t *testing.T) {
	const forge, slug = "bitbucket.org", "example/widget"
	calls := foreground424Fixture(t, "exit 0\n")
	clock := admission424FakeClock(t)
	admission424Queue(t, forge, 9, clock.now)
	foreground424Budget(t, forge, 0, clock.now)
	for _, number := range []string{"31", "32"} {
		if _, err := statusFetchPRRawWithRequestContext(context.Background(), PRRecord{Number: number}, slug, forge, nil); !errors.Is(err, errForgeBudget) {
			t.Fatalf("PR %s audit: %v", number, err)
		}
	}
	waiters := statusReadOrdinaryWaiters(forge, clock.now)
	want := []string{statusOrdinaryIdentity(statusOrdinaryRead(context.Background(), "pr", forge, slug, "31")), statusOrdinaryIdentity(statusOrdinaryRead(context.Background(), "pr", forge, slug, "32"))}
	if len(waiters.Queue) != 2 || waiters.Queue[0].Key != want[0] || waiters.Queue[1].Key != want[1] {
		t.Fatalf("audit waiters %+v", waiters.Queue)
	}
	if foreground424Count(calls) != 0 {
		t.Fatal("refused audit reached the provider")
	}
}

// A refused read serves prior bytes under their original observation time:
// skipped provider reads never freshen cached facts.
func TestStatusAdmission424RefusalKeepsPriorObservationTime(t *testing.T) {
	const forge, slug, number = "bitbucket.org", "example/widget", "21"
	foreground424Fixture(t, "exit 0\n")
	clock := admission424FakeClock(t)
	admission424Queue(t, forge, 9, clock.now)
	foreground424Budget(t, forge, 0, clock.now)
	old := time.Now().Add(-2 * time.Hour).UTC().Truncate(time.Second)
	saved := []byte(`{"id":21,"state":"OPEN"}`)
	if !statusWriteForgeEntryObserved("pr", forge, slug, number, saved, old) {
		t.Fatal("seed stale entry")
	}
	raw, live, refused, err := statusFetchSharedAdmissionContext(context.Background(), "pr", forge, slug, number, func() ([]byte, error) {
		t.Fatal("fetched without admission")
		return nil, nil
	})
	if err != nil || live || string(raw) != string(saved) || refused.reason != "shared-budget" || refused.retry.IsZero() {
		t.Fatalf("stale fallback: live=%v err=%v %+v", live, err, refused)
	}
	_, observed, ok := statusReadForgeEntryObservedContext(context.Background(), "pr", forge, slug, number, 365*24*time.Hour)
	if !ok || !observed.Equal(old) {
		t.Fatalf("observation time %s, want %s", observed, old)
	}
}
