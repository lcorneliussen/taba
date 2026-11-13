package wtc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// This fake commits encoded bytes, not shared Go pointers. It models one
// durable journal and a separate existing runtime-schema fence; no real paths,
// provider, credential, runtime or daemon are opened.
type tunnelLifecycleFixture struct {
	t                 *testing.T
	mu                sync.Mutex
	data              []byte
	writes, failWrite int
	fenced, failFence bool
	definition        tunnelDefinition
	controller        tunnelLifecycle
	calls             []string
	events            []tunnelEvent
	failStage         string
	lastCall          tunnelEffectCall
	lastResult        tunnelEffectResult
	beforeProvider    func(tunnelEffectCall)
}

func newTunnelLifecycleFixture(t *testing.T) *tunnelLifecycleFixture {
	t.Helper()
	f := &tunnelLifecycleFixture{t: t, definition: testTunnelDefinition()}
	f.definition.Owner = "synthetic-owner"
	f.definition.HooksDigest = tunnelDigest([]byte("synthetic-events"))
	f.definition.ReloadDigest = tunnelDigest([]byte("synthetic-reload"))
	f.data, _ = encodeTunnelState(tunnelState{Schema: 1, Owner: f.definition.Owner}, f.definition.Owner)
	f.controller = tunnelLifecycle{Owner: f.definition.Owner, Store: tunnelLifecycleStore{
		Read: func(context.Context) (tunnelState, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			return decodeTunnelState(f.data, f.definition.Owner)
		},
		Update: func(_ context.Context, change func(*tunnelState) error) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			s, err := decodeTunnelState(f.data, f.definition.Owner)
			if err != nil {
				return err
			}
			if err = change(&s); err != nil {
				return err
			}
			data, err := encodeTunnelState(s, f.definition.Owner)
			if err != nil {
				return err
			}
			f.writes++
			if f.writes == f.failWrite {
				return errors.New("synthetic durable write failure")
			}
			f.data = data
			return nil
		},
		Fence: func(context.Context) error {
			if f.failFence {
				return errors.New("synthetic fence failure")
			}
			f.fenced = true
			return nil
		},
	}}
	f.controller.Effects = tunnelLifecycleEffects{
		Authorize: func(context.Context, tunnelEffectCall) error { return nil },
		Provider: func(_ context.Context, c tunnelEffectCall) (tunnelEffectResult, error) {
			f.effect(c)
			if f.beforeProvider != nil {
				f.beforeProvider(c)
			}
			r := tunnelEffectResult{}
			switch c.Stage {
			case "register":
				r.Address = f.address()
				r.Add = []tunnelResourceRef{f.resource("registration")}
			case "start":
				r.Address = f.address()
				r.Add = []tunnelResourceRef{f.resource("session")}
			case "stop":
				for _, receipt := range c.Entry.Resources {
					if receipt.Lifetime == "session" {
						r.Remove = append(r.Remove, receipt)
					}
				}
			case "unregister":
				for _, receipt := range c.Entry.Resources {
					if receipt.Ownership == "owned" {
						r.Remove = append(r.Remove, receipt)
					}
				}
			}
			return r, f.response(c, r)
		},
		Event: func(_ context.Context, e tunnelEvent) (tunnelEffectResult, error) {
			if err := e.validate(); err != nil {
				t.Fatal(err)
			}
			c := f.pending("event-" + e.Type)
			f.effect(c)
			f.events = append(f.events, e)
			return tunnelEffectResult{}, f.response(c, tunnelEffectResult{})
		},
		Publish: func(_ context.Context, snapshot tunnelHostSnapshot) error {
			c := f.pending("publish")
			f.effect(c)
			s, _ := f.controller.Store.Read(context.Background())
			current, _ := buildTunnelHostSnapshot(tunnelStateActive(s))
			if current.Revision != snapshot.Revision {
				return errors.New("stale publication refused")
			}
			return f.response(c, tunnelEffectResult{})
		},
		Reload: func(_ context.Context, c tunnelEffectCall) error {
			f.effect(c)
			return f.response(c, tunnelEffectResult{})
		},
	}
	return f
}

func (f *tunnelLifecycleFixture) address() *tunnelAddress {
	a, err := parseTunnelAddress("https://web.example.test:8443/preview")
	if err != nil {
		f.t.Fatal(err)
	}
	return &a
}
func (f *tunnelLifecycleFixture) resource(lifetime string) tunnelResourceRef {
	r := testTunnelResource(lifetime, "synthetic-"+lifetime)
	r.Lifetime = lifetime
	return r
}
func (f *tunnelLifecycleFixture) request(id, action string) tunnelLifecycleRequest {
	c, err := newTunnelConsent(f.definition, true, tunnelAccessEvidence{})
	if err != nil {
		f.t.Fatal(err)
	}
	return tunnelLifecycleRequest{ID: id, Action: action, Definition: f.definition, Consent: c}
}
func (f *tunnelLifecycleFixture) read() tunnelState {
	s, err := f.controller.Store.Read(context.Background())
	if err != nil {
		f.t.Fatal(err)
	}
	return s
}
func (f *tunnelLifecycleFixture) pending(stage string) tunnelEffectCall {
	s := f.read()
	e := lifecycleEntry(&s, f.definition.Entry.Key)
	if e == nil || e.Operation == nil {
		f.t.Fatal("effect without journal")
	}
	c, err := f.controller.call(s, *e, stage)
	if err != nil {
		f.t.Fatal(err)
	}
	return c
}
func (f *tunnelLifecycleFixture) effect(c tunnelEffectCall) {
	if !f.mu.TryLock() {
		f.t.Fatal("external callback ran under journal lock")
	}
	f.mu.Unlock()
	s := f.read()
	e := lifecycleEntry(&s, c.Entry.Key)
	if !f.fenced || e == nil || e.Operation == nil || len(e.Operation.Steps) == 0 {
		f.t.Fatal("effect before fence/durable intent")
	}
	last := e.Operation.Steps[len(e.Operation.Steps)-1]
	if last.Name != c.Stage || last.Status != "pending" {
		f.t.Fatalf("effect without pending step: %+v", last)
	}
	f.calls = append(f.calls, c.Stage)
}
func (f *tunnelLifecycleFixture) response(c tunnelEffectCall, r tunnelEffectResult) error {
	f.lastCall = c
	f.lastResult = r
	if f.failStage == c.Stage {
		f.failStage = ""
		return errors.New("synthetic-sensitive-provider-output")
	}
	return nil
}

func TestTunnelLifecycleFourEventsStoppedRegistrationAndSharedHosts(t *testing.T) {
	f := newTunnelLifecycleFixture(t)
	ctx := context.Background()
	// Another named entry sharing the hostname must survive this entry's stop.
	err := f.controller.Store.Update(ctx, func(s *tunnelState) error {
		key := f.definition.Entry.Key
		key.Name = "other"
		s.Entries = append(s.Entries, tunnelEntryState{Key: key, Provider: "cloudflare", Mode: "named", Definition: tunnelDigest([]byte("other")), Active: f.address()})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	r := f.request("start-one", "start")
	if err = f.controller.Execute(ctx, r); err != nil {
		t.Fatal(err)
	}
	calls := len(f.calls)
	if err = f.controller.Execute(ctx, r); err != nil || len(f.calls) != calls {
		t.Fatal("completed operation replayed", err)
	}
	if err = f.controller.Execute(ctx, f.request("stop-one", "stop")); err != nil {
		t.Fatal(err)
	}
	s := f.read()
	e := lifecycleEntry(&s, r.Definition.Entry.Key)
	if !e.Registered || e.Active != nil || e.Known == nil || len(e.Resources) != 1 || !e.needsCleanup() {
		t.Fatal("stop lost persistent cleanup receipt")
	}
	if err = f.controller.Execute(ctx, f.request("unregister-one", "unregister")); err != nil {
		t.Fatal(err)
	}
	s = f.read()
	e = lifecycleEntry(&s, r.Definition.Entry.Key)
	if e.needsCleanup() {
		t.Fatal("confirmed cleanup remained pending")
	}
	want := []string{"registered", "started", "stopped", "unregistered"}
	got := []string{}
	for _, event := range f.events {
		got = append(got, event.Type)
		if event.After.Hosts[0] != "web.example.test" || len(event.After.Hosts) != 1 {
			t.Fatal("shared hostname removed")
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatal(got)
	}
	if f.events[1].Current == nil || f.events[1].Current.Origin != "https://web.example.test:8443" || f.events[2].Previous == nil || f.events[2].Current != nil || f.events[3].Previous == nil {
		t.Fatal("structured old/new address lost")
	}
}

func TestTunnelLifecycleLostResponsesRequireExplicitConfirmation(t *testing.T) {
	for _, stage := range []string{"register", "event-registered", "start", "event-started", "publish", "reload"} {
		t.Run(stage, func(t *testing.T) {
			f := newTunnelLifecycleFixture(t)
			ctx := context.Background()
			f.failStage = stage
			r := f.request("uncertain", "start")
			if err := f.controller.Execute(ctx, r); !errors.Is(err, errTunnelReconcile) || strings.Contains(err.Error(), "sensitive") {
				t.Fatal(err)
			}
			original, result := f.lastCall, f.lastResult
			before, _ := json.Marshal(original.Event)
			calls := len(f.calls)
			if err := f.controller.Execute(ctx, r); !errors.Is(err, errTunnelReconcile) || len(f.calls) != calls {
				t.Fatal("uncertain effect replayed", err)
			}
			if stage == "register" && len(f.read().Entries[0].Resources) != 1 {
				t.Fatal("failed result lost returned allocation receipt")
			}
			f.controller.Effects.Confirm = func(_ context.Context, c tunnelEffectCall) (tunnelEffectResult, bool, error) {
				payload, _ := json.Marshal(c.Event)
				if c.OperationID != original.OperationID || c.Stage != stage || !reflect.DeepEqual(payload, before) {
					t.Fatal("confirmation inputs changed")
				}
				return result, false, nil
			}
			if err := f.controller.Reconcile(ctx, r.Definition.Entry.Key, r.ID); !errors.Is(err, errTunnelReconcile) {
				t.Fatal(err)
			}
			f.controller.Effects.Confirm = func(context.Context, tunnelEffectCall) (tunnelEffectResult, bool, error) { return result, true, nil }
			if err := f.controller.Reconcile(ctx, r.Definition.Entry.Key, r.ID); err != nil {
				t.Fatal(err)
			}
			count := 0
			for _, called := range f.calls {
				if called == stage {
					count++
				}
			}
			if count != 1 {
				t.Fatal("original effect repeated", f.calls)
			}
		})
	}
}

func TestTunnelLifecycleFenceAndDurableWriteFailures(t *testing.T) {
	ctx := context.Background()
	t.Run("fence", func(t *testing.T) {
		f := newTunnelLifecycleFixture(t)
		f.failFence = true
		r := f.request("fence", "start")
		if f.controller.Execute(ctx, r) == nil || len(f.calls) != 0 || len(f.read().Entries[0].Operation.Steps) != 0 {
			t.Fatal("effect passed failed fence")
		}
		f.failFence = false
		if err := f.controller.Execute(ctx, r); err != nil {
			t.Fatal(err)
		}
	})
	for _, write := range []int{1, 2, 3} {
		t.Run(fmt.Sprint("write-", write), func(t *testing.T) {
			f := newTunnelLifecycleFixture(t)
			f.failWrite = write
			r := f.request("write", "start")
			if f.controller.Execute(ctx, r) == nil {
				t.Fatal("failed commit accepted")
			}
			if write < 3 {
				if len(f.calls) != 0 {
					t.Fatal("provider before committed intent")
				}
				return
			}
			if len(f.calls) != 1 || f.read().Entries[0].Operation.Steps[0].Status != "pending" {
				t.Fatal("lost receipt write hid uncertainty")
			}
			result := f.lastResult
			f.controller.Effects.Confirm = func(context.Context, tunnelEffectCall) (tunnelEffectResult, bool, error) { return result, true, nil }
			if err := f.controller.Reconcile(ctx, r.Definition.Entry.Key, r.ID); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestTunnelLifecycleFreshConsentAndUseTimeAuthority(t *testing.T) {
	f := newTunnelLifecycleFixture(t)
	ctx := context.Background()
	f.definition.Entry.Policy = tunnelPolicyEntry{Provider: "tailscale", Mode: "serve", Access: "tailnet", Options: tunnelProviderOptions{ListenerPort: 8443}}
	digest, _ := f.definition.digest()
	evidence := tunnelAccessEvidence{digest, "tailnet", true}
	consent, err := newTunnelConsent(f.definition, false, evidence)
	if err != nil {
		t.Fatal(err)
	}
	r := tunnelLifecycleRequest{ID: "private", Action: "start", Definition: f.definition, Consent: consent, Evidence: evidence}
	f.controller.Effects.Access = func(context.Context, tunnelDefinition) (tunnelAccessEvidence, error) {
		return tunnelAccessEvidence{}, nil
	}
	if f.controller.Execute(ctx, r) == nil || len(f.calls) != 0 {
		t.Fatal("stale private evidence admitted effect")
	}
	r.Consent, _ = newTunnelConsent(f.definition, true, tunnelAccessEvidence{})
	f.controller.Effects.Authorize = func(context.Context, tunnelEffectCall) error { return errors.New("authority revoked") }
	if f.controller.Execute(ctx, r) == nil || len(f.calls) != 0 {
		t.Fatal("receiver authority not checked at use")
	}
}

func TestTunnelLifecycleReplacementWaitsForOldCleanup(t *testing.T) {
	f := newTunnelLifecycleFixture(t)
	ctx := context.Background()
	if err := f.controller.Execute(ctx, f.request("register-old", "register")); err != nil {
		t.Fatal(err)
	}
	old := f.definition
	f.definition.Target = "http://127.0.0.1:4000"
	if f.controller.Execute(ctx, f.request("replace", "start")) == nil {
		t.Fatal("replacement orphaned old registration")
	}
	f.definition = old
	if err := f.controller.Execute(ctx, f.request("remove-old", "unregister")); err != nil {
		t.Fatal(err)
	}
	f.definition.Target = "http://127.0.0.1:4000"
	if err := f.controller.Execute(ctx, f.request("start-new", "start")); err != nil {
		t.Fatal(err)
	}
}

func TestTunnelLifecycleConcurrentOperationDoesNotHoldHookLock(t *testing.T) {
	f := newTunnelLifecycleFixture(t)
	ctx := context.Background()
	entered, release := make(chan struct{}), make(chan struct{})
	f.beforeProvider = func(c tunnelEffectCall) {
		if c.Stage == "register" {
			close(entered)
			<-release
		}
	}
	done := make(chan error, 1)
	go func() { done <- f.controller.Execute(ctx, f.request("first", "start")) }()
	<-entered
	if err := f.controller.Execute(ctx, f.request("second", "start")); !errors.Is(err, errTunnelReconcile) {
		t.Fatal(err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestTunnelLifecycleCleanupCanCancelUnattemptedStartStages(t *testing.T) {
	f := newTunnelLifecycleFixture(t)
	ctx := context.Background()
	f.controller.Effects.Authorize = func(_ context.Context, c tunnelEffectCall) error {
		if c.Entry.Operation.Action == "start" && c.Stage == "event-started" {
			return errors.New("callback authority revoked")
		}
		return nil
	}
	if f.controller.Execute(ctx, f.request("start-denied-hook", "start")) == nil {
		t.Fatal("missing callback authorization accepted")
	}
	if f.read().Entries[0].Active == nil {
		t.Fatal("confirmed start was lost")
	}
	if err := f.controller.Execute(ctx, f.request("cleanup", "unregister")); err != nil {
		t.Fatal(err)
	}
	if f.read().Entries[0].needsCleanup() {
		t.Fatal("unattempted callback blocked safe cleanup")
	}
}

func TestTunnelLifecycleTemporaryModesHaveNoRegistrationEvents(t *testing.T) {
	for _, policy := range []tunnelPolicyEntry{{Provider: "cloudflare", Mode: "quick", Access: "public"}, {Provider: "devtunnel", Mode: "temporary", Access: "public"}} {
		t.Run(policy.Provider, func(t *testing.T) {
			f := newTunnelLifecycleFixture(t)
			ctx := context.Background()
			f.definition.Entry.Policy = policy
			if f.controller.Execute(ctx, f.request("invalid-register", "register")) == nil {
				t.Fatal("temporary registration accepted")
			}
			if err := f.controller.Execute(ctx, f.request("temporary-start", "start")); err != nil {
				t.Fatal(err)
			}
			if f.read().Entries[0].Registered {
				t.Fatal("temporary registration persisted")
			}
			if err := f.controller.Execute(ctx, f.request("temporary-cleanup", "unregister")); err != nil {
				t.Fatal(err)
			}
			if len(f.events) != 2 || f.events[0].Type != "started" || f.events[1].Type != "stopped" {
				t.Fatal("fabricated registration event", f.events)
			}
		})
	}
}

func TestTunnelLifecycleJournalCannotSkipEventsOrHideReceipts(t *testing.T) {
	f := newTunnelLifecycleFixture(t)
	ctx := context.Background()
	f.failStage = "event-started"
	if err := f.controller.Execute(ctx, f.request("pending", "start")); !errors.Is(err, errTunnelReconcile) {
		t.Fatal(err)
	}
	original := append([]byte(nil), f.data...)
	for _, change := range []func(*tunnelEntryState){
		func(e *tunnelEntryState) { e.Operation.Lifecycle.Stages = []string{"start", "publish"} },
		func(e *tunnelEntryState) { e.Operation.Lifecycle.Definition.Target = "http://127.0.0.1:4000" },
		func(e *tunnelEntryState) { e.Operation.Lifecycle.Event.OperationID = "different" },
		func(e *tunnelEntryState) { e.Operation.Lifecycle.Event.After.Hosts = []string{} },
		func(e *tunnelEntryState) { e.Resources[0].Lifetime = "unknown" },
	} {
		s, err := decodeTunnelState(original, f.definition.Owner)
		if err != nil {
			t.Fatal(err)
		}
		change(&s.Entries[0])
		data, _ := json.Marshal(s)
		if _, err = decodeTunnelState(data, f.definition.Owner); err == nil {
			t.Fatal("corrupt lifecycle state accepted")
		}
	}
}

func TestTunnelLifecycleCleanupFailureRetainsStoppedRegistration(t *testing.T) {
	f := newTunnelLifecycleFixture(t)
	ctx := context.Background()
	if err := f.controller.Execute(ctx, f.request("register", "register")); err != nil {
		t.Fatal(err)
	}
	f.failStage = "unregister"
	if err := f.controller.Execute(ctx, f.request("cleanup", "unregister")); !errors.Is(err, errTunnelReconcile) {
		t.Fatal(err)
	}
	e := f.read().Entries[0]
	if e.Active != nil || !e.Registered || e.Known == nil || len(e.Resources) != 1 || !e.needsCleanup() {
		t.Fatal("failed cleanup forgot stopped registration")
	}
	if err := f.controller.Execute(ctx, f.request("force-retry", "unregister")); !errors.Is(err, errTunnelReconcile) {
		t.Fatal("uncertain cleanup bypassed", err)
	}
}

func TestTunnelLifecycleUnattemptedCleanupHookRemainsAnObligation(t *testing.T) {
	f := newTunnelLifecycleFixture(t)
	ctx := context.Background()
	if err := f.controller.Execute(ctx, f.request("registered", "register")); err != nil {
		t.Fatal(err)
	}
	f.controller.Effects.Authorize = func(_ context.Context, c tunnelEffectCall) error {
		if c.Stage == "event-unregistered" {
			return errors.New("temporary callback refusal")
		}
		return nil
	}
	if f.controller.Execute(ctx, f.request("cleanup", "unregister")) == nil {
		t.Fatal("cleanup callback refusal ignored")
	}
	e := f.read().Entries[0]
	if e.Registered || len(e.Resources) != 0 || !e.needsCleanup() {
		t.Fatal("unattempted cleanup suffix lost its obligation")
	}
	if err := f.controller.Execute(ctx, f.request("different-cleanup", "unregister")); !errors.Is(err, errTunnelReconcile) {
		t.Fatal("cleanup suffix discarded", err)
	}
	f.controller.Effects.Authorize = func(context.Context, tunnelEffectCall) error { return nil }
	if err := f.controller.Reconcile(ctx, f.definition.Entry.Key, "cleanup"); err != nil {
		t.Fatal(err)
	}
	if f.read().Entries[0].needsCleanup() {
		t.Fatal("confirmed callback left false obligation")
	}
}

func TestTunnelLifecycleCancellationRetainsDefinitionAndNeverUncertainEffects(t *testing.T) {
	f := newTunnelLifecycleFixture(t)
	ctx := context.Background()
	r := f.request("unattempted", "start")
	f.failFence = true
	if f.controller.Execute(ctx, r) == nil {
		t.Fatal("failed fence ignored")
	}
	if err := f.controller.Execute(ctx, f.request("cancel", "stop")); err != nil {
		t.Fatal(err)
	}
	e := f.read().Entries[0]
	if e.needsCleanup() || e.Operation == nil || !e.Operation.Lifecycle.Cancelled || e.Operation.Lifecycle.Definition.Target != f.definition.Target {
		t.Fatal("cancellation lost definition or invented completion")
	}
	if f.controller.Execute(ctx, r) == nil {
		t.Fatal("cancelled start reported success")
	}
}

func TestTunnelLifecycleReadStateRequiresNoMutationOrProviderBindings(t *testing.T) {
	f := newTunnelLifecycleFixture(t)
	ctx := context.Background()
	if err := f.controller.Execute(ctx, f.request("registered", "register")); err != nil {
		t.Fatal(err)
	}
	before := append([]byte(nil), f.data...)
	writes, calls := f.writes, len(f.calls)
	reader := tunnelLifecycle{Owner: f.definition.Owner, Store: tunnelLifecycleStore{Read: f.controller.Store.Read}}
	s, err := reader.ReadState(ctx)
	if err != nil || !s.Entries[0].Registered || !reflect.DeepEqual(f.data, before) || f.writes != writes || len(f.calls) != calls {
		t.Fatal("read required mutation/provider", err)
	}
	reader.Owner = "different-owner"
	if _, err = reader.ReadState(ctx); err == nil {
		t.Fatal("foreign journal read accepted")
	}
}

func TestTunnelLifecycleUnverifiedProviderIdentityNeverGrantsCleanup(t *testing.T) {
	f := newTunnelLifecycleFixture(t)
	ctx := context.Background()
	u := tunnelResourceRef{Provider: "cloudflare", Scope: "synthetic-account/synthetic-zone", Kind: "tunnel", ID: "unexpected-provider-id", Ownership: "unverified", Lifetime: "registration"}
	provider := f.controller.Effects.Provider
	f.controller.Effects.Provider = func(_ context.Context, c tunnelEffectCall) (tunnelEffectResult, error) {
		f.effect(c)
		return tunnelEffectResult{Add: []tunnelResourceRef{u}}, errors.New("unverified response")
	}
	r := f.request("unverified", "start")
	if err := f.controller.Execute(ctx, r); !errors.Is(err, errTunnelReconcile) {
		t.Fatal(err)
	}
	e := f.read().Entries[0]
	if !e.needsCleanup() || len(e.Resources) != 1 || e.Resources[0] != u || e.Registered {
		t.Fatal("unverified identity lost or promoted")
	}
	if targets, err := tunnelCleanupTargets(f.definition.Owner, e.Resources, []tunnelResourceObservation{{Resource: u, Absent: true}}); err == nil || len(targets) != 0 {
		t.Fatal("unverified response became deletion authority")
	}
	verified := u
	verified.Ownership = "owned"
	verified.Owner = f.definition.Owner
	verified.Fingerprint = tunnelDigest([]byte("exact confirmed identity"))
	f.controller.Effects.Confirm = func(context.Context, tunnelEffectCall) (tunnelEffectResult, bool, error) {
		return tunnelEffectResult{Address: f.address(), Add: []tunnelResourceRef{verified}}, true, nil
	}
	if f.controller.Reconcile(ctx, r.Definition.Entry.Key, r.ID) == nil {
		t.Fatal("unresolved identity silently replaced")
	}
	f.controller.Effects.Provider = provider
	f.controller.Effects.Confirm = func(context.Context, tunnelEffectCall) (tunnelEffectResult, bool, error) {
		return tunnelEffectResult{Address: f.address(), Remove: []tunnelResourceRef{u}, Add: []tunnelResourceRef{verified}}, true, nil
	}
	if err := f.controller.Reconcile(ctx, r.Definition.Entry.Key, r.ID); err != nil {
		t.Fatal(err)
	}
	bad := u
	bad.Owner = f.definition.Owner
	if bad.validate(f.definition.Owner) == nil {
		t.Fatal("unverified identity accepted local ownership evidence")
	}
}

func TestTunnelLifecycleRegistrationNeedsRetainedIdentityReceipt(t *testing.T) {
	f := newTunnelLifecycleFixture(t)
	ctx := context.Background()
	f.controller.Effects.Provider = func(_ context.Context, c tunnelEffectCall) (tunnelEffectResult, error) {
		f.effect(c)
		return tunnelEffectResult{Address: f.address()}, nil
	}
	if f.controller.Execute(ctx, f.request("missing-receipt", "register")) == nil {
		t.Fatal("registration confirmed without identity receipt")
	}
	e := f.read().Entries[0]
	if e.Registered || !e.needsCleanup() || e.Operation.Steps[0].Status != "pending" {
		t.Fatal("missing receipt hid an uncertain allocation")
	}
}

func TestTunnelLifecycleUnverifiedCleanupReplyCanBeReconciledWithoutDeletion(t *testing.T) {
	f := newTunnelLifecycleFixture(t)
	ctx := context.Background()
	if err := f.controller.Execute(ctx, f.request("register", "register")); err != nil {
		t.Fatal(err)
	}
	u := tunnelResourceRef{Provider: "cloudflare", Scope: "synthetic-account/synthetic-zone", Kind: "tunnel", ID: "unexpected-cleanup-response", Ownership: "unverified", Lifetime: "registration"}
	f.controller.Effects.Provider = func(_ context.Context, c tunnelEffectCall) (tunnelEffectResult, error) {
		f.effect(c)
		return tunnelEffectResult{Add: []tunnelResourceRef{u}}, errors.New("unexpected cleanup response")
	}
	if err := f.controller.Execute(ctx, f.request("cleanup", "unregister")); !errors.Is(err, errTunnelReconcile) {
		t.Fatal(err)
	}
	f.controller.Effects.Confirm = func(_ context.Context, c tunnelEffectCall) (tunnelEffectResult, bool, error) {
		// Fake exact observations confirm the owned target gone and the
		// unexpected reply identity absent. No deletion is issued here.
		return tunnelEffectResult{Remove: c.Entry.Resources}, true, nil
	}
	calls := len(f.calls)
	if err := f.controller.Reconcile(ctx, f.definition.Entry.Key, "cleanup"); err != nil {
		t.Fatal(err)
	}
	if f.read().Entries[0].needsCleanup() || len(f.calls) != calls+1 || f.calls[len(f.calls)-1] != "event-unregistered" {
		t.Fatal("confirmation replayed provider cleanup")
	}
}
