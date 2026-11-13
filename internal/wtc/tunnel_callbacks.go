package wtc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Wire identities have no ownership/permission fields. The receiving callback
// code is definition-bound; only this local constructor mints its receipts.
type tunnelCallbackIdentity struct {
	Kind        string `json:"kind"`
	Scope       string `json:"scope"`
	ID          string `json:"id"`
	Fingerprint string `json:"fingerprint"`
}

func (c *Context) verifyTunnelCode(d tunnelDefinition) error {
	scope, err := c.ValidateControlRoot()
	if err != nil {
		return err
	}
	scopeBytes, _ := json.Marshal([2]string{scope.OwnerRoot, scope.Root})
	if d.ControlScope == "" || tunnelDigest(scopeBytes) != d.ControlScope {
		return fmt.Errorf("retained callback credential permission changed; restore before cleanup")
	}
	identity, err := c.prRepositories(d.Entry.Key.Repo).resolve(d.Entry.Key.Repo)
	if err != nil || identity == nil || identity.dir() != d.Directory {
		return fmt.Errorf("retained tunnel repository identity changed; restore before cleanup")
	}
	data, _ := json.Marshal([3]string{identity.name, prForgeIdentity(identity.remote), d.Directory})
	if tunnelDigest(data) != d.RepositoryIdentity {
		return fmt.Errorf("retained tunnel repository identity changed")
	}
	digest, err := c.tunnelCallbackDigest(d.Directory, d.Entry.Policy.Events)
	if err != nil {
		return err
	}
	if digest != d.HooksDigest {
		return fmt.Errorf("declared tunnel callback code changed; restore original code before cleanup")
	}
	return nil
}

func (c *Context) tunnelCallbackDigest(dir string, commands map[string]tunnelEventCommand) (string, error) {
	if len(commands) == 0 {
		return "", nil
	}
	root, err := canonicalPath(filepath.Join(c.Collection, dir))
	if err != nil {
		return "", err
	}
	inputs := map[string]string{}
	for event, cmd := range commands {
		if len(cmd.Command) == 0 || !tunnelRelativePath(cmd.Command[0]) {
			return "", fmt.Errorf("invalid declared tunnel event command")
		}
		path := filepath.Join(root, cmd.Command[0])
		resolved, err := canonicalPath(path)
		if err != nil || !withinPath(root, resolved) {
			return "", fmt.Errorf("tunnel event code escapes receiving repository")
		}
		data, err := readTunnelBoundedFile(path, 1<<20)
		if err != nil {
			return "", fmt.Errorf("declared tunnel event code unavailable")
		}
		inputs[event] = tunnelDigest(data)
	}
	data, _ := json.Marshal(inputs)
	return tunnelDigest(data), nil
}

func (c *Context) dispatchTunnelEvent(ctx context.Context, event tunnelEvent) (tunnelEffectResult, error) {
	result := tunnelEffectResult{}
	state, err := c.readTunnelJournal()
	if err != nil {
		return result, err
	}
	e := lifecycleEntry(&state, event.Key)
	if e == nil || e.Operation == nil || e.Operation.ID != event.OperationID || e.Operation.Lifecycle == nil || e.Operation.Lifecycle.Event == nil {
		return result, fmt.Errorf("persisted tunnel event intent required")
	}
	d := e.Operation.Lifecycle.Definition
	if err = c.verifyTunnelCode(d); err != nil {
		return result, err
	}
	command, exists := d.Entry.Policy.Events[event.Type]
	if !exists {
		return result, nil
	}
	data, err := json.Marshal(event)
	if err != nil {
		return result, err
	}
	retained, err := json.Marshal(e.Operation.Lifecycle.Event)
	if err != nil || !bytes.Equal(data, retained) {
		return result, fmt.Errorf("event differs from the retained operation input")
	}
	path := filepath.Join(c.Collection, d.Directory, command.Command[0])
	prepared, err := c.ReceivingCommand(path, command.Command[1:]...)
	if err != nil {
		return result, err
	}
	bound, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(bound, prepared.Args[0], prepared.Args[1:]...)
	cmd.Env, err = c.ReceivingEnvironment(minimalTunnelEnvironment(c.Collection))
	if err != nil {
		return result, err
	}
	cmd.Dir = filepath.Join(c.Collection, d.Directory)
	cmd.Stdin = bytes.NewReader(data)
	// Output may contain credentials or provider responses. It is never
	// included in diagnostics, logs, events or public artifacts.
	var output statusLimitedBuffer
	output.limit = 1 << 20
	cmd.Stdout = &output
	cmd.Stderr = io.Discard
	cmd.WaitDelay = 200 * time.Millisecond
	if err = cmd.Run(); err != nil {
		return result, fmt.Errorf("tunnel event failed; reconcile original event id")
	}
	if len(bytes.TrimSpace(output.Bytes())) == 0 {
		return result, nil
	}
	var wire struct {
		Resources []tunnelCallbackIdentity `json:"resources"`
		Released  []tunnelCallbackIdentity `json:"released"`
	}
	decoder := json.NewDecoder(bytes.NewReader(output.Bytes()))
	decoder.DisallowUnknownFields()
	if !tunnelUniqueJSONKeys(json.NewDecoder(bytes.NewReader(output.Bytes()))) || decoder.Decode(&wire) != nil {
		return result, fmt.Errorf("invalid tunnel callback receipts; reconcile event")
	}
	makeReceipt := func(identity tunnelCallbackIdentity) (tunnelResourceRef, error) {
		if !tunnelComponent.MatchString(identity.Kind) || !validTunnelDigest(identity.Fingerprint) {
			return tunnelResourceRef{}, fmt.Errorf("invalid callback identity")
		}
		lifetime := "registration"
		if event.Type == "started" || event.Type == "stopped" {
			lifetime = "session"
		}
		r := tunnelResourceRef{Provider: "callback", Scope: identity.Scope, Kind: "callback-" + identity.Kind, ID: identity.ID, Ownership: "owned", Owner: c.tunnelOwner(), Fingerprint: identity.Fingerprint, Lifetime: lifetime}
		return r, r.validate(c.tunnelOwner())
	}
	for _, identity := range wire.Resources {
		r, err := makeReceipt(identity)
		if err != nil {
			return result, err
		}
		result.Add = append(result.Add, r)
	}
	for _, identity := range wire.Released {
		matched := false
		for _, receipt := range event.Resources {
			if receipt.Provider == "callback" && receipt.Kind == "callback-"+identity.Kind && receipt.Scope == identity.Scope && receipt.ID == identity.ID && receipt.Fingerprint == identity.Fingerprint && receipt.Ownership == "owned" && receipt.Owner == c.tunnelOwner() {
				result.Remove = append(result.Remove, receipt)
				matched = true
				break
			}
		}
		if !matched {
			return result, fmt.Errorf("callback cleanup does not match retained receipt")
		}
	}
	return result, nil
}

func tunnelReloadOrder(plan *runtimeManifest, targets []string) ([]string, error) {
	byPath := map[string]runtimeTask{}
	for _, task := range plan.Tasks {
		byPath[task.Path] = task
	}
	colors := map[string]int{}
	order := []string{}
	selected := map[string]bool{}
	for _, target := range targets {
		selected[target] = true
	}
	var visit func(string) error
	visit = func(path string) error {
		if colors[path] == 1 {
			return fmt.Errorf("tunnel reload dependency cycle")
		}
		if colors[path] == 2 {
			return nil
		}
		task, exists := byPath[path]
		if !exists || task.Kind == "endpoint" || task.ManagedTunnel || selected[path] && task.Kind != "service" {
			return fmt.Errorf("reload must target a declared app service, never a tunnel")
		}
		colors[path] = 1
		for _, dep := range task.Deps {
			if err := visit(dep); err != nil {
				return err
			}
		}
		colors[path] = 2
		if selected[path] {
			order = append(order, path)
		}
		return nil
	}
	for _, target := range targets {
		if err := visit(target); err != nil {
			return nil, err
		}
	}
	return order, nil
}

func (c *Context) reloadTunnelApps(ctx context.Context, call tunnelEffectCall) error {
	if _, err := c.ValidateControlRoot(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	m, err := c.runtimeManifest()
	if err != nil || m == nil {
		return fmt.Errorf("runtime missing for declared app reload")
	}
	order, digest, err := c.tunnelReloadPlan(m, call.Definition.Entry.Policy.Reload)
	if err != nil {
		return err
	}
	if digest != call.Definition.ReloadDigest {
		return fmt.Errorf("declared app reload identity changed; reconcile retained operation")
	}
	for _, path := range order {
		if _, err = runtimeCommand(m, true, "restart", path); err != nil {
			return fmt.Errorf("declared app reload failed; reconcile without replay")
		}
	}
	return nil
}

// No inherited provider tokens, proxy, TLS, SDK or global profile controls.
// Absolute executable paths and supplied tokens bind built-in provider effects;
// this environment is not an OS sandbox for arbitrary receiving hook code.
func minimalTunnelEnvironment(home string) []string {
	return []string{"HOME=" + home, "PATH=/usr/bin:/bin", "TMPDIR=" + home, "LANG=C", "LC_ALL=C"}
}

func (c *Context) tunnelReloadPlan(plan *runtimeManifest, declared []string) ([]string, string, error) {
	identities := c.prRepositories()
	targets := []string{}
	bindings := []any{}
	for _, ref := range declared {
		parts := strings.SplitN(ref, "/", 2)
		if len(parts) != 2 {
			return nil, "", fmt.Errorf("invalid declared app reload")
		}
		identity, err := identities.resolve(parts[0])
		if err != nil || identity == nil || identity.dir() == "" || identity.name != parts[0] {
			return nil, "", fmt.Errorf("reload requires an unambiguous registered app identity")
		}
		path := identity.dir() + "/" + parts[1]
		found := false
		for _, task := range plan.Tasks {
			if task.Path == path && task.Repo == identity.name && task.Dir == identity.dir() {
				targets = append(targets, path)
				bindings = append(bindings, []string{ref, path, prForgeIdentity(identity.remote), task.Spec})
				found = true
				break
			}
		}
		if !found {
			return nil, "", fmt.Errorf("declared app reload target is not present")
		}
	}
	order, err := tunnelReloadOrder(plan, targets)
	if err != nil {
		return nil, "", err
	}
	data, _ := json.Marshal(bindings)
	return order, tunnelDigest(data), nil
}
