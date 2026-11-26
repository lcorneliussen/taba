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
// bounded first read. Current-client reads go through the real admission
// function; released-client reads go through admission548Released below. Both
// use the real budget lock and shared files on the fake decision clock of the
// #424 tests; no provider is contacted.

// admission548Released models the released (0.2.7-0.2.9) Bitbucket
// admission decision, following v0.2.9's statusTakeForgeTokenForContext with
// the clock seams; it leaves out the GitHub branch, the unlimited-allowance
// shortcut, the cancellation checks and the cooldown recheck after the refill
// wait, none of which these tests exercise. It lets mixed-version
// contention be replayed against the current client. Released clients know the
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
			// Bound: one first-observation gap (the turn may have just been
			// taken), two refills (one waited for in the lock, one more if a
			// released head took the refill first) and one 30 s pass between
			// asks; the 20 s slack is the first ask's delay into the run.
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
	// Asserted below: at least readers/2 turns, each at least one 16 s gap
	// after the last, and every first read served within the bound. Run
	// observation, not a bound: the last first read was served at about
	// 28 minutes, 66 of the 90 by turn.
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
	// Every first-observation turn, as recorded for all processes.
	var turns []time.Time
	noteTurn := func() {
		if last := statusReadFirstWaiters(forge, clock.now).Last; !last.IsZero() && (len(turns) == 0 || !last.Equal(turns[len(turns)-1])) {
			turns = append(turns, last)
		}
	}
	for pane := 0; pane < 30; pane++ {
		for pr := 0; pr < 3; pr++ {
			number := fmt.Sprintf("%d", 100+pane*3+pr)
			slug := fmt.Sprintf("example/pane-%d", pane)
			read := statusFirstObservation(statusOrdinaryRead(context.Background(), "pr", forge, slug, number), forge, slug, number)
			readers++
			actors = append(actors, &admission424Actor{next: start.Add(time.Duration(pane) * time.Second), run: func() (time.Duration, bool) {
				ok, retry := statusTakeForgeTokenRetry(read, forge, "")
				noteTurn()
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
	t.Logf("%s: %d/%d first reads (%d by turn), last after %s; %+v", elapsed.Round(time.Second), len(served), readers, len(turns), last, load)
	total := load.confirmations + load.releasedOrdinary + load.currentOrdinary + load.first
	if ceiling := burst + int(elapsed/every); total > ceiling {
		t.Fatalf("admitted %d reads, provider ceiling %d", total, ceiling)
	}
	// One first turn per gap at most, whoever asks. With ninety readers
	// asking at least every 30 s, the gap is what limits the turns; reads
	// admitted through their ordinary place are not turns.
	if len(turns) < readers/2 {
		t.Fatalf("only %d first-observation turns for %d readers", len(turns), readers)
	}
	for i := 1; i < len(turns); i++ {
		if spacing := turns[i].Sub(turns[i-1]); spacing < gap {
			t.Fatalf("turns %d and %d only %s apart, gap %s", i-1, i, spacing, gap)
		}
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
// bucket. The new PR's own detail read takes a first turn (its repository's
// open list stays ordinary work), so it is observed in the first pass; the old
// PR keeps its last observation, deferred. Once observed, a PR is marked and
// takes no further first turns.
func TestStatusAdmission548CollectorReadsNewPRInFirstPass(t *testing.T) {
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
	details, _, err := c.statusEnrichRecordsWithIntent(context.Background(), StatusRefreshBackground, records, nil)
	if err != nil {
		t.Fatal(err)
	}
	if details[1].State != "DRAFT" || statusObservationUnavailable(details[1].Freshness) {
		t.Fatalf("new PR not observed in the first pass: %+v", details[1])
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

// admission548Turn sets up a first-observation turn under test: the decision
// clock is fake, eight dormant confirmations reserve every token an ordinary
// read could take, and the bucket holds tokens at the current fake time.
func admission548Turn(t *testing.T, tokens int) (*admission424Clock, string) {
	t.Helper()
	const forge = "bitbucket.org"
	foreground424Fixture(t, "exit 0\n")
	clock := admission424FakeClock(t)
	admission424Queue(t, forge, 8, clock.now)
	foreground424Budget(t, forge, tokens, clock.now)
	return clock, forge
}

func admission548First(number string) context.Context {
	return statusFirstObservation(statusOrdinaryRead(context.Background(), "pr", "bitbucket.org", "example/widget", number), "bitbucket.org", "example/widget", number)
}

// For one refill after a turn opens only the oldest waiter that asked
// recently may take it; after that any first read may, so a waiter that went
// away delays the others by at most one refill.
func TestStatusAdmission548OldestWaiterExclusivity(t *testing.T) {
	clock, forge := admission548Turn(t, 1)
	_, every := statusForgeAllowance(forge)
	gap := statusFirstObservationGap(forge)
	start := clock.now
	if !statusSaveFirstWaiters(forge, statusFirstWaiters{Last: start}) {
		t.Fatal("seed turn")
	}
	older, newer, quiet := admission548First("1"), admission548First("2"), admission548First("3")
	ask := func(ctx context.Context, at time.Duration) (bool, time.Time) {
		clock.now = start.Add(at)
		foreground424Budget(t, forge, 1, clock.now)
		return statusTakeForgeTokenRetry(ctx, forge, "")
	}
	if ok, _ := ask(older, time.Second); ok {
		t.Fatal("admitted before the turn opened")
	}
	if ok, retry := ask(newer, 2*time.Second); ok || !retry.Equal(start.Add(2*gap)) {
		t.Fatalf("second waiter admitted=%v retry=%s, want one gap after the head's turn", ok, retry.Sub(start))
	}
	if ok, _ := ask(newer, gap+time.Second); ok {
		t.Fatal("younger waiter took the head's exclusive refill")
	}
	if ok, _ := ask(older, gap+2*time.Second); !ok {
		t.Fatal("head refused its open turn")
	}
	// The next turn: the head is gone; a waiter that registers and then stays
	// quiet is head, but only for one refill.
	turn := statusReadFirstWaiters(forge, clock.now).Last
	if ok, _ := ask(quiet, gap+3*time.Second); ok {
		t.Fatal("admitted inside the gap")
	}
	clock.now = turn.Add(gap).Add(time.Second)
	foreground424Budget(t, forge, 1, clock.now)
	if ok, _ := statusTakeForgeTokenRetry(newer, forge, ""); !ok {
		// newer registered before quiet, so it is the head now.
		t.Fatal("oldest remaining waiter refused")
	}
	turn = statusReadFirstWaiters(forge, clock.now).Last
	other := admission548First("4")
	clock.now = turn.Add(gap).Add(time.Second)
	foreground424Budget(t, forge, 1, clock.now)
	if ok, _ := statusTakeForgeTokenRetry(other, forge, ""); ok {
		t.Fatal("took a quiet head's exclusive refill")
	}
	clock.now = turn.Add(gap).Add(every)
	foreground424Budget(t, forge, 1, clock.now)
	if ok, _ := statusTakeForgeTokenRetry(other, forge, ""); !ok {
		t.Fatal("a quiet head blocked the turn past its exclusive refill")
	}
}

// Waiting in the lock for a refill: cancellation or a provider cooldown that
// starts meanwhile refuses the read without spending a token or a turn, and a
// turn whose record cannot be written is refused too.
func TestStatusAdmission548TurnWaitFailures(t *testing.T) {
	for _, failure := range []string{"cancelled", "cooldown", "unrecorded"} {
		t.Run(failure, func(t *testing.T) {
			tokens := 0
			if failure == "unrecorded" {
				tokens = 1
			}
			clock, forge := admission548Turn(t, tokens)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			read := statusFirstObservation(statusOrdinaryRead(ctx, "pr", forge, "example/widget", "1"), forge, "example/widget", "1")
			waited := false
			statusAdmissionWait = func(ctx context.Context, delay time.Duration) error {
				waited = true
				clock.now = clock.now.Add(delay)
				switch failure {
				case "cancelled":
					cancel()
				case "cooldown":
					statusForgeCool(forge, time.Hour)
				}
				return ctx.Err()
			}
			if failure == "unrecorded" {
				if err := os.Mkdir(statusFirstWaitPath(forge), 0700); err != nil {
					t.Fatal(err)
				}
			}
			before := admission424Tokens(t, forge)
			if ok, _ := statusTakeForgeTokenRetry(read, forge, ""); ok {
				t.Fatal("admitted")
			}
			if failure != "unrecorded" && !waited {
				t.Fatal("did not wait for the refill")
			}
			if after := admission424Tokens(t, forge); after != before {
				t.Fatalf("budget %q became %q", before, after)
			}
			if failure != "unrecorded" && !statusReadFirstWaiters(forge, clock.now).Last.IsZero() {
				t.Fatal("refused read recorded a turn")
			}
		})
	}
}

// In a credential context whose observations stay private to each status
// operation (here: no Bitbucket API credential, so the context is unknown),
// the next operation cannot see the PR's observation, so only the shared
// observed mark ends its first turns. Both writers set it: periodic
// enrichment and the explicit refresh.
func TestStatusAdmission548ObservedMarkEndsFirstTurnsInPrivateContexts(t *testing.T) {
	const forge, slug = "bitbucket.org", "example/widget"
	for _, intent := range []StatusRefreshIntent{StatusRefreshBackground, StatusRefreshForeground} {
		t.Run(map[StatusRefreshIntent]string{StatusRefreshBackground: "periodic", StatusRefreshForeground: "explicit"}[intent], func(t *testing.T) {
			refresh535Provider(t, []string{"90"})
			t.Setenv("BB_USERNAME", "")
			t.Setenv("BB_API_TOKEN", "")
			c := refresh535Collection(t, forge, nil, []string{"90"})
			records, err := c.ListPRs()
			if err != nil {
				t.Fatal(err)
			}
			if statusEntryKnown(statusAuthOperation(context.Background()), "pr", forge, slug, "90") {
				t.Fatal("fixture context is verifiable; this test needs a private one")
			}
			foreground424Budget(t, forge, 8, time.Now())
			details, _, err := c.statusEnrichRecordsWithIntent(context.Background(), intent, records, nil)
			if err != nil {
				t.Fatal(err)
			}
			if details[0].State != "DRAFT" || statusObservationUnavailable(details[0].Freshness) {
				t.Fatalf("not observed: %+v", details[0])
			}
			next := statusAuthOperation(context.Background())
			if _, seen := statusReadForgeEntryAnyContext(next, "pr", forge, slug, "90"); seen {
				t.Fatal("observation leaked out of its private operation")
			}
			if statusFirstObservationDue(next, records[0], slug, forge) {
				t.Fatal("observed PR still asks for first turns in a private context")
			}
		})
	}
}

// A first read admitted through its ordinary place leaves the first-turn
// waiters, so it cannot hold a later turn's exclusive refill.
func TestStatusAdmission548OrdinaryAdmissionLeavesFirstWaiters(t *testing.T) {
	const forge = "bitbucket.org"
	foreground424Fixture(t, "exit 0\n")
	clock := admission424FakeClock(t)
	if !statusSaveFirstWaiters(forge, statusFirstWaiters{Last: clock.now}) {
		t.Fatal("seed turn")
	}
	foreground424Budget(t, forge, 0, clock.now)
	read := admission548First("1")
	if ok, _ := statusTakeForgeTokenRetry(read, forge, ""); ok {
		t.Fatal("admitted from an empty bucket")
	}
	if got := len(statusReadFirstWaiters(forge, clock.now).Queue); got != 1 {
		t.Fatalf("refused first read not waiting: %d waiters", got)
	}
	// Inside the gap, with no confirmations queued, the ordinary path admits it.
	clock.now = clock.now.Add(time.Second)
	foreground424Budget(t, forge, 2, clock.now)
	if ok, _ := statusTakeForgeTokenRetry(read, forge, ""); !ok {
		t.Fatal("ordinary path refused with spare tokens")
	}
	if got := len(statusReadFirstWaiters(forge, clock.now).Queue); got != 0 {
		t.Fatalf("admitted read still waits for a first turn: %d waiters", got)
	}
}

// A refused first read never reports a retry in the past: with a one-token
// allowance or a cooldown no first turn applies, and with no turn recorded yet
// the zero time must not become a year-one retry.
func TestStatusAdmission548FirstReadRetryIsNeverInThePast(t *testing.T) {
	for _, tc := range []string{"one-token", "cooling", "younger-waiter"} {
		t.Run(tc, func(t *testing.T) {
			clock, forge := admission548Turn(t, 0)
			switch tc {
			case "one-token":
				t.Setenv("WTC_FORGE_MIN_INTERVAL", "5")
			case "cooling":
				statusForgeCool(forge, time.Hour)
			case "younger-waiter":
				// The last turn was taken just over a gap ago and an older
				// waiter is asking, so it holds the open turn's exclusive refill.
				gap := statusFirstObservationGap(forge)
				if !statusSaveFirstWaiters(forge, statusFirstWaiters{Last: clock.now.Add(-gap - time.Second),
					Queue: []statusOrdinaryWaiter{{Key: statusFirstObservationIdentity(admission548First("9")), Since: clock.now.Add(-time.Minute), Seen: clock.now}}}) {
					t.Fatal("seed waiters")
				}
			}
			ok, retry := statusTakeForgeTokenRetry(admission548First("1"), forge, "")
			if ok {
				t.Fatal("admitted")
			}
			if !retry.IsZero() && retry.Before(clock.now) {
				t.Fatalf("retry %s is before now %s", retry, clock.now)
			}
			if tc != "younger-waiter" {
				if got := len(statusReadFirstWaiters(forge, clock.now).Queue); got != 0 {
					t.Fatalf("no turn applies, yet %d first waiters were recorded", got)
				}
			}
		})
	}
}
