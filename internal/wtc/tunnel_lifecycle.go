package wtc

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
)

var errTunnelReconcile = errors.New("tunnel effect needs reconciliation; it will not be replayed")

// Update must serialize, validate and durably commit this SAME state journal
// before returning. Its mutation closure performs no external effects. Fence
// persists the existing runtime's forward schema before any provider or hook.
// These are injected storage boundaries, not a second lock, ledger or resolver.
type tunnelLifecycleStore struct {
	Read   func(context.Context) (tunnelState, error)
	Update func(context.Context, func(*tunnelState) error) error
	Fence  func(context.Context) error
}

type tunnelLifecycleRequest struct {
	ID, Action string
	Definition tunnelDefinition
	Consent    tunnelConsent
	Evidence   tunnelAccessEvidence
}

type tunnelLifecycleOperation struct {
	Definition   tunnelDefinition     `json:"definition"`
	Stages       []string             `json:"stages"`
	Previous     *tunnelAddress       `json:"previous"`
	Registration *tunnelAddress       `json:"registration"`
	Before       tunnelEventSnapshot  `json:"before"`
	Event        *tunnelEvent         `json:"event,omitempty"`
	View         *tunnelEventSnapshot `json:"view,omitempty"`
	Cancelled    bool                 `json:"cancelled,omitempty"` // only an unattempted suffix; never an uncertain effect
}

// The adapter supplies only non-secret receipts and confirmed addresses. Even
// on failure, returned additions are retained for cleanup. Removals are applied
// only after success or explicit confirmation, never after a failed request.
type tunnelEffectResult struct {
	Address     *tunnelAddress
	Add, Remove []tunnelResourceRef
}

type tunnelEffectCall struct {
	Owner, OperationID, Stage string
	Definition                tunnelDefinition
	Entry                     tunnelEntryState
	Event                     *tunnelEvent
	Snapshot                  tunnelHostSnapshot
}

type tunnelLifecycleEffects struct {
	// Revalidate receiver authority and exact role inputs at EVERY use. This
	// callback must not allocate or run hooks. No ambient auth fallback exists.
	Authorize func(context.Context, tunnelEffectCall) error
	Access    func(context.Context, tunnelDefinition) (tunnelAccessEvidence, error)
	Provider  func(context.Context, tunnelEffectCall) (tunnelEffectResult, error)
	Event     func(context.Context, tunnelEvent) (tunnelEffectResult, error)
	// Publication must compare the supplied revision with the SAME journal's
	// current active snapshot before switching the paired generation. A stale
	// revision is a refusal, never permission to overwrite a newer entry's URL.
	Publish func(context.Context, tunnelHostSnapshot) error
	Reload  func(context.Context, tunnelEffectCall) error
	// Confirmation queries are explicit actions, never status behavior. False
	// leaves the original intent untouched. There is no automatic hook retry.
	Confirm func(context.Context, tunnelEffectCall) (tunnelEffectResult, bool, error)
}

type tunnelLifecycle struct {
	Owner   string
	Store   tunnelLifecycleStore
	Effects tunnelLifecycleEffects
}

// ReadState requires neither provider availability nor credentials, Node,
// consent probes, hooks, materialization, fencing or repair. Production status
// can combine this recorded state with pure desired-policy diagnostics.
func (l tunnelLifecycle) ReadState(ctx context.Context) (tunnelState, error) {
	if l.Store.Read == nil {
		return tunnelState{}, fmt.Errorf("explicit tunnel journal reader required")
	}
	s, err := l.Store.Read(ctx)
	if err != nil {
		return tunnelState{}, err
	}
	if err = s.validate(l.Owner); err != nil {
		return tunnelState{}, err
	}
	return s, nil
}

func (p tunnelLifecycleOperation) validate(owner string, entry tunnelEntryState, op tunnelOperation) error {
	if p.Cancelled && (lifecycleUncertain(&op) || op.Action != "start" && op.Action != "register") {
		return fmt.Errorf("invalid cancelled tunnel operation")
	}
	digest, err := p.Definition.digest()
	if err != nil || p.Definition.Owner != owner || p.Definition.Entry.Key != entry.Key || digest != op.Definition || digest != entry.Definition || entry.Provider != p.Definition.Entry.Policy.Provider || entry.Mode != p.Definition.Entry.Policy.Mode || len(p.Stages) == 0 || len(op.Steps) > len(p.Stages) {
		return fmt.Errorf("invalid persisted tunnel lifecycle definition")
	}
	seen := map[string]bool{}
	if !validTunnelLifecycleStages(op.Action, p.Definition.Entry.Policy, p.Stages) {
		return fmt.Errorf("invalid tunnel lifecycle action plan")
	}
	for i, stage := range p.Stages {
		switch stage {
		case "register", "start", "stop", "unregister", "event-registered", "event-started", "event-stopped", "event-unregistered", "publish", "reload":
		default:
			return fmt.Errorf("unknown tunnel lifecycle stage")
		}
		if seen[stage] {
			return fmt.Errorf("duplicate tunnel lifecycle stage")
		}
		seen[stage] = true
		if i < len(op.Steps) && op.Steps[i].Name != stage {
			return fmt.Errorf("tunnel lifecycle step differs from plan")
		}
	}
	if seen["reload"] != (p.Definition.ReloadDigest != "" && (seen["start"] || seen["stop"])) {
		return fmt.Errorf("tunnel reload plan differs from definition")
	}
	if err := p.Before.validate(); err != nil {
		return err
	}
	if p.View != nil {
		if err := p.View.validate(); err != nil {
			return err
		}
	}
	for _, a := range []*tunnelAddress{p.Previous, p.Registration} {
		if a != nil {
			parsed, err := parseTunnelAddress(a.URL)
			if err != nil || parsed != *a {
				return fmt.Errorf("invalid lifecycle previous address")
			}
		}
	}
	if p.Event != nil {
		if err := p.Event.validate(); err != nil {
			return err
		}
		if p.Event.OperationID != op.ID || p.Event.Key != entry.Key || p.Event.Definition != digest {
			return fmt.Errorf("tunnel event differs from operation")
		}
		if len(op.Steps) == 0 || op.Steps[len(op.Steps)-1].Name != "event-"+p.Event.Type {
			return fmt.Errorf("tunnel event differs from stage")
		}
	}
	return nil
}

func validTunnelLifecycleStages(action string, policy tunnelPolicyEntry, stages []string) bool {
	if len(stages) == 0 {
		return false
	}
	remaining := append([]string(nil), stages...)
	take := func(sequence ...string) bool {
		if len(remaining) < len(sequence) || !reflect.DeepEqual(remaining[:len(sequence)], sequence) {
			return false
		}
		remaining = remaining[len(sequence):]
		return true
	}
	reload := func() {
		if policyReloadStage(remaining) {
			take("reload")
		}
	}
	contract, err := tunnelContract(policy.Provider, policy.Mode)
	if err != nil {
		return false
	}
	switch action {
	case "register":
		return contract.Registration != "none" && take("register", "event-registered") && len(remaining) == 0
	case "start":
		if remaining[0] == "register" {
			if contract.Registration == "none" || !take("register", "event-registered") {
				return false
			}
		}
		if !take("start", "event-started", "publish") {
			return false
		}
		reload()
	case "stop":
		if !take("stop", "event-stopped", "publish") {
			return false
		}
		reload()
	case "unregister":
		if remaining[0] == "stop" {
			if !take("stop", "event-stopped", "publish") {
				return false
			}
			reload()
		}
		if !take("unregister") {
			return false
		}
		if len(remaining) > 0 && remaining[0] == "event-unregistered" {
			if contract.Registration == "none" {
				return false
			}
			take("event-unregistered")
		}
	default:
		return false
	}
	return len(remaining) == 0
}

func policyReloadStage(stages []string) bool { return len(stages) > 0 && stages[0] == "reload" }

func lifecycleEntry(s *tunnelState, key tunnelKey) *tunnelEntryState {
	for i := range s.Entries {
		if s.Entries[i].Key == key {
			return &s.Entries[i]
		}
	}
	return nil
}

func lifecycleComplete(op *tunnelOperation) bool {
	if op == nil || op.Lifecycle == nil {
		return false
	}
	if op.Lifecycle.Cancelled {
		return !lifecycleUncertain(op)
	}
	if len(op.Steps) != len(op.Lifecycle.Stages) {
		return false
	}
	for _, s := range op.Steps {
		if s.Status != "complete" {
			return false
		}
	}
	return true
}

func lifecycleUncertain(op *tunnelOperation) bool {
	for _, step := range op.Steps {
		if step.Status != "complete" {
			return true
		}
	}
	return false
}

func lifecycleStages(e tunnelEntryState, action string, contract tunnelProviderContract, reload bool) []string {
	stages := []string{}
	register := func() { stages = append(stages, "register", "event-registered") }
	stop := func() {
		stages = append(stages, "stop", "event-stopped", "publish")
		if reload {
			stages = append(stages, "reload")
		}
	}
	switch action {
	case "register":
		if !e.Registered {
			register()
		}
	case "start":
		if e.Active != nil {
			break
		}
		if contract.Registration != "none" && !e.Registered {
			register()
		}
		stages = append(stages, "start", "event-started", "publish")
		if reload {
			stages = append(stages, "reload")
		}
	case "stop":
		if e.Active != nil || lifecycleSessionResources(e) {
			stop()
		}
	case "unregister":
		if e.Active != nil || lifecycleSessionResources(e) {
			stop()
		}
		if e.Registered || lifecycleOwnedResources(e) {
			stages = append(stages, "unregister")
			if e.Registered {
				stages = append(stages, "event-unregistered")
			}
		}
	}
	return stages
}

func lifecycleSessionResources(e tunnelEntryState) bool {
	for _, r := range e.Resources {
		if r.Ownership == "owned" && r.Lifetime == "session" {
			return true
		}
	}
	return false
}
func lifecycleOwnedResources(e tunnelEntryState) bool {
	for _, r := range e.Resources {
		if r.Ownership == "owned" || r.Ownership == "unverified" {
			return true
		}
	}
	return false
}

func (l tunnelLifecycle) validate() error {
	if !tunnelComponent.MatchString(l.Owner) || l.Store.Read == nil || l.Store.Update == nil || l.Store.Fence == nil || l.Effects.Authorize == nil || l.Effects.Provider == nil || l.Effects.Publish == nil {
		return fmt.Errorf("explicit tunnel storage, fence and scoped effects are required")
	}
	return nil
}

func (l tunnelLifecycle) update(ctx context.Context, change func(*tunnelState) error) error {
	return l.Store.Update(ctx, func(s *tunnelState) error {
		if err := s.validate(l.Owner); err != nil {
			return err
		}
		if err := change(s); err != nil {
			return err
		}
		return s.validate(l.Owner)
	})
}

func (l tunnelLifecycle) authorize(ctx context.Context, call tunnelEffectCall) error {
	if call.Stage == "reload" && l.Effects.Reload == nil || strings.HasPrefix(call.Stage, "event-") && call.Definition.HooksDigest != "" && l.Effects.Event == nil {
		return fmt.Errorf("declared tunnel callbacks are unavailable")
	}
	if call.Entry.Operation.Action == "register" || call.Entry.Operation.Action == "start" {
		evidence := tunnelAccessEvidence{}
		if call.Entry.Consent == nil {
			return fmt.Errorf("missing definition-bound approval")
		}
		if !call.Entry.Consent.Public {
			if l.Effects.Access == nil {
				return fmt.Errorf("fresh private access evidence required")
			}
			var err error
			evidence, err = l.Effects.Access(ctx, call.Definition)
			if err != nil {
				return fmt.Errorf("private access evidence unavailable")
			}
		}
		if !call.Entry.Consent.permits(call.Definition, evidence) {
			return fmt.Errorf("current definition-bound tunnel approval is required")
		}
	}
	if err := l.Effects.Authorize(ctx, call); err != nil {
		return fmt.Errorf("scoped tunnel effect authorization refused")
	}
	return nil
}

// Execute admits one named entry operation. A different definition must first
// clean the retained old definition; deleting policy never deletes obligations.
// Concurrent actors compete only in Update; callbacks run outside its lock.
func (l tunnelLifecycle) Execute(ctx context.Context, r tunnelLifecycleRequest) error {
	if err := l.validate(); err != nil {
		return err
	}
	digest, err := r.Definition.digest()
	if err != nil {
		return err
	}
	if r.Definition.Owner != l.Owner || !tunnelComponent.MatchString(r.ID) {
		return fmt.Errorf("invalid receiving tunnel operation")
	}
	switch r.Action {
	case "register", "start", "stop", "unregister":
	default:
		return fmt.Errorf("invalid tunnel action")
	}
	contract, _ := tunnelContract(r.Definition.Entry.Policy.Provider, r.Definition.Entry.Policy.Mode)
	if r.Action == "register" && contract.Registration == "none" {
		return fmt.Errorf("temporary tunnel has no registration action")
	}
	if (r.Action == "register" || r.Action == "start") && !r.Consent.permits(r.Definition, r.Evidence) {
		return fmt.Errorf("current definition-bound tunnel approval is required")
	}
	if r.Definition.HooksDigest != "" && l.Effects.Event == nil || r.Definition.ReloadDigest != "" && l.Effects.Reload == nil {
		return fmt.Errorf("declared tunnel callbacks are unavailable")
	}
	err = l.update(ctx, func(s *tunnelState) error {
		if err := s.validate(l.Owner); err != nil {
			return err
		}
		e := lifecycleEntry(s, r.Definition.Entry.Key)
		if e == nil {
			if r.Action == "stop" || r.Action == "unregister" {
				return nil
			}
			s.Entries = append(s.Entries, tunnelEntryState{Key: r.Definition.Entry.Key, Provider: r.Definition.Entry.Policy.Provider, Mode: r.Definition.Entry.Policy.Mode, Definition: digest})
			e = &s.Entries[len(s.Entries)-1]
		}
		if e.Definition != digest {
			if e.needsCleanup() || r.Action == "stop" || r.Action == "unregister" {
				return fmt.Errorf("old tunnel definition must be cleaned before replacement")
			}
			*e = tunnelEntryState{Key: r.Definition.Entry.Key, Provider: r.Definition.Entry.Policy.Provider, Mode: r.Definition.Entry.Policy.Mode, Definition: digest}
		}
		if op := e.Operation; op != nil {
			if op.ID == r.ID {
				if op.Action != r.Action || op.Definition != digest || op.Lifecycle == nil {
					return fmt.Errorf("tunnel operation id reused")
				}
				if r.Action == "register" || r.Action == "start" {
					consent := r.Consent
					e.Consent = &consent
				}
				return nil
			}
			if !lifecycleComplete(op) && (lifecycleUncertain(op) || r.Action != "stop" && r.Action != "unregister" || op.Action != "start" && op.Action != "register") {
				return errTunnelReconcile
			}
		}
		stages := lifecycleStages(*e, r.Action, contract, r.Definition.ReloadDigest != "")
		if len(stages) == 0 {
			if e.Operation != nil && !lifecycleComplete(e.Operation) {
				e.Operation.Lifecycle.Cancelled = true
			}
			return nil
		}
		before, err := tunnelSnapshotForEvent(tunnelStateActive(*s))
		if err != nil {
			return err
		}
		consent := r.Consent
		if r.Action == "register" || r.Action == "start" {
			e.Consent = &consent
		}
		e.Operation = &tunnelOperation{ID: r.ID, Action: r.Action, Definition: digest, Lifecycle: &tunnelLifecycleOperation{Definition: r.Definition, Stages: stages, Previous: e.Active, Registration: e.Known, Before: before}}
		return nil
	})
	if err != nil {
		return err
	}
	return l.advance(ctx, r.Definition.Entry.Key, r.ID)
}

func (l tunnelLifecycle) call(s tunnelState, e tunnelEntryState, stage string) (tunnelEffectCall, error) {
	op := e.Operation
	call := tunnelEffectCall{Owner: l.Owner, OperationID: op.ID, Stage: stage, Definition: op.Lifecycle.Definition, Entry: e}
	snapshot, err := buildTunnelHostSnapshot(tunnelStateActive(s))
	if err != nil {
		return call, err
	}
	call.Snapshot = snapshot
	if len(op.Steps) > 0 && op.Steps[len(op.Steps)-1].Name == stage && op.Steps[len(op.Steps)-1].Status != "complete" {
		if op.Lifecycle.View == nil {
			return call, fmt.Errorf("missing persisted tunnel effect snapshot")
		}
		active := []tunnelActiveAddress{}
		for _, e := range op.Lifecycle.View.Endpoints {
			active = append(active, tunnelActiveAddress{e.Key, e.Address.URL})
		}
		call.Snapshot, err = buildTunnelHostSnapshot(active)
		if err != nil {
			return call, err
		}
	}
	if strings.HasPrefix(stage, "event-") {
		if op.Lifecycle.Event != nil && op.Lifecycle.Event.Type == strings.TrimPrefix(stage, "event-") {
			call.Event = op.Lifecycle.Event
			return call, nil
		}
		after, err := tunnelSnapshotForEvent(tunnelStateActive(s))
		if err != nil {
			return call, err
		}
		name := strings.TrimPrefix(stage, "event-")
		previous, current := op.Lifecycle.Previous, e.Active
		if name == "registered" {
			previous, current = op.Lifecycle.Registration, e.Known
		}
		if name == "unregistered" {
			previous, current = op.Lifecycle.Registration, (*tunnelAddress)(nil)
		}
		call.Event = &tunnelEvent{Schema: 1, ID: tunnelDigest([]byte(op.ID + "/" + e.Key.String() + "/" + name)), OperationID: op.ID, Type: name, Key: e.Key, Definition: e.Definition, Previous: previous, Current: current, Before: op.Lifecycle.Before, After: after}
	}
	return call, nil
}

func (l tunnelLifecycle) advance(ctx context.Context, key tunnelKey, id string) error {
	for {
		s, err := l.Store.Read(ctx)
		if err != nil {
			return err
		}
		if err = s.validate(l.Owner); err != nil {
			return err
		}
		e := lifecycleEntry(&s, key)
		if e == nil || e.Operation == nil || e.Operation.ID != id {
			return nil
		}
		op := e.Operation
		if op.Lifecycle != nil && op.Lifecycle.Cancelled {
			return fmt.Errorf("tunnel operation was cancelled before remaining effects")
		}
		if lifecycleComplete(op) {
			if op.Action == "unregister" && e.needsCleanup() {
				return fmt.Errorf("tunnel cleanup receipts remain")
			}
			if op.Action == "stop" && lifecycleSessionResources(*e) {
				return fmt.Errorf("tunnel session cleanup receipts remain")
			}
			return nil
		}
		if op.Lifecycle == nil {
			return errTunnelReconcile
		}
		for _, step := range op.Steps {
			if step.Status != "complete" {
				return errTunnelReconcile
			}
		}
		stage := op.Lifecycle.Stages[len(op.Steps)]
		// An unverified identity can be queried by explicit reconciliation,
		// but must never be sent to the provider's cleanup effect as authority.
		if stage == "unregister" {
			for _, r := range e.Resources {
				if r.Ownership == "unverified" {
					return errTunnelReconcile
				}
			}
		}
		call, err := l.call(s, *e, stage)
		if err != nil {
			return err
		}
		if err = l.authorize(ctx, call); err != nil {
			return err
		}
		if err = l.Store.Fence(ctx); err != nil {
			return fmt.Errorf("tunnel forward schema could not be persisted")
		}
		err = l.update(ctx, func(latest *tunnelState) error {
			current := lifecycleEntry(latest, key)
			if current == nil || !reflect.DeepEqual(*current, *e) {
				return errTunnelReconcile
			}
			// Rebuild the publication/event under the same transaction as intent,
			// so another named entry's committed URL is included in the payload.
			var err error
			call, err = l.call(*latest, *current, stage)
			if err != nil {
				return err
			}
			current.Operation.Lifecycle.Event = call.Event
			view, err := tunnelSnapshotForEvent(tunnelStateActive(*latest))
			if err != nil {
				return err
			}
			current.Operation.Lifecycle.View = &view
			current.Operation.Steps = append(current.Operation.Steps, tunnelEffectStep{stage, "pending"})
			return nil
		})
		if err != nil {
			return err
		}
		result, effectErr := l.effect(ctx, call)
		if err = l.finish(ctx, key, id, stage, result, effectErr == nil); err != nil {
			return err
		}
		if effectErr != nil {
			return errTunnelReconcile
		}
	}
}

func (l tunnelLifecycle) effect(ctx context.Context, call tunnelEffectCall) (tunnelEffectResult, error) {
	switch call.Stage {
	case "register", "start", "stop", "unregister":
		return l.Effects.Provider(ctx, call)
	case "publish":
		return tunnelEffectResult{}, l.Effects.Publish(ctx, call.Snapshot)
	case "reload":
		return tunnelEffectResult{}, l.Effects.Reload(ctx, call)
	default:
		if call.Event == nil {
			return tunnelEffectResult{}, fmt.Errorf("missing tunnel event")
		}
		if call.Definition.HooksDigest == "" {
			return tunnelEffectResult{}, nil
		}
		return l.Effects.Event(ctx, *call.Event)
	}
}

func lifecycleApply(e *tunnelEntryState, owner, stage string, result tunnelEffectResult, confirmed bool) error {
	resources := append([]tunnelResourceRef(nil), e.Resources...)
	if confirmed {
		for _, r := range result.Remove {
			if stage == "stop" && r.Lifetime != "session" {
				return fmt.Errorf("stop cannot forget registration receipts")
			}
			found := -1
			for i, existing := range resources {
				if existing == r {
					found = i
					break
				}
			}
			if found < 0 {
				return fmt.Errorf("cleanup receipt does not match")
			}
			resources = append(resources[:found], resources[found+1:]...)
		}
	}
	for _, r := range result.Add {
		if err := r.validate(owner); err != nil {
			return err
		}
		if r.Lifetime == "" {
			return fmt.Errorf("new tunnel receipt requires a lifetime")
		}
		found := false
		for _, existing := range resources {
			if existing.key() == r.key() {
				if existing != r {
					return fmt.Errorf("changed tunnel receipt")
				}
				found = true
			}
		}
		if !found {
			resources = append(resources, r)
		}
	}
	if confirmed {
		for _, r := range resources {
			if r.Ownership == "unverified" {
				return fmt.Errorf("unverified tunnel identity needs reconciliation")
			}
		}
		if result.Address != nil {
			a, err := parseTunnelAddress(result.Address.URL)
			if err != nil || a != *result.Address {
				return fmt.Errorf("invalid provider address")
			}
		}
		switch stage {
		case "register":
			retained := false
			for _, r := range resources {
				if r.Lifetime == "registration" {
					retained = true
				}
			}
			if !retained {
				return fmt.Errorf("registration lacks a retained identity receipt")
			}
			e.Registered = true
			e.Known = result.Address
		case "start":
			if result.Address == nil {
				return fmt.Errorf("started tunnel has no confirmed address")
			}
			e.Active = result.Address
			if e.Registered {
				e.Known = result.Address
			}
		case "stop":
			e.Active = nil
		case "unregister":
			e.Registered = false
			e.Known = nil
		}
	}
	e.Resources = resources
	return nil
}

func (l tunnelLifecycle) finish(ctx context.Context, key tunnelKey, id, stage string, result tunnelEffectResult, confirmed bool) error {
	return l.update(ctx, func(s *tunnelState) error {
		e := lifecycleEntry(s, key)
		if e == nil || e.Operation == nil || e.Operation.ID != id || len(e.Operation.Steps) == 0 {
			return errTunnelReconcile
		}
		last := &e.Operation.Steps[len(e.Operation.Steps)-1]
		if last.Name != stage || last.Status == "complete" {
			return errTunnelReconcile
		}
		if err := lifecycleApply(e, l.Owner, stage, result, confirmed); err != nil {
			return err
		}
		last.Status = "needs-reconcile"
		if confirmed {
			last.Status = "complete"
		}
		return nil
	})
}

// Reconcile confirms the exact original pending effect, including its persisted
// event payload. It never interprets absence as permission to replay an effect
// that an interrupted process might still complete.
func (l tunnelLifecycle) Reconcile(ctx context.Context, key tunnelKey, id string) error {
	if err := l.validate(); err != nil {
		return err
	}
	s, err := l.Store.Read(ctx)
	if err != nil {
		return err
	}
	if err = s.validate(l.Owner); err != nil {
		return err
	}
	e := lifecycleEntry(&s, key)
	if e == nil || e.Operation == nil || e.Operation.ID != id || e.Operation.Lifecycle == nil {
		return errTunnelReconcile
	}
	op := e.Operation
	if len(op.Steps) == 0 {
		return l.advance(ctx, key, id)
	}
	last := op.Steps[len(op.Steps)-1]
	if last.Status == "complete" {
		return l.advance(ctx, key, id)
	}
	if l.Effects.Confirm == nil {
		return errTunnelReconcile
	}
	call, err := l.call(s, *e, last.Name)
	if err != nil {
		return err
	}
	if err = l.authorize(ctx, call); err != nil {
		return err
	}
	if err = l.Store.Fence(ctx); err != nil {
		return fmt.Errorf("tunnel forward schema could not be persisted")
	}
	result, confirmed, err := l.Effects.Confirm(ctx, call)
	if err != nil || !confirmed {
		return errTunnelReconcile
	}
	if err = l.finish(ctx, key, id, last.Name, result, true); err != nil {
		return err
	}
	return l.advance(ctx, key, id)
}
