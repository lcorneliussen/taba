package wtc

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"
)

// #548: many status panes, most of them released clients, share one
// Bitbucket bucket. A PR that no view has observed yet must still get a
// bounded first read. Every test drives the real admission function under its
// real lock and shared files on the fake decision clock of the #424 tests;
// no provider is contacted.

// admission548Released is the released (0.2.7-0.2.9) admission decision,
// kept here verbatim but for the clock seams, so mixed-version contention can
// be replayed against the current client. Released clients know the
// confirmation FIFO and foreground leases, take any token above one per
// queued confirmation for ordinary reads, and let the FIFO head wait in the
// budget lock for the next refill. They know neither ordinary waiters, held
// tokens nor first-observation turns.
func admission548Released(ctx context.Context, forge, confirmation string) bool {
	burst, every := statusForgeAllowance(forge)
	path := statusForgeStatePath("budget", forge)
	unlock, err := statusLockCacheContext(ctx, path)
	if err != nil {
		return false
	}
	defer unlock()
	now := statusAdmissionNow()
	queue := statusReadPendingConfirmations(forge, now)
	admitted := len(queue) == 0 || queue[0].Key == confirmation
	if confirmation != "" {
		queue = statusQueueConfirmation(queue, confirmation, now)
	}
	if len(queue) > 0 && queue[0].Foreground {
		statusSavePendingConfirmations(forge, queue)
		return false
	}
	tokens, updated := statusReadForgeBudget(path, burst, every, now)
	if confirmation != "" && admitted && tokens == 0 {
		if delay := updated.Add(every).Sub(now); delay > 0 && delay <= statusBitbucketEvery {
			statusSavePendingConfirmations(forge, queue)
			if statusAdmissionWait(ctx, delay) != nil {
				return false
			}
			now = statusAdmissionNow()
			tokens, updated = statusReadForgeBudget(path, burst, every, now)
		}
	}
	if confirmation == "" && tokens > len(queue) {
		admitted = true
	}
	if tokens < 1 || !admitted {
		statusSavePendingConfirmations(forge, queue)
		return false
	}
	if confirmation != "" && len(queue) > 0 && queue[0].Key == confirmation {
		queue = queue[1:]
	}
	statusSavePendingConfirmations(forge, queue)
	return os.WriteFile(path, []byte(fmt.Sprintf("%d %d\n", tokens-1, updated.UnixNano())), 0600) == nil
}

type admission548Load struct {
	confirmations, releasedOrdinary, currentOrdinary, first int
}

// admission548Panes adds the observed load of the report: about thirty
// released panes, each re-confirming one omitted PR per pass (two thirds of
// them in the background, every five minutes; the rest focused, every 30 s),
// released and current panes polling ordinary list reads every 30 s, and
// current panes waiting in the ordinary FIFO.
func admission548Panes(t *testing.T, clock *admission424Clock, actors *[]*admission424Actor, load *admission548Load) {
	t.Helper()
	const forge = "bitbucket.org"
	ctx := context.Background()
	start := clock.now
	for i := 0; i < 30; i++ {
		key := statusConfirmationKey(fmt.Sprintf("released-%d", i))
		pass := 5 * time.Minute
		if i%3 == 0 {
			pass = 30 * time.Second
		}
		// A dormant pane keeps its FIFO place until it asks again.
		statusSavePendingConfirmations(forge, statusQueueConfirmation(statusReadPendingConfirmations(forge, clock.now), key, clock.now))
		*actors = append(*actors, &admission424Actor{next: start.Add(time.Duration(i) * 7 * time.Second), run: func() (time.Duration, bool) {
			if admission548Released(ctx, forge, key) {
				load.confirmations++
			}
			return pass, false
		}})
	}
	for i := 0; i < 10; i++ {
		*actors = append(*actors, &admission424Actor{next: start.Add(time.Duration(i) * 3 * time.Second), run: func() (time.Duration, bool) {
			if admission548Released(ctx, forge, "") {
				load.releasedOrdinary++
			}
			return 30 * time.Second, false
		}})
	}
	for i := 0; i < 10; i++ {
		read := statusOrdinaryRead(ctx, "list", forge, fmt.Sprintf("example/current-%d", i), "open")
		*actors = append(*actors, &admission424Actor{next: start.Add(time.Duration(i) * 5 * time.Second), run: func() (time.Duration, bool) {
			if ok, _ := statusTakeForgeTokenRetry(read, forge, ""); ok {
				load.currentOrdinary++
			}
			return 30 * time.Second, false
		}})
	}
}

// admission548Poll is how soon a current view asks again after a refusal: at
// its retry hint, but not sooner than 15 s nor later than its 30 s pass.
func admission548Poll(now, retry time.Time) time.Duration {
	wait := 30 * time.Second
	if !retry.IsZero() {
		wait = min(wait, max(15*time.Second, retry.Sub(now)))
	}
	return wait
}

// The reported starvation, replayed: a newly opened PR is missing from its
// repository's cached open list, so its first read is an omission
// confirmation (the same applies to an ordinary detail read). Behind thirty
// released panes it must still be read within a few minutes, without
// exceeding the provider allowance or stopping everyone else.
func TestStatusAdmission548NeverObservedPRBehindReleasedPanes(t *testing.T) {
	const forge, duration = "bitbucket.org", 30 * time.Minute
	for _, kind := range []string{"confirmation", "ordinary"} {
		t.Run(kind, func(t *testing.T) {
			foreground424Fixture(t, "exit 0\n")
			clock := admission424FakeClock(t)
			burst, every := statusForgeAllowance(forge)
			start := clock.now
			foreground424Budget(t, forge, 0, start)
			var actors []*admission424Actor
			var load admission548Load
			admission548Panes(t, clock, &actors, &load)
			confirmation := ""
			if kind == "confirmation" {
				confirmation = statusConfirmationKey("example/widget#90")
			}
			read := statusFirstObservation(statusOrdinaryRead(context.Background(), "pr", forge, "example/widget", "90"), forge, "example/widget", "90")
			admittedAt := time.Duration(-1)
			asked := 0
			actors = append(actors, &admission424Actor{next: start.Add(20 * time.Second), run: func() (time.Duration, bool) {
				asked++
				ok, retry := statusTakeForgeTokenRetry(read, forge, confirmation)
				if ok {
					load.first++
					admittedAt = clock.now.Sub(start)
					return 0, true
				}
				return admission548Poll(clock.now, retry), false
			}})
			admission424Run(clock, start.Add(duration), &actors)
			elapsed := clock.now.Sub(start)
			total := load.confirmations + load.releasedOrdinary + load.currentOrdinary + load.first
			t.Logf("%s: first read admitted after %s (%d asks); released confirmations %d, released ordinary %d, current ordinary %d",
				elapsed.Round(time.Second), admittedAt, asked, load.confirmations, load.releasedOrdinary, load.currentOrdinary)
			if admittedAt < 0 {
				t.Fatalf("never-observed PR not read in %s behind released panes", duration)
			}
			// One first-observation turn, a refill and one poll.
			if bound := statusFirstObservationGap(forge) + 2*every + 30*time.Second; admittedAt > bound+20*time.Second {
				t.Fatalf("never-observed PR waited %s, bound %s", admittedAt, bound)
			}
			if ceiling := burst + int(elapsed/every); total > ceiling {
				t.Fatalf("admitted %d reads, provider ceiling %d", total, ceiling)
			}
			// Background work keeps progressing. Which ordinary reader wins a
			// refill depends on the phase of these strictly periodic actors (any
			// released head waiting in the lock shifts it), so only the stable
			// confirmation rate of this load is pinned: 83 without the first read.
			if load.confirmations < 80 || load.currentOrdinary == 0 {
				t.Fatalf("background starved: %+v", load)
			}
		})
	}
}

// Repeated startup: thirty current panes come up at once, each with three
// PRs nobody has observed, beside the released load. First reads may take at
// most one turn per gap, so confirmations keep progressing, and every first
// read still lands within its bounded share.
func TestStatusAdmission548FirstReadsShareIsBounded(t *testing.T) {
	// Ninety first reads need ninety turns: about 36 fake minutes.
	const forge, duration = "bitbucket.org", 45 * time.Minute
	foreground424Fixture(t, "exit 0\n")
	clock := admission424FakeClock(t)
	burst, every := statusForgeAllowance(forge)
	gap := statusFirstObservationGap(forge)
	start := clock.now
	foreground424Budget(t, forge, 0, start)
	var actors []*admission424Actor
	var load admission548Load
	admission548Panes(t, clock, &actors, &load)
	served := map[string]time.Duration{}
	readers := 0
	for pane := 0; pane < 30; pane++ {
		for pr := 0; pr < 3; pr++ {
			number := fmt.Sprintf("%d", 100+pane*3+pr)
			slug := fmt.Sprintf("example/pane-%d", pane)
			read := statusFirstObservation(statusOrdinaryRead(context.Background(), "pr", forge, slug, number), forge, slug, number)
			readers++
			actors = append(actors, &admission424Actor{next: start.Add(time.Duration(pane) * time.Second), run: func() (time.Duration, bool) {
				ok, retry := statusTakeForgeTokenRetry(read, forge, "")
				if ok {
					load.first++
					served[slug+"#"+number] = clock.now.Sub(start)
					return 0, true
				}
				return admission548Poll(clock.now, retry), false
			}})
		}
	}
	admission424Run(clock, start.Add(duration), &actors)
	elapsed := clock.now.Sub(start)
	last := time.Duration(0)
	for _, at := range served {
		last = max(last, at)
	}
	t.Logf("%s: %d/%d first reads, last after %s; %+v", elapsed.Round(time.Second), len(served), readers, last, load)
	total := load.confirmations + load.releasedOrdinary + load.currentOrdinary + load.first
	if ceiling := burst + int(elapsed/every); total > ceiling {
		t.Fatalf("admitted %d reads, provider ceiling %d", total, ceiling)
	}
	// One first turn per gap at most, whoever asks.
	if share := 1 + int(elapsed/gap); load.first > share {
		t.Fatalf("first reads took %d turns, share %d", load.first, share)
	}
	if len(served) != readers {
		t.Fatalf("%d of %d first reads served in %s", len(served), readers, duration)
	}
	// Each turn opens a gap after the last and waits at most one refill.
	if bound := time.Duration(readers)*(gap+every) + 30*time.Second; last > bound {
		t.Fatalf("last first read after %s, bound %s", last, bound)
	}
	if load.confirmations < 40 || load.currentOrdinary == 0 {
		t.Fatalf("background starved by first reads: %+v", load)
	}
}

// GitHub keeps its own admission: a first read there needs no turn and is
// admitted like any ordinary read, and the turn file is never created.
func TestStatusAdmission548GitHubFirstReadsUnchanged(t *testing.T) {
	const forge = "github.com"
	foreground424Fixture(t, "exit 0\n")
	clock := admission424FakeClock(t)
	foreground424Budget(t, forge, 1, clock.now)
	read := statusFirstObservation(statusOrdinaryRead(context.Background(), "pr", forge, "example/widget", "7"), forge, "example/widget", "7")
	if ok, _ := statusTakeForgeTokenRetry(read, forge, ""); !ok {
		t.Fatal("GitHub first read refused with a token available")
	}
	if ok, retry := statusTakeForgeTokenRetry(read, forge, ""); ok || retry.IsZero() {
		t.Fatalf("empty GitHub bucket admitted=%v retry=%s", ok, retry)
	}
	if _, err := os.Lstat(statusFirstWaitPath(forge)); !os.IsNotExist(err) {
		t.Fatalf("GitHub first-observation state written: %v", err)
	}
}

// The collector end to end: a periodic pass enlists a new Bitbucket PR and an
// old declined one behind thirty dormant released confirmations and an empty
// bucket. The new PR's reads take first turns (its repository's open list,
// then the PR itself when the list omits it), so it is observed within two
// passes; the old PR keeps its last observation, deferred. Once observed, a
// PR is marked and takes no further first turns.
func TestStatusAdmission548CollectorReadsNewPRWithinTwoPasses(t *testing.T) {
	const forge = "bitbucket.org"
	refresh535Provider(t, []string{"90"})
	clock := admission424FakeClock(t)
	c := refresh535Collection(t, forge, []string{"11"}, []string{"90"})
	records, err := c.ListPRs()
	if err != nil {
		t.Fatal(err)
	}
	admission424Queue(t, forge, 30, clock.now)
	foreground424Budget(t, forge, 0, clock.now)
	if !statusFirstObservationDue(statusAuthOperation(context.Background()), records[1], "example/widget", forge) {
		t.Fatal("new PR not due for a first read")
	}
	if statusFirstObservationDue(statusAuthOperation(context.Background()), records[0], "example/widget", forge) {
		t.Fatal("observed PR due for a first read")
	}
	var details []statusPRDetail
	pass := 0
	for ; pass < 2; pass++ {
		details, _, err = c.statusEnrichRecordsWithIntent(context.Background(), StatusRefreshBackground, records, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("pass %d: #90 %s %s %s retry %s", pass, details[1].State, details[1].Freshness, details[1].Reason, details[1].RetryAt)
		if details[1].State == "DRAFT" && !statusObservationUnavailable(details[1].Freshness) {
			break
		}
		if details[1].Freshness != "deferred" || details[1].RetryAt == "" {
			t.Fatalf("waiting PR without a deferral and retry: %+v", details[1])
		}
		// The view comes back at the retry (the TUI's floor is 30 s).
		clock.now = clock.now.Add(30 * time.Second)
	}
	if pass == 2 {
		t.Fatalf("new PR not observed in two passes: %+v", details[1])
	}
	if details[0].State != "DECLINED" || details[0].ObservedAt == "" {
		t.Fatalf("old PR lost its last observation: %+v", details[0])
	}
	if statusFirstObservationDue(statusAuthOperation(context.Background()), records[1], "example/widget", forge) {
		t.Fatal("observed PR still due for a first read")
	}
	// The dormant released FIFO keeps its order and is not starved by us.
	if got := len(statusReadPendingConfirmations(forge, clock.now)); got < 30 {
		t.Fatalf("released confirmations dropped: %d", got)
	}
}
